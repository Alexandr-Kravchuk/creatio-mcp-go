package mobileguide

import (
	"fmt"
	"regexp"
	"strings"
)

// The analysis engine: WebToMobileAnalysisService.cs (clio 8.1.0.142), ported decision for decision. The
// names follow clio's methods so a reader can hold the two side by side.

const (
	guidanceArticleName      = "freedom-page-web-to-mobile-conversion"
	positionalFallbackParent = "MainContainer"
	itemsPropertyName        = "items"
	invalidRuleIdentifier    = "<invalid-name>"
	maxParseDepth            = 64
)

var (
	resourceStringsRefPattern = regexp.MustCompile(`\$Resources\.Strings\.([A-Za-z_][A-Za-z0-9_]*)`)
	dollarRefPattern          = regexp.MustCompile(`\$([A-Za-z_][A-Za-z0-9_]*)`)
	safeIdentifierPattern     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	safeContainerNamePattern  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(:[A-Za-z_][A-Za-z0-9_]*)?$`)
	templateTokenPattern      = regexp.MustCompile(`\{\{\s*([^{}]+?)\s*\}\}`)
	macroResourcePattern      = regexp.MustCompile(`#ResourceString\(([^)]+)\)#`)
	dollarResourcePattern     = regexp.MustCompile(`\$Resources\.Strings\.([A-Za-z0-9_]+)`)
)

// mapEntry is ElementMapEntry, the working element map.
type mapEntry struct {
	WebName          *string
	WebType          *string
	Operation        string
	Name             *string
	MobileType       *string
	ParentName       *string
	PropertyName     *string
	ParentSource     *string
	Index            *int
	CaptionResource  *CaptionResource
	DeclaredByRule   bool
	Values           *jn
	Reason           []*ReasonCode
	PositionalAnchor *string
	MergeParentName  *string
}

func isDrop(e *mapEntry) bool { return e != nil && e.Operation == OperationDrop }
func isRelocateChildren(e *mapEntry) bool {
	return e != nil && e.Operation == OperationRelocateChildren
}
func isMerge(e *mapEntry) bool  { return e != nil && e.Operation == OperationMerge }
func isInsert(e *mapEntry) bool { return e != nil && e.Operation == OperationInsert }

// reasonParam is one (key, JsonNode value) pair of a reason code.
type reasonParam struct {
	key   string
	value *jn
}

func rp(key string, value *string) reasonParam { return reasonParam{key, stjString(value)} }

// reason is WebToMobileAnalysisService.Reason: null-valued pairs are dropped; no pair leaves params absent.
func reason(code string, params ...reasonParam) *ReasonCode {
	result := &ReasonCode{Code: strp(code)}
	for _, param := range params {
		if param.value == nil {
			continue
		}
		if result.Params == nil {
			result.Params = &OrderedMap[*jn]{}
		}
		result.Params.Set(param.key, param.value)
	}
	return result
}

func reasons(codes ...*ReasonCode) []*ReasonCode {
	list := []*ReasonCode{}
	for _, code := range codes {
		if code != nil {
			list = append(list, code)
		}
	}
	return list
}

func drop(name, typ *string, codes ...*ReasonCode) *mapEntry {
	return &mapEntry{WebName: name, WebType: nzp(typ), Operation: OperationDrop, Reason: reasons(codes...)}
}

// Analyze is WebToMobileAnalysisService.Analyze. A panic of engineFault (clio's exception) is returned as an
// error carrying the exception message.
func Analyze(in *AnalyzeInput) (guide *Guide, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			if fault, ok := recovered.(engineFault); ok {
				guide, err = nil, fault
				return
			}
			panic(recovered)
		}
	}()
	return analyze(in), nil
}

