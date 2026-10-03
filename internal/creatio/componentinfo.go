package creatio

// get-component-info: clio's ComponentInfoTool, ComponentInfoResolution, ComponentInfoResponseFactory,
// ComponentDocumentationLoader and the platform-version probe of PlatformVersionResolver.

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/redact"
)

const (
	componentInfoResolvedFromEnvironment         = "environment"
	componentInfoResolvedFromEnvironmentSuperset = "environment-superset"
	componentInfoResolvedFromLatestFallback      = "latest-fallback"
	componentInfoMaxNotFoundSuggestions          = 8
	componentInfoDocumentationSeparator          = "\n\n---\n\n"

	componentInfoEnvironmentSupersetWarning = "The catalog for the requested platform version was not published on the CDN; " +
		"'latest' was served as the closest available. " +
		"This catalog is a superset and may include components not yet present in the target " +
		"environment's actual platform version. " +
		"Verify critical component types against the target environment before generating an " +
		"implementation plan, or pass an explicit version to scope the catalog precisely."

	componentInfoLatestFallbackWarning = "Catalog was loaded from 'latest' (a superset of all GA versions). " +
		"A component listed here may not exist in the target environment's actual platform version, " +
		"so a page built against it can fail to render at runtime. " +
		"The target platform version could not be determined: do NOT silently assume this component set. " +
		"Before generating an implementation plan, tell the user the version is unknown and request explicit " +
		"confirmation before proceeding against 'latest'. " +
		"To scope results to a real version, pass an explicit version or target a registered " +
		"environment so clio can resolve its platform version (no cliogate required — resolved via " +
		"ApplicationInfoService, with the cliogate GetSysInfo probe as fallback)."

	componentInfoCompositeOnlyHint = "This component has no standalone Designer toolbar presence. First look for a composite that assembles it: " +
		"call get-component-info in list mode, scan the 'composites' array, and fetch each plausible candidate's " +
		"recipe with get-component-info composite=\"<caption>\" to confirm it uses this component. " +
		"If a composite assembles this component, build that composite and follow its recipe. " +
		"If no composite assembles it, build this component directly as a fallback — but only if this " +
		"component's own applicability allows it: check appliesToCustomEntities / entityCouplingNote on this " +
		"response first, and do not build it standalone on an entity those fields exclude."

	componentInfoInsertedFieldContractSummary = "Standard field components (crt.Input, crt.NumberInput, crt.Checkbox, crt.ComboBox, " +
		"crt.PhoneInput, crt.EmailInput, crt.DateTimePicker, crt.WebInput, crt.RichTextEditor, " +
		"crt.ColorPicker, crt.ImageInput, crt.FileInput, crt.EncryptedInput, crt.Slider) inserted " +
		"via operation:\"insert\" in viewConfigDiff are validated for self-consistency in the SAME " +
		"update-page call: (a) the body must declare the control's binding attribute in viewModelConfigDiff with a " +
		"DS-bound modelConfig.path, or the control has no data source; and (b) the label must resolve — prefer the " +
		"auto-provided form: set it to $Resources.Strings.<bindingAttribute> (the control's own DS-bound attribute) " +
		"and the platform supplies the caption with no registration; pass the key via the 'resources' parameter only " +
		"to override the caption or for a non-DS-bound key. See the page-schema-resources guide for the full rule" +
		". Violations are rejected at update-page validation time; the diagnostic names the offending " +
		"field, attribute, and section. This contract does NOT apply to operation:\"merge\". " +
		"Use merge in viewConfigDiff for parent-introduced components; for an own-body component, " +
		"edit its complete insert and include its attribute declaration in the submitted body."

	componentInfoDataSourceBindingContract = "This is a data-source-bound field component. " + componentInfoInsertedFieldContractSummary

	componentInfoLocalDocsWarningPrefix = "A component-registry local-file override is active, so documentation is served only from the " +
		"working copy next to the override file; the published CDN copy is deliberately NOT substituted. " +
		"Not found locally: "
	componentInfoLocalDocsWarningSuffix = ". Generate the missing file into that directory, or unset the override to read published documentation."
)

// componentInfoStandardFieldTypes is SchemaValidationService.StandardFieldComponentTypes (case-insensitive).
var componentInfoStandardFieldTypes = map[string]bool{}

