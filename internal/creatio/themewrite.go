package creatio

// Theme writes through Creatio's native ThemeService (clio master 914dab286: CreateThemeCommand,
// UpdateThemeCommand, DeleteThemeCommand, ClearThemesCacheCommand, ThemeRequestBuilder,
// ThemeParameterValidator, ThemeServiceResponseParser) and the Creatio version floor clio enforces before
// any of them runs (RequiresCreatioVersion("10.0.0"), CreatioVersionChecker, CreatioVersionProvider).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/redact"
)

const (
	themeWriteCreateRoute     = "ServiceModel/ThemeService.svc/CreateTheme"
	themeWriteUpdateRoute     = "ServiceModel/ThemeService.svc/UpdateTheme"
	themeWriteDeleteRoute     = "ServiceModel/ThemeService.svc/DeleteTheme"
	themeWriteClearCacheRoute = "ServiceModel/ThemeService.svc/ClearThemesCache"
	// themeWriteTimeout is RemoteCommandOptions' default request timeout (100 000 ms).
	themeWriteTimeout = 100 * time.Second
	// themeWriteMaxCSSBytes is ThemeParameterValidator.MaxCssContentBytes.
	themeWriteMaxCSSBytes = 1024 * 1024
	// themeWriteVersionExitCode is clio's Program.CreatioVersionRequirementExitCode.
	themeWriteVersionExitCode = 78
)