func analyze(in *AnalyzeInput) *Guide {
	if in.Bundle == nil {
		panic(engineFault{"Value cannot be null. (Parameter 'bundle')"})
	}
	if in.MobileTypes == nil {
		panic(engineFault{"Value cannot be null. (Parameter 'mobileTypes')"})
	}
	if in.WebTypes == nil {
		panic(engineFault{"Value cannot be null. (Parameter 'webTypes')"})
	}
	if in.Rules == nil {
		panic(engineFault{"Value cannot be null. (Parameter 'rules')"})
	}
	if in.MobileRequestTypes == nil {
		panic(engineFault{"Value cannot be null. (Parameter 'mobileRequestTypes')"})
	}
	bundle := in.Bundle
	rules := in.Rules
	containerMap := in.ContainerNameMap
	if containerMap == nil {
		containerMap = newCI[string]()
	}
	componentMap := in.ComponentNameMap
	if componentMap == nil {
		componentMap = newCI[*ComponentMappingRule]()
	}
	mobileTypesByName := in.MobileTemplateTypesByName
	if mobileTypesByName == nil {
		mobileTypesByName = newCI[*string]()
	}
	webBaselineNodes := in.WebTemplateBaselineNodes
	if webBaselineNodes == nil {
		webBaselineNodes = newCI[*jn]()
	}
	mobileLayoutConfigs := in.MobileTemplateLayoutConfigs
	if mobileLayoutConfigs == nil {
		mobileLayoutConfigs = newCI[*jn]()
	}
	mobileContainerParents := in.MobileContainerParents

	templateProbeAvailable := in.MobileTemplateTypesByName.len() > 0
	selection := selectDeclaredElements(in.TemplateRule, in.MobileTypes, bundle.ViewConfig, containerMap,
		in.TemplateComponentNames, mobileTypesByName, templateProbeAvailable)
	declared := selection.accepted
	containerMap = selection.containerNameMap
	if templateProbeAvailable && len(declared) > 0 {
		mobileTypesByName = withDeclaredTypes(mobileTypesByName, declared)
		mobileContainerParents = withDeclaredParents(mobileContainerParents, declared)
		mobileLayoutConfigs = withDeclaredElementLayouts(mobileLayoutConfigs, declared)
	}

	var tree *jn
	if bundle.ViewConfig == nil {
		tree = newArr()
	} else {
		tree = toJToken(bundle.ViewConfig)
	}
	sourceNamedCount := 0
	if bundle.ViewConfig != nil {
		sourceNamedCount = collectComponentNames(bundle.ViewConfig).len()
	}
	if in.TemplateComponentNames.len() > 0 {
		scopeNames := collectScopeContainerNames(rules)
		tree = pruneTemplateComponents(tree, containerMap, componentMap, in.TemplateComponentNames, mobileTypesByName,
			in.MobileByType, webBaselineNodes, scopeNames)
	}

	var structure []*SourceComponentInfo
	namesByType := newCI[[]string]()
	walkStructure(tree, nil, containerMap, in.WebByType, in.MobileByType, &structure, namesByType)
	if structure == nil {
		structure = []*SourceComponentInfo{}
	}

	var layoutResolution *string
	if len(structure) == 0 && sourceNamedCount > 0 {
		layoutResolution = strp(fmt.Sprintf("empty: the source page has %d component(s) but the converted layout is empty — "+
			"the web-template baseline may be unresolved (e.g. a replacing schema over a same-named base, "+
			"or the parent template could not be read). Verify the source page and its template ancestry.", sourceNamedCount))
	}

	webOnly := collectWebOnlySections(bundle)
	dataSources := collectDataSources(bundle)

	attrToColumn := buildAttrToColumn(bundle)
	resources := parseResources(bundle)
	requestMap := buildRequestMap(rules)
	collectors := &requestCollectors{}
	sourceLayouts := newCI[*jn]()
	gridContainerColumns := newCI[int]()
	positionalParentByAnchor := resolvePositionalParents(in.PositionalPlacements, mobileContainerParents)
	positionalAnchorByWebAnchor := resolvePositionalAnchors(in.PositionalPlacements)
	var actionTargets *dict[*ActionTargetResolution]
	if in.ActionTargetsProbe != nil {
		actionTargets = in.ActionTargetsProbe.TargetsByKey
	}
	if actionTargets == nil {
		actionTargets = newCI[*ActionTargetResolution]()
	}
	actionComponentProperties := actionComponentPropertiesOf(rules)
	componentRemovals := componentRemovalsOf(rules)
	elementMap := buildElementMap(tree, containerMap, componentMap, in.MobileTypes, in.MobileByType, in.WebByType, rules,
		attrToColumn, resources, requestMap, collectors, sourceLayouts, gridContainerColumns, actionComponentProperties,
		positionalParentByAnchor, positionalAnchorByWebAnchor, mobileTypesByName, in.MobileTemplateNodesByName,
		webBaselineNodes, in.WebTemplateResources, declared, actionTargets, in.MobileRequestTypes)

	excludedRemovedNames, excludedRemovedMobileNames := removeExcludedComponents(&elementMap, rules)

	applyComponentRemovals(&elementMap, requestMap, in.MobileRequestTypes, actionComponentProperties, componentRemovals,
		collectors)

	emptyRemovedNames, emptyRemovedMobileNames := removeEmptyContainers(elementMap, rules)
	compactPositionalIndexes(elementMap)
	assignConvertedTabIndexes(elementMap)

	var declaredProps *declaredPropertyIndex
	if in.MobileRegistryGeneration.CatalogIsRuntimeDerived() {
		declaredProps = declaredPropertyIndexFromRegistry(in.MobileByType, in.MobileRegistryGeneration.BaseInputs)
	} else {
		declaredProps = disabledPropertyIndex()
	}
	propertyPrune := pruneUndeclaredProperties(elementMap, declaredProps, collectors)
	requestConversions := buildRequestConversionInfo(collectors, emptyRemovedMobileNames, excludedRemovedMobileNames,
		in.ActionTargetsProbe)

	adaptiveLayout := buildAdaptiveLayout(&elementMap, sourceLayouts, gridContainerColumns, mobileContainerParents)
	placePositionalGroups(&elementMap, mobileLayoutConfigs)
	tabAreaLayers := buildTabAreaLayers(&elementMap, rules, in.SourcePage)
	initializeContainerChildSlots(elementMap, in.MobileByType)
	overrides := applyComponentPropertyOverrides(elementMap, rules)
	normalizePlacements(elementMap)
	stampParentSource(elementMap, in.MobileTemplateTypesByName)

	suggestions := buildComponentSuggestions(namesByType, rules, in.WebTypes, elementMap)
	var baseInputs *dict[*jn]
	if in.MobileRegistryGeneration.CatalogIsRuntimeDerived() {
		baseInputs = in.MobileRegistryGeneration.BaseInputs
	}
	contracts := buildMobileContracts(suggestions, in.MobileByType, baseInputs)

	modelConfig := passthroughModelConfig(bundle)
	layoutRemovedNames := newSet(true, emptyRemovedNames.keys()...)
	for _, name := range excludedRemovedNames.keys() {
		layoutRemovedNames.add(name)
	}
	viewModelConfig := buildMobileViewModelConfig(bundle, tree, elementMap, layoutRemovedNames)
	viewModelConfigDiff, vmcConflicts := buildTargetedDiff(viewModelConfig, in.MobileTemplateViewModelConfig, strp("viewModelConfig"))
	modelConfigDiff, mcConflicts := buildTargetedDiff(modelConfig, in.MobileTemplateModelConfig, strp("modelConfig"))
	dataSectionConflicts := append(append([]*DataSectionConflict{}, vmcConflicts...), mcConflicts...)

	pageBusinessRules := convertPageBusinessRules(in.PageBusinessRulesProbe, elementMap, bundle.ViewModelConfig)
	resourceStrings := collectResourceStrings(elementMap, modelConfig, viewModelConfig, resources)

	guide := &Guide{
		SourcePage:                in.SourcePage,
		SourceType:                strp(SourceTypeFreedomWeb),
		SourceStructure:           structure,
		LayoutResolution:          layoutResolution,
		ModelConfig:               rawOf(modelConfig),
		ViewModelConfig:           rawOf(viewModelConfig),
		ModelConfigDiff:           rawOf(modelConfigDiff),
		ViewModelConfigDiff:       rawOf(viewModelConfigDiff),
		ComponentSuggestions:      suggestions,
		ViewConfigDiff:            projectViewConfigDiff(elementMap),
		NameMap:                   projectNameMap(elementMap),
		UnresolvedParents:         projectUnresolvedParents(elementMap),
		DroppedElements:           projectDroppedElements(elementMap),
		MobileContracts:           contracts,
		SectionRegistration:       in.SectionRegistration,
		ExistingMobilePages:       in.ExistingMobilePages,
		PageBusinessRules:         pageBusinessRules,
		RequestConversions:        requestConversions,
		Normalizations:            buildNormalizations(overrides),
		PropertyPruneApplied:      declaredProps.enabled,
		GuidanceArticle:           strp(guidanceArticleName),
		SuggestedTargetSchemaName: in.SuggestedTarget,
	}
	if in.SourceTemplate != nil && !isBlank(*in.SourceTemplate) {
		guide.SourceTemplate = in.SourceTemplate
	}
	if guide.ExistingMobilePages == nil {
		guide.ExistingMobilePages = []*ExistingMobilePageInfo{}
	}
	if len(webOnly) > 0 {
		guide.WebOnlySections = webOnly
	}
	if len(dataSources) > 0 {
		guide.DataSources = dataSources
	}
	if len(dataSectionConflicts) > 0 {
		guide.DataSectionConflicts = dataSectionConflicts
	}
	if in.TemplateRule != nil {
		guide.RecommendedMobileTemplate = in.TemplateRule.Mobile
	}
	guide.TemplateMatch = resolveTemplateMatch(in.TemplateRule)
	guide.ContainerMap = buildContainerMap(in.TemplateRule)
	if len(adaptiveLayout) > 0 {
		guide.AdaptiveLayout = adaptiveLayout
	}
	if len(tabAreaLayers) > 0 {
		guide.TabAreaLayers = tabAreaLayers
	}
	if len(propertyPrune) > 0 {
		guide.PrunedProperties = propertyPrune
	}
	if resourceStrings.Len() > 0 {
		guide.ResourceStrings = resourceStrings
	}
	return guide
}