func init() {
	for _, name := range []string{"crt.Input", "crt.NumberInput", "crt.Checkbox", "crt.DateTimePicker", "crt.ComboBox",
		"crt.RichTextEditor", "crt.PhoneInput", "crt.EmailInput", "crt.WebInput", "crt.ColorPicker", "crt.ImageInput",
		"crt.FileInput", "crt.EncryptedInput", "crt.Slider"} {
		componentInfoStandardFieldTypes[componentInfoFold(name)] = true
	}
}

// ComponentInfoResponse is clio's ComponentInfoResponse; fields follow its serialization order (the
// selection metadata inherited from ComponentSelectionMetadata comes last).
type ComponentInfoResponse struct {
	Success                     bool                             `json:"success"`
	Mode                        string                           `json:"mode"`
	Count                       int                              `json:"count"`
	Error                       *string                          `json:"error,omitempty"`
	ComponentType               *string                          `json:"componentType,omitempty"`
	Caption                     *string                          `json:"caption,omitempty"`
	Description                 *string                          `json:"description,omitempty"`
	Synonyms                    []string                         `json:"synonyms,omitempty"`
	UseCases                    []string                         `json:"useCases,omitempty"`
	CompositeOnly               *bool                            `json:"compositeOnly,omitempty"`
	CompositeOnlyHint           *string                          `json:"compositeOnlyHint,omitempty"`
	Container                   *bool                            `json:"container,omitempty"`
	ParentTypes                 []string                         `json:"parentTypes,omitempty"`
	Properties                  componentInfoProperties          `json:"properties,omitempty"`
	Inputs                      componentInfoBindings            `json:"inputs,omitempty"`
	Outputs                     componentInfoBindings            `json:"outputs,omitempty"`
	TypicalChildren             []string                         `json:"typicalChildren,omitempty"`
	Example                     json.RawMessage                  `json:"example,omitempty"`
	DataSourceBindingContract   *string                          `json:"dataSourceBindingContract,omitempty"`
	Items                       *[]ComponentInfoListItem         `json:"items,omitempty"`
	Composites                  *[]ComponentInfoCompositeSummary `json:"composites,omitempty"`
	ResolvedTargetVersion       *string                          `json:"resolvedTargetVersion,omitempty"`
	ResolvedFrom                *string                          `json:"resolvedFrom,omitempty"`
	VersionWarning              *string                          `json:"versionWarning,omitempty"`
	SchemaTypeWarning           *string                          `json:"schemaTypeWarning,omitempty"`
	RequiresVersionConfirmation *bool                            `json:"requiresVersionConfirmation,omitempty"`
	ResolvedFromReason          *string                          `json:"resolvedFromReason,omitempty"`
	Documentation               *string                          `json:"documentation,omitempty"`
	DocumentationSource         *string                          `json:"documentationSource,omitempty"`
	DocumentationWarning        *string                          `json:"documentationWarning,omitempty"`
	DocumentationUnavailable    *bool                            `json:"documentationUnavailable,omitempty"`
	References                  *ComponentInfoReferences         `json:"references,omitempty"`
	WhenToUse                   *string                          `json:"whenToUse,omitempty"`
	WhenNotToUse                *string                          `json:"whenNotToUse,omitempty"`
	AppliesToCustomEntities     *bool                            `json:"appliesToCustomEntities,omitempty"`
	EntityCouplingNote          *string                          `json:"entityCouplingNote,omitempty"`
}

// ComponentInfoReferences is the detail response's references block: the type definitions the component
// actually needs.
type ComponentInfoReferences struct {
	TypeDefinitions componentInfoBindings `json:"typeDefinitions,omitempty"`
}

// ComponentInfoListItem is one component in list mode.
type ComponentInfoListItem struct {
	ComponentType string  `json:"componentType"`
	Description   *string `json:"description,omitempty"`
	CompositeOnly *bool   `json:"compositeOnly,omitempty"`
}

// ComponentInfoCompositeSummary is one composite in list mode.
type ComponentInfoCompositeSummary struct {
	Caption     string  `json:"caption"`
	Description *string `json:"description,omitempty"`
}

// ComponentInfoRequest carries the tool's arguments. Resolve returns the client for environment-name/uri; it
// is called only when one of them is given and no explicit version is.
type ComponentInfoRequest struct {
	ComponentType   string
	Composite       string
	Search          string
	SchemaType      string
	Version         string
	EnvironmentName string
	URI             string
	Resolve         func() (*Client, error)
}

