package mobileguide

import "strings"

// Clicked convertibility (ClickedConvertibility).
const (
	clickedNone = iota
	clickedUnsupported
	clickedConvertible
)

// walkScoped is WalkElements' branch for a node inside a non-converting scope container.
func walkScoped(ctx *walkContext, node *jn, name, typ *string, items *jn, mobileParentName *string, sourceAncestors []string,
	hostableParentName *string) {
	scopeContainer := *name
	for index := len(sourceAncestors) - 1; index >= 0; index-- {
		if ctx.scopeContainerNames.contains(sourceAncestors[index]) {
			scopeContainer = sourceAncestors[index]
			break
		}
	}
	scopedType := resolveTemplateTargetType(ctx.rules, node, sourceAncestors)
	var scopedTarget *placement
	if scopedType != nil {
		if target, ok := resolveTemplatePlacement(ctx, node, scopedType, *name, resolveParent(ctx, mobileParentName),
			itemsPropertyName, sourceAncestors); ok {
			scopedTarget = &target
		}
	}
	clicked, scopedRequest := classifyClicked(ctx, node)
	targetMissing := scopedTarget != nil && retargetTargetMissing(ctx, scopedTarget.parent)
	switch {
	case scopedTarget != nil && clicked == clickedConvertible && retargetSourceIsInheritedChrome(ctx, *name):
		ctx.add(drop(name, typ, reason(DropInheritedChrome, rp("scope", nz(scopeContainer)),
			rp("targetParent", nz(scopedTarget.parent)), rp("targetSlot", nz(scopedTarget.property)))))
		if scopedRequest != nil {
			ctx.collectors.dropped = append(ctx.collectors.dropped, &DroppedRequest{ElementName: name, Binding: strp("clicked"),
				WebRequest: scopedRequest, Reason: []*ReasonCode{reason(DropRequestChromeNative)}})
		}
	case scopedTarget != nil && clicked == clickedConvertible && !targetMissing:
		caption := resolveCaptionResource(ctx, node, *name)
		values := buildMobileValues(ctx, node, *name, scopedType, caption, strp(scopedTarget.parent),
			strp(scopedTarget.property), sourceAncestors)
		ctx.add(&mapEntry{WebName: name, WebType: nzp(typ), Operation: OperationInsert, Name: name, MobileType: scopedType,
			ParentName: strp(scopedTarget.parent), PropertyName: strp(scopedTarget.property), CaptionResource: caption, Values: values})
	default:
		dropReason := scopeDropReason(ctx, scopeContainer, scopedType, scopedTarget, clicked, scopedRequest, targetMissing)
		ctx.add(drop(name, typ, dropReason))
		if scopedRequest != nil {
			ctx.collectors.dropped = append(ctx.collectors.dropped, &DroppedRequest{ElementName: name, Binding: strp("clicked"),
				WebRequest: scopedRequest, Reason: []*ReasonCode{dropReason}})
		}
	}
	childAncestors := appendAncestor(sourceAncestors, name)
	if items != nil {
		walkElements(ctx, items, mobileParentName, itemsPropertyName, childAncestors, true, hostableParentName)
	}
	recurseType := scopedType
	if recurseType == nil {
		recurseType = typ
	}
	recurseChildArrays(ctx, node, name, recurseType, childAncestors, true)
}

func hoistChildArraysInto(ctx *walkContext, node *jn, mobileParentName *string, mobileType *string, childAncestors []string) {
	for _, key := range node.keyList() {
		if eqi(key, itemsPropertyName) {
			continue
		}
		value := node.props[key]
		if isChildElementArray(ctx, mobileType, key, value, childAncestors) {
			walkElements(ctx, value, mobileParentName, itemsPropertyName, childAncestors, false, nil)
		}
	}
}