// --- page business rules ---------------------------------------------------------------------------------

func convertPageBusinessRules(probe *PageBusinessRuleProbeResult, elementMap []*mapEntry, viewModelConfig *jn) *PageBusinessRuleConversionInfo {
	if probe == nil {
		return nil
	}
	if !probe.ProbeOk {
		return &PageBusinessRuleConversionInfo{ProbeOk: false, Note: probe.Note, ConvertedRules: []*ConvertedPageBusinessRule{}, DroppedRules: []*DroppedPageBusinessRule{}}
	}
	survivors := newCI[string]()
	for _, entry := range elementMap {
		if entry == nil || entry.WebName == nil || isBlank(*entry.WebName) {
			continue
		}
		if isMerge(entry) || isInsert(entry) {
			name := *entry.WebName
			if entry.Name != nil && !isBlank(*entry.Name) {
				name = *entry.Name
			}
			survivors.set(*entry.WebName, name)
		}
	}
	resolver := buildAttributePathResolver(viewModelConfig)
	converted := []*ConvertedPageBusinessRule{}
	dropped := []*DroppedPageBusinessRule{}
	for _, rule := range probe.Rules {
		if rule.ConditionIssue != ConditionIssueNone {
			code := DropRuleConditionUnconvertible
			switch rule.ConditionIssue {
			case ConditionIssueMixedAndOr:
				code = DropRuleConditionMixedAndOr
			case ConditionIssueUnrecognizedComparison:
				code = DropRuleConditionUnsupportedComparison
			}
			dropped = append(dropped, &DroppedPageBusinessRule{Caption: rule.Caption, Reason: []*ReasonCode{reason(code)}})
			continue
		}
		actions := newArr()
		anyConverted := false
		for _, action := range rule.Actions {
			seen := newSet(true)
			items := newArr()
			for _, name := range action.ElementItems {
				if name == nil {
					continue
				}
				mobile, ok := survivors.get(*name)
				if !ok {
					continue
				}
				if seen.add(mobile) {
					items.arr = append(items.arr, newStr(mobile))
				}
			}
			if len(items.arr) > 0 {
				object := newObj()
				object.set("type", stjString(action.ActionType))
				object.set("items", items)
				actions.arr = append(actions.arr, object)
				anyConverted = true
			}
		}
		if !anyConverted {
			dropped = append(dropped, &DroppedPageBusinessRule{Caption: rule.Caption, Reason: []*ReasonCode{reason(DropRuleNoActionConverts)}})
			continue
		}
		condition := rule.Condition.clone()
		remapConditionAttributePaths(condition, resolver)
		ruleNode := newObj()
		ruleNode.set("caption", stjString(rule.Caption))
		ruleNode.set("condition", condition)
		ruleNode.set("actions", actions)
		converted = append(converted, &ConvertedPageBusinessRule{Caption: rule.Caption, Rule: rawOf(ruleNode)})
	}
	return &PageBusinessRuleConversionInfo{ProbeOk: true, Note: probe.Note, ConvertedRules: converted, DroppedRules: dropped}
}