// ComponentInfoArgumentFailure is clio's answer to arguments it could not bind (a legacy alias or an
// unknown key): a list-shaped failure.
func ComponentInfoArgumentFailure(message string) ComponentInfoResponse {
	return componentInfoFailure(message)
}

func componentInfoFailure(message string) ComponentInfoResponse {
	return ComponentInfoResponse{Success: false, Mode: "list", Error: &message, Items: &[]ComponentInfoListItem{}}
}

// ComponentInfoGet answers get-component-info the way clio's tool does. A failure the tool catches (an
// unknown environment, an unreachable registry) is reported redacted in the list-shaped envelope.
func ComponentInfoGet(ctx context.Context, request ComponentInfoRequest) ComponentInfoResponse {
	mobile, schemaWarning := componentInfoResolveSchemaType(request.SchemaType)
	response, err := componentInfoBuild(ctx, request, mobile)
	if err != nil {
		response = componentInfoFailure(redact.Text(err.Error()))
	}
	response.SchemaTypeWarning = schemaWarning
	return response
}

// componentInfoResolveSchemaType accepts omitted, 'web' or 'mobile'; anything else falls back to web with a
// warning naming the value.
func componentInfoResolveSchemaType(schemaType string) (bool, *string) {
	if componentInfoBlank(schemaType) {
		return false, nil
	}
	trimmed := strings.TrimSpace(schemaType)
	switch {
	case strings.EqualFold(trimmed, "mobile"):
		return true, nil
	case strings.EqualFold(trimmed, "web"):
		return false, nil
	}
	warning := fmt.Sprintf("Unrecognized schema-type '%s'. Expected 'web' (default) or 'mobile'. "+
		"Falling back to the WEB catalog — if you intended the mobile catalog this is likely a typo; re-call with schema-type='mobile'.", trimmed)
	return false, &warning
}

func componentInfoBuild(ctx context.Context, request ComponentInfoRequest, mobile bool) (ComponentInfoResponse, error) {
	hasVersion := !componentInfoBlank(request.Version)
	hasEnvironment := !componentInfoBlank(request.EnvironmentName) || !componentInfoBlank(request.URI)
	if hasVersion && hasEnvironment {
		return componentInfoFailure("'version' and 'environment-name'/'uri' are mutually exclusive. Pass one or neither."), nil
	}
	if _, ok := componentInfoThreePartVersion(request.Version); hasVersion && !ok {
		return componentInfoFailure(fmt.Sprintf("'version' value '%s' is not a valid platform version. Use a 3-part semver, for example '8.3.3'.",
			request.Version)), nil
	}
	resolution, err := componentInfoResolveVersion(ctx, request, hasVersion, hasEnvironment)
	if err != nil {
		return ComponentInfoResponse{}, err
	}
	flavor := componentInfoWebFlavor
	if mobile {
		flavor = componentInfoMobileFlavor
	}
	fetched, err := componentInfoFetchRegistry(ctx, flavor, strings.TrimSpace(resolution.version))
	if err != nil {
		return ComponentInfoResponse{}, err
	}
	catalog, err := componentInfoParseCatalog(fetched.content, fetched.resolvedVersion, fetched.source)
	if err != nil {
		return ComponentInfoResponse{}, err
	}
	markers := componentInfoMarkersFor(resolution, catalog.resolvedVersion)

	hasComposite := !componentInfoBlank(request.Composite)
	listRequested := componentInfoBlank(request.ComponentType) || strings.EqualFold(request.ComponentType, "list")
	if hasComposite && !listRequested {
		response := componentInfoFailure("'composite' and 'component-type' are mutually exclusive. Pass 'composite' for a composite " +
			"Designer element, or 'component-type' for a single component.")
		markers.apply(&response)
		return response, nil
	}
	if hasComposite {
		return componentInfoCompositeDetail(ctx, catalog, strings.TrimSpace(request.Composite), mobile, markers), nil
	}
	if listRequested {
		entries := componentInfoFilterEntries(catalog.entries, request.Search)
		composites := componentInfoCompositeItems(componentInfoFilterComposites(catalog.composites, request.Search))
		items := componentInfoListItems(entries)
		response := ComponentInfoResponse{Success: true, Mode: "list", Count: len(entries), Items: &items}
		if len(composites) > 0 {
			response.Composites = &composites
		}
		markers.apply(&response)
		return response, nil
	}
	requested := strings.TrimSpace(request.ComponentType)
	if entry, ok := catalog.lookup[componentInfoFold(requested)]; ok {
		var docs []string
		if entry.References != nil {
			docs = entry.References.Docs
		}
		documentation := componentInfoLoadDocumentation(ctx, docs, catalog.resolvedVersion)
		return componentInfoDetail(entry, catalog.global, documentation, markers), nil
	}
	return componentInfoNotFound(catalog, requested, request.Search, markers), nil
}

