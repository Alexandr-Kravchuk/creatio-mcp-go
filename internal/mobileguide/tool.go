package mobileguide

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// The handler of MobilePageConversionGuideTool.GetMobilePageConversionGuide over the stand reads a Stand gives.

// Args is MobilePageConversionGuideArgs (nil is an absent or null member).
type Args struct {
	SchemaName       *string
	TargetSchemaName *string
	Version          *string
	EnvironmentName  *string
	URI              *string
}

// PageRead is the part of a get-page answer (PageGetResponse) the tool reads. The configs are the merged
// bundle sections as compact Newtonsoft JSON (nil when absent).
type PageRead struct {
	Success          bool
	Error            string
	SchemaName       string
	SchemaUID        string
	PackageUID       string
	ParentSchemaName string
	SchemaType       string
	ViewConfig       []byte
	ViewModelConfig  []byte
	ModelConfig      []byte
	ResourcesStrings []byte
	Handlers         *string
	Converters       *string
	Validators       *string
	Chain            []string
}

// VersionResolution is PlatformVersionResolution.
type VersionResolution struct {
	Version     string
	Environment bool
	Reason      string
}

// Catalog is a loaded component registry: its entries as raw JSON in the catalog's order, the
// references.baseInputs object (nil when absent) and the version the fetch chain served.
type Catalog struct {
	Entries         []json.RawMessage
	BaseInputs      json.RawMessage
	ResolvedVersion string
}

// Stand is what the tool reads outside the engine. ReadPage's error is a failure clio's ReadPageUnderTenantLock
// raises (the environment could not be resolved); an unsuccessful read is a PageRead with Success false.
type Stand interface {
	ReadPage(ctx context.Context, schemaName string) (PageRead, error)
	ResolveVersion(ctx context.Context) (VersionResolution, error)
	LoadCatalog(ctx context.Context, mobile bool, version string) (*Catalog, error)
	LoadRequestCatalog(ctx context.Context, version string) (requestTypes []string, resolvedVersion string, err error)
	FetchRules(ctx context.Context, version string) ([]byte, error)
	VersionWarning(resolvedFrom string) *string
	Probes() ProbeClient
}

func newtonsoftConfig(data []byte) (*jn, error) {
	if data == nil {
		return nil, nil
	}
	node, err := parseNewtonsoft(data)
	if err != nil {
		return nil, err
	}
	return toJsonNode(node), nil
}

// bundleOf is PageBundleInfo as the tool sees it: each section parsed by Json.NET, then re-read by
// System.Text.Json (PageBundleMergeHelpers.ToJsonNode).
func bundleOf(page PageRead) (*Bundle, error) {
	bundle := &Bundle{Handlers: page.Handlers, Converters: page.Converters, Validators: page.Validators}
	var err error
	if bundle.ViewConfig, err = newtonsoftConfig(page.ViewConfig); err != nil {
		return nil, err
	}
	if bundle.ViewModelConfig, err = newtonsoftConfig(page.ViewModelConfig); err != nil {
		return nil, err
	}
	if bundle.ModelConfig, err = newtonsoftConfig(page.ModelConfig); err != nil {
		return nil, err
	}
	if bundle.ResourcesStrings, err = newtonsoftConfig(page.ResourcesStrings); err != nil {
		return nil, err
	}
	return bundle, nil
}

// orderedObject reads a JSON object's members in document order; a repeated key keeps its first position and
// its last value (Dictionary's indexer).
func orderedObject(raw json.RawMessage) ([]string, map[string]json.RawMessage, bool) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil {
		return nil, nil, false
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '{' {
		return nil, nil, false
	}
	var keys []string
	values := map[string]json.RawMessage{}
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, nil, false
		}
		key, _ := keyToken.(string)
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, nil, false
		}
		if _, seen := values[key]; !seen {
			keys = append(keys, key)
		}
		values[key] = value
	}
	return keys, values, true
}