type attributePathResolver struct {
	byPath, byColumn *dict[string]
	names            *strSet
}

func (r *attributePathResolver) resolve(sourcePath string) string {
	if isBlank(sourcePath) || r.names.contains(sourcePath) {
		return sourcePath
	}
	if value, ok := r.byPath.get(sourcePath); ok {
		return value
	}
	column := sourcePath
	if dot := strings.LastIndex(sourcePath, "."); dot >= 0 {
		column = sourcePath[dot+1:]
	}
	if value, ok := r.byColumn.get(column); ok {
		return value
	}
	return sourcePath
}

func buildAttributePathResolver(viewModelConfig *jn) *attributePathResolver {
	resolver := &attributePathResolver{byPath: newCI[string](), byColumn: newCI[string](), names: newSet(true)}
	ambiguous := newSet(true)
	if viewModelConfig.isObj() {
		if attributes := viewModelConfig.get("attributes"); attributes.isObj() {
			for _, key := range attributes.keys {
				resolver.names.add(key)
				value := attributes.props[key]
				var pathNode *jn
				if value.isObj() {
					if modelConfig := value.get("modelConfig"); modelConfig.isObj() {
						pathNode = modelConfig.get("path")
					}
				}
				if pathNode == nil {
					continue
				}
				path := pathNode.getStringValue()
				if isBlank(path) {
					continue
				}
				resolver.byPath.set(path, key)
				column := path
				if dot := strings.LastIndex(path, "."); dot >= 0 {
					column = path[dot+1:]
				}
				if ambiguous.contains(column) {
					continue
				}
				if resolver.byColumn.has(column) {
					resolver.byColumn.remove(column)
					ambiguous.add(column)
				} else {
					resolver.byColumn.set(column, key)
				}
			}
		}
	}
	return resolver
}

