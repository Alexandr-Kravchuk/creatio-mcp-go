package creatio

// create-app-section: clio's ApplicationSectionCreateService — validation, section code, entity checks, the
// guarded InsertQuery with clio's recovery state machine (committed / detail-less rejection verified and retried
// once / timeout verified with backoff), the readback with the icon-background UpdateQuery, and the failure
// classification (error-class, section-created, retry-guidance).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/redact"
)

// AppSectionCreateRequest is clio's ApplicationSectionCreateRequest after the tool resolved the color.
type AppSectionCreateRequest struct {
	ApplicationCode  string
	Caption          string
	Description      *string
	EntitySchemaName *string
	WithMobilePages  bool
	IconBackground   *string
	CaptionCulture   string
	Code             string
}

// AppSectionCreateResponse is clio's ApplicationSectionContextResponse.
type AppSectionCreateResponse struct {
	Success            bool           `json:"success"`
	PackageUID         string         `json:"package-u-id,omitempty"`
	PackageName        string         `json:"package-name,omitempty"`
	ApplicationID      string         `json:"application-id,omitempty"`
	ApplicationName    string         `json:"application-name,omitempty"`
	ApplicationCode    string         `json:"application-code,omitempty"`
	ApplicationVersion *string        `json:"application-version,omitempty"`
	Section            *AppSection    `json:"section,omitempty"`
	Entity             *AppEntityInfo `json:"entity,omitempty"`
	Pages              []PageListItem `json:"pages,omitzero"`
	Error              string         `json:"error,omitempty"`
	ErrorClass         string         `json:"error-class,omitempty"`
	SectionCreated     string         `json:"section-created,omitempty"`
	RetryGuidance      string         `json:"retry-guidance,omitempty"`
	Warnings           []string       `json:"warnings,omitempty"`
	NextStep           string         `json:"next-step,omitempty"`
}

// AppSectionCreateFailure is clio's CreateSectionContextErrorResponse for an unclassified failure.
func AppSectionCreateFailure(message string) AppSectionCreateResponse {
	return AppSectionCreateResponse{Error: message}
}

// AppSectionInProgress is ApplicationToolHelper.CreateSectionInProgressResponse: the answer once clio's
// response deadline passed while the section is still being created.
func AppSectionInProgress(caption, code string) AppSectionCreateResponse {
	hint := ""
	if strings.TrimSpace(code) != "" {
		trimmed := strings.TrimSpace(code)
		hint = fmt.Sprintf(" (code '%s', pages '%s_ListPage' / '%s_FormPage')", trimmed, trimmed, trimmed)
	}
	return AppSectionCreateResponse{
		Error:          fmt.Sprintf("Section '%s'%s is still being created server-side and did not finish within the response deadline.", caption, hint),
		ErrorClass:     "creatio-timeout",
		SectionCreated: "in-progress",
		RetryGuidance: "The section creation is still running on the server. Do NOT retry create-app-section " +
			"(a retry would create a duplicate section) and do NOT fall back to create-page. Wait a " +
			"short while, then poll list-app-sections and get-app-info until the section and its " +
			"generated List and Form pages appear; only then continue. If the section still does not " +
			"appear after several minutes of polling, the background creation has failed (not merely " +
			"slowed) and a single retry of create-app-section is then safe.",
	}
}

// ValidateAppSectionCreate is ApplicationSectionCreateTool.ValidateSectionCreateArgs.
func ValidateAppSectionCreate(applicationCode, caption string, localizationMaps bool) error {
	if strings.TrimSpace(applicationCode) == "" {
		return errors.New("application-code is required.")
	}
	if strings.TrimSpace(caption) == "" {
		return errors.New("caption is required.")
	}
	if localizationMaps {
		return errors.New("create-app-section is scalar-only. Do not send title-localizations, description-localizations, caption-localizations, or name-localizations.")
	}
	return nil
}

// appWriteSectionFailure is clio's ApplicationSectionCreateException.
type appWriteSectionFailure struct {
	message  string
	class    string
	created  *bool
	guidance string
}

func (f *appWriteSectionFailure) Error() string { return f.message }

func (f *appWriteSectionFailure) response() AppSectionCreateResponse {
	created := "unknown"
	if f.created != nil {
		created = fmt.Sprint(*f.created)
	}
	return AppSectionCreateResponse{Error: f.message, ErrorClass: f.class, SectionCreated: created, RetryGuidance: f.guidance}
}