func rawIsNull(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	return trimmed == "" || trimmed == "null"
}

func rawDict[V any](raw json.RawMessage, value func(json.RawMessage) (V, error)) (*dict[V], error) {
	if rawIsNull(raw) {
		return nil, nil
	}
	keys, values, ok := orderedObject(raw)
	if !ok {
		return nil, errors.New("expected a JSON object")
	}
	result := newDict[V](false)
	for _, key := range keys {
		converted, err := value(values[key])
		if err != nil {
			return nil, err
		}
		result.set(key, converted)
	}
	return result, nil
}

func stjValue(raw json.RawMessage) (*jn, error) {
	if rawIsNull(raw) {
		return nil, nil
	}
	return parseSTJ(raw)
}

// ParseRegistryEntry reads one ComponentRegistryEntry the way System.Text.Json does with clio's registry options
// (member names ignoring case, dictionaries ordinal in document order).
func ParseRegistryEntry(raw json.RawMessage) (*RegistryEntry, error) {
	if rawIsNull(raw) {
		return nil, nil
	}
	var shape struct {
		ComponentType    *string         `json:"componentType"`
		Container        *bool           `json:"container"`
		Description      *string         `json:"description"`
		Example          json.RawMessage `json:"example"`
		DesignerDefaults json.RawMessage `json:"designerDefaults"`
		Properties       json.RawMessage `json:"properties"`
		Inputs           json.RawMessage `json:"inputs"`
		Outputs          json.RawMessage `json:"outputs"`
	}
	if err := json.Unmarshal(raw, &shape); err != nil {
		return nil, err
	}
	entry := &RegistryEntry{ComponentType: deref(shape.ComponentType), Container: shape.Container, Description: shape.Description}
	if !rawIsNull(shape.Example) {
		entry.Example = shape.Example
	}
	if !rawIsNull(shape.DesignerDefaults) {
		entry.DesignerDefaults = shape.DesignerDefaults
	}
	var err error
	entry.Properties, err = rawDict(shape.Properties, func(value json.RawMessage) (*PropertyDefinition, error) {
		if rawIsNull(value) {
			return nil, nil
		}
		var definition struct {
			Type    *string         `json:"type"`
			Default json.RawMessage `json:"default"`
		}
		if err := json.Unmarshal(value, &definition); err != nil {
			return nil, err
		}
		result := &PropertyDefinition{Type: definition.Type}
		if !rawIsNull(definition.Default) {
			node, err := parseSTJ(definition.Default)
			if err != nil {
				return nil, err
			}
			result.Default = node
		}
		return result, nil
	})
	if err != nil {
		return nil, err
	}
	if entry.Inputs, err = rawDict(shape.Inputs, stjValue); err != nil {
		return nil, err
	}
	if entry.Outputs, err = rawDict(shape.Outputs, stjValue); err != nil {
		return nil, err
	}
	return entry, nil
}

// mapResolvedFrom is ComponentInfoResolution.MapResolvedFrom.
func mapResolvedFrom(resolution VersionResolution, servedVersion string) string {
	if !resolution.Environment {
		return "latest-fallback"
	}
	if strings.EqualFold(servedVersion, strings.TrimSpace(resolution.Version)) {
		return "environment"
	}
	return "environment-superset"
}

// isVersionAccepted is the tool's 'version' gate: blank, 'latest' or a platform version.
func isVersionAccepted(version string, threePart func(string) bool) bool {
	return isBlank(version) || strings.EqualFold(dotnetTrim(version), "latest") || threePart(version)
}

