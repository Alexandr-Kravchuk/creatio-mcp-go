package creatio

// sync-pages: clio's PageSyncTool. Every page is validated, saved through update-page's command with its own
// baseline, and optionally read back; a failing page does not stop the others.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// PageSyncPageInput is clio's PageSyncPageInput.
type PageSyncPageInput struct {
	SchemaName         string `json:"schema-name"`
	Body               string `json:"body"`
	Resources          string `json:"resources"`
	OptionalProperties string `json:"optional-properties"`
	Force              *bool  `json:"force"`
	Checksum           string `json:"checksum"`
}

// PageSyncRequest is clio's PageSyncArgs.
type PageSyncRequest struct {
	EnvironmentName string
	Pages           []PageSyncPageInput
	Validate        bool
	Verify          bool
	OutputDirectory string
}

// PageSyncResponse is clio's PageSyncResponse.
type PageSyncResponse struct {
	Success bool                 `json:"success"`
	Pages   []PageSyncPageResult `json:"pages"`
}

// PageSyncPageResult is clio's PageSyncPageResult.
type PageSyncPageResult struct {
	SchemaName          string                  `json:"schema-name,omitempty"`
	Success             bool                    `json:"success"`
	BodyLength          int                     `json:"body-length,omitempty"`
	Validation          *PageValidationOutcomes `json:"validation,omitempty"`
	Error               string                  `json:"error,omitempty"`
	ResourcesRegistered int                     `json:"resources-registered,omitempty"`
	Page                *PageMetadata           `json:"page,omitempty"`
	VerifiedBodyFile    string                  `json:"verified-body-file,omitempty"`
	Conflict            bool                    `json:"conflict,omitempty"`
	ConflictDetails     *PageConflictDetails    `json:"conflict-details,omitempty"`
}

// PageSyncPrepass runs the per-page checks that need no environment: the syntax gate and, for the first
// occurrence of each schema, client-side validation. A nil entry is a page still to be saved.
func PageSyncPrepass(request PageSyncRequest) []*PageSyncPageResult {
	results := make([]*PageSyncPageResult, len(request.Pages))
	seen := map[string]bool{}
	for index, page := range request.Pages {
		if !pageWriteIsMobileBody(page.Body) {
			if _, err := classicPageParse(page.Body); err != nil {
				message := pageWriteSyncSyntaxMessage(page, pageValidateSyntaxMessage(page.Body, err), request.Validate)
				results[index] = &PageSyncPageResult{SchemaName: page.SchemaName, Error: message,
					Validation: &PageValidationOutcomes{Errors: []string{message}}}
				continue
			}
		}
		deferContent := seen[page.SchemaName]
		seen[page.SchemaName] = true
		if pageWriteIsMobileBody(page.Body) || !request.Validate || deferContent {
			continue
		}
		if validation := pageWriteSyncValidate(page); !validation.MarkersOK || !validation.JSSyntaxOK || !validation.ContentOK {
			results[index] = &PageSyncPageResult{SchemaName: page.SchemaName, Validation: validation,
				Error: "Client-side validation failed: " + strings.Join(validation.Errors, "; ")}
		}
	}
	return results
}

// PageSyncFillPending answers every page still to be saved with one failure (an unusable environment).
func PageSyncFillPending(results []*PageSyncPageResult, request PageSyncRequest, message string) PageSyncResponse {
	for index := range results {
		if results[index] == nil {
			results[index] = &PageSyncPageResult{SchemaName: request.Pages[index].SchemaName, Error: message}
		}
	}
	return pageSyncResponse(results)
}

func pageSyncResponse(results []*PageSyncPageResult) PageSyncResponse {
	response := PageSyncResponse{Success: len(results) > 0, Pages: make([]PageSyncPageResult, 0, len(results))}
	for _, result := range results {
		response.Pages = append(response.Pages, *result)
		response.Success = response.Success && result.Success
	}
	return response
}

// SyncPages saves the pages the prepass left pending.
func (c *Client) SyncPages(ctx context.Context, request PageSyncRequest, results []*PageSyncPageResult) PageSyncResponse {
	for index := range results {
		if results[index] != nil {
			continue
		}
		result := c.syncPage(ctx, request, request.Pages[index])
		results[index] = &result
	}
	return pageSyncResponse(results)
}