func remapConditionAttributePaths(condition *jn, resolver *attributePathResolver) {
	if !condition.isObj() {
		return
	}
	if inner := condition.get("conditions"); inner.isArr() {
		for _, child := range inner.arr {
			remapConditionAttributePaths(child, resolver)
		}
	}
	remapOperandPath(condition.get("leftExpression"), resolver)
	remapOperandPath(condition.get("rightExpression"), resolver)
}

func remapOperandPath(expression *jn, resolver *attributePathResolver) {
	if !expression.isObj() {
		return
	}
	pathNode := expression.get("path")
	if pathNode == nil {
		return
	}
	path := pathNode.getStringValue()
	if isBlank(path) {
		return
	}
	if typeNode := expression.get("type"); typeNode != nil {
		if typ := typeNode.getStringValue(); !eqi(typ, "AttributeValue") {
			return
		}
	}
	expression.set("path", newStr(resolver.resolve(path)))
}

// --- structure walk --------------------------------------------------------------------------------------

func walkStructure(nodes *jn, parentName *string, containerMap *dict[string], webByType, mobileByType *dict[*RegistryEntry],
	structure *[]*SourceComponentInfo, namesByType *dict[[]string]) {
	for _, node := range nodes.arr {
		if !node.isObj() {
			continue
		}
		name := node.get("name").tstrp()
		typ := node.get("type").tstrp()
		isMapped := name != nil && *name != "" && containerMap.has(*name)
		holds := false
		if items := node.get(itemsPropertyName); items.isArr() {
			for _, child := range items.arr {
				if child.isObj() && isComponentObjectJ(child) {
					holds = true
					break
				}
			}
		}
		holds = holds || len(childComponentSlotsJ(node).arr) > 0
		isContainer := isMapped || holds || isLayoutContainer(typ, name, webByType, mobileByType)
		info := &SourceComponentInfo{Name: name, ParentName: parentName, IsContainer: isContainer}
		if typ != nil && !isBlank(*typ) {
			info.Type = typ
		}
		*structure = append(*structure, info)
		if typ != nil && !isBlank(*typ) && name != nil && *name != "" {
			list, _ := namesByType.get(*typ)
			namesByType.set(*typ, append(list, *name))
		}
		childParent := parentName
		if name != nil && *name != "" {
			childParent = name
		}
		if items := node.get("items"); items.isArr() {
			walkStructure(items, childParent, containerMap, webByType, mobileByType, structure, namesByType)
		}
		walkStructure(childComponentSlotsJ(node), childParent, containerMap, webByType, mobileByType, structure, namesByType)
	}
}