func recurseChildArrays(ctx *walkContext, node *jn, mobileParentName *string, mobileType *string, childAncestors []string,
	inScope bool) {
	for _, key := range node.keyList() {
		if eqi(key, itemsPropertyName) {
			continue
		}
		value := node.props[key]
		if isChildElementArray(ctx, mobileType, key, value, childAncestors) {
			walkElements(ctx, value, mobileParentName, key, childAncestors, inScope, nil)
		}
	}
}

func classifyClicked(ctx *walkContext, node *jn) (int, *string) {
	typ := node.get("type").tstrp()
	clicked := actionBindingOf(ctx.actionComponentProperties, node, typ)
	if clicked == nil {
		return clickedNone, nil
	}
	request := clicked.get("request").tstr()
	if isRequestSupported(ctx.requestMap, ctx.mobileRequestTypes, request) {
		return clickedConvertible, &request
	}
	if isActionOnlyType(ctx.actionComponentProperties, typ) {
		return clickedUnsupported, &request
	}
	return clickedConvertible, &request
}

func retargetTargetMissing(ctx *walkContext, parentName string) bool {
	return ctx.mobileTypesByName.len() > 0 && parentName != "" && !ctx.mobileTypesByName.has(parentName)
}

func retargetSourceIsInheritedChrome(ctx *walkContext, name string) bool {
	return name != "" && ctx.webBaselineNodes.has(name)
}

func scopeDropReason(ctx *walkContext, scopeContainer string, scopedType *string, scopedTarget *placement, clicked int,
	request *string, targetMissing bool) *ReasonCode {
	scope := nz(scopeContainer)
	if targetMissing && scopedTarget != nil {
		return reason(DropTargetMissing, rp("missingParent", nz(scopedTarget.parent)), rp("scope", scope))
	}
	if clicked == clickedUnsupported {
		return unsupportedRequestDropReason(ctx.requestMap, deref(request), scope)
	}
	if scopedType == nil {
		return reason(DropNoRuleInScope, rp("scope", scope))
	}
	return reason(DropNotAnActionInScope, rp("scope", scope))
}

func isChildElementArray(ctx *walkContext, mobileType *string, propName string, value *jn, childAncestors []string) bool {
	if !value.isArr() || len(value.arr) == 0 {
		return false
	}
	if mobileType != nil && *mobileType != "" {
		if entry, ok := ctx.mobileByType.get(*mobileType); ok && entry != nil && resolveExpectedShape(entry, propName) == shapeObject {
			return false
		}
	}
	for _, element := range value.arr {
		if !element.isObj() {
			return false
		}
		typ := element.get("type")
		if typ == nil || typ.k != jsString || !hasPrefixFold(typ.s, "crt.") || !resolvesToMobileType(ctx, element, childAncestors) {
			return false
		}
	}
	return true
}

func resolveConvertedMobileType(ctx *walkContext, node *jn, sourceAncestors []string) *string {
	if templated := resolveTemplateTargetType(ctx.rules, node, sourceAncestors); templated != nil {
		return templated
	}
	typ := node.get("type").tstrp()
	if typ != nil && *typ != "" && ctx.mobileTypes.contains(*typ) {
		return typ
	}
	if rule := findRule(ctx.rules, typ); rule != nil && len(rule.Mobile) > 0 {
		return rule.Mobile[0]
	}
	return nil
}

func resolvesToMobileType(ctx *walkContext, node *jn, sourceAncestors []string) bool {
	resolved := resolveConvertedMobileType(ctx, node, sourceAncestors)
	return resolved != nil && *resolved != ""
}

func findRule(rules *Rules, webType *string) *ComponentEquivalenceRule {
	if rules.Components == nil {
		return nil
	}
	for _, rule := range rules.Components {
		if rule == nil || rule.Web == nil {
			continue
		}
		for _, web := range rule.Web {
			if eqip(web, webType) {
				return rule
			}
		}
	}
	return nil
}