// ThemeWriteCreateResult is clio's CreateThemeResult: id on success, error on failure, warnings when the
// brand-mode build raised any.
type ThemeWriteCreateResult struct {
	Success  bool     `json:"success"`
	ID       string   `json:"id,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
	Error    string   `json:"error,omitempty"`
}

// ThemeWriteCreateFailure is CreateThemeResult.Failure: a blank message reads "unknown".
func ThemeWriteCreateFailure(message string, warnings []string) ThemeWriteCreateResult {
	if strings.TrimSpace(message) == "" {
		message = "unknown"
	}
	return ThemeWriteCreateResult{Error: message, Warnings: warnings}
}

// ThemeWriteCreateOptions are clio's CreateThemeOptions as the MCP tool fills them.
type ThemeWriteCreateOptions struct {
	ID, Caption, CSSClassName, CSSContent, PackageName string
}

// ThemeWriteVersionError is clio's CreatioVersionRequirementException: the message and its stable code.
type ThemeWriteVersionError struct {
	Message string
	Code    string
}

// Error renders the exception the way clio's MCP surface does: "{message} [{code}]".
func (e *ThemeWriteVersionError) Error() string { return e.Message + " [" + e.Code + "]" }

// ThemeWriteVersionFailure is CommandExecutionResult.FromCreatioVersionRequirementError: exit code 78.
func ThemeWriteVersionFailure(err *ThemeWriteVersionError) CommandResult {
	return NewCommandResult(themeWriteVersionExitCode, "Error", err.Error())
}

// themeWriteVersion is a parsed System.Version: two to four non-negative components; an absent build or
// revision is -1, as in .NET.
type themeWriteVersion [4]int

func themeWriteParseVersion(text string) (themeWriteVersion, bool) {
	parts := strings.Split(strings.TrimSpace(text), ".")
	if len(parts) < 2 || len(parts) > 4 {
		return themeWriteVersion{}, false
	}
	version := themeWriteVersion{-1, -1, -1, -1}
	for index, part := range parts {
		// .NET's Version.TryParse admits surrounding white space per component and a leading '+'.
		number, err := strconv.ParseUint(strings.TrimPrefix(strings.TrimSpace(part), "+"), 10, 31)
		if err != nil || strings.TrimSpace(part) == "" {
			return themeWriteVersion{}, false
		}
		version[index] = int(number)
	}
	return version, true
}

func (v themeWriteVersion) less(other themeWriteVersion) bool {
	for index := range v {
		if v[index] != other[index] {
			return v[index] < other[index]
		}
	}
	return false
}

func (v themeWriteVersion) String() string {
	parts := []string{}
	for _, part := range v {
		if part < 0 {
			break
		}
		parts = append(parts, strconv.Itoa(part))
	}
	return strings.Join(parts, ".")
}

// isDevBuild is CreatioVersionChecker.IsDevBuild: 0.0, 0.0.0 and 0.0.0.0 pass every floor.
func (v themeWriteVersion) isDevBuild() bool {
	return v[0] == 0 && v[1] == 0 && v[2] <= 0 && v[3] <= 0
}

// ThemeWriteEnsureVersion is clio's version gate for the theme commands (CreatioVersionChecker.EnsureRequirements
// over CreatioVersionProvider): the core version from ApplicationInfoService, then cliogate's GetSysInfo when
// that yields none, compared with the 10.0.0 ThemeService floor. It returns the resolved version (4-part as
// the environment reported it) or the refusal clio raises.
func (c *Client) ThemeWriteEnsureVersion(ctx context.Context) (string, *ThemeWriteVersionError) {
	floor := themingServiceMinVersion
	required, _ := themeWriteParseVersion(floor)
	version, status := c.themeWriteResolveVersion(ctx)
	switch status {
	case themeWriteProbeFailed:
		return "", &ThemeWriteVersionError{Code: "version-check-failed", Message: "Could not perform the Creatio platform version check " +
			"for the target environment (it could not be reached, or access was denied). This command requires " + floor +
			" or later — verify connectivity and permissions, then retry."}
	case themeWriteReachableWithoutVersion:
		return "", &ThemeWriteVersionError{Code: "version-undeterminable", Message: "Could not determine the Creatio platform version " +
			"of the target environment; this command requires " + floor + " or later."}
	}
	if !version.isDevBuild() && version.less(required) {
		return "", &ThemeWriteVersionError{Code: "version-too-old", Message: "This command requires Creatio " + floor +
			" or later. The target environment runs " + version.String() + ". Update Creatio and retry."}
	}
	return version.String(), nil
}

type themeWriteVersionStatus int

const (
	themeWriteResolved themeWriteVersionStatus = iota
	themeWriteReachableWithoutVersion
	themeWriteProbeFailed
)

// themeWriteResolveVersion is CreatioVersionProvider.Probe. A source "responded" when its HTTP call came
// back, whatever the status or body; clio's GET helper turns a transport failure into an empty body, so the
// cliogate probe responds unless the session itself cannot be established.
func (c *Client) themeWriteResolveVersion(ctx context.Context) (themeWriteVersion, themeWriteVersionStatus) {
	responded := false
	primary, err := c.serviceRequest(ctx, serviceCall{Route: applicationInfoRoute, Body: []byte("{}"), Timeout: themeWriteTimeout})
	if err == nil {
		responded = true
		if version, ok := themeWriteParseVersion(themeWriteStringAt(primary.payload, "applicationInfo", "sysValues", "coreVersion")); ok {
			return version, themeWriteResolved
		}
	}
	secondary, err := c.serviceRequest(ctx, serviceCall{Method: http.MethodGet, Route: clioGateSysInfoRoute, Timeout: themeWriteTimeout})
	switch {
	case err == nil:
		responded = true
		if version, ok := themeWriteParseVersion(themeWriteStringAt(secondary.payload, "SysInfo", "CoreVersion")); ok {
			return version, themeWriteResolved
		}
	case isTransportError(err):
		responded = true
	}
	if responded {
		return themeWriteVersion{}, themeWriteReachableWithoutVersion
	}
	return themeWriteVersion{}, themeWriteProbeFailed
}

// themeWriteStringAt walks an object path and returns the string leaf, or "" for anything else.
func themeWriteStringAt(payload []byte, path ...string) string {
	var current any
	if json.Unmarshal(payload, &current) != nil {
		return ""
	}
	for _, segment := range path {
		object, ok := current.(map[string]any)
		if !ok {
			return ""
		}
		if current, ok = object[segment]; !ok {
			return ""
		}
	}
	text, _ := current.(string)
	return text
}

// themeWritePost sends a ThemeService call and returns the body whatever the status, as clio's
// ExecutePostRequest does; only a transport or session failure is an error.
func (c *Client) themeWritePost(ctx context.Context, route string, body []byte) (string, error) {
	response, err := c.serviceRequest(ctx, serviceCall{Route: route, Body: body, Timeout: themeWriteTimeout})
	if err != nil {
		return "", err
	}
	return string(response.payload), nil
}

// themeWriteResponseFailure is ThemeServiceResponseParser.TryGetFailure: an empty body or a JSON body
// without success:false is success; a body that is not the BaseResponse JSON, or success:false, is a
// failure with a display-safe message (empty when the server sent none).
func themeWriteResponseFailure(response string) (string, bool) {
	if strings.TrimSpace(response) == "" {
		return "", false
	}
	var decoded *struct {
		Success   *bool `json:"success"`
		ErrorInfo *struct {
			ErrorCode *string `json:"errorCode"`
			Message   *string `json:"message"`
		} `json:"errorInfo"`
	}
	if err := json.Unmarshal([]byte(response), &decoded); err != nil {
		return "Unexpected response from server: " + sanitizeThemeText(response, themeDisplayMaxLength), true
	}
	if decoded != nil && decoded.Success != nil && !*decoded.Success {
		message := ""
		if decoded.ErrorInfo != nil && decoded.ErrorInfo.Message != nil {
			message = sanitizeThemeText(*decoded.ErrorInfo.Message, themeDisplayMaxLength)
		}
		return message, true
	}
	return "", false
}

// themeWriteDescribeFailure is ThemeServiceResponseParser.DescribeFailure.
func themeWriteDescribeFailure(operation, serverMessage string) string {
	if strings.TrimSpace(serverMessage) == "" {
		return operation + " returned success=false. Check the Creatio application logs for details."
	}
	return operation + " failed: " + serverMessage
}

// themeWriteRequest is clio's ThemeRequest (create adds packageUId).
type themeWriteRequest struct {
	ID           string `json:"id"`
	Caption      string `json:"caption"`
	CSSClassName string `json:"cssClassName"`
	CSSContent   string `json:"cssContent"`
	PackageUID   string `json:"packageUId,omitempty"`
}

var (
	themeWriteClassPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]*$`)
	themeWriteNonSlugRun   = regexp.MustCompile(`[^a-z0-9]+`)
)

