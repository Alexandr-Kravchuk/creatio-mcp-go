package creatio

// create-app: clio's ApplicationCreateTool (argument checks, optional template data), its Data Forge
// enrichment (ApplicationCreateEnrichmentService + DataForgeEnrichmentBuilder) and ApplicationCreateService
// (caption-script guard, code sanitizing, icon and color, CreateApp, timeout polling, readback, OData build
// gate, navigation cache reset).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/redact"
)

// AppCreateRequest is clio's ApplicationCreateArgs as the tool reads it. Nil pointers are absent arguments.
type AppCreateRequest struct {
	Name                     string
	Code                     string
	TemplateCode             *string
	Description              *string
	IconBackground           string
	IconID                   string
	ClientTypeID             string
	WithMobilePages          bool
	OptionalTemplateDataJSON string
	// LocalizationMaps is true when any title/description/caption/name-localizations argument was sent.
	LocalizationMaps bool
}

// AppTemplateData is clio's ApplicationOptionalTemplateData.
type AppTemplateData struct {
	EntitySchemaName        *string `json:"entitySchemaName,omitempty"`
	UseExistingEntitySchema *bool   `json:"useExistingEntitySchema,omitempty"`
	UseAIContentGeneration  *bool   `json:"useAIContentGeneration,omitempty"`
	AppSectionDescription   *string `json:"appSectionDescription,omitempty"`
}

// AppCreateResponse is clio's ApplicationContextResponse as create-app returns it.
type AppCreateResponse struct {
	Success                 bool               `json:"success"`
	PackageUID              string             `json:"package-u-id,omitempty"`
	PackageName             string             `json:"package-name,omitempty"`
	CanonicalMainEntityName string             `json:"canonical-main-entity-name,omitempty"`
	ApplicationID           string             `json:"application-id,omitempty"`
	ApplicationName         string             `json:"application-name,omitempty"`
	ApplicationCode         string             `json:"application-code,omitempty"`
	ApplicationVersion      *string            `json:"application-version,omitempty"`
	Entities                []AppEntityInfo    `json:"entities,omitzero"`
	Pages                   []PageListItem     `json:"pages,omitzero"`
	SchemaNamePrefix        *string            `json:"schema-name-prefix,omitempty"`
	DataForge               *AppWriteDataForge `json:"dataforge,omitempty"`
	Error                   string             `json:"error,omitempty"`
	Warnings                []string           `json:"warnings,omitempty"`
	NextStep                string             `json:"next-step,omitempty"`
}

// AppWriteDataForge is clio's ApplicationDataForgeResult.
type AppWriteDataForge struct {
	Used           bool                        `json:"used"`
	Health         *DataForgeHealth            `json:"health,omitempty"`
	Status         *DataForgeMaintenanceStatus `json:"status,omitempty"`
	Coverage       *DataForgeCoverage          `json:"coverage,omitempty"`
	Warnings       []string                    `json:"warnings"`
	ContextSummary *AppWriteDataForgeSummary   `json:"context-summary,omitempty"`
}

// AppWriteDataForgeSummary is clio's ApplicationDataForgeContextSummary.
type AppWriteDataForgeSummary struct {
	SimilarTables  []DataForgeSimilarTable  `json:"similar-tables"`
	SimilarLookups []DataForgeSimilarLookup `json:"similar-lookups"`
	RelationPairs  []string                 `json:"relation-pairs"`
	ColumnHints    []AppWriteColumnHint     `json:"column-hints"`
}

// AppWriteColumnHint is clio's ApplicationDataForgeColumnHint.
type AppWriteColumnHint struct {
	TableName           string `json:"table-name"`
	ColumnCount         int    `json:"column-count"`
	RequiredColumnCount int    `json:"required-column-count"`
	LookupColumnCount   int    `json:"lookup-column-count"`
}

// appWriteKnownTemplates is ApplicationCreateTool.KnownTemplates.
var appWriteKnownTemplates = []string{"AppFreedomUI", "AppFreedomUIv2", "AppWithHomePage", "EmptyApp"}

const appWriteDefaultTemplate = "AppFreedomUI"