func resolveTemplateTargetType(rules *Rules, node *jn, sourceAncestors []string) *string {
	if rules == nil || rules.Components == nil {
		return nil
	}
	for _, entry := range rules.Components {
		if !ruleAppliesTo(entry, node, sourceAncestors) {
			continue
		}
		for _, template := range entry.ViewConfigTemplates {
			value := templateValue(template)
			if value.isObj() {
				if typ := value.get("type"); typ != nil && typ.k == jsString {
					return strp(typ.s)
				}
			}
		}
	}
	return nil
}

// templateValue is ViewConfigTemplateRule.Value as a node (nil when absent or null).
func templateValue(template *ViewConfigTemplateRule) *jn {
	if template == nil {
		panic(engineFault{"Object reference not set to an instance of an object."})
	}
	if template.Value == nil {
		return nil
	}
	return rawNode(*template.Value)
}

func ruleAppliesTo(entry *ComponentEquivalenceRule, node *jn, sourceAncestors []string) bool {
	return entry != nil && len(entry.ViewConfigTemplates) > 0 && matchesAnyFilter(entry.Filters, node) &&
		matchesPath(entry.Path, sourceAncestors)
}

func matchesAnyFilter(filters []*ElementFilterRule, node *jn) bool {
	if len(filters) == 0 {
		return true
	}
	typ := node.get("type").tstrp()
	for _, filter := range filters {
		if !declaresFilter(filter) {
			continue
		}
		if filter.Type != nil && !isBlank(*filter.Type) && !eqip(filter.Type, typ) {
			continue
		}
		if matchesValueConstraints(filter, func(key string) *jn { return toJsonNode(node.get(key)) }) {
			return true
		}
	}
	return false
}

func declaresFilter(filter *ElementFilterRule) bool {
	return filter != nil && ((filter.Type != nil && !isBlank(*filter.Type)) || (filter.Values != nil && len(filter.Values.Keys) > 0))
}

func matchesValueConstraints(filter *ElementFilterRule, resolve func(string) *jn) bool {
	if filter.Values == nil || len(filter.Values.Keys) == 0 {
		return true
	}
	for _, key := range filter.Values.Keys {
		if !jsonValueEquals(resolve(key), rawNode(filter.Values.Values[key])) {
			return false
		}
	}
	return true
}

// jsonValueEquals is JsonValueEquals: numbers by parsed double, objects key-for-key, arrays in order.
func jsonValueEquals(actual, expected *jn) bool {
	if expected == nil || expected.k == jsNull {
		return actual == nil
	}
	switch expected.k {
	case jsObject:
		if !actual.isObj() {
			return false
		}
		for _, key := range expected.keys {
			child, ok := actual.props[key]
			if !ok || !jsonValueEquals(child, expected.props[key]) {
				return false
			}
		}
		return len(actual.keys) == len(expected.keys)
	case jsArray:
		if !actual.isArr() || len(actual.arr) != len(expected.arr) {
			return false
		}
		for index := range expected.arr {
			if !jsonValueEquals(actual.arr[index], expected.arr[index]) {
				return false
			}
		}
		return true
	case jsString:
		return actual != nil && actual.k == jsString && actual.s == expected.s
	case jsNumber:
		x, okX := actual.tryDouble()
		y, okY := expected.tryDouble()
		return okX && okY && x == y
	case jsBool:
		value, ok := actual.tryBool()
		return ok && value == expected.b
	}
	return actual == nil
}

func matchesPath(rulePath []*string, ancestors []string) bool {
	if len(rulePath) == 0 {
		return true
	}
	matched := 0
	for _, ancestor := range ancestors {
		if matched < len(rulePath) && eqi(ancestor, deref(rulePath[matched])) {
			matched++
		}
	}
	return matched == len(rulePath)
}

func captureSource(ctx *walkContext, name string, node *jn) {
	if layout := node.get("layoutConfig"); layout.isObj() {
		ctx.sourceLayouts.set(name, layout.clone())
	}
	if columns := node.get("columns"); columns.isArr() && len(columns.arr) > 0 {
		ctx.gridContainerColumns.set(name, len(columns.arr))
	}
}