const themeWriteMaxClassLength = 100

// themeWriteValidClassName is ThemeParameterValidator.IsValidCssClassName (length in UTF-16 units; the
// pattern admits ASCII only, so runes and units agree whenever it matches).
func themeWriteValidClassName(value string) bool {
	return value != "" && utf16Length(value) <= themeWriteMaxClassLength && themeWriteClassPattern.MatchString(value)
}

// ThemeWriteDeriveClassName is ThemeParameterValidator.DeriveCssClassNameFromCaption: lower-cased, runs of
// anything but [a-z0-9] become one hyphen, a slug not starting with a letter gets "t-", capped at 100.
func ThemeWriteDeriveClassName(caption string) (string, bool) {
	if strings.TrimSpace(caption) == "" {
		return "", false
	}
	slug := themeWriteNonSlugRun.ReplaceAllString(strings.ToLower(dotnetTrim(caption)), "-")
	slug = strings.Trim(slug, "-")
	if slug == "" {
		slug = "theme"
	}
	if first, _ := utf8.DecodeRuneInString(slug); !unicode.IsLetter(first) {
		slug = "t-" + slug
	}
	if len(slug) > themeWriteMaxClassLength {
		slug = strings.TrimRight(slug[:themeWriteMaxClassLength], "-")
	}
	return slug, true
}

