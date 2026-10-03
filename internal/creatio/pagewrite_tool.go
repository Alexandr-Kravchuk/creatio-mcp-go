package creatio

// The MCP-side gates of clio's update-page and sync-pages that run before the command: body loading, the
// append full-config guard, the JavaScript syntax gate with ResolveSyntaxFailure's more specific causes, and
// the content checks this server has.

import (
	"context"
	"strings"
)

const (
	// PageWriteForceValidateAdvisory is clio's PageUpdateTool.ForceValidateAdvisory.
	PageWriteForceValidateAdvisory = "Both the content-validation chain (validate=false) and the baseline/conflict guard (force=true) are disabled for this save; only the structural floor and the baseline refresh still apply."
	// PageWriteValidationGapWarning names the parts of clio's page validation this server does not run, so a
	// save that passed here is never read as having passed clio's full chain.
	PageWriteValidationGapWarning = "This server ran only the structural part of clio's page validation (JavaScript syntax read structurally, " +
		"section marker pairs, the JSON/object content of each section, resources, optional-properties and parentName resolution). It did NOT " +
		"run clio's field and column binding, handler, converter, validator, localizable-text, schema-deps, context-await, AST lint, " +
		"chart-widget, run-process-button, inserted-widget caption, insert-downgrade / inert-operation or mobile content checks; run clio " +
		"validate-page for the full verdict."
)

// PageWriteSucceededWithGap adds PageWriteValidationGapWarning to a successful save that ran validation.
func PageWriteSucceededWithGap(response *PageUpdateResponse, validate bool) {
	if response.Success && validate {
		response.Warnings = append(response.Warnings, PageWriteValidationGapWarning)
	}
}

// PageUpdatePreflight is the outcome of the MCP-side gates.
type PageUpdatePreflight struct {
	Failure *PageUpdateResponse
	// SyntaxOnly marks the generic JavaScript syntax failure, which clio replaces by an environment
	// resolution failure when there is one.
	SyntaxOnly bool
	Warnings   []string
}

// PageUpdateCheck runs clio's PageUpdateTool.TryCreatePreExecutionFailureAsync for one request. The body is
// loaded into request.Body.
func PageUpdateCheck(request *PageUpdateRequest) PageUpdatePreflight {
	fail := func(text string) PageUpdatePreflight {
		failure := pageUpdateFailure(text)
		return PageUpdatePreflight{Failure: &failure}
	}
	body, problem := PageWriteLoadBody(request.Body, request.BodyFile)
	if problem != "" {
		return fail(problem)
	}
	request.Body = body
	if strings.TrimSpace(body) == "" {
		return fail("Either 'body' or 'body-file' must provide page body content.")
	}
	if pageWriteIsAppend(request) {
		if full, message := pageWriteUsesFullConfig(body, false); full {
			return fail("Append merge cannot use this body: " + message + " See docs://mcp/guides/page-modification for the append diff-form contract.")
		}
	}
	mobile := pageWriteIsMobileBody(body)
	if !mobile {
		if _, err := classicPageParse(body); err != nil {
			return pageWriteSyntaxFailure(request, pageValidateSyntaxMessage(body, err))
		}
	}
	var warnings []string
	if !request.Validate && request.Force {
		warnings = append(warnings, PageWriteForceValidateAdvisory)
	}
	if request.Validate {
		if !mobile {
			if problems := pageValidateMarkerContent(body); len(problems) > 0 {
				failure := pageUpdateFailure("Validation failed: " + strings.Join(problems, "; ") + PageWriteEscapeHatchHint)
				return PageUpdatePreflight{Failure: &failure}
			}
		}
	}
	return PageUpdatePreflight{Warnings: warnings}
}

// pageWriteSyntaxFailure is ResolveSyntaxFailure: a malformed argument payload or an offline content failure
// is reported instead of the generic syntax error.
func pageWriteSyntaxFailure(request *PageUpdateRequest, syntax string) PageUpdatePreflight {
	failure := pageUpdateFailure(syntax)
	if !request.Validate {
		return PageUpdatePreflight{Failure: &failure}
	}
	if _, ok := pageWriteParseResources(request.Resources); !ok {
		argument := pageUpdateFailure(pageWriteInvalidResources)
		return PageUpdatePreflight{Failure: &argument}
	}
	if _, problem := pageWriteParseOptionalProperties(request.OptionalProperties); problem != "" {
		argument := pageUpdateFailure(problem)
		return PageUpdatePreflight{Failure: &argument}
	}
	if len(pageValidateMarkerIntegrity(request.Body)) == 0 {
		if problems := pageValidateMarkerContent(request.Body); len(problems) > 0 {
			content := pageUpdateFailure("Validation failed: " + strings.Join(problems, "; "))
			return PageUpdatePreflight{Failure: &content}
		}
	}
	return PageUpdatePreflight{Failure: &failure, SyntaxOnly: true}
}

// ArmPageBaseline is PageBaselineGuard.TryArm for this client: a call that named neither an environment nor a
// URI (this server's CREATIO_* default) is identified by the configured URL.
func (c *Client) ArmPageBaseline(request *PageUpdateRequest, outputDirectory string) (string, bool, string) {
	c.pageWriteIdentity(request)
	return pageWriteArm(request, outputDirectory)
}

func (c *Client) pageWriteIdentity(request *PageUpdateRequest) {
	if strings.TrimSpace(request.EnvironmentName) == "" && strings.TrimSpace(request.EnvironmentURI) == "" {
		request.EnvironmentURI = strings.TrimSpace(c.config.BaseURL)
	}
}

// RefreshPageBaseline is PageBaselineGuard.RefreshOrDrop.
func RefreshPageBaseline(metaPath string, request *PageUpdateRequest, response *PageUpdateResponse) string {
	return pageWriteRefreshOrDrop(metaPath, request, response)
}

// VerifyPage is update-page's best-effort read-back: the page metadata get-page would answer.
func (c *Client) VerifyPage(ctx context.Context, schemaName string, includeOperations *bool) (page *PageMetadata) {
	defer func() {
		if recover() != nil {
			page = nil
		}
	}()
	result := c.GetPage(ctx, PageGetRequest{SchemaName: schemaName, IncludeOperations: includeOperations})
	if !result.Success {
		return nil
	}
	return result.Page
}