func resolveParent(ctx *walkContext, mobileParentName *string) string {
	if mobileParentName != nil && *mobileParentName != "" {
		return *mobileParentName
	}
	return ctx.relocateTarget
}

func contentContainerTypesOf(rules *Rules) *strSet {
	declared := []*string{}
	if rules != nil && len(rules.ContentContainerTypes) > 0 {
		declared = rules.ContentContainerTypes
	} else if bundled := BundledRules(); bundled != nil && bundled.ContentContainerTypes != nil {
		declared = bundled.ContentContainerTypes
	}
	set := newSet(true)
	for _, typ := range declared {
		if typ != nil && !isBlank(*typ) {
			set.add(*typ)
		}
	}
	return set
}

func canHostChildren(ctx *walkContext, mobileName *string) bool {
	if mobileName == nil || *mobileName == "" {
		return false
	}
	mobileType, ok := ctx.mobileTypesByName.get(*mobileName)
	return ok && canHostChildrenOfType(ctx, mobileType)
}

func canHostChildrenOfType(ctx *walkContext, mobileType *string) bool {
	return mobileType != nil && !isBlank(*mobileType) && ctx.contentContainerTypes.contains(*mobileType)
}

func nearestHostable(ctx *walkContext, candidate, inherited *string) *string {
	if canHostChildren(ctx, candidate) {
		return candidate
	}
	return inherited
}

func resolveHostingParent(ctx *walkContext, walkParent string, hostableParentName *string, childMobileType *string,
	parentPropertyName string) string {
	if !eqi(parentPropertyName, itemsPropertyName) || walkParent == "" || hostableParentName == nil || *hostableParentName == "" ||
		eqi(walkParent, *hostableParentName) {
		return walkParent
	}
	walkParentType, ok := ctx.mobileTypesByName.get(walkParent)
	if !ok || walkParentType == nil || isBlank(*walkParentType) || canHostChildrenOfType(ctx, walkParentType) {
		return walkParent
	}
	if ctx.rules != nil && ctx.rules.TabAreaLayers != nil {
		if tabType := ctx.rules.TabAreaLayers.TabComponentType; tabType != nil && !isBlank(*tabType) && eqip(childMobileType, tabType) {
			return walkParent
		}
	}
	return *hostableParentName
}

func resolvePositionalSiblings(ctx *walkContext, nodes *jn) map[string]positionalPlace {
	result := map[string]positionalPlace{}
	if ctx.positionalParentByAnchor.len() == 0 {
		return result
	}
	anchorIndex := -1
	parent := ""
	var mobileAnchor *string
	type namedNode struct {
		position int
		name     string
	}
	var named []namedNode
	for index, node := range nodes.arr {
		if !node.isObj() {
			continue
		}
		name := node.get("name").tstr()
		if name == "" {
			continue
		}
		named = append(named, namedNode{index, name})
		if anchorIndex < 0 {
			if anchorParent, ok := ctx.positionalParentByAnchor.get(name); ok {
				anchorIndex = index
				parent = anchorParent
				if anchor, found := ctx.positionalAnchorByWebAnchor.get(name); found {
					mobileAnchor = strp(anchor)
				}
			}
		}
	}
	if anchorIndex < 0 || parent == "" {
		return result
	}
	topIndex := 0
	for _, entry := range named {
		if entry.position == anchorIndex {
			continue
		}
		if entry.position < anchorIndex {
			index := topIndex
			topIndex++
			result[entry.name] = positionalPlace{parent: parent, index: &index, anchor: mobileAnchor}
		} else {
			result[entry.name] = positionalPlace{parent: parent, anchor: mobileAnchor}
		}
	}
	return result
}

