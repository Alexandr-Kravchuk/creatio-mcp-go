package mobileguide

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/redact"
)

// SourceTypeFreedomWeb is WebToMobileAnalysisService.SourceTypeFreedomWeb.
const SourceTypeFreedomWeb = "freedom-web"

// Response is MobilePageConversionGuideResponse, members in clio's order with its omission rules
// (WhenWritingNull on every member but success; WhenWritingDefault on requiresVersionConfirmation).
// Guide stays raw JSON until MobilePageConversionGuideModels.cs is ported.
type Response struct {
	Success                     bool            `json:"success"`
	SourceSchemaName            *string         `json:"sourceSchemaName,omitzero"`
	SourceType                  *string         `json:"sourceType,omitzero"`
	Guide                       json.RawMessage `json:"guide,omitzero"`
	ResolvedTargetVersion       *string         `json:"resolvedTargetVersion,omitzero"`
	ResolvedFrom                *string         `json:"resolvedFrom,omitzero"`
	VersionWarning              *string         `json:"versionWarning,omitzero"`
	RequiresVersionConfirmation bool            `json:"requiresVersionConfirmation,omitzero"`
	ResolvedFromReason          *string         `json:"resolvedFromReason,omitzero"`
	RequestRegistryWarning      *string         `json:"requestRegistryWarning,omitzero"`
	RulesWarning                *string         `json:"rulesWarning,omitzero"`
	Error                       *string         `json:"error,omitzero"`
}

// Fail is MobilePageConversionGuideTool.Fail: the error text goes through SensitiveErrorTextRedactor.
func Fail(schemaName, sourceType *string, message string) Response {
	text := redact.Text(message)
	return Response{SourceSchemaName: schemaName, SourceType: sourceType, Error: &text}
}

// DetectSourceType is MobilePageConversionGuideTool.DetectSourceType.
func DetectSourceType(schemaType string) string {
	switch {
	case strings.EqualFold(schemaType, "web"):
		return SourceTypeFreedomWeb
	case strings.EqualFold(schemaType, "mobile"):
		return "mobile"
	case strings.TrimSpace(schemaType) == "":
		return "unknown"
	}
	return strings.ToLower(strings.TrimSpace(schemaType))
}

// RejectUnsupportedSourceType is MobilePageConversionGuideTool.RejectUnsupportedSourceType.
func RejectUnsupportedSourceType(schemaName *string, sourceType string) *Response {
	name := ""
	if schemaName != nil {
		name = *schemaName
	}
	var failure Response
	switch {
	case strings.EqualFold(sourceType, "mobile"):
		failure = Fail(schemaName, &sourceType, fmt.Sprintf("Source page '%s' is already a mobile page. Nothing to convert.", name))
	case !strings.EqualFold(sourceType, SourceTypeFreedomWeb):
		failure = Fail(schemaName, &sourceType, fmt.Sprintf("Source page '%s' has source type '%s', which is not yet supported by get-mobile-page-conversion-guide "+
			"(supported today: '%s'). "+
			"A Classic UI page must first be converted to a Freedom UI web page (use the dedicated classic-web -> freedom-web converter), "+
			"then run get-mobile-page-conversion-guide.", name, sourceType, SourceTypeFreedomWeb))
	default:
		return nil
	}
	return &failure
}

// UnobtainableMobileTemplateError is RejectUnobtainableMobileTemplate's text.
func UnobtainableMobileTemplateError(mobileTemplateName string) string {
	if strings.TrimSpace(mobileTemplateName) == "" {
		return "No mobile template could be determined for this page: its web template matches no conversion " +
			"rule and the conversion rules declare no defaultMobileTemplate. Without a mobile template " +
			"the guide cannot detect which elements the template already provides, so it would insert " +
			"duplicates of native elements, and both data-section diffs would degrade to a root merge " +
			"that can strip the template's own arrays. Add a templates entry for this web template, or a " +
			"defaultMobileTemplate, to the conversion rules."
	}
	return fmt.Sprintf("Could not read the mobile template '%s'. Without it the guide cannot detect "+
		"which elements that template already provides, so it would insert DUPLICATES of native "+
		"elements (e.g. Feed, Tabs) instead of merging onto them, retarget targets would not be "+
		"validated, and both data-section diffs would degrade to a root merge that can strip the "+
		"template's own arrays. Verify the mobile package is installed in the target environment and "+
		"that the schema name is reachable, then re-run.", mobileTemplateName)
}

// UnobtainableWebTemplateError is RejectUnobtainableWebTemplate's text.
func UnobtainableWebTemplateError(webTemplateName string) string {
	named := "this page's web template"
	if strings.TrimSpace(webTemplateName) != "" {
		named = fmt.Sprintf("the web template '%s'", webTemplateName)
	}
	return fmt.Sprintf("Could not read %s, which this page inherits from. Without that baseline the guide cannot "+
		"tell an element the page AUTHORED from one it merely inherits, so it would convert the whole "+
		"inherited chrome (title container, back/save/cancel/close buttons) into inserts and would detect "+
		"no same-name twins — the mobile page would ship duplicates of elements its own template already "+
		"provides. Verify the source package is installed in the target environment and that the template "+
		"schema is reachable, then re-run.", named)
}

// RequestRegistryWarning is MobilePageConversionGuideTool.BuildRequestRegistryWarning.
func RequestRegistryWarning(requestedVersion, servedVersion string) *string {
	if strings.TrimSpace(servedVersion) == "" || strings.EqualFold(requestedVersion, servedVersion) {
		return nil
	}
	text := fmt.Sprintf("Request support was read from the '%s' mobile request registry, not from "+
		"'%s'. The registry is published under a single rolling version, so this is "+
		"expected today and cannot be narrowed by passing a version. It means the set of supported "+
		"request types may include types a '%s' runtime does not dispatch; confirm any "+
		"converted action that the page did not already use.", servedVersion, requestedVersion, requestedVersion)
	return &text
}

// WorseResolvedFrom is MobilePageConversionGuideTool.WorseResolvedFrom: latest-fallback over
// environment-superset over anything else; a tie keeps the first.
func WorseResolvedFrom(a, b string) string {
	rank := func(tier string) int {
		switch {
		case strings.EqualFold(tier, "latest-fallback"):
			return 2
		case strings.EqualFold(tier, "environment-superset"):
			return 1
		}
		return 0
	}
	if rank(a) >= rank(b) {
		return a
	}
	return b
}

// DeriveVersionError is the refusal for a 'version' that is neither 'latest' nor a platform version.
func DeriveVersionError(version string) string {
	return fmt.Sprintf("'version' value '%s' is not a valid platform version. Use a 3-part semver, for example '8.3.3', or 'latest'.", version)
}