// componentInfoVersionResolution is clio's PlatformVersionResolution.
type componentInfoVersionResolution struct {
	version     string
	environment bool
	reason      string
}

func componentInfoResolveVersion(ctx context.Context, request ComponentInfoRequest, hasVersion, hasEnvironment bool) (componentInfoVersionResolution, error) {
	switch {
	case hasVersion:
		return componentInfoVersionResolution{version: strings.TrimSpace(request.Version), environment: true}, nil
	case hasEnvironment:
		if request.Resolve == nil {
			return componentInfoVersionResolution{}, fmt.Errorf("no environment resolver")
		}
		client, err := request.Resolve()
		if err != nil {
			return componentInfoVersionResolution{}, err
		}
		return client.componentInfoProbeVersion(ctx)
	}
	return componentInfoVersionResolution{version: componentInfoLatestVersion, reason: "no-active-environment"}, nil
}

// componentInfoProbeVersion is PlatformVersionResolver.ProbeAsync: ApplicationInfoService first, cliogate's
// GetSysInfo when that yields no core version. A probe that fails (transport, rejected session, non-2xx) is
// the transient 'probe-error'; one that answers without a core version is 'core-version-missing'.
func (c *Client) componentInfoProbeVersion(ctx context.Context) (componentInfoVersionResolution, error) {
	if c == nil || strings.TrimSpace(c.config.BaseURL) == "" {
		return componentInfoVersionResolution{version: componentInfoLatestVersion, reason: "no-active-environment"}, nil
	}
	transient := false
	coreVersion := ""
	primary, err := c.serviceRequest(ctx, serviceCall{Route: applicationInfoRoute, Body: []byte("{}")})
	switch {
	case ctx.Err() != nil:
		return componentInfoVersionResolution{}, ctx.Err()
	case err != nil || primary.status < http.StatusOK || primary.status >= http.StatusMultipleChoices:
		transient = true
	default:
		coreVersion = themeWriteStringAt(primary.payload, "applicationInfo", "sysValues", "coreVersion")
	}
	if strings.TrimSpace(coreVersion) == "" {
		secondary, err := c.serviceRequest(ctx, serviceCall{Method: http.MethodGet, Route: clioGateSysInfoRoute})
		switch {
		case ctx.Err() != nil:
			return componentInfoVersionResolution{}, ctx.Err()
		case err != nil || secondary.status < http.StatusOK || secondary.status >= http.StatusMultipleChoices:
			transient = true
		default:
			coreVersion = themeWriteStringAt(secondary.payload, "SysInfo", "CoreVersion")
		}
	}
	if strings.TrimSpace(coreVersion) == "" {
		reason := "core-version-missing"
		if transient {
			reason = "probe-error"
		}
		return componentInfoVersionResolution{version: componentInfoLatestVersion, reason: reason}, nil
	}
	threePart, ok := componentInfoThreePartVersion(coreVersion)
	if !ok {
		return componentInfoVersionResolution{version: componentInfoLatestVersion, reason: "core-version-unparseable"}, nil
	}
	return componentInfoVersionResolution{version: threePart, environment: true}, nil
}

// componentInfoThreePartVersion is TryNormaliseToThreePartSemver over System.Version.TryParse: two to four
// non-negative Int32 components (each may carry surrounding whitespace and a sign), reduced to
// Major.Minor.Build with a missing build as 0.
func componentInfoThreePartVersion(value string) (string, bool) {
	value = strings.TrimFunc(value, componentInfoIsSpace)
	if value == "" {
		return "", false
	}
	parts := strings.Split(value, ".")
	if len(parts) < 2 || len(parts) > 4 {
		return "", false
	}
	numbers := make([]int64, len(parts))
	for index, part := range parts {
		number, ok := componentInfoVersionComponent(part)
		if !ok {
			return "", false
		}
		numbers[index] = number
	}
	build := int64(0)
	if len(numbers) > 2 {
		build = numbers[2]
	}
	return fmt.Sprintf("%d.%d.%d", numbers[0], numbers[1], build), true
}