const (
	appWriteTransportGuidance = "The request never reached Creatio, so no section was created and retrying is safe. " +
		"Verify the environment URL and connectivity first (clio ping -e <env> / clio get-info -e <env>), " +
		"then retry create-app-section."
	appWriteServerErrorGuidance = "Creatio rejected the operation, so retrying with the same arguments will most likely fail again. " +
		"Inspect the error, fix the inputs or the server state, and use list-app-sections to inspect " +
		"existing sections before retrying."
	appWriteTimeoutNotCreatedGuidance = "Do not retry immediately: Creatio may still be processing the insert, and a retry can create a " +
		"duplicate section or fail with an 'already bound' error. Wait a few minutes, then run " +
		"list-app-sections; if the section appeared, the operation completed despite the timeout. " +
		"Retry only if the section is still absent and the environment is healthy (clio healthcheck -e <env>). " +
		"To extend the budget, set the CLIO_CREATE_SECTION_TIMEOUT_SECONDS environment variable."
	appWriteTimeoutUnknownGuidance = "Do not retry blindly: the post-timeout verification readback also failed, so the section may or may " +
		"not have been created. Check environment health (clio healthcheck -e <env>), wait a few minutes, " +
		"then run list-app-sections to verify the section state before any retry."
	appWritePreparationGuidance = "No section insert was attempted, so no section was created and retrying is safe once the underlying " +
		"issue is resolved. Verify the environment first (clio ping -e <env> / clio healthcheck -e <env>), " +
		"then retry create-app-section."
	appWriteContentionGuidance = "Creatio aborted the section insert without a detailed reason. This can happen when sections are " +
		"created in the same application in parallel, but a detail-less rejection can equally be a " +
		"server-side failure unrelated to concurrency — or a plain code collision — because the server " +
		"returned no detail to tell them apart. Do not blindly retry: first run list-app-sections to see " +
		"the current sections. A section with the generated or explicit code may already exist; if so, " +
		"change the caption or pass a different --code and try again. If you were creating sections " +
		"concurrently, create them one at a time (clio serializes and retries once automatically). If a " +
		"single sequential create with a fresh code still fails, treat it as a server-side issue — check " +
		"environment health (clio healthcheck -e <env>) and the Creatio server logs before retrying."
)

// appWriteSectionTimings are the section path's waits; tests shorten them.
type appWriteSectionTimings struct {
	insert, readback, verify, pollDelay, recoveryInitial, recoveryMax, recoveryBudget, serializationCap time.Duration
}

// appWriteSectionWaits is the MCP path: BackgroundInsertTimeoutMs, BackgroundReadbackTimeoutMs and clio's
// verification budgets.
var appWriteSectionWaits = appWriteSectionTimings{insert: 600 * time.Second, readback: 30 * time.Second,
	verify: 30 * time.Second, pollDelay: 2 * time.Second, recoveryInitial: 2 * time.Second, recoveryMax: 8 * time.Second,
	recoveryBudget: 40 * time.Second, serializationCap: 120 * time.Second}

// appWriteSectionLocks is SectionCreateSerializationGuard: one creation at a time per environment and app.
var appWriteSectionLocks = struct {
	sync.Mutex
	gates map[string]chan struct{}
}{gates: map[string]chan struct{}{}}

func (c *Client) appWriteSerialized(ctx context.Context, applicationCode string, wait time.Duration, work func() (appWriteInsertOutcome, error)) (appWriteInsertOutcome, error) {
	key := strings.ToLower(strings.TrimRight(strings.TrimSpace(c.config.BaseURL), "/")) + "\x1f" + strings.ToLower(applicationCode)
	appWriteSectionLocks.Lock()
	gate, ok := appWriteSectionLocks.gates[key]
	if !ok {
		gate = make(chan struct{}, 1)
		appWriteSectionLocks.gates[key] = gate
	}
	appWriteSectionLocks.Unlock()
	timer := time.NewTimer(wait)
	defer timer.Stop()
	acquired := false
	select {
	case gate <- struct{}{}:
		acquired = true
	case <-timer.C:
		// clio proceeds without serialization (best-effort); contention is recovered below.
	case <-ctx.Done():
		return 0, ctx.Err()
	}
	if acquired {
		defer func() { <-gate }()
	}
	return work()
}

type appWriteInsertOutcome int

const (
	appWriteCommitted appWriteInsertOutcome = iota + 1
	appWriteContention
	appWriteTimedOut
)

// appWriteResolvedSection is ResolvedApplicationSectionCreateRequest.
type appWriteResolvedSection struct {
	id, applicationID, applicationName, applicationCode string
	applicationVersion                                  *string
	packageUID, packageName, caption, sectionCode       string
	description, entitySchemaName                       *string
	iconID, iconBackground                              string
	clientTypeID                                        *string
}

var appWriteSectionCodePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)

// CreateAppSection is ApplicationSectionCreateService.CreateSection (the MCP overload: 600 s insert budget,
// 30 s readback, contention retry enabled).
func (c *Client) CreateAppSection(ctx context.Context, request AppSectionCreateRequest, stage func(string)) AppSectionCreateResponse {
	if stage == nil {
		stage = func(string) {}
	}
	result, err := c.appWriteCreateSection(ctx, request, stage)
	if err != nil {
		var failure *appWriteSectionFailure
		if errors.As(err, &failure) {
			failure.message = redact.Text(failure.message)
			return failure.response()
		}
		return AppSectionCreateFailure(redact.Text(err.Error()))
	}
	return result
}