func pageWriteSyncSyntaxMessage(page PageSyncPageInput, syntax string, validate bool) string {
	if !validate || len(pageValidateMarkerIntegrity(page.Body)) > 0 {
		return syntax
	}
	if errors := pageWriteSyncValidate(page).Errors; len(errors) > 0 {
		return "Client-side validation failed: " + strings.Join(errors, "; ")
	}
	return syntax
}

// pageWriteSyncValidate is PageSyncTool.ValidateBody with the checks this server has.
func pageWriteSyncValidate(page PageSyncPageInput) *PageValidationOutcomes {
	markers := pageValidateMarkerIntegrity(page.Body)
	errors := append([]string{}, markers...)
	contentOK := true
	if len(markers) == 0 {
		if content := pageValidateMarkerContent(page.Body); len(content) > 0 {
			contentOK = false
			errors = append(errors, content...)
		} else if _, ok := pageWriteParseResources(page.Resources); !ok {
			contentOK = false
			errors = append(errors, pageWriteInvalidResources)
		}
	}
	outcome := &PageValidationOutcomes{MarkersOK: len(markers) == 0, JSSyntaxOK: true, ContentOK: contentOK}
	if len(errors) > 0 {
		outcome.Errors = errors
	}
	return outcome
}

func pageWriteSyncWarnings(validation *PageValidationOutcomes, warnings ...string) *PageValidationOutcomes {
	added := []string{}
	for _, warning := range warnings {
		if strings.TrimSpace(warning) != "" {
			added = append(added, warning)
		}
	}
	if len(added) == 0 {
		return validation
	}
	if validation == nil {
		return &PageValidationOutcomes{MarkersOK: true, JSSyntaxOK: true, ContentOK: true, Warnings: added}
	}
	copied := *validation
	copied.Warnings = append(append([]string{}, validation.Warnings...), added...)
	return &copied
}

func (c *Client) syncPage(ctx context.Context, request PageSyncRequest, page PageSyncPageInput) (result PageSyncPageResult) {
	defer func() {
		if recovered := recover(); recovered != nil {
			result = PageSyncPageResult{SchemaName: page.SchemaName, Error: pageWriteRecoveredText(recovered)}
		}
	}()
	var validation *PageValidationOutcomes
	if request.Validate {
		if pageWriteIsMobileBody(page.Body) {
			validation = &PageValidationOutcomes{MarkersOK: true, JSSyntaxOK: true, ContentOK: true}
		} else {
			validation = pageWriteSyncValidate(page)
			if !validation.MarkersOK || !validation.JSSyntaxOK || !validation.ContentOK {
				return PageSyncPageResult{SchemaName: page.SchemaName, Validation: validation,
					Error: "Client-side validation failed: " + strings.Join(validation.Errors, "; ")}
			}
		}
	}
	force := page.Force != nil && *page.Force
	if !request.Validate && force {
		validation = pageWriteSyncWarnings(validation, PageWriteForceValidateAdvisory)
	}
	update := &PageUpdateRequest{SchemaName: page.SchemaName, Body: page.Body, Resources: page.Resources,
		OptionalProperties: page.OptionalProperties, EnvironmentName: request.EnvironmentName, Force: force,
		Validate: request.Validate, ExpectedChecksum: page.Checksum}
	metaPath, refresh, baselineWarning := c.ArmPageBaseline(update, request.OutputDirectory)
	validation = pageWriteSyncWarnings(validation, baselineWarning)
	response := c.UpdatePage(ctx, update)
	if !response.Success {
		return PageSyncPageResult{SchemaName: page.SchemaName, Validation: validation, Error: response.Error,
			Conflict: response.Conflict, ConflictDetails: response.ConflictDetails}
	}
	validation = pageWriteSyncWarnings(validation, response.Warnings...)
	if request.Verify {
		return c.syncVerify(ctx, request, page, response, validation)
	}
	if refresh || update.conditionalApplied {
		validation = pageWriteSyncWarnings(validation, pageWriteRefreshOrDrop(metaPath, update, &response))
	}
	if request.Validate {
		validation = pageWriteSyncWarnings(validation, PageWriteValidationGapWarning)
	}
	return PageSyncPageResult{SchemaName: page.SchemaName, Success: true, BodyLength: response.BodyLength, Validation: validation,
		ResourcesRegistered: response.ResourcesRegistered}
}