func relocateTargetFor(containerMap *dict[string]) string {
	values := newSet(true)
	for _, pair := range containerMap.pairs() {
		values.add(pair.value)
	}
	for _, preferred := range []string{"AreaProfileContainer", "GeneralTabContainer", "MainContainer"} {
		if values.contains(preferred) {
			return preferred
		}
	}
	return "MainContainer"
}

// --- requests --------------------------------------------------------------------------------------------

func buildRequestMap(rules *Rules) *dict[*RequestMappingRule] {
	result := newCI[*RequestMappingRule]()
	if rules == nil {
		return result
	}
	for _, rule := range rules.Requests {
		if rule != nil && rule.Web != nil && !isBlank(*rule.Web) {
			result.set(*rule.Web, rule)
		}
	}
	return result
}

func isRequestSupported(requestMap *dict[*RequestMappingRule], mobileRequestTypes *strSet, webRequest string) bool {
	if rule, ok := requestMap.get(webRequest); ok {
		return rule.Mobile != nil && !isBlank(*rule.Mobile)
	}
	return mobileRequestTypes.contains(webRequest)
}

func isActionOnlyType(actionComponents *dict[[]string], typ *string) bool {
	return typ != nil && *typ != "" && actionComponents.has(*typ)
}

var defaultActionPropertyNames = []string{"clicked"}

func actionComponentPropertiesOf(rules *Rules) *dict[[]string] {
	var declared []*ActionComponentRule
	if rules != nil {
		declared = rules.ActionComponents
	}
	result := actionComponentPropertiesFrom(declared)
	if result.len() > 0 {
		return result
	}
	if bundled := BundledRules(); bundled != nil {
		return actionComponentPropertiesFrom(bundled.ActionComponents)
	}
	return actionComponentPropertiesFrom(nil)
}

func actionComponentPropertiesFrom(declared []*ActionComponentRule) *dict[[]string] {
	result := newCI[[]string]()
	for _, rule := range declared {
		if rule == nil || rule.Type == nil || *rule.Type == "" || isBlank(*rule.Type) {
			continue
		}
		var names []string
		for _, name := range rule.ActionPropertyNames {
			if name != nil && !isBlank(*name) {
				names = append(names, *name)
			}
		}
		if len(names) == 0 {
			names = defaultActionPropertyNames
		}
		result.set(*rule.Type, names)
	}
	return result
}

func componentRemovalsOf(rules *Rules) []*ComponentRemovalRule {
	var declared []*ComponentRemovalRule
	if rules != nil {
		declared = usableRemovalRules(rules.ComponentRemovals)
	}
	if len(declared) > 0 {
		return declared
	}
	if bundled := BundledRules(); bundled != nil {
		return usableRemovalRules(bundled.ComponentRemovals)
	}
	return nil
}

func usableRemovalRules(declared []*ComponentRemovalRule) []*ComponentRemovalRule {
	var usable []*ComponentRemovalRule
	for _, rule := range declared {
		if rule != nil && rule.Type != nil && *rule.Type != "" && rule.Filters != nil {
			usable = append(usable, rule)
		}
	}
	return usable
}

func actionBindingOf(actionComponents *dict[[]string], node *jn, typ *string) *jn {
	properties := defaultActionPropertyNames
	if typ != nil && *typ != "" {
		if declared, ok := actionComponents.get(*typ); ok {
			properties = declared
		}
	}
	for _, property := range properties {
		if binding := node.get(property); binding.isObj() && isEventBindingJ(binding) {
			return binding
		}
	}
	return nil
}

func unsupportedRequestDropReason(requestMap *dict[*RequestMappingRule], request string, scope *string) *ReasonCode {
	if rule, ok := requestMap.get(request); ok && (rule.Mobile == nil || isBlank(*rule.Mobile)) {
		return reason(DropUnsupportedRequest, rp("request", nz(request)), rp("scope", scope))
	}
	return reason(DropUnknownRequest, rp("request", nz(request)), rp("scope", scope))
}