func (c *Client) appWriteCreateSection(ctx context.Context, request AppSectionCreateRequest, stage func(string)) (AppSectionCreateResponse, error) {
	if strings.TrimSpace(request.ApplicationCode) == "" {
		return AppSectionCreateResponse{}, errors.New("application-code is required.")
	}
	if strings.TrimSpace(request.Caption) == "" {
		return AppSectionCreateResponse{}, errors.New("caption is required.")
	}
	if request.IconBackground != nil {
		if err := appWriteValidatePalette(*request.IconBackground); err != nil {
			return AppSectionCreateResponse{}, err
		}
	}
	if strings.TrimSpace(request.Code) != "" {
		trimmed := strings.TrimSpace(request.Code)
		for _, r := range trimmed {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_') {
				return AppSectionCreateResponse{}, fmt.Errorf("Section code '%s' is invalid. Codes must contain only Latin letters, digits, or underscore. (Parameter 'request')", trimmed)
			}
		}
	}
	effectiveCulture, err := c.appWriteCaptionCulture(ctx, request.CaptionCulture)
	if err != nil {
		return AppSectionCreateResponse{}, err
	}
	profileCulture, _ := c.appWriteCaptionCulture(ctx, "")
	if err := schemaWriteCaptionMatchesCulture(profileCulture, request.Caption, "caption"); err != nil {
		return AppSectionCreateResponse{}, err
	}
	if request.Description != nil {
		if err := schemaWriteCaptionMatchesCulture(profileCulture, *request.Description, "description"); err != nil {
			return AppSectionCreateResponse{}, err
		}
	}
	before, resolved, body, err := c.appWritePrepareSection(ctx, request, stage)
	if err != nil {
		if class := appWriteClassify(err); class != "" {
			return AppSectionCreateResponse{}, &appWriteSectionFailure{
				message: fmt.Sprintf("Failed to create section '%s' in application '%s': a preparation step failed before the section insert was attempted (%s).",
					request.Caption, request.ApplicationCode, appWriteRootCause(err)),
				class: class, created: appWriteBool(false), guidance: appWritePreparationGuidance}
		}
		return AppSectionCreateResponse{}, err
	}
	stage("creating section")
	if err := c.appWriteCommitSection(ctx, request.ApplicationCode, resolved, body); err != nil {
		return AppSectionCreateResponse{}, err
	}
	stage("loading created section")
	created, err := c.appWriteLoadCreatedSection(ctx, before, resolved, effectiveCulture)
	if err != nil {
		return AppSectionCreateResponse{}, err
	}
	if warning := c.appWriteResetNavigation(ctx); warning != "" {
		created.Warnings = append(created.Warnings, warning)
	}
	created.NextStep = c.appWriteBrowserSessionNote()
	return created, nil
}

func appWriteBool(value bool) *bool { return &value }

// appWritePrepareSection is the try block before the insert: prefix, application info, resolved request,
// insert body, entity-schema check.
func (c *Client) appWritePrepareSection(ctx context.Context, request AppSectionCreateRequest, stage func(string)) (AppInfoResponse, appWriteResolvedSection, []byte, error) {
	rawPrefix, err := c.readSysSettingValue(ctx, schemaNamePrefixSettingCode)
	if err != nil {
		return AppInfoResponse{}, appWriteResolvedSection{}, nil, err
	}
	prefix := strings.TrimSpace(strings.Trim(strings.TrimSpace(rawPrefix), `"`))
	stage("loading application info")
	before, err := c.appWriteLoadInfo(ctx, "", request.ApplicationCode)
	if err != nil {
		return AppInfoResponse{}, appWriteResolvedSection{}, nil, err
	}
	sectionCode, err := appWriteSectionCode(request, prefix)
	if err != nil {
		return AppInfoResponse{}, appWriteResolvedSection{}, nil, err
	}
	iconBackground := ""
	if request.IconBackground == nil || strings.TrimSpace(*request.IconBackground) == "" {
		iconBackground = appWritePickColor()
	} else {
		iconBackground = strings.TrimSpace(*request.IconBackground)
	}
	iconID, err := c.appWriteRandomIconID(ctx, false)
	if err != nil {
		return AppInfoResponse{}, appWriteResolvedSection{}, nil, err
	}
	if before.ApplicationID == "" {
		return AppInfoResponse{}, appWriteResolvedSection{}, nil, errors.New("Application id was not returned by get-app-info.")
	}
	resolved := appWriteResolvedSection{id: newGUID(), applicationID: before.ApplicationID, applicationName: before.ApplicationName,
		applicationCode: before.ApplicationCode, applicationVersion: before.ApplicationVersion, packageUID: before.PackageUID,
		packageName: before.PackageName, caption: strings.TrimSpace(request.Caption), sectionCode: sectionCode,
		description: appWriteTrimmed(request.Description), entitySchemaName: appWriteTrimmed(request.EntitySchemaName),
		iconID: iconID, iconBackground: iconBackground}
	if !request.WithMobilePages {
		value := appWriteWebClientTypeID
		resolved.clientTypeID = &value
	}
	items := map[string]any{
		"Id":             appWriteParam(appWriteGUIDDataValueType, resolved.id),
		"Caption":        appWriteParam(appWriteTextDataValueType, resolved.caption),
		"ApplicationId":  appWriteParam(appWriteGUIDDataValueType, resolved.applicationID),
		"PackageId":      appWriteParam(appWriteGUIDDataValueType, resolved.packageUID),
		"LogoId":         appWriteParam(appWriteGUIDDataValueType, resolved.iconID),
		"IconBackground": appWriteParam(appWriteTextDataValueType, resolved.iconBackground),
		"Type":           appWriteParam(appWriteIntDataValueType, 0),
		"Code":           appWriteParam(appWriteTextDataValueType, resolved.sectionCode),
	}
	if resolved.description != nil && *resolved.description != "" {
		items["Description"] = appWriteParam(appWriteTextDataValueType, *resolved.description)
	}
	if resolved.entitySchemaName != nil && *resolved.entitySchemaName != "" {
		items["EntitySchemaName"] = appWriteParam(appWriteTextDataValueType, *resolved.entitySchemaName)
	}
	if resolved.clientTypeID != nil {
		items["ClientTypeId"] = appWriteParam(appWriteGUIDDataValueType, *resolved.clientTypeID)
	}
	body, err := json.Marshal(map[string]any{"rootSchemaName": appWriteSectionSchema, "columnValues": map[string]any{"items": items}})
	if err != nil {
		return AppInfoResponse{}, appWriteResolvedSection{}, nil, err
	}
	if resolved.entitySchemaName == nil || *resolved.entitySchemaName == "" {
		err = c.appWriteEntityMustBeAbsent(ctx, sectionCode, request.Caption)
	} else {
		err = c.appWriteEntityMustExist(ctx, *resolved.entitySchemaName, request.Caption)
	}
	return before, resolved, body, err
}