// EffectiveTemplate is the template the call uses: the trimmed argument, or AppFreedomUI when it is blank.
func (r AppCreateRequest) EffectiveTemplate() string {
	if r.TemplateCode == nil || strings.TrimSpace(*r.TemplateCode) == "" {
		return appWriteDefaultTemplate
	}
	return strings.TrimSpace(*r.TemplateCode)
}

// ValidateAppCreate is ApplicationCreateTool.ValidateCreateArgs followed by ParseOptionalTemplateData: the
// checks clio makes before it resolves the environment.
func ValidateAppCreate(request AppCreateRequest) (*AppTemplateData, error) {
	if strings.TrimSpace(request.Name) == "" {
		return nil, errors.New("name is required.")
	}
	if strings.TrimSpace(request.Code) == "" {
		return nil, errors.New("code is required.")
	}
	template := request.EffectiveTemplate()
	known := false
	for _, name := range appWriteKnownTemplates {
		if strings.EqualFold(name, template) {
			known = true
		}
	}
	if !known {
		raw := ""
		if request.TemplateCode != nil {
			raw = *request.TemplateCode
		}
		return nil, fmt.Errorf("Unknown template-code '%s'. Use the technical template name, not the display name. "+
			"Known templates: %s. Omit template-code to use the default AppFreedomUI.", raw, strings.Join(appWriteKnownTemplates, ", "))
	}
	if strings.TrimSpace(request.IconBackground) != "" {
		if err := appWriteValidatePalette(strings.TrimSpace(request.IconBackground)); err != nil {
			return nil, err
		}
	}
	if request.LocalizationMaps {
		return nil, errors.New("create-app is scalar-only. Do not send title-localizations, description-localizations, caption-localizations, or name-localizations.")
	}
	return appWriteParseTemplateData(request.OptionalTemplateDataJSON)
}

// appWriteParseTemplateData is ApplicationToolHelper.ParseOptionalTemplateData.
func appWriteParseTemplateData(text string) (*AppTemplateData, error) {
	if strings.TrimSpace(text) == "" {
		return nil, nil
	}
	var parsed *struct {
		EntitySchemaName        *string `json:"entitySchemaName"`
		UseExistingEntitySchema *bool   `json:"useExistingEntitySchema"`
		UseAIContentGeneration  *bool   `json:"useAiContentGeneration"`
		AppSectionDescription   *string `json:"appSectionDescription"`
	}
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		return nil, fmt.Errorf("Invalid optional-template-data-json format: %s (Parameter 'optionalTemplateDataJson')", err.Error())
	}
	if parsed == nil {
		return nil, nil
	}
	if parsed.UseAIContentGeneration != nil && *parsed.UseAIContentGeneration {
		return nil, errors.New("useAiContentGeneration=true is not supported in application tools.")
	}
	useExisting := parsed.UseExistingEntitySchema != nil && *parsed.UseExistingEntitySchema
	if parsed.EntitySchemaName != nil && strings.TrimSpace(*parsed.EntitySchemaName) != "" && !useExisting {
		return nil, errors.New("entitySchemaName is only valid together with useExistingEntitySchema=true. " +
			"The entity must already exist in Creatio before create-app is called. " +
			"To create a new app with an auto-generated entity, omit optional-template-data-json entirely.")
	}
	if useExisting && (parsed.EntitySchemaName == nil || strings.TrimSpace(*parsed.EntitySchemaName) == "") {
		return nil, errors.New("entitySchemaName is required when useExistingEntitySchema=true.")
	}
	return &AppTemplateData{EntitySchemaName: parsed.EntitySchemaName, UseExistingEntitySchema: parsed.UseExistingEntitySchema,
		UseAIContentGeneration: parsed.UseAIContentGeneration, AppSectionDescription: parsed.AppSectionDescription}, nil
}

// AppCreateFailure is clio's CreateContextErrorResponse.
func AppCreateFailure(message string) AppCreateResponse {
	return AppCreateResponse{Error: message}
}

// appWriteCreateTimings are the waits of the create path; tests shorten them.
type appWriteCreateTimings struct {
	pollDelay     time.Duration
	gateInterval  time.Duration
	createTimeout time.Duration
}