// syncVerify is VerifySavedPage: the page is read back, its body written to .clio-pages/{schema}/body.js and
// meta.json rewritten from the read-back.
func (c *Client) syncVerify(ctx context.Context, request PageSyncRequest, page PageSyncPageInput, response PageUpdateResponse,
	validation *PageValidationOutcomes) PageSyncPageResult {
	read := c.GetPage(ctx, PageGetRequest{SchemaName: page.SchemaName})
	if !read.Success {
		return PageSyncPageResult{SchemaName: page.SchemaName, BodyLength: response.BodyLength, Validation: validation,
			Error: "Page saved but verification failed: " + read.Error}
	}
	bodyFile, warning := c.syncPublishReadBack(page.SchemaName, request, read)
	validation = pageWriteSyncWarnings(validation, warning)
	if request.Validate {
		validation = pageWriteSyncWarnings(validation, PageWriteValidationGapWarning)
	}
	return PageSyncPageResult{SchemaName: page.SchemaName, Success: true, BodyLength: response.BodyLength, Validation: validation,
		ResourcesRegistered: response.ResourcesRegistered, Page: read.fullPage, VerifiedBodyFile: bodyFile}
}

func (c *Client) syncPublishReadBack(schemaName string, request PageSyncRequest, read PageGetResult) (string, string) {
	anchor, err := pageOutputAnchor(request.OutputDirectory)
	if err != nil {
		panic(err)
	}
	schemaDir := filepath.Join(anchor, clioPagesDirectoryName, schemaName)
	bodyFile := filepath.Join(schemaDir, "body.js")
	metaFile := filepath.Join(schemaDir, "meta.json")
	warning, err := pageWriteGated(metaFile, func() (string, error) {
		if err := os.MkdirAll(schemaDir, 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(bodyFile, []byte(read.rawBody), 0o644); err != nil {
			return "", err
		}
		return c.syncFreshMeta(metaFile, schemaName, request.EnvironmentName, read), nil
	})
	if err != nil {
		panic(err)
	}
	return bodyFile, warning
}

func (c *Client) syncFreshMeta(metaFile, schemaName, environmentName string, read PageGetResult) string {
	fetchedAt := pageWriteRoundTripTime(time.Now())
	fields := orderedFields{{"fetchedAt", fetchedAt}, {"page", pageMetadataNode(read.fullPage)}}
	if read.Editable != nil {
		baseline := &pageWriteBaseline{SchemaName: schemaName, EnvironmentName: strings.TrimSpace(environmentName),
			EditableSchemaExists: read.Editable.EditableSchemaExists, EditableSchemaUID: read.Editable.EditableSchemaUID,
			CapturedAt: fetchedAt}
		if baseline.EnvironmentName == "" {
			baseline.EnvironmentURI = strings.TrimSpace(c.config.BaseURL)
		}
		if read.Editable.Checksum != nil {
			baseline.Checksum = *read.Editable.Checksum
		}
		if read.Editable.ModifiedOn != nil {
			baseline.ModifiedOn = *read.Editable.ModifiedOn
		}
		if pageWriteFileExists(metaFile) {
			if meta, err := pageWriteReadMeta(metaFile); err == nil && meta.isObject() {
				baseline = pageWriteMergeIdentity(baseline, pageWriteBaselineFrom(meta.get("baseline")))
			}
		}
		fields = append(fields, field{"baseline", baseline.node()})
	}
	if err := pageWriteMetaAtomically(metaFile, toJNode(fields).stjJSON()); err != nil {
		return fmt.Sprintf("The page was saved and verified, but its conflict baseline '%s' could not be rewritten from the verified "+
			"read-back (%s). The next save of this page may report a conflict that is not real; re-run get-page to recapture the baseline.",
			metaFile, err.Error())
	}
	return ""
}