func appWriteTrimmed(value *string) *string {
	if value == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	return &trimmed
}

func (c *Client) appWriteSysSchemaCount(ctx context.Context, name string) (int, error) {
	rows, err := c.selectRows(ctx, buildSelectQuery("SysSchema", map[string]string{"Name": "Name"},
		map[string]any{"filter0": comparisonFilter("Name", name, appWriteTextDataValueType, 3)}, 10000))
	return len(rows), err
}

// appWriteEntityMustBeAbsent is CheckEntitySchemaDoesNotExist: a schema named like the section code means the
// caller should reuse it. A failed lookup that is not a rejected query is ignored, as clio ignores it.
func (c *Client) appWriteEntityMustBeAbsent(ctx context.Context, schemaName, caption string) error {
	count, err := c.appWriteSysSchemaCount(ctx, schemaName)
	if err != nil {
		if isTransportError(err) || isAuthenticationError(err) {
			return nil
		}
		return err
	}
	if count > 0 {
		return fmt.Errorf("Entity schema '%s' already exists. To create section '%s' reusing the existing entity, add: --entity-schema-name %s", schemaName, caption, schemaName)
	}
	return nil
}

// appWriteEntityMustExist is CheckEntitySchemaExists: a lookup that fails is not a refusal.
func (c *Client) appWriteEntityMustExist(ctx context.Context, name, caption string) error {
	count, err := c.appWriteSysSchemaCount(ctx, name)
	if err != nil {
		return nil
	}
	if count == 0 {
		return fmt.Errorf("Entity schema '%s' does not exist in this environment, so section '%s' cannot be bound to it. "+
			"Verify the object name (names are case-sensitive), or omit --entity-schema-name to create a new object for the section.", name, caption)
	}
	return nil
}

// appWriteSectionCode is ResolveSectionCode.
func appWriteSectionCode(request AppSectionCreateRequest, prefix string) (string, error) {
	if strings.TrimSpace(request.Code) == "" {
		return appWriteCodeFromCaption(request.Caption, prefix)
	}
	code := strings.TrimSpace(request.Code)
	if prefix != "" {
		if len(code) >= len(prefix) && strings.EqualFold(code[:len(prefix)], prefix) {
			code = prefix + code[len(prefix):]
		} else {
			code = prefix + code
		}
	}
	if !appWriteSectionCodePattern.MatchString(code) {
		return "", fmt.Errorf("Section code '%s' is invalid. Section codes must start with a Latin letter and contain only "+
			"Latin letters, digits, or underscore. (Parameter 'request')", code)
	}
	return code, nil
}