var appWriteCreateWaits = appWriteCreateTimings{pollDelay: 2 * time.Second, gateInterval: 3 * time.Second, createTimeout: 30 * time.Minute}

// CreateApp runs clio's create-app after ValidateAppCreate: Data Forge enrichment, then
// ApplicationCreateService.CreateApplication. stage reports clio's progress stages.
func (c *Client) CreateApp(ctx context.Context, request AppCreateRequest, templateData *AppTemplateData, stage func(string)) AppCreateResponse {
	if stage == nil {
		stage = func(string) {}
	}
	stage("enriching application model")
	forge := c.appWriteEnrich(ctx, request, templateData)
	response, err := c.appWriteCreate(ctx, request, templateData, stage)
	if err != nil {
		return AppCreateFailure(redact.Text(err.Error()))
	}
	response.DataForge = &forge
	return response
}

// appWriteEnrich is ApplicationCreateEnrichmentService.Enrich over the dataforge-context service. Data Forge is
// best-effort: a failure becomes a "dataforge:" warning.
func (c *Client) appWriteEnrich(ctx context.Context, request AppCreateRequest, data *AppTemplateData) AppWriteDataForge {
	var entity, sectionDescription *string
	if data != nil {
		entity, sectionDescription = data.EntitySchemaName, data.AppSectionDescription
	}
	description := request.Description
	name := &request.Name
	candidates := appWriteNormalizeTerms(name, description, sectionDescription, entity)
	lookups := appWriteNormalizeTerms(entity, sectionDescription, name)
	summary := ""
	for _, value := range []*string{description, sectionDescription, name} {
		if value != nil && strings.TrimSpace(*value) != "" {
			summary = strings.TrimSpace(*value)
			break
		}
	}
	forgeContext := c.DataForgeContext(ctx, DataForgeContextRequest{RequirementSummary: summary, CandidateTerms: candidates, LookupHints: lookups})
	if !forgeContext.Success {
		message := "Data Forge context aggregation failed."
		if forgeContext.Error != nil {
			message = forgeContext.Error.Message
		}
		return AppWriteDataForge{Used: true, Coverage: &DataForgeCoverage{},
			Warnings: []string{"dataforge:" + redact.Text(message)},
			ContextSummary: &AppWriteDataForgeSummary{SimilarTables: []DataForgeSimilarTable{}, SimilarLookups: []DataForgeSimilarLookup{},
				RelationPairs: []string{}, ColumnHints: []AppWriteColumnHint{}}}
	}
	hints := []AppWriteColumnHint{}
	if forgeContext.Columns != nil {
		keys := append([]string{}, forgeContext.Columns.keys...)
		sort.SliceStable(keys, func(i, j int) bool { return compareOrdinalIgnoreCase(keys[i], keys[j]) < 0 })
		for _, key := range keys {
			columns, _ := forgeContext.Columns.values[key].([]DataForgeColumn)
			hint := AppWriteColumnHint{TableName: key, ColumnCount: len(columns)}
			for _, column := range columns {
				if column.Required {
					hint.RequiredColumnCount++
				}
				if column.ReferenceSchemaName != nil && strings.TrimSpace(*column.ReferenceSchemaName) != "" {
					hint.LookupColumnCount++
				}
			}
			hints = append(hints, hint)
		}
	}
	pairs := []string{}
	if forgeContext.Relations != nil {
		pairs = append(pairs, forgeContext.Relations.keys...)
		sort.SliceStable(pairs, func(i, j int) bool { return compareOrdinalIgnoreCase(pairs[i], pairs[j]) < 0 })
	}
	coverage := forgeContext.Coverage
	warnings := forgeContext.Warnings
	if warnings == nil {
		warnings = []string{}
	}
	return AppWriteDataForge{Used: true, Health: forgeContext.Health, Status: forgeContext.Status, Coverage: &coverage, Warnings: warnings,
		ContextSummary: &AppWriteDataForgeSummary{SimilarTables: forgeContext.SimilarTables, SimilarLookups: forgeContext.SimilarLookups,
			RelationPairs: pairs, ColumnHints: hints}}
}