func isLayoutContainer(typ, name *string, webByType, mobileByType *dict[*RegistryEntry]) bool {
	if typ != nil && !isBlank(*typ) {
		if entry, ok := webByType.get(*typ); ok && entry != nil && entry.Container != nil {
			return *entry.Container
		}
		if entry, ok := mobileByType.get(*typ); ok && entry != nil && entry.Container != nil {
			return *entry.Container
		}
	}
	return name != nil && *name != "" && (hasSuffixFold(*name, "Container") || hasSuffixFold(*name, "Panel"))
}

func hasSuffixFold(value, suffix string) bool {
	return len(value) >= len(suffix) && eqi(value[len(value)-len(suffix):], suffix)
}

func hasPrefixFold(value, prefix string) bool {
	return len(value) >= len(prefix) && eqi(value[:len(prefix)], prefix)
}

// collectComponentNames is CollectComponentNames over a System.Text.Json tree.
func collectComponentNames(viewConfig *jn) *strSet {
	names := newSet(true)
	collectComponentNamesInto(viewConfig, names)
	return names
}

func collectComponentNamesInto(nodes *jn, names *strSet) {
	if nodes == nil {
		return
	}
	for _, node := range nodes.arr {
		if !node.isObj() {
			continue
		}
		if nameNode, ok := node.props["name"]; ok && nameNode != nil {
			name := nameNode.getStringValue()
			if !isBlank(name) {
				names.add(name)
			}
		}
		if items, ok := node.props["items"]; ok && items.isArr() {
			collectComponentNamesInto(items, names)
		}
		collectComponentNamesInto(childComponentSlotsS(node), names)
	}
}

// childComponentSlotsS is ChildComponentSlots(JsonObject).
func childComponentSlotsS(obj *jn) *jn {
	collected := newArr()
	for _, key := range obj.keys {
		if eqi(key, "items") {
			continue
		}
		value := obj.props[key]
		if value.isObj() && isComponentObjectS(value) {
			collected.arr = append(collected.arr, value.clone())
		} else if value.isArr() {
			for _, element := range value.arr {
				if element.isObj() && isComponentObjectS(element) {
					collected.arr = append(collected.arr, element.clone())
				}
			}
		}
	}
	return collected
}