// appWriteCodeFromCaption is GenerateCodeFromCaption: ASCII letters and digits of each word, capitalized.
func appWriteCodeFromCaption(caption, prefix string) (string, error) {
	refusal := fmt.Errorf("Caption '%s' has no Latin letters or digits to generate a section code. "+
		"Provide an explicit code via --code (for example --code UsrContacts), or use a Latin caption. (Parameter 'caption')", caption)
	words := strings.FieldsFunc(strings.TrimSpace(caption), func(r rune) bool { return !appWriteIsLetterOrDigit(r) })
	if len(words) == 0 {
		return "", refusal
	}
	var builder strings.Builder
	builder.WriteString(prefix)
	for _, word := range words {
		builder.WriteString(appWriteNormalizeWord(word, false))
	}
	code := builder.String()
	if len(code) == len(prefix) {
		return "", refusal
	}
	if code[len(prefix)] >= '0' && code[len(prefix)] <= '9' {
		code = code[:len(prefix)] + "_" + code[len(prefix):]
	}
	return code, nil
}

func appWriteIsLetterOrDigit(r rune) bool {
	return unicode.IsLetter(r) || unicode.Is(unicode.Nd, r)
}

// appWriteClassify is ClassifyInsertFailure for a Go error: "" when the failure is not a transport failure.
func appWriteClassify(err error) string {
	if !isTransportError(err) {
		if errors.Is(err, context.DeadlineExceeded) {
			return "creatio-timeout"
		}
		return ""
	}
	if appWriteIsTimeout(err) {
		return "creatio-timeout"
	}
	return "transport"
}

// appWriteRootCause is GetBaseException().Message: the innermost error's text.
func appWriteRootCause(err error) string {
	for {
		next := errors.Unwrap(err)
		if next == nil {
			return err.Error()
		}
		err = next
	}
}

// appWriteCommitSection is CommitSectionWithContentionRecovery.
func (c *Client) appWriteCommitSection(ctx context.Context, applicationCode string, resolved appWriteResolvedSection, body []byte) error {
	committed, err := c.appWriteGuardedInsert(ctx, applicationCode, resolved, body)
	if err != nil {
		return err
	}
	if committed {
		return nil
	}
	visible := c.appWriteVerifyPolling(ctx, resolved, 3, func(int) time.Duration { return appWriteSectionWaits.pollDelay }, time.Duration(math.MaxInt64))
	if visible != nil && *visible {
		return nil
	}
	if visible == nil {
		return appWriteContentionFailure(resolved, nil)
	}
	if !appWriteSleep(ctx, appWriteSectionWaits.pollDelay) {
		return ctx.Err()
	}
	retried, err := c.appWriteGuardedInsert(ctx, applicationCode, resolved, body)
	if err != nil {
		var failure *appWriteSectionFailure
		if errors.As(err, &failure) && failure.class == "server-error" {
			if again := c.appWriteVerifyPolling(ctx, resolved, 3, func(int) time.Duration { return appWriteSectionWaits.pollDelay }, time.Duration(math.MaxInt64)); again != nil && *again {
				return nil
			}
		}
		return err
	}
	if retried {
		return nil
	}
	after := c.appWriteVerifyPolling(ctx, resolved, 3, func(int) time.Duration { return appWriteSectionWaits.pollDelay }, time.Duration(math.MaxInt64))
	if after != nil && *after {
		return nil
	}
	return appWriteContentionFailure(resolved, after)
}

// appWriteGuardedInsert is CommitGuardedInsert: true committed, false detail-less rejection.
func (c *Client) appWriteGuardedInsert(ctx context.Context, applicationCode string, resolved appWriteResolvedSection, body []byte) (bool, error) {
	wait := min(appWriteSectionWaits.insert, appWriteSectionWaits.serializationCap)
	var timeoutCause error
	outcome, err := c.appWriteSerialized(ctx, applicationCode, wait, func() (appWriteInsertOutcome, error) {
		outcome, cause, err := c.appWriteInsertAttempt(ctx, resolved, body)
		timeoutCause = cause
		return outcome, err
	})
	if err != nil {
		return false, err
	}
	switch outcome {
	case appWriteTimedOut:
		return c.appWriteRecoverTimeout(ctx, resolved, timeoutCause)
	case appWriteCommitted:
		return true, nil
	default:
		return false, nil
	}
}