// appWriteNormalizeTerms is NormalizeTerms: non-blank values, trimmed, distinct ignoring case.
func appWriteNormalizeTerms(values ...*string) []*string {
	var terms []*string
	seen := map[string]bool{}
	for _, value := range values {
		if value == nil || strings.TrimSpace(*value) == "" {
			continue
		}
		trimmed := strings.TrimSpace(*value)
		if key := strings.ToUpper(trimmed); !seen[key] {
			seen[key] = true
			terms = append(terms, &trimmed)
		}
	}
	return terms
}

// appWriteCreateTimeout is ApplicationCreateService.TimeoutRegex: the App Installer's own timeout text.
var appWriteCreateTimeout = regexp.MustCompile(`(?is)(App Installer CreateApp request failed.*timeout of \d+ms exceeded|timeout of \d+ms exceeded)`)

// appWriteCreate is ApplicationCreateService.CreateApplicationCore.
func (c *Client) appWriteCreate(ctx context.Context, request AppCreateRequest, data *AppTemplateData, stage func(string)) (AppCreateResponse, error) {
	if strings.TrimSpace(request.ClientTypeID) != "" {
		if _, ok := parseGUID(request.ClientTypeID); !ok {
			return AppCreateResponse{}, errors.New("Client type id must be a valid GUID. (Parameter 'request')")
		}
	}
	if strings.TrimSpace(request.IconID) != "" && !strings.EqualFold(request.IconID, "auto") {
		if _, ok := parseGUID(request.IconID); !ok {
			return AppCreateResponse{}, errors.New("Icon id must be a valid GUID or 'auto'. (Parameter 'request')")
		}
	}
	culture := c.schemaWriteProfileCulture(ctx)
	if err := schemaWriteCaptionMatchesCulture(culture, request.Name, "name"); err != nil {
		return AppCreateResponse{}, err
	}
	if request.Description != nil {
		if err := schemaWriteCaptionMatchesCulture(culture, *request.Description, "description"); err != nil {
			return AppCreateResponse{}, err
		}
	}
	rawPrefix, err := c.readSysSettingValue(ctx, schemaNamePrefixSettingCode)
	if err != nil {
		return AppCreateResponse{}, err
	}
	prefix := strings.TrimSpace(strings.Trim(strings.TrimSpace(rawPrefix), `"`))
	code, err := appWriteSanitizeCode(request.Code, prefix)
	if err != nil {
		return AppCreateResponse{}, err
	}
	iconBackground := strings.TrimSpace(request.IconBackground)
	if iconBackground == "" {
		iconBackground = appWritePickColor()
	}
	iconID := ""
	if strings.TrimSpace(request.IconID) == "" || strings.EqualFold(request.IconID, "auto") {
		if iconID, err = c.appWriteRandomIconID(ctx, true); err != nil {
			return AppCreateResponse{}, err
		}
	} else {
		iconID, _ = parseGUID(request.IconID)
	}
	var clientType *string
	if strings.TrimSpace(request.ClientTypeID) != "" {
		value, _ := parseGUID(request.ClientTypeID)
		clientType = &value
	} else if !request.WithMobilePages {
		value := appWriteWebClientTypeID
		clientType = &value
	}
	var description *string
	if request.Description != nil {
		trimmed := strings.TrimSpace(*request.Description)
		description = &trimmed
	}
	optional := AppTemplateData{}
	if data != nil {
		optional = *data
	}
	body := map[string]any{"name": strings.TrimSpace(request.Name), "iconBackground": iconBackground,
		"templateCode": request.EffectiveTemplate(), "iconId": iconID, "code": code, "optionalTemplateData": optional}
	if description != nil {
		body["description"] = *description
	}
	if clientType != nil {
		body["clientTypeId"] = *clientType
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return AppCreateResponse{}, err
	}
	c.appWriteODataGate(ctx, appWriteCreateWaits.gateInterval)
	stage("creating application package")
	response, err := c.serviceRequest(ctx, serviceCall{Route: "ServiceModel/AppInstallerService.svc/CreateApp", Body: encoded,
		Timeout: appWriteCreateWaits.createTimeout, Label: "App Installer CreateApp"})
	timedOut := err != nil && isTransportError(err) && appWriteIsTimeout(err)
	if err == nil && appWriteCreateTimeout.MatchString(string(response.payload)) && !appWriteCreateSucceeded(response.payload) {
		timedOut = true
	}
	if timedOut {
		stage("waiting for application to be ready")
		info, err := c.appWriteLoadWithRetry(ctx, "", code, fmt.Sprintf("CreateApp request timed out and application '%s' could not be loaded", code))
		if err != nil {
			return AppCreateResponse{}, err
		}
		return c.appWriteCreated(ctx, info, prefix), nil
	}
	if err != nil {
		return AppCreateResponse{}, err
	}
	if response.status < 200 || response.status >= 300 {
		if looksLikeHTML(response.payload) || strings.TrimSpace(string(response.payload)) == "" {
			return AppCreateResponse{}, fmt.Errorf("App Installer CreateApp returned HTTP %d", response.status)
		}
	}
	if strings.TrimSpace(string(response.payload)) == "" {
		return AppCreateResponse{}, errors.New("CreateApp returned an empty response.")
	}
	var answer struct {
		Success   bool `json:"success"`
		ErrorInfo *struct {
			Message string `json:"message"`
		} `json:"errorInfo"`
		Value              *string `json:"value"`
		DependenciesErrors []struct {
			Source    string `json:"source"`
			Reference string `json:"reference"`
			Package   string `json:"package"`
		} `json:"dependenciesErrors"`
	}
	if err := json.Unmarshal(response.payload, &answer); err != nil {
		return AppCreateResponse{}, fmt.Errorf("CreateApp returned an unreadable response: %w", err)
	}
	if !answer.Success {
		message := "Failed to create application."
		if answer.ErrorInfo != nil && strings.TrimSpace(answer.ErrorInfo.Message) != "" {
			message = answer.ErrorInfo.Message
		}
		if len(answer.DependenciesErrors) > 0 {
			parts := make([]string, 0, len(answer.DependenciesErrors))
			for _, dependency := range answer.DependenciesErrors {
				var fields []string
				if strings.TrimSpace(dependency.Source) != "" {
					fields = append(fields, "source="+dependency.Source)
				}
				if strings.TrimSpace(dependency.Reference) != "" {
					fields = append(fields, "reference="+dependency.Reference)
				}
				if strings.TrimSpace(dependency.Package) != "" {
					fields = append(fields, "package="+dependency.Package)
				}
				if len(fields) == 0 {
					parts = append(parts, "unknown dependency error")
				} else {
					parts = append(parts, strings.Join(fields, ", "))
				}
			}
			message += " Dependencies: " + strings.Join(parts, "; ")
		}
		return AppCreateResponse{}, errors.New(message)
	}
	if answer.Value == nil {
		return AppCreateResponse{}, errors.New("CreateApp returned an invalid application identifier.")
	}
	if _, ok := parseGUID(*answer.Value); !ok {
		return AppCreateResponse{}, errors.New("CreateApp returned an invalid application identifier.")
	}
	stage("loading application metadata")
	info, err := c.appWriteLoadWithRetry(ctx, *answer.Value, code, fmt.Sprintf("Application '%s' was created but its metadata could not be loaded", code))
	if err != nil {
		return AppCreateResponse{}, err
	}
	c.appWriteODataGate(ctx, appWriteCreateWaits.gateInterval)
	return c.appWriteCreated(ctx, info, prefix), nil
}