// Run answers get-mobile-page-conversion-guide in clio's order. threePart is
// PlatformVersionResolver.TryNormaliseToThreePartSemver.
func Run(ctx context.Context, stand Stand, args Args, threePart func(string) bool) (Response, error) {
	name := deref(args.SchemaName)
	page, err := stand.ReadPage(ctx, name)
	if err != nil {
		return Fail(args.SchemaName, nil, fmt.Sprintf("Failed to read source page '%s': %s", name, err.Error())), nil
	}
	if !page.Success {
		reason := page.Error
		if reason == "" {
			reason = "unknown error"
		}
		return Fail(args.SchemaName, nil, fmt.Sprintf("Could not read source page '%s': %s. "+
			"If the page is a Classic UI page, migrate it to a Freedom UI web page first.", name, reason)), nil
	}
	sourceType := DetectSourceType(page.SchemaType)
	if rejection := RejectUnsupportedSourceType(args.SchemaName, sourceType); rejection != nil {
		return *rejection, nil
	}
	if args.Version != nil && !isVersionAccepted(*args.Version, threePart) {
		return Fail(args.SchemaName, &sourceType, DeriveVersionError(*args.Version)), nil
	}
	resolution := VersionResolution{Version: "latest", Reason: "no-active-environment"}
	switch {
	case args.Version != nil && !isBlank(*args.Version):
		resolution = VersionResolution{Version: dotnetTrim(*args.Version), Environment: true}
	case (args.EnvironmentName != nil && !isBlank(*args.EnvironmentName)) || (args.URI != nil && !isBlank(*args.URI)):
		if resolution, err = stand.ResolveVersion(ctx); err != nil {
			return Response{}, err
		}
	}
	version := resolution.Version
	mobileCatalog, err := stand.LoadCatalog(ctx, true, version)
	if err != nil {
		if ctx.Err() != nil {
			return Response{}, ctx.Err()
		}
		return Fail(args.SchemaName, &sourceType, fmt.Sprintf("Failed to load the MOBILE component catalog for version '%s': %s", version, err.Error())), nil
	}
	webCatalog, err := stand.LoadCatalog(ctx, false, version)
	if err != nil {
		if ctx.Err() != nil {
			return Response{}, ctx.Err()
		}
		return Fail(args.SchemaName, &sourceType, fmt.Sprintf("Failed to load the WEB component catalog for version '%s': %s", version, err.Error())), nil
	}
	requestTypes, requestVersion, err := stand.LoadRequestCatalog(ctx, version)
	if err != nil {
		if ctx.Err() != nil {
			return Response{}, ctx.Err()
		}
		return Fail(args.SchemaName, &sourceType, fmt.Sprintf("Failed to load the MOBILE request catalog for version '%s': %s", version, err.Error())), nil
	}
	resolvedFrom := WorseResolvedFrom(mapResolvedFrom(resolution, mobileCatalog.ResolvedVersion), mapResolvedFrom(resolution, webCatalog.ResolvedVersion))
	requestWarning := RequestRegistryWarning(version, requestVersion)
	mobileEntries, err := parseEntries(mobileCatalog.Entries)
	if err != nil {
		return Fail(args.SchemaName, &sourceType, fmt.Sprintf("Failed to load the MOBILE component catalog for version '%s': %s", version, err.Error())), nil
	}
	webEntries, err := parseEntries(webCatalog.Entries)
	if err != nil {
		return Fail(args.SchemaName, &sourceType, fmt.Sprintf("Failed to load the WEB component catalog for version '%s': %s", version, err.Error())), nil
	}
	mobileTypes, webTypes := newSet(true), newSet(true)
	for _, entry := range mobileEntries {
		mobileTypes.add(entry.ComponentType)
	}
	for _, entry := range webEntries {
		webTypes.add(entry.ComponentType)
	}
	rules, err := GetRules(ctx, version, stand.FetchRules)
	if err != nil {
		return Response{}, err
	}
	withheld := TargetCarryingRegistryOnlyRequests(rules, requestTypes)
	mobileRequestTypes := newSet(true)
	for _, requestType := range requestTypes {
		if !withheld.contains(requestType) {
			mobileRequestTypes.add(requestType)
		}
	}
	baseInputs, err := rawDict(mobileCatalog.BaseInputs, stjValue)
	if err != nil {
		baseInputs = nil
	}
	generation := &RegistryGeneration{BaseInputs: baseInputs}
	rulesWarning := RulesWarning(rules, mobileRequestTypes)
	bundle, err := bundleOf(page)
	if err != nil {
		return Fail(args.SchemaName, &sourceType, fmt.Sprintf("Failed to analyze source page '%s': %s", name, err.Error())), nil
	}
	chain := make([]*string, 0, len(page.Chain))
	for index := range page.Chain {
		chain = append(chain, &page.Chain[index])
	}
	ownName, parentName := strp(page.SchemaName), strp(page.ParentSchemaName)
	effectiveTemplate := ResolveEffectiveTemplateName(ownName, parentName, chain, rules)
	templateRule := ResolveTemplateRule(rules, effectiveTemplate)
	if templateRule == nil {
		templateRule = DefaultTemplateRule(rules)
	}
	var mobileTemplateName *string
	if templateRule != nil {
		mobileTemplateName = templateRule.Mobile
	}
	mobileTemplate := loadMobileTemplate(ctx, stand, mobileTemplateName)
	if mobileTemplate.unavailable {
		return Fail(args.SchemaName, &sourceType, UnobtainableMobileTemplateError(deref(mobileTemplateName))), nil
	}
	webBaseline := loadWebTemplateBaseline(ctx, stand, effectiveTemplate, ownName)
	if webBaseline.unavailable {
		return Fail(args.SchemaName, &sourceType, UnobtainableWebTemplateError(deref(effectiveTemplate))), nil
	}
	targetName := DeriveMobileSchemaName(args.SchemaName)
	if args.TargetSchemaName != nil && !isBlank(*args.TargetSchemaName) {
		targetName = dotnetTrim(*args.TargetSchemaName)
	}
	isFormPage := IsFormPage(args.SchemaName, parentName, templateRule, rules)
	probes := stand.Probes()
	schemaUID, packageUID := strp(page.SchemaUID), strp(page.PackageUID)
	section := ProbeSectionRegistration(ctx, probes, schemaUID, isFormPage)
	pageRules := ProbePageBusinessRules(ctx, probes, args.SchemaName, packageUID)
	actionTargets := ProbeActionTargets(ctx, probes, bundle.ViewConfig, rules, bundle.ModelConfig, packageUID)
	existing := ProbeExistingMobilePages(ctx, probes, section, isFormPage, bundle.ModelConfig, packageUID, targetName)
	guide, err := Analyze(&AnalyzeInput{
		Bundle: bundle, MobileTypes: mobileTypes, WebTypes: webTypes, WebByType: IndexByComponentType(webEntries),
		MobileByType: IndexByComponentType(mobileEntries), Rules: rules, TemplateRule: templateRule,
		MobileRequestTypes: mobileRequestTypes, SourcePage: args.SchemaName, SourceTemplate: effectiveTemplate,
		SuggestedTarget: &targetName, ContainerNameMap: BuildContainerNameMap(templateRule), SectionRegistration: section,
		PageBusinessRulesProbe: pageRules, TemplateComponentNames: webBaseline.names, ComponentNameMap: BuildComponentNameMap(templateRule),
		PositionalPlacements: BuildPositionalPlacements(templateRule), MobileContainerParents: mobileTemplate.parents,
		MobileTemplateViewModelConfig: mobileTemplate.viewModelConfig, MobileTemplateModelConfig: mobileTemplate.modelConfig,
		MobileTemplateTypesByName: mobileTemplate.typesByName, MobileTemplateLayoutConfigs: mobileTemplate.layoutConfigs,
		MobileTemplateNodesByName: mobileTemplate.nodesByName, WebTemplateBaselineNodes: webBaseline.nodes,
		WebTemplateResources: webBaseline.resources, ActionTargetsProbe: actionTargets, ExistingMobilePages: existing,
		MobileRegistryGeneration: generation,
	})
	if err != nil {
		return Fail(args.SchemaName, &sourceType, fmt.Sprintf("Failed to analyze source page '%s': %s", name, err.Error())), nil
	}
	text, err := guideText(guide)
	if err != nil {
		return Response{}, err
	}
	response := Response{Success: true, SourceSchemaName: args.SchemaName, SourceType: &sourceType, Guide: json.RawMessage(text),
		ResolvedTargetVersion: strp(version), ResolvedFrom: strp(resolvedFrom), VersionWarning: stand.VersionWarning(resolvedFrom),
		RequestRegistryWarning: requestWarning, RequiresVersionConfirmation: resolvedFrom == "latest-fallback", RulesWarning: rulesWarning}
	if resolvedFrom == "latest-fallback" && resolution.Reason != "" {
		response.ResolvedFromReason = strp(resolution.Reason)
	}
	return response, nil
}