// appWriteInsertAttempt is TryCommitAttempt plus ClassifyInsertResponse.
func (c *Client) appWriteInsertAttempt(ctx context.Context, resolved appWriteResolvedSection, body []byte) (appWriteInsertOutcome, error, error) {
	response, err := c.serviceRequest(ctx, serviceCall{Route: "DataService/json/SyncReply/InsertQuery", Body: body, Timeout: appWriteSectionWaits.insert, Label: "DataService InsertQuery"})
	if err != nil {
		switch appWriteClassify(err) {
		case "creatio-timeout":
			return appWriteTimedOut, err, nil
		case "transport":
			return 0, nil, &appWriteSectionFailure{
				message: fmt.Sprintf("Failed to create section '%s' (code '%s'): the Creatio server could not be reached (%s). The request never reached the server, so no section was created.",
					resolved.caption, resolved.sectionCode, appWriteRootCause(err)),
				class: "transport", created: appWriteBool(false), guidance: appWriteTransportGuidance}
		default:
			return 0, nil, err
		}
	}
	if appWriteStatusIsTimeout(response.status) && !appWriteCreateSucceeded(response.payload) {
		return appWriteTimedOut, fmt.Errorf("DataService InsertQuery returned HTTP %d", response.status), nil
	}
	trimmed := bytes.TrimSpace(response.payload)
	var answer *struct {
		Success   bool `json:"success"`
		ErrorInfo *struct {
			Message *string `json:"message"`
		} `json:"errorInfo"`
	}
	if len(trimmed) == 0 || json.Unmarshal(trimmed, &answer) != nil {
		return 0, nil, &appWriteSectionFailure{
			message: fmt.Sprintf("Failed to create section '%s' (code '%s'): Creatio returned a non-JSON response to the section insert (an HTML error page is the usual cause), so the server is likely misconfigured or in a broken state.",
				resolved.caption, resolved.sectionCode),
			class: "server-error", guidance: appWriteServerErrorGuidance}
	}
	if answer == nil {
		return 0, nil, &appWriteSectionFailure{
			message: fmt.Sprintf("Failed to create section '%s' (code '%s'): Creatio returned an empty insert response, so the actual insert outcome is unknown.",
				resolved.caption, resolved.sectionCode),
			class: "server-error", guidance: appWriteServerErrorGuidance}
	}
	if answer.Success {
		return appWriteCommitted, nil, nil
	}
	var serverMessage *string
	if answer.ErrorInfo != nil {
		serverMessage = answer.ErrorInfo.Message
	}
	if appWriteDetailLess(serverMessage) {
		return appWriteContention, nil, nil
	}
	return 0, nil, &appWriteSectionFailure{message: appWriteInsertFailureMessage(resolved, serverMessage), class: "server-error",
		created: appWriteBool(false), guidance: appWriteServerErrorGuidance}
}

// appWriteDetailLess is IsDetailLessInsertRejection.
func appWriteDetailLess(message *string) bool {
	trimmed := ""
	if message != nil {
		trimmed = strings.TrimSpace(*message)
	}
	return trimmed == "" || strings.EqualFold(trimmed, "InsertQuery failed") || strings.EqualFold(trimmed, "InsertQuery failed.")
}

// appWriteInsertFailureMessage is BuildSectionInsertFailureMessage.
func appWriteInsertFailureMessage(resolved appWriteResolvedSection, serverMessage *string) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "Failed to create section '%s' (code '%s')", resolved.caption, resolved.sectionCode)
	if resolved.entitySchemaName != nil && strings.TrimSpace(*resolved.entitySchemaName) != "" {
		fmt.Fprintf(&builder, " bound to entity '%s'", *resolved.entitySchemaName)
	}
	if strings.TrimSpace(resolved.applicationCode) != "" {
		fmt.Fprintf(&builder, " in application '%s'", resolved.applicationCode)
	}
	builder.WriteString(".")
	if serverMessage != nil {
		if trimmed := strings.TrimSpace(*serverMessage); trimmed != "" {
			builder.WriteString(" Server error: " + trimmed)
			if last := trimmed[len(trimmed)-1]; last != '.' && last != '!' && last != '?' {
				builder.WriteString(".")
			}
		}
	}
	fmt.Fprintf(&builder, " A section with code '%s' may already exist. Run 'list-app-sections' to inspect existing sections, then change the caption or pass a different --code to use another section code.", resolved.sectionCode)
	return builder.String()
}

// appWriteContentionFailure is BuildContentionFailure.
func appWriteContentionFailure(resolved appWriteResolvedSection, created *bool) error {
	var builder strings.Builder
	fmt.Fprintf(&builder, "Failed to create section '%s' (code '%s')", resolved.caption, resolved.sectionCode)
	if strings.TrimSpace(resolved.applicationCode) != "" {
		fmt.Fprintf(&builder, " in application '%s'", resolved.applicationCode)
	}
	builder.WriteString(": Creatio aborted the section insert without a detailed reason (InsertQuery failed). " +
		"This may be contention from creating sections in parallel, or a server-side rejection unrelated " +
		"to concurrency — the server returned no detail to distinguish them.")
	return &appWriteSectionFailure{message: builder.String(), class: "contention", created: created, guidance: appWriteContentionGuidance}
}

// appWriteRecoverTimeout is RecoverFromInsertTimeout: up to 6 checks, backoff 2 s doubling to 8 s, 40 s total.
func (c *Client) appWriteRecoverTimeout(ctx context.Context, resolved appWriteResolvedSection, cause error) (bool, error) {
	visible := c.appWriteVerifyPolling(ctx, resolved, 6, func(attempt int) time.Duration {
		delay := time.Duration(float64(appWriteSectionWaits.recoveryInitial) * math.Pow(2, float64(attempt-1)))
		return min(delay, appWriteSectionWaits.recoveryMax)
	}, appWriteSectionWaits.recoveryBudget)
	if visible != nil && *visible {
		return true, nil
	}
	seconds := int(appWriteSectionWaits.insert / time.Second)
	outcome := "The post-timeout verification readback also failed, so it is unknown whether the section was created."
	guidance := appWriteTimeoutUnknownGuidance
	if visible != nil {
		outcome = fmt.Sprintf("A post-timeout check did not find section '%s' yet — Creatio may still be processing the insert.", resolved.sectionCode)
		guidance = appWriteTimeoutNotCreatedGuidance
	}
	_ = cause
	return false, &appWriteSectionFailure{
		message: fmt.Sprintf("Creatio did not respond within %ds while creating section '%s' (code '%s'). %s", seconds, resolved.caption, resolved.sectionCode, outcome),
		class:   "creatio-timeout", created: visible, guidance: guidance}
}