// componentInfoVersionComponent is Int32.TryParse with NumberStyles.Integer, rejecting a negative value.
func componentInfoVersionComponent(part string) (int64, bool) {
	part = strings.Trim(part, "\t\n\v\f\r ")
	sign := int64(1)
	if strings.HasPrefix(part, "+") || strings.HasPrefix(part, "-") {
		if part[0] == '-' {
			sign = -1
		}
		part = part[1:]
	}
	if part == "" {
		return 0, false
	}
	for _, r := range part {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	number, err := strconv.ParseInt(part, 10, 64)
	if err != nil {
		return 0, false
	}
	number *= sign
	if number < 0 || number > math.MaxInt32 {
		return 0, false
	}
	return number, true
}

func componentInfoIsSpace(r rune) bool {
	return r == ' ' || (r >= '\t' && r <= '\r') || r == '\u0085' || r == ' '
}

// componentInfoMarkers are the version markers every catalog-backed answer carries.
type componentInfoMarkers struct {
	resolvedVersion string
	resolvedFrom    string
	reason          string
}

func componentInfoMarkersFor(resolution componentInfoVersionResolution, loadedVersion string) componentInfoMarkers {
	markers := componentInfoMarkers{resolvedVersion: loadedVersion, resolvedFrom: componentInfoResolvedFromLatestFallback}
	if resolution.environment {
		markers.resolvedFrom = componentInfoResolvedFromEnvironmentSuperset
		if strings.EqualFold(loadedVersion, strings.TrimSpace(resolution.version)) {
			markers.resolvedFrom = componentInfoResolvedFromEnvironment
		}
	}
	if markers.resolvedFrom == componentInfoResolvedFromLatestFallback {
		markers.reason = resolution.reason
	}
	return markers
}

func (m componentInfoMarkers) apply(response *ComponentInfoResponse) {
	response.ResolvedTargetVersion = componentInfoText(m.resolvedVersion)
	response.ResolvedFrom = componentInfoText(m.resolvedFrom)
	switch m.resolvedFrom {
	case componentInfoResolvedFromLatestFallback:
		warning, confirm := componentInfoLatestFallbackWarning, true
		response.VersionWarning = &warning
		response.RequiresVersionConfirmation = &confirm
	case componentInfoResolvedFromEnvironmentSuperset:
		warning := componentInfoEnvironmentSupersetWarning
		response.VersionWarning = &warning
	}
	response.ResolvedFromReason = componentInfoText(m.reason)
}

func componentInfoText(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func componentInfoNonBlank(value string) *string {
	if componentInfoBlank(value) {
		return nil
	}
	return &value
}

func componentInfoNonBlankPtr(value *string) *string {
	if value == nil {
		return nil
	}
	return componentInfoNonBlank(*value)
}

func componentInfoListItems(entries []*componentInfoEntry) []ComponentInfoListItem {
	ordered := append([]*componentInfoEntry{}, entries...)
	componentInfoSortStable(ordered, func(entry *componentInfoEntry) string { return entry.ComponentType })
	items := make([]ComponentInfoListItem, 0, len(ordered))
	for _, entry := range ordered {
		item := ComponentInfoListItem{ComponentType: entry.ComponentType, Description: componentInfoNonBlank(entry.Description)}
		if entry.CompositeOnly != nil && *entry.CompositeOnly {
			flag := true
			item.CompositeOnly = &flag
		}
		items = append(items, item)
	}
	return items
}

func componentInfoCompositeItems(composites []*componentInfoComposite) []ComponentInfoCompositeSummary {
	ordered := append([]*componentInfoComposite{}, composites...)
	componentInfoSortStable(ordered, func(composite *componentInfoComposite) string { return composite.Caption })
	items := make([]ComponentInfoCompositeSummary, 0, len(ordered))
	for _, composite := range ordered {
		items = append(items, ComponentInfoCompositeSummary{Caption: composite.Caption, Description: componentInfoNonBlankPtr(composite.Description)})
	}
	return items
}

func componentInfoDetail(entry *componentInfoEntry, global *componentInfoGlobalReferences, documentation componentInfoDocumentation,
	markers componentInfoMarkers) ComponentInfoResponse {
	var baseInputs, globalTypes componentInfoBindings
	if global != nil {
		baseInputs, globalTypes = global.BaseInputs, global.TypeDefinitions
	}
	componentType := entry.ComponentType
	response := ComponentInfoResponse{
		Success:                 true,
		Mode:                    "detail",
		Count:                   1,
		ComponentType:           &componentType,
		Description:             componentInfoNonBlank(entry.Description),
		Synonyms:                entry.Synonyms,
		UseCases:                entry.UseCases,
		Container:               entry.Container,
		ParentTypes:             entry.ParentTypes,
		Properties:              entry.Properties,
		Inputs:                  componentInfoMergeBindings(baseInputs, entry.Inputs),
		TypicalChildren:         entry.TypicalChildren,
		WhenToUse:               componentInfoNonBlankPtr(entry.WhenToUse),
		WhenNotToUse:            componentInfoNonBlankPtr(entry.WhenNotToUse),
		AppliesToCustomEntities: entry.AppliesToCustomEntities,
		EntityCouplingNote:      componentInfoNonBlankPtr(entry.EntityCouplingNote),
		Documentation:           documentation.text,
		DocumentationSource:     documentation.source,
		DocumentationWarning:    documentation.warning,
	}
	if len(entry.Outputs) > 0 {
		response.Outputs = entry.Outputs
	}
	if example := strings.TrimSpace(string(entry.Example)); example != "" && example != "null" {
		response.Example = entry.Example
	}
	if entry.CompositeOnly != nil && *entry.CompositeOnly {
		flag, hint := true, componentInfoCompositeOnlyHint
		response.CompositeOnly, response.CompositeOnlyHint = &flag, &hint
	}
	if componentInfoStandardFieldTypes[componentInfoFold(entry.ComponentType)] {
		contract := componentInfoDataSourceBindingContract
		response.DataSourceBindingContract = &contract
	}
	var localTypes componentInfoBindings
	if entry.References != nil {
		localTypes = entry.References.TypeDefinitions
	}
	if closure := componentInfoTypeClosure(entry.Inputs, entry.Outputs, localTypes, globalTypes); closure != nil {
		response.References = &ComponentInfoReferences{TypeDefinitions: closure}
	}
	markers.apply(&response)
	return response
}

func componentInfoCompositeDetail(ctx context.Context, catalog *componentInfoCatalog, caption string, mobile bool,
	markers componentInfoMarkers) ComponentInfoResponse {
	var composite *componentInfoComposite
	for _, candidate := range catalog.composites {
		if strings.EqualFold(candidate.Caption, caption) {
			composite = candidate
			break
		}
	}
	if composite == nil {
		known := "this catalog declares no composites"
		switch {
		case len(catalog.composites) > 0:
			captions := make([]string, 0, len(catalog.composites))
			for _, item := range catalog.composites {
				captions = append(captions, "'"+item.Caption+"'")
			}
			known = "known composites: " + strings.Join(captions, ", ")
		case mobile:
			known = "composites are a web-only Designer feature; the mobile catalog has none — query the web component catalog instead"
		}
		response := componentInfoFailure(fmt.Sprintf("Composite '%s' was not found (%s). "+
			"Omit 'composite' and use list mode to see every composite with its description.", caption, known))
		response.Mode = "composite"
		markers.apply(&response)
		return response
	}
	documentation := componentInfoLoadDocumentation(ctx, composite.Docs, catalog.resolvedVersion)
	captionText := composite.Caption
	response := ComponentInfoResponse{
		Success:              true,
		Mode:                 "composite",
		Count:                1,
		Caption:              &captionText,
		Description:          componentInfoNonBlankPtr(composite.Description),
		Documentation:        documentation.text,
		DocumentationSource:  documentation.source,
		DocumentationWarning: documentation.warning,
	}
	if len(composite.Docs) > 0 && documentation.text == nil {
		unavailable := true
		response.DocumentationUnavailable = &unavailable
	}
	markers.apply(&response)
	return response
}

// componentInfoNotFound is CreateComponentNotFoundResponse: a label naming a composite routes to it; else
// components matching the label by name/description, else the closest types by edit distance.
func componentInfoNotFound(catalog *componentInfoCatalog, requested, search string, markers componentInfoMarkers) ComponentInfoResponse {
	query := strings.TrimSpace(requested)
	queryComposites := componentInfoFilterComposites(catalog.composites, query)
	var exact *componentInfoComposite
	for _, composite := range queryComposites {
		if strings.EqualFold(composite.Caption, query) {
			exact = composite
			break
		}
	}
	nameMatches := componentInfoFilterEntries(catalog.entries, query)
	hasComponentMatch := len(nameMatches) > 0
	if exact != nil || (!hasComponentMatch && len(queryComposites) > 0) {
		directive := queryComposites[0].Caption
		if exact != nil {
			directive = exact.Caption
		}
		captions := make([]string, 0, len(queryComposites))
		for _, composite := range queryComposites {
			captions = append(captions, "'"+composite.Caption+"'")
		}
		response := componentInfoFailure(fmt.Sprintf("'%s' is not a component type — no such componentType exists in the catalog. "+
			"Searching composites found: %s. "+
			"REQUIRED: call get-component-info composite=\"%s\" to get the authoritative assembly recipe. "+
			"Do NOT synthesize this structure from memory, guidance articles, or raw component docs — "+
			"those sources are incomplete and will produce a broken result.", query, strings.Join(captions, ", "), directive))
		composites := componentInfoCompositeItems(queryComposites)
		response.Composites = &composites
		markers.apply(&response)
		return response
	}
	var suggestions []*componentInfoEntry
	var message string
	if hasComponentMatch {
		suggestions = nameMatches
		if len(suggestions) > componentInfoMaxNotFoundSuggestions {
			suggestions = suggestions[:componentInfoMaxNotFoundSuggestions]
		}
		message = fmt.Sprintf("'%s' is not a component type. "+
			"Showing %d component(s) matching '%s' by name/description — pass the correct componentType, "+
			"or omit 'component-type' to list the full catalog (components AND composites).", query, len(suggestions), query)
	} else {
		suggestions = componentInfoSuggest(catalog.entries, query, search, componentInfoMaxNotFoundSuggestions)
		message = fmt.Sprintf("'%s' is not a component type and does not match any composite. "+
			"Showing the %d closest known type(s) — pass the correct componentType, "+
			"or omit 'component-type' to list the full catalog (components AND composites).", query, len(suggestions))
	}
	response := componentInfoFailure(message)
	response.Count = len(suggestions)
	items := componentInfoListItems(suggestions)
	response.Items = &items
	if composites := componentInfoCompositeItems(componentInfoFilterComposites(catalog.composites, search)); len(composites) > 0 {
		response.Composites = &composites
	}
	markers.apply(&response)
	return response
}

// componentInfoDocumentation is clio's ComponentDocumentationOutcome.
type componentInfoDocumentation struct {
	text, source, warning *string
}

// componentInfoLoadDocumentation is ComponentDocumentationLoader.LoadAsync: every served file joined with
// the separator, the tier (local/cache/cdn/mixed/none) and a warning for files a local override lacks.
func componentInfoLoadDocumentation(ctx context.Context, docs []string, version string) componentInfoDocumentation {
	if len(docs) == 0 {
		return componentInfoDocumentation{}
	}
	var blocks, localMisses []string
	served := map[componentInfoSource]bool{}
	for _, docPath := range docs {
		result := componentInfoFetchDoc(ctx, version, docPath)
		if result.source != componentInfoSourceNone {
			served[result.source] = true
			if result.content != nil && *result.content != "" {
				blocks = append(blocks, *result.content)
			}
			continue
		}
		if result.localVariable != "" {
			localMisses = append(localMisses, fmt.Sprintf("'%s' (expected in the directory of %s)", docPath, result.localVariable))
		}
	}
	outcome := componentInfoDocumentation{}
	if len(blocks) > 0 {
		outcome.text = componentInfoText(strings.Join(blocks, componentInfoDocumentationSeparator))
	}
	source := "none"
	if len(served) > 1 {
		source = "mixed"
	} else {
		for tier := range served {
			switch tier {
			case componentInfoSourceLocal:
				source = "local"
			case componentInfoSourceCache:
				source = "cache"
			case componentInfoSourceCDN:
				source = "cdn"
			}
		}
	}
	outcome.source = &source
	if len(localMisses) > 0 {
		warning := componentInfoLocalDocsWarningPrefix + strings.Join(localMisses, "; ") + componentInfoLocalDocsWarningSuffix
		outcome.warning = &warning
	}
	return outcome
}