// isEventBindingJ is IsEventBinding(JToken).
func isEventBindingJ(value *jn) bool {
	if !value.isObj() {
		return false
	}
	request := value.get("request")
	return request != nil && request.k == jsString && !isBlank(request.s)
}

// isEventBindingS is IsEventBinding(JsonNode).
func isEventBindingS(value *jn) bool {
	if !value.isObj() {
		return false
	}
	request := stringProp(value, "request")
	return request != nil && !isBlank(*request)
}

func unsupportedRequestOf(ctx *walkContext, node *jn) *string {
	for _, key := range node.keys {
		value := node.props[key]
		if !isEventBindingJ(value) {
			continue
		}
		webRequest := value.get("request").tstr()
		if !isRequestSupported(ctx.requestMap, ctx.mobileRequestTypes, webRequest) {
			return &webRequest
		}
	}
	return nil
}

func processEventBindings(ctx *walkContext, node, values *jn, elementName string) {
	for _, key := range node.keys {
		if value := node.props[key]; isEventBindingJ(value) {
			processOneEventBinding(ctx, elementName, key, value, values)
		}
	}
}

// targetKey is MobileActionTargetProbe.TargetKey.
func targetKey(kind, target *string) string {
	var kindText, targetText string
	if kind != nil {
		kindText = strings.ToLower(dotnetTrim(*kind))
	}
	if target != nil {
		targetText = dotnetTrim(*target)
	}
	return kindText + ":" + targetText
}

// blanksTargetOnMissing is MobileActionTargetProbe.BlanksTargetOnMissing.
func blanksTargetOnMissing(kind *string) bool {
	return kind != nil && eqi(dotnetTrim(*kind), KindWebPage)
}

// Target kinds (MobileActionTargetProbe).
const (
	KindWebPage                 = "web-page"
	KindEntityDefaultMobilePage = "entity-default-mobile-page"
)

func processOneEventBinding(ctx *walkContext, elementName, binding string, source, values *jn) {
	webRequest := source.get("request").tstr()
	values.remove(binding)
	element, bindingName, request := strp(elementName), strp(binding), strp(webRequest)
	if rule, ok := ctx.requestMap.get(webRequest); ok {
		if rule.Mobile != nil && !isBlank(*rule.Mobile) {
			target := resolvedTargetOf(ctx, rule, source)
			if target != nil && (target.State == TargetMissing || target.State == TargetUnknown) {
				missing := target.State == TargetMissing
				definitional := missing && blanksTargetOnMissing(target.Kind)
				state := StateUnknown
				if missing {
					state = StateMissing
				}
				ctx.collectors.unresolved = append(ctx.collectors.unresolved, &UnresolvedTargetRequest{
					ElementName: element, Binding: bindingName, WebRequest: request, TargetKind: target.Kind,
					Target: target.Target, State: strp(state), BindingRemoved: definitional,
					ResolvedCandidateSchemaName: target.ResolvedCandidateSchemaName,
				})
				if definitional {
					blanked := source.clone()
					blanked.set("request", newStr(*rule.Mobile))
					applyParamMap(blanked, rule.ParamMap)
					effective := deref(rule.TargetParam)
					if rule.ParamMap != nil {
						if mapped, found := rule.ParamMap.Values[effective]; found && mapped != nil && !isBlank(*mapped) {
							effective = *mapped
						}
					}
					blanked.get("params").set(effective, newStr(""))
					values.set(binding, blanked)
					ctx.collectors.dropped = append(ctx.collectors.dropped, &DroppedRequest{ElementName: element, Binding: bindingName,
						WebRequest: request, Reason: []*ReasonCode{reason(DropRequestTargetMissing, rp("targetKind", nzp(target.Kind)),
							rp("target", nzp(target.Target)))}})
					return
				}
			}
			clone := source.clone()
			clone.set("request", newStr(*rule.Mobile))
			applyParamMap(clone, rule.ParamMap)
			values.set(binding, clone)
			ctx.collectors.converted = append(ctx.collectors.converted, &ConvertedRequest{ElementName: element,
				Binding: bindingName, WebRequest: request, MobileRequest: rule.Mobile})
		} else {
			var note *string
			if rule.Note != nil && !isBlank(*rule.Note) {
				note = rule.Note
			}
			ctx.collectors.dropped = append(ctx.collectors.dropped, &DroppedRequest{ElementName: element, Binding: bindingName,
				WebRequest: request, Reason: []*ReasonCode{reason(DropRequestUnsupported, rp("note", note))}})
		}
		return
	}
	values.set(binding, source.clone())
	ctx.collectors.flagged = append(ctx.collectors.flagged, &FlaggedRequest{ElementName: element, Binding: bindingName,
		Request: request, Reason: []*ReasonCode{reason(FlagRequestUnmapped)}})
}