// appWriteVerifyPolling is TryVerifySectionExistsPolling: true as soon as the section is visible, false when
// at least one check answered "absent", nil when every check failed.
func (c *Client) appWriteVerifyPolling(ctx context.Context, resolved appWriteResolvedSection, attempts int, backoff func(int) time.Duration, budget time.Duration) *bool {
	started := time.Now()
	sawAbsent := false
	for attempt := 1; attempt <= attempts; attempt++ {
		rows, err := c.appWriteSelectSections(ctx, appWriteSectionQuery(resolved.applicationID), appWriteSectionWaits.verify, "")
		if err == nil {
			for _, row := range rows {
				if strings.EqualFold(row.ID, resolved.id) {
					return appWriteBool(true)
				}
			}
			sawAbsent = true
		}
		if attempt >= attempts || time.Since(started) >= budget {
			break
		}
		if !appWriteSleep(ctx, backoff(attempt)) {
			break
		}
	}
	if sawAbsent {
		return appWriteBool(false)
	}
	return nil
}

// appWriteLoadCreatedSection is LoadCreatedSection: 15 attempts, 2 s apart.
func (c *Client) appWriteLoadCreatedSection(ctx context.Context, before AppInfoResponse, resolved appWriteResolvedSection, culture string) (AppSectionCreateResponse, error) {
	const attempts = 15
	var last error
	for attempt := 1; attempt <= attempts; attempt++ {
		result, err := c.appWriteReadCreatedSection(ctx, before, resolved, culture)
		if err == nil {
			return result, nil
		}
		last = err
		if ctx.Err() != nil {
			return AppSectionCreateResponse{}, ctx.Err()
		}
		if attempt < attempts && !appWriteSleep(ctx, appWriteSectionWaits.pollDelay) {
			return AppSectionCreateResponse{}, ctx.Err()
		}
	}
	return AppSectionCreateResponse{}, fmt.Errorf("Section '%s' was created but its metadata could not be loaded after %d attempts. Last error: %s", resolved.sectionCode, attempts, last.Error())
}

func (c *Client) appWriteReadCreatedSection(ctx context.Context, before AppInfoResponse, resolved appWriteResolvedSection, culture string) (AppSectionCreateResponse, error) {
	after, err := c.appWriteLoadInfo(ctx, "", resolved.applicationCode)
	if err != nil {
		return AppSectionCreateResponse{}, err
	}
	rows, err := c.appWriteSelectSections(ctx, appWriteSectionQuery(resolved.applicationID), appWriteSectionWaits.readback, "")
	if err != nil {
		return AppSectionCreateResponse{}, err
	}
	var record *appWriteSectionRecord
	for i := range rows {
		if strings.EqualFold(rows[i].ID, resolved.id) {
			record = &rows[i]
			break
		}
	}
	entityName := ""
	if resolved.entitySchemaName != nil {
		entityName = *resolved.entitySchemaName
	}
	if record == nil {
		for i := range rows {
			if strings.EqualFold(rows[i].Code, resolved.sectionCode) ||
				strings.TrimSpace(entityName) != "" && rows[i].EntitySchemaName != nil && strings.EqualFold(*rows[i].EntitySchemaName, entityName) {
				record = &rows[i]
				break
			}
		}
	}
	if record == nil {
		description := fmt.Sprintf("'%s'", resolved.sectionCode)
		if strings.TrimSpace(entityName) != "" {
			description = fmt.Sprintf("'%s' or entity schema name '%s'", resolved.sectionCode, entityName)
		}
		return AppSectionCreateResponse{}, fmt.Errorf("Section %s was not found in application '%s'.", description, resolved.applicationID)
	}
	if err := c.appWriteSetIconBackground(ctx, *record, resolved.iconBackground); err != nil {
		return AppSectionCreateResponse{}, err
	}
	entitySchema := record.EntitySchemaName
	if entitySchema == nil || strings.TrimSpace(*entitySchema) == "" {
		entitySchema = resolved.entitySchemaName
	}
	section := record.section()
	section.Caption = appWriteLocalizedCaption(record.Caption, resolved.caption, culture)
	section.EntitySchemaName = entitySchema
	background := resolved.iconBackground
	section.IconBackground = &background
	result := AppSectionCreateResponse{Success: true, PackageUID: after.PackageUID, PackageName: after.PackageName,
		ApplicationID: firstNonEmpty(after.ApplicationID, resolved.applicationID), ApplicationName: firstNonEmpty(after.ApplicationName, resolved.applicationName),
		ApplicationCode: firstNonEmpty(after.ApplicationCode, resolved.applicationCode), ApplicationVersion: after.ApplicationVersion,
		Section: &section, Pages: appWriteCreatedPages(before.Pages, after.Pages)}
	if result.ApplicationVersion == nil {
		result.ApplicationVersion = resolved.applicationVersion
	}
	result.Entity = appWriteResolveEntity(after, before, entitySchema)
	return result, nil
}