// childComponentSlotsJ is ChildComponentSlots(JObject).
func childComponentSlotsJ(node *jn) *jn {
	collected := newArr()
	for _, key := range node.keys {
		if eqi(key, itemsPropertyName) {
			continue
		}
		value := node.props[key]
		if value.isObj() && isComponentObjectJ(value) {
			collected.arr = append(collected.arr, value.clone())
		} else if value.isArr() {
			for _, element := range value.arr {
				if element.isObj() && isComponentObjectJ(element) {
					collected.arr = append(collected.arr, element.clone())
				}
			}
		}
	}
	return collected
}

func holdsChildComponents(node *jn) bool {
	for _, key := range node.keys {
		value := node.props[key]
		if value.isObj() && isComponentObjectJ(value) {
			return true
		}
		if value.isArr() {
			for _, element := range value.arr {
				if element.isObj() && isComponentObjectJ(element) {
					return true
				}
			}
		}
	}
	return false
}

func isComponentObjectS(obj *jn) bool {
	typ := stringProp(obj, "type")
	return typ != nil && *typ != "" && hasPrefixFold(*typ, "crt.")
}

func isComponentObjectJ(obj *jn) bool {
	typ := obj.get("type")
	return typ != nil && typ.k == jsString && hasPrefixFold(typ.s, "crt.")
}

func isComponentArray(array *jn) bool {
	if len(array.arr) == 0 {
		return false
	}
	for _, element := range array.arr {
		if !element.isObj() || !isComponentObjectJ(element) {
			return false
		}
	}
	return true
}

// stringProp is StringProp: a string property, or null when absent or not a string.
func stringProp(obj *jn, name string) *string {
	value := obj.get(name)
	if value != nil && value.k == jsString {
		text := value.s
		return &text
	}
	return nil
}

// CollectComponentTypesByName is the public collector the tool runs over the mobile template.
func CollectComponentTypesByName(viewConfig *jn) *dict[*string] {
	result := newCI[*string]()
	collectComponentTypesByName(viewConfig, result)
	return result
}

func collectComponentTypesByName(nodes *jn, result *dict[*string]) {
	if nodes == nil {
		return
	}
	for _, node := range nodes.arr {
		if !node.isObj() {
			continue
		}
		name := stringProp(node, "name")
		if name != nil && !isBlank(*name) && !result.has(*name) {
			result.set(*name, stringProp(node, "type"))
		}
		if items, ok := node.props["items"]; ok && items.isArr() {
			collectComponentTypesByName(items, result)
		}
		collectComponentTypesByName(childComponentSlotsS(node), result)
	}
}

// CollectLayoutConfigByName is CollectLayoutConfigByName.
func CollectLayoutConfigByName(viewConfig *jn) *dict[*jn] {
	result := newCI[*jn]()
	collectLayoutConfigByName(viewConfig, result)
	return result
}

func collectLayoutConfigByName(nodes *jn, result *dict[*jn]) {
	if nodes == nil {
		return
	}
	for _, node := range nodes.arr {
		if !node.isObj() {
			continue
		}
		name := stringProp(node, "name")
		if name != nil && !isBlank(*name) && !result.has(*name) {
			if layout, ok := node.props["layoutConfig"]; ok && layout.isObj() {
				result.set(*name, layout)
			}
		}
		if items, ok := node.props["items"]; ok && items.isArr() {
			collectLayoutConfigByName(items, result)
		}
		collectLayoutConfigByName(childComponentSlotsS(node), result)
	}
}

// CollectComponentNodesByName is CollectComponentNodesByName: Newtonsoft nodes of a System.Text.Json tree.
func CollectComponentNodesByName(viewConfig *jn) *dict[*jn] {
	result := newCI[*jn]()
	if viewConfig == nil {
		return result
	}
	collectComponentNodesByName(toJToken(viewConfig), result)
	return result
}