// appWriteCreateSucceeded reports a CreateApp body that says success:true.
func appWriteCreateSucceeded(payload []byte) bool {
	var answer struct {
		Success bool `json:"success"`
	}
	return json.Unmarshal(payload, &answer) == nil && answer.Success
}

// appWriteCreated maps the readback (ApplicationToolResultMapper.Map) with the prefix the service read, then
// resets the navigation cache (WithNavigationCacheReset).
func (c *Client) appWriteCreated(ctx context.Context, info AppInfoResponse, prefix string) AppCreateResponse {
	response := AppCreateResponse{Success: true, PackageUID: info.PackageUID, PackageName: info.PackageName,
		CanonicalMainEntityName: info.CanonicalMainEntityName, ApplicationID: info.ApplicationID,
		ApplicationName: info.ApplicationName, ApplicationCode: info.ApplicationCode,
		ApplicationVersion: info.ApplicationVersion, Entities: info.Entities, Pages: info.Pages, SchemaNamePrefix: &prefix}
	if warning := c.appWriteResetNavigation(ctx); warning != "" {
		response.Warnings = []string{warning}
	}
	response.NextStep = c.appWriteBrowserSessionNote()
	return response
}

// appWriteLoadWithRetry is LoadApplicationInfoWithRetry: 15 attempts, 2 s apart.
func (c *Client) appWriteLoadWithRetry(ctx context.Context, id, code, failurePrefix string) (AppInfoResponse, error) {
	const attempts = 15
	var last error
	for attempt := 1; attempt <= attempts; attempt++ {
		info, err := c.appWriteLoadInfo(ctx, id, code)
		if err == nil {
			return info, nil
		}
		if !appWriteRetryable(ctx, err) {
			return AppInfoResponse{}, err
		}
		last = err
		if attempt < attempts && !appWriteSleep(ctx, appWriteCreateWaits.pollDelay) {
			return AppInfoResponse{}, ctx.Err()
		}
	}
	message := fmt.Sprintf("%s after %d attempts.", failurePrefix, attempts)
	if strings.TrimSpace(last.Error()) != "" {
		message += " Last error: " + last.Error()
	}
	return AppInfoResponse{}, errors.New(message)
}