// appWriteSetIconBackground is SetIconBackground: an UpdateQuery that writes the resolved color.
func (c *Client) appWriteSetIconBackground(ctx context.Context, record appWriteSectionRecord, color string) error {
	logo, pkg := "", ""
	if record.LogoID != nil {
		logo = *record.LogoID
	}
	if record.PackageID != nil {
		pkg = *record.PackageID
	}
	body := map[string]any{
		"__type": "Terrasoft.Nui.ServiceModel.DataContract.UpdateQuery", "operationType": 2,
		"rootSchemaName": appWriteSectionSchema, "isForceUpdate": false,
		"columnValues": map[string]any{"items": map[string]any{
			"Id":             appWriteParam(appWriteGUIDDataValueType, record.ID),
			"ApplicationId":  appWriteParam(appWriteGUIDDataValueType, record.ApplicationID),
			"LogoId":         appWriteParam(appWriteGUIDDataValueType, logo),
			"PackageId":      appWriteParam(appWriteGUIDDataValueType, pkg),
			"IconBackground": appWriteParam(appWriteTextDataValueType, color),
		}},
		"filters": appWriteIDFilter(record.ID, appWriteTextDataValueType),
	}
	payload, err := c.appWritePost(ctx, "DataService/json/SyncReply/UpdateQuery", body, appWriteSectionWaits.readback)
	if err != nil {
		return err
	}
	var answer *appWriteDataServiceAnswer
	if err := json.Unmarshal(payload, &answer); err != nil {
		return fmt.Errorf("Icon background UpdateQuery returned an unreadable response: %w", err)
	}
	if answer == nil {
		return errors.New("Icon background UpdateQuery returned an empty response.")
	}
	if !answer.Success {
		return errors.New(answer.message("Icon background UpdateQuery failed."))
	}
	return nil
}

// appWriteLocalizedCaption is ResolveLocalizedCaption: a stored localized-JSON caption gives the effective
// culture's value, then en-US, then the first non-blank one; anything else is returned as stored.
func appWriteLocalizedCaption(value *string, fallback, culture string) string {
	if value == nil || strings.TrimSpace(*value) == "" {
		return fallback
	}
	decoder := json.NewDecoder(strings.NewReader(*value))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return *value
	}
	var keys, values []string
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return *value
		}
		var text string
		if err := decoder.Decode(&text); err != nil {
			return *value
		}
		keys = append(keys, keyToken.(string))
		values = append(values, text)
	}
	if _, err := decoder.Token(); err != nil {
		return *value
	}
	if len(keys) == 0 {
		return *value
	}
	lookup := func(name string) string {
		for i, key := range keys {
			if key == name {
				return values[i]
			}
		}
		return ""
	}
	if culture != "" && strings.TrimSpace(lookup(culture)) != "" {
		return lookup(culture)
	}
	if strings.TrimSpace(lookup("en-US")) != "" {
		return lookup("en-US")
	}
	for _, text := range values {
		if strings.TrimSpace(text) != "" {
			return text
		}
	}
	return fallback
}

// appWriteResolveEntity is ResolveEntity: the named entity, otherwise the first entity the section added.
func appWriteResolveEntity(after, before AppInfoResponse, name *string) *AppEntityInfo {
	if name != nil && strings.TrimSpace(*name) != "" {
		for i := range after.Entities {
			if strings.EqualFold(after.Entities[i].Name, *name) {
				return &after.Entities[i]
			}
		}
		return nil
	}
	previous := map[string]bool{}
	for _, entity := range before.Entities {
		if strings.TrimSpace(entity.Name) != "" {
			previous[strings.ToLower(entity.Name)] = true
		}
	}
	for i := range after.Entities {
		if !previous[strings.ToLower(after.Entities[i].Name)] {
			return &after.Entities[i]
		}
	}
	return nil
}

// appWriteCreatedPages is ResolveCreatedPages: pages present after and not before (schema|uId|package).
func appWriteCreatedPages(before, after []PageListItem) []PageListItem {
	key := func(page PageListItem) string {
		return strings.ToLower(page.SchemaName + "|" + page.UID + "|" + page.PackageName)
	}
	previous := map[string]bool{}
	for _, page := range before {
		previous[key(page)] = true
	}
	created := []PageListItem{}
	for _, page := range after {
		if !previous[key(page)] {
			created = append(created, page)
		}
	}
	return created
}