func collectComponentNodesByName(nodes *jn, result *dict[*jn]) {
	for _, node := range nodes.arr {
		if !node.isObj() {
			continue
		}
		name := node.get("name").tstrp()
		if name != nil && !isBlank(*name) && !result.has(*name) {
			result.set(*name, node)
		}
		if items := node.get("items"); items.isArr() {
			collectComponentNodesByName(items, result)
		}
		collectComponentNodesByName(childComponentSlotsJ(node), result)
	}
}

// CollectParentByName is CollectParentByName.
func CollectParentByName(viewConfig *jn) *dict[string] {
	result := newCI[string]()
	collectParentByName(viewConfig, nil, result)
	return result
}

func collectParentByName(nodes *jn, parentName *string, result *dict[string]) {
	if nodes == nil {
		return
	}
	for _, node := range nodes.arr {
		if !node.isObj() {
			continue
		}
		var name *string
		if nameNode, ok := node.props["name"]; ok && nameNode != nil {
			name = strp(nameNode.getStringValue())
		}
		if name != nil && !isBlank(*name) && parentName != nil && !isBlank(*parentName) && !result.has(*name) {
			result.set(*name, *parentName)
		}
		childParent := parentName
		if name != nil && !isBlank(*name) {
			childParent = name
		}
		if items, ok := node.props["items"]; ok && items.isArr() {
			collectParentByName(items, childParent, result)
		}
		collectParentByName(childComponentSlotsS(node), childParent, result)
	}
}

// --- template chrome prune -------------------------------------------------------------------------------

func pruneTemplateComponents(nodes *jn, containerMap *dict[string], componentMap *dict[*ComponentMappingRule],
	baseline *strSet, mobileTypesByName *dict[*string], mobileByType *dict[*RegistryEntry], webBaselineNodes *dict[*jn],
	scopeNames *strSet) *jn {
	result := newArr()
	recurse := func(array *jn) *jn {
		return pruneTemplateComponents(array, containerMap, componentMap, baseline, mobileTypesByName, mobileByType,
			webBaselineNodes, scopeNames)
	}
	for _, node := range nodes.arr {
		if !node.isObj() {
			result.arr = append(result.arr, node)
			continue
		}
		name := node.get("name").tstrp()
		typ := node.get("type").tstrp()
		items := node.get("items")
		if !items.isArr() {
			items = nil
		}
		named := name != nil && *name != ""
		isMappedTwin := named && (containerMap.has(*name) || (componentMap != nil && componentMap.has(*name)))
		isContainerLike := (items != nil && len(items.arr) > 0) || isLayoutContainer(typ, name, nil, mobileByType)
		isAutoTwin := false
		if !isMappedTwin && named && webBaselineNodes.has(*name) && mobileTypesByName != nil {
			if twinType, ok := mobileTypesByName.get(*name); ok && typ != nil && *typ != "" && twinType != nil &&
				eqi(*twinType, *typ) && !isContainerLike {
				isAutoTwin = true
			}
		}
		isScope := named && scopeNames.contains(*name)
		isTemplateOwned := named && baseline.contains(*name) && !isMappedTwin && !isAutoTwin && !isScope
		if isTemplateOwned {
			if items != nil {
				result.arr = append(result.arr, recurse(items).arr...)
			}
			for _, key := range node.keys {
				value := node.props[key]
				if !eqi(key, itemsPropertyName) && value.isArr() && isComponentArray(value) {
					result.arr = append(result.arr, recurse(value).arr...)
				}
			}
			continue
		}
		if items != nil {
			node.set("items", recurse(items))
		}
		for _, key := range node.keyList() {
			value := node.props[key]
			if !eqi(key, itemsPropertyName) && value.isArr() && isComponentArray(value) {
				node.set(key, recurse(value))
			}
		}
		result.arr = append(result.arr, node)
	}
	return result
}