// appWriteSanitizeCode is ApplicationCreateService.SanitizeCode: words of letters and digits, each with an
// upper-case first letter, the SchemaNamePrefix applied once, "_" before a leading digit.
func appWriteSanitizeCode(code, prefix string) (string, error) {
	trimmed := strings.TrimSpace(code)
	if trimmed == "" {
		return "", errors.New("Application code is required. (Parameter 'code')")
	}
	noValid := fmt.Errorf("Application code '%s' contains no valid characters. (Parameter 'code')", code)
	words := strings.FieldsFunc(trimmed, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.Is(unicode.Nd, r) })
	if len(words) == 0 {
		return "", noValid
	}
	first := appWriteNormalizeWord(words[0], true)
	if first == "" {
		return "", noValid
	}
	var builder strings.Builder
	if prefix == "" {
		builder.WriteString(first)
	} else if len([]rune(first)) >= len([]rune(prefix)) && strings.EqualFold(string([]rune(first)[:len([]rune(prefix))]), prefix) {
		builder.WriteString(prefix)
		if rest := string([]rune(first)[len([]rune(prefix)):]); rest != "" {
			builder.WriteString(appWriteNormalizeWord(rest, true))
		}
	} else {
		builder.WriteString(prefix)
		builder.WriteString(first)
	}
	for _, word := range words[1:] {
		builder.WriteString(appWriteNormalizeWord(word, true))
	}
	result := []rune(builder.String())
	prefixLength := len([]rune(prefix))
	if len(result) <= prefixLength {
		return "", noValid
	}
	if unicode.IsDigit(result[prefixLength]) {
		if prefixLength == 0 {
			return "", fmt.Errorf("Application code '%s' starts with a digit. Prefix the code with a letter or configure SchemaNamePrefix in the environment. (Parameter 'code')", code)
		}
		result = append(result[:prefixLength], append([]rune{'_'}, result[prefixLength:]...)...)
	}
	return string(result), nil
}

// appWriteNormalizeWord is NormalizeWord: keep letters and digits (any script when unicodeLetters, ASCII
// only otherwise), upper-case the first one.
func appWriteNormalizeWord(word string, unicodeLetters bool) string {
	var kept []rune
	for _, r := range word {
		if unicodeLetters && (unicode.IsLetter(r) || unicode.Is(unicode.Nd, r)) ||
			!unicodeLetters && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			kept = append(kept, r)
		}
	}
	if len(kept) == 0 {
		return ""
	}
	kept[0] = unicode.ToUpper(kept[0])
	return string(kept)
}