func resolvedTargetOf(ctx *walkContext, rule *RequestMappingRule, source *jn) *ActionTargetResolution {
	if ctx.actionTargets.len() == 0 || rule.TargetParam == nil || isBlank(*rule.TargetParam) || rule.TargetKind == nil ||
		isBlank(*rule.TargetKind) {
		return nil
	}
	var target string
	if parameters := source.get("params"); parameters.isObj() {
		if value := parameters.get(*rule.TargetParam); value != nil {
			target = dotnetTrim(value.tstr())
		}
	}
	if target == "" {
		return nil
	}
	resolution, _ := ctx.actionTargets.get(targetKey(rule.TargetKind, &target))
	return resolution
}

func applyParamMap(binding *jn, paramMap *StringMapRule) {
	if paramMap == nil || len(paramMap.Keys) == 0 {
		return
	}
	parameters := binding.get("params")
	if !parameters.isObj() {
		return
	}
	for _, key := range paramMap.Keys {
		mapped := paramMap.Values[key]
		if isBlank(key) || mapped == nil || isBlank(*mapped) {
			continue
		}
		if moved, ok := parameters.props[key]; ok && moved != nil {
			parameters.remove(key)
			parameters.set(*mapped, moved)
		}
	}
}

// TargetCarryingRegistryOnlyRequests is WebToMobileAnalysisService.TargetCarryingRegistryOnlyRequests.
func TargetCarryingRegistryOnlyRequests(rules *Rules, registryRequestTypes []string) *strSet {
	withheld := newSet(true)
	if registryRequestTypes == nil {
		return withheld
	}
	pending := newSet(true, "crt.CreateCalendarRecordRequest", "crt.CreateRecordFromLookupRequest",
		"crt.CopyCalendarRecordRequest", "crt.CreateRecordWithGeodataRequest", "crt.SelectSurveyRequest")
	mapped := newSet(true)
	if rules != nil {
		for _, rule := range rules.Requests {
			if rule != nil && rule.Web != nil && !isBlank(*rule.Web) {
				mapped.add(*rule.Web)
			}
		}
	}
	for _, requestType := range registryRequestTypes {
		if !isBlank(requestType) && pending.contains(requestType) && !mapped.contains(requestType) {
			withheld.add(requestType)
		}
	}
	return withheld
}

// UnknownMobileRequestTargets is WebToMobileAnalysisService.UnknownMobileRequestTargets.
func UnknownMobileRequestTargets(rules *Rules, mobileRequestTypes *strSet) []string {
	if mobileRequestTypes.len() == 0 {
		return nil
	}
	var unknown []string
	if rules != nil {
		for _, rule := range rules.Requests {
			if rule != nil && rule.Mobile != nil && !isBlank(*rule.Mobile) && !mobileRequestTypes.contains(*rule.Mobile) {
				unknown = append(unknown, deref(rule.Web)+" -> "+*rule.Mobile)
			}
		}
	}
	return unknown
}