// dotnetTrim trims what .NET's string.Trim() trims (Unicode white space). Only [a-z0-9] survive the slug
// pattern afterwards, so strings.ToLower and .NET's ToLowerInvariant agree on everything that matters.
func dotnetTrim(value string) string {
	return strings.TrimFunc(value, unicode.IsSpace)
}

// ThemeWriteResolveClassName is ThemeParameterValidator.TryResolveCssClassName.
func ThemeWriteResolveClassName(cssClassName, caption string) (string, error) {
	if strings.TrimSpace(cssClassName) != "" {
		if !themeWriteValidClassName(cssClassName) {
			return "", fmt.Errorf("css-class-name must match ^[A-Za-z][A-Za-z0-9_-]*$ (start with a letter; letters, digits, "+
				"hyphen, underscore only) and be at most %d characters. Received: '%s'.", themeWriteMaxClassLength, cssClassName)
		}
		return cssClassName, nil
	}
	resolved, ok := ThemeWriteDeriveClassName(caption)
	if !ok {
		return "", errors.New("Provide a caption (the theme name) or a css-class-name — at least one is required.")
	}
	return resolved, nil
}

// themeWriteDeriveCaption is ThemeRequestBuilder.DeriveCaptionFromCssClassName: split on '-'/'_', drop a
// trailing "theme" word, Title-Case each word.
func themeWriteDeriveCaption(cssClassName string) string {
	if strings.TrimSpace(cssClassName) == "" {
		return ""
	}
	words := strings.FieldsFunc(cssClassName, func(r rune) bool { return r == '-' || r == '_' })
	if len(words) > 1 && strings.EqualFold(words[len(words)-1], "theme") {
		words = words[:len(words)-1]
	}
	for index, word := range words {
		first, size := utf8.DecodeRuneInString(word)
		words[index] = string(unicode.ToUpper(first)) + word[size:]
	}
	return strings.Join(words, " ")
}

// themeWriteValidateID is ThemeParameterValidator.TryValidateId.
func themeWriteValidateID(id string) error {
	if strings.TrimSpace(id) == "" {
		return errors.New("Theme id is required.")
	}
	if _, ok := parseGUID(id); !ok {
		return fmt.Errorf("Theme id must be a GUID. Received: '%s'.", id)
	}
	return nil
}

// themeWriteValidateRequest is ThemeRequestBuilder.TryValidateRequest: id, css-class-name, caption, CSS.
func themeWriteValidateRequest(request themeWriteRequest) error {
	if err := themeWriteValidateID(request.ID); err != nil {
		return err
	}
	switch {
	case strings.TrimSpace(request.CSSClassName) == "":
		return errors.New("css-class-name is required.")
	case !themeWriteValidClassName(request.CSSClassName) && utf16Length(request.CSSClassName) > themeWriteMaxClassLength:
		return fmt.Errorf("css-class-name must be at most %d characters.", themeWriteMaxClassLength)
	case !themeWriteValidClassName(request.CSSClassName):
		return errors.New("css-class-name must match ^[A-Za-z][A-Za-z0-9_-]*$ (start with a letter; letters, digits, hyphen, underscore only).")
	}
	switch {
	case strings.TrimSpace(request.Caption) == "":
		return errors.New("Theme caption is required.")
	case utf16Length(request.Caption) > 250:
		return errors.New("Theme caption must be at most 250 characters.")
	}
	switch {
	case strings.TrimSpace(request.CSSContent) == "":
		return errors.New("Theme CSS content is required and cannot be empty.")
	case len(request.CSSContent) > themeWriteMaxCSSBytes:
		return errors.New("Theme CSS content must be at most 1 MiB.")
	}
	return nil
}