func parseEntries(raw []json.RawMessage) ([]*RegistryEntry, error) {
	entries := make([]*RegistryEntry, 0, len(raw))
	for _, item := range raw {
		entry, err := ParseRegistryEntry(item)
		if err != nil {
			return nil, err
		}
		if entry != nil {
			entries = append(entries, entry)
		}
	}
	return entries, nil
}

type mobileTemplateProbe struct {
	parents         *dict[string]
	layoutConfigs   *dict[*jn]
	viewModelConfig *jn
	modelConfig     *jn
	unavailable     bool
	typesByName     *dict[*string]
	nodesByName     *dict[*jn]
}

func emptyMobileTemplate() mobileTemplateProbe {
	return mobileTemplateProbe{parents: newCI[string](), layoutConfigs: newCI[*jn](), unavailable: true,
		typesByName: newCI[*string](), nodesByName: newCI[*jn]()}
}

func loadMobileTemplate(ctx context.Context, stand Stand, name *string) mobileTemplateProbe {
	if name == nil || isBlank(*name) {
		return emptyMobileTemplate()
	}
	page, err := stand.ReadPage(ctx, *name)
	if err != nil || !page.Success || page.ViewConfig == nil {
		return emptyMobileTemplate()
	}
	bundle, err := bundleOf(page)
	if err != nil || bundle.ViewConfig == nil {
		return emptyMobileTemplate()
	}
	return mobileTemplateProbe{parents: CollectParentByName(bundle.ViewConfig), layoutConfigs: CollectLayoutConfigByName(bundle.ViewConfig),
		viewModelConfig: bundle.ViewModelConfig, modelConfig: bundle.ModelConfig, typesByName: CollectComponentTypesByName(bundle.ViewConfig),
		nodesByName: CollectComponentNodesByName(bundle.ViewConfig)}
}

type webTemplateBaseline struct {
	names       *strSet
	nodes       *dict[*jn]
	unavailable bool
	resources   *jn
}

func loadWebTemplateBaseline(ctx context.Context, stand Stand, parentName, ownName *string) webTemplateBaseline {
	absent := webTemplateBaseline{names: newSet(true), nodes: newCI[*jn]()}
	if parentName == nil || isBlank(*parentName) || eqip(parentName, ownName) {
		return absent
	}
	page, err := stand.ReadPage(ctx, *parentName)
	if err == nil && page.Success && page.ViewConfig != nil {
		if bundle, err := bundleOf(page); err == nil && bundle.ViewConfig != nil {
			nodes := CollectComponentNodesByName(bundle.ViewConfig)
			names := newSet(true, nodes.keys()...)
			var resources *jn
			if bundle.ResourcesStrings != nil {
				resources = toJToken(bundle.ResourcesStrings)
			}
			return webTemplateBaseline{names: names, nodes: nodes, resources: resources}
		}
	}
	absent.unavailable = true
	return absent
}