// CreateTheme is CreateThemeCommand.TryCreateTheme as create-theme's tool reports it: the effective id on
// success; a validation, package or ThemeService failure, redacted, otherwise.
func (c *Client) CreateTheme(ctx context.Context, options ThemeWriteCreateOptions, warnings []string) ThemeWriteCreateResult {
	id := options.ID
	if strings.TrimSpace(id) == "" {
		id = newGUID()
	}
	cssClassName, err := ThemeWriteResolveClassName(options.CSSClassName, options.Caption)
	if err != nil {
		return ThemeWriteCreateFailure(redact.Text(err.Error()), warnings)
	}
	caption := options.Caption
	if strings.TrimSpace(caption) == "" {
		caption = themeWriteDeriveCaption(cssClassName)
	}
	request := themeWriteRequest{ID: id, Caption: caption, CSSClassName: cssClassName, CSSContent: options.CSSContent}
	if err := themeWriteValidateRequest(request); err != nil {
		return ThemeWriteCreateFailure(redact.Text(err.Error()), warnings)
	}
	if strings.TrimSpace(options.PackageName) != "" {
		packageUID, err := c.relatedAddonPackageUID(ctx, options.PackageName)
		if err != nil {
			return ThemeWriteCreateFailure(redact.Text(err.Error()), warnings)
		}
		request.PackageUID = packageUID
	}
	body, _ := json.Marshal(request)
	response, err := c.themeWritePost(ctx, themeWriteCreateRoute, body)
	if err != nil {
		return ThemeWriteCreateFailure(redact.Text(err.Error()), warnings)
	}
	if message, failed := themeWriteResponseFailure(response); failed {
		return ThemeWriteCreateFailure(redact.Text(themeWriteDescribeFailure("CreateTheme", message)), warnings)
	}
	return ThemeWriteCreateResult{Success: true, ID: id, Warnings: warnings}
}

// themeWriteCommandDone is RemoteCommand.Execute's success line.
func themeWriteCommandDone(verb string) CommandResult {
	return CommandInfo("Done " + verb)
}

// UpdateTheme is UpdateThemeCommand: validate the full overwrite, post it, and report clio's command log.
func (c *Client) UpdateTheme(ctx context.Context, id, caption, cssClassName, cssContent string) CommandResult {
	request := themeWriteRequest{ID: id, Caption: caption, CSSClassName: cssClassName, CSSContent: cssContent}
	if err := themeWriteValidateRequest(request); err != nil {
		return CommandFailure(err.Error())
	}
	body, _ := json.Marshal(request)
	return c.themeWriteCommand(ctx, "update-theme", "UpdateTheme", themeWriteUpdateRoute, body)
}

// DeleteTheme is DeleteThemeCommand: the id must be a GUID; an unknown id is the server's failure.
func (c *Client) DeleteTheme(ctx context.Context, id string) CommandResult {
	if err := themeWriteValidateID(id); err != nil {
		return CommandFailure(err.Error())
	}
	body, _ := json.Marshal(map[string]string{"id": id})
	return c.themeWriteCommand(ctx, "delete-theme", "DeleteTheme", themeWriteDeleteRoute, body)
}

// ClearThemesCache is ClearThemesCacheCommand: an empty request to ThemeService.ClearThemesCache.
func (c *Client) ClearThemesCache(ctx context.Context) CommandResult {
	return c.themeWriteCommand(ctx, "clear-themes-cache", "ClearThemesCache", themeWriteClearCacheRoute, []byte("{}"))
}

func (c *Client) themeWriteCommand(ctx context.Context, verb, operation, route string, body []byte) CommandResult {
	response, err := c.themeWritePost(ctx, route, body)
	if err != nil {
		return CommandFailure(err.Error())
	}
	if message, failed := themeWriteResponseFailure(response); failed {
		return CommandFailure(themeWriteDescribeFailure(operation, message))
	}
	return themeWriteCommandDone(verb)
}

// ThemeWriteSanitize is clio's TextUtilities.SanitizeForDisplay with an explicit cap.
func ThemeWriteSanitize(text string, maxLength int) string {
	return sanitizeThemeText(text, maxLength)
}
