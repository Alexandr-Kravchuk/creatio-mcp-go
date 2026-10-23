package mobileguide

import "strings"

// Building mobile values: the generic copy, conversion templates, twins, captions and resources.

type placement struct{ parent, property string }

// Expected container shapes (JsonValueKind Object / Array).
const (
	shapeNone = iota
	shapeObject
	shapeArray
)

var excludedSourceProps = newSet(true, "name", "type")
var twinMergeExcludedProps = newSet(true, "layoutConfig")
var twinTemplateExcludedProps = newSet(true, "name", "type", "items", "layoutConfig")
var standardFieldComponentTypes = newSet(true, "crt.Input", "crt.NumberInput", "crt.Checkbox", "crt.DateTimePicker",
	"crt.ComboBox", "crt.RichTextEditor", "crt.PhoneInput", "crt.EmailInput", "crt.WebInput", "crt.ColorPicker",
	"crt.ImageInput", "crt.FileInput", "crt.EncryptedInput", "crt.Slider")

const maxTemplateDepth = 32

// extractKeys is ResourceStringHelper.ExtractKeys: #ResourceString(KEY)# keys, then $Resources.Strings.KEY ones,
// each once, in that order.
func extractKeys(body string) []string {
	seen := map[string]bool{}
	var keys []string
	for _, match := range macroResourcePattern.FindAllStringSubmatch(body, -1) {
		if !seen[match[1]] {
			seen[match[1]] = true
			keys = append(keys, match[1])
		}
	}
	for _, match := range dollarResourcePattern.FindAllStringSubmatch(body, -1) {
		if !seen[match[1]] {
			seen[match[1]] = true
			keys = append(keys, match[1])
		}
	}
	return keys
}

func firstKey(body string) string {
	if keys := extractKeys(body); len(keys) > 0 {
		return keys[0]
	}
	return ""
}

func buildMobileValues(ctx *walkContext, node *jn, mobileName string, mobileType *string, caption *CaptionResource,
	parentName, propertyName *string, sourceAncestors []string) *jn {
	if mobileType == nil || *mobileType == "" {
		return nil
	}
	values := newObj()
	values.set("type", newStr(*mobileType))
	diff := newObj()
	diff.set("name", newStr(mobileName))
	diff.set("parentName", tokenOf(parentName))
	diff.set("propertyName", tokenOf(propertyName))
	roots := templateRoots{diff: diff, source: node}
	templates := matchingConversionTemplates(ctx, node, mobileType, sourceAncestors)
	preserve := false
	for _, template := range templates {
		if bool(template.PreserveSourceProperties) {
			preserve = true
		}
	}
	childAncestors := appendAncestor(sourceAncestors, &mobileName)
	if len(templates) == 0 || preserve {
		for _, key := range node.keys {
			value := node.props[key]
			if eqi(key, itemsPropertyName) && value.isArr() {
				continue
			}
			if isChildElementArray(ctx, mobileType, key, value, childAncestors) {
				continue
			}
			if excludedSourceProps.contains(key) {
				continue
			}
			if isEventBindingJ(value) {
				continue
			}
			values.set(key, coerceToDeclaredShape(ctx, mobileType, key, value.clone()))
		}
	}
	if values.get("layoutConfig") == nil {
		if layout := node.get("layoutConfig"); layout.isObj() {
			values.set("layoutConfig", layout.clone())
		}
	}
	if caption != nil {
		if carried := values.get("caption"); carried != nil {
			if firstKey(carried.tstr()) != deref(caption.Key) {
				values.set("caption", newStr("#ResourceString("+deref(caption.Key)+")#"))
			}
		}
	}
	for _, template := range templates {
		renderOne(ctx, template, values, roots)
	}
	processEventBindings(ctx, node, values, mobileName)
	hasCarriedLabel := false
	if label := values.get("label"); label != nil && !isBlank(label.tstr()) {
		hasCarriedLabel = true
	}
	if !hasCarriedLabel {
		if label := resolveFieldLabel(ctx, node, mobileName, mobileType, caption); label != "" {
			values.set("label", newStr(label))
		}
	}
	return toJsonNode(values)
}

// tokenOf is a JValue built from a .NET string (null becomes a null token).
func tokenOf(value *string) *jn {
	if value == nil {
		return newTokenNull()
	}
	return newStr(*value)
}

func matchingConversionTemplates(ctx *walkContext, node *jn, mobileType *string, sourceAncestors []string) []*ViewConfigTemplateRule {
	if ctx.rules == nil || len(ctx.rules.Components) == 0 {
		return nil
	}
	var matches []*ViewConfigTemplateRule
	for _, entry := range ctx.rules.Components {
		if !ruleAppliesTo(entry, node, sourceAncestors) {
			continue
		}
		for _, template := range entry.ViewConfigTemplates {
			if declaresTargetType(templateValue(template), mobileType) {
				matches = append(matches, template)
			}
		}
	}
	return matches
}

func declaresTargetType(value *jn, mobileType *string) bool {
	if value == nil || mobileType == nil || isBlank(*mobileType) || !value.isObj() {
		return false
	}
	typ := value.get("type")
	return typ != nil && typ.k == jsString && eqi(typ.s, *mobileType)
}

type templateRoots struct{ diff, source *jn }

func renderOne(ctx *walkContext, template *ViewConfigTemplateRule, values *jn, roots templateRoots) {
	rendered := renderTemplateToken(toJToken(templateValue(template)), roots, nil, 0)
	if !rendered.isObj() {
		return
	}
	for _, key := range rendered.keys {
		nested := rendered.props[key]
		if nested.isObj() && nested.get("type") != nil && values.get(key) != nil {
			return
		}
	}
	overlayRenderedValues(ctx, values, values.get("type").tstrp(), rendered)
	validateIntroducedStructure(ctx, values, rendered)
}

func validateIntroducedStructure(ctx *walkContext, values, rendered *jn) {
	for _, key := range rendered.keys {
		introduced := rendered.props[key]
		if !introduced.isObj() {
			continue
		}
		typ := introduced.get("type").tstr()
		if typ == "" {
			continue
		}
		if shipped := values.get(key); shipped.isObj() {
			dropValuesContradictingDeclaredScalars(ctx, &typ, shipped)
		}
		return
	}
}

func resolveTemplatePath(path string, roots templateRoots, item *jn) *jn {
	var root *jn
	switch {
	case strings.HasPrefix(path, "diff."):
		root, path = roots.diff, path[len("diff."):]
	case strings.HasPrefix(path, "source."):
		root, path = roots.source, path[len("source."):]
	default:
		if item == nil {
			return nil
		}
		root = item
	}
	token, err := selectToken(root, path)
	if err != nil {
		return nil
	}
	return token
}

func resolveTemplateCollection(path string, roots templateRoots, item *jn) []*jn {
	var root *jn
	switch {
	case strings.HasPrefix(path, "diff."):
		root, path = roots.diff, path[len("diff."):]
	case strings.HasPrefix(path, "source."):
		root, path = roots.source, path[len("source."):]
	default:
		if item == nil {
			return nil
		}
		root = item
	}
	tokens, err := selectTokens(root, path)
	if err != nil {
		return nil
	}
	return tokens
}

func resolveTemplatePlacement(ctx *walkContext, node *jn, mobileType *string, mobileName, computedParent,
	computedProperty string, sourceAncestors []string) (placement, bool) {
	diff := newObj()
	diff.set("name", newStr(mobileName))
	diff.set("parentName", newStr(computedParent))
	diff.set("propertyName", newStr(computedProperty))
	roots := templateRoots{diff: diff, source: node}
	for _, template := range matchingConversionTemplates(ctx, node, mobileType, sourceAncestors) {
		parent := computedParent
		if rendered, ok := renderPlacementField(template.ParentName, roots); ok {
			parent = rendered
		}
		property := computedProperty
		if rendered, ok := renderPlacementField(template.PropertyName, roots); ok {
			property = rendered
		}
		if parent != computedParent || property != computedProperty {
			return placement{parent, property}, true
		}
	}
	return placement{}, false
}

func renderPlacementField(declared *string, roots templateRoots) (string, bool) {
	if declared == nil || isBlank(*declared) {
		return "", false
	}
	rendered := renderTemplateString(*declared, roots, nil)
	if rendered == nil {
		return "", false
	}
	return rendered.tstr(), true
}

func overlayRenderedValues(ctx *walkContext, target *jn, mobileType *string, rendered *jn) {
	for _, key := range rendered.keys {
		value := rendered.props[key]
		if excludedSourceProps.contains(key) {
			continue
		}
		if eqi(key, "items") && value.isArr() {
			continue
		}
		target.set(key, coerceToDeclaredShape(ctx, mobileType, key, value.clone()))
	}
}

func renderTemplateToken(template *jn, roots templateRoots, item *jn, depth int) *jn {
	if depth > maxTemplateDepth {
		return nil
	}
	switch {
	case template != nil && template.k == jsString:
		return renderTemplateString(template.s, roots, item)
	case template.isObj() && template.get("$each") != nil:
		return renderEach(template.get("$each").tstr(), template.get("as"), roots, item, depth)
	case template.isObj():
		rendered := newObj()
		for _, key := range template.keys {
			if value := renderTemplateToken(template.props[key], roots, item, depth+1); value != nil && value.k != jsNull {
				rendered.set(key, value.clone())
			}
		}
		return rendered
	case template.isArr():
		rendered := newArr()
		for _, element := range template.arr {
			if value := renderTemplateToken(element, roots, item, depth+1); value != nil {
				rendered.arr = append(rendered.arr, value.clone())
			}
		}
		return rendered
	}
	return template
}

func renderEach(collectionPath string, body *jn, roots templateRoots, outerItem *jn, depth int) *jn {
	result := newArr()
	if body == nil {
		return result
	}
	for _, member := range resolveTemplateCollection(collectionPath, roots, outerItem) {
		if value := renderTemplateToken(body, roots, member, depth+1); value != nil {
			result.arr = append(result.arr, value.clone())
		}
	}
	return result
}

func renderTemplateString(template string, roots templateRoots, item *jn) *jn {
	if single := templateTokenPattern.FindStringSubmatchIndex(template); single != nil &&
		utf16Len(template[single[0]:single[1]]) == utf16Len(dotnetTrim(template)) {
		return resolveTemplatePath(template[single[2]:single[3]], roots, item)
	}
	resolvedEverything := true
	var builder strings.Builder
	last := 0
	for _, match := range templateTokenPattern.FindAllStringSubmatchIndex(template, -1) {
		builder.WriteString(template[last:match[0]])
		value := resolveTemplatePath(template[match[2]:match[3]], roots, item)
		if value == nil || value.k == jsNull {
			resolvedEverything = false
		} else {
			builder.WriteString(value.tstr())
		}
		last = match[1]
	}
	builder.WriteString(template[last:])
	if !resolvedEverything {
		return nil
	}
	return newStr(builder.String())
}

func dropValuesContradictingDeclaredScalars(ctx *walkContext, mobileType *string, row *jn) {
	if mobileType == nil || isBlank(*mobileType) || ctx == nil || ctx.mobileByType == nil {
		return
	}
	entry, ok := ctx.mobileByType.get(*mobileType)
	if !ok || entry == nil {
		return
	}
	for _, key := range row.keyList() {
		if declaresScalarString(entry, key) && !(row.props[key] != nil && row.props[key].k == jsString) {
			row.remove(key)
		}
	}
}

func declaresScalarString(entry *RegistryEntry, propName string) bool {
	if entry.Inputs != nil {
		for _, pair := range entry.Inputs.pairs() {
			if eqi(pair.key, propName) {
				if !pair.value.isObj() {
					return false
				}
				typ := pair.value.get("type")
				return typ != nil && typ.k == jsString && eqi(typ.s, "string")
			}
		}
	}
	if entry.Properties != nil {
		for _, pair := range entry.Properties.pairs() {
			if eqi(pair.key, propName) {
				return pair.value != nil && eqi(deref(pair.value.Type), "string")
			}
		}
	}
	return false
}

func coerceToDeclaredShape(ctx *walkContext, mobileType *string, propName string, value *jn) *jn {
	if value == nil || mobileType == nil || *mobileType == "" {
		return value
	}
	entry, ok := ctx.mobileByType.get(*mobileType)
	if !ok || entry == nil {
		return value
	}
	switch resolveExpectedShape(entry, propName) {
	case shapeObject:
		if value.isArr() {
			for _, element := range value.arr {
				if element.isObj() {
					return element
				}
			}
		}
	case shapeArray:
		if value.isObj() {
			return newArr(value)
		}
	}
	return value
}

func resolveExpectedShape(entry *RegistryEntry, propName string) int {
	if entry.Inputs != nil {
		for _, pair := range entry.Inputs.pairs() {
			if eqi(pair.key, propName) {
				return shapeFromDescriptor(pair.value)
			}
		}
	}
	if entry.Properties != nil {
		for _, pair := range entry.Properties.pairs() {
			if eqi(pair.key, propName) {
				if pair.value == nil {
					return shapeNone
				}
				return shapeFromTypeAndDefault(pair.value.Type, pair.value.Default)
			}
		}
	}
	return shapeNone
}

func shapeFromDescriptor(descriptor *jn) int {
	if !descriptor.isObj() {
		return shapeNone
	}
	var typ *string
	if value := descriptor.get("type"); value != nil && value.k == jsString {
		typ = strp(value.s)
	}
	def, hasDefault := descriptor.props["default"]
	if !hasDefault {
		return shapeFromTypeAndDefault(typ, nil)
	}
	if def == nil {
		def = newTokenNull()
	}
	return shapeFromTypeAndDefault(typ, def)
}

func shapeFromTypeAndDefault(typ *string, def *jn) int {
	if typ != nil && eqi(*typ, "array") {
		return shapeArray
	}
	if typ != nil && (eqi(*typ, "object") || eqi(*typ, "map")) {
		return shapeObject
	}
	if def != nil {
		if def.isObj() {
			return shapeObject
		}
		if def.isArr() {
			return shapeArray
		}
	}
	return shapeNone
}

// --- twins -----------------------------------------------------------------------------------------------

func applyTemplatesToTwin(ctx *walkContext, node *jn, twin *mapEntry, webType *string, sourceAncestors []string) {
	if twin == nil || twin.Name == nil || *twin.Name == "" || twin.MobileType == nil || *twin.MobileType == "" {
		return
	}
	twinName := *twin.Name
	provided, ok := ctx.mobileNodesByName.get(twinName)
	if !ok || provided == nil {
		return
	}
	templates := matchingConversionTemplates(ctx, node, twin.MobileType, sourceAncestors)
	if len(templates) == 0 {
		return
	}
	diff := newObj()
	diff.set("name", newStr(twinName))
	roots := templateRoots{diff: diff, source: node}
	var targetOrder []string
	payloads := newCI[*twinPayload]()
	for _, template := range templates {
		if rendered := renderTemplateToken(toJToken(templateValue(template)), roots, nil, 0); rendered.isObj() {
			diffOntoProvided(ctx, rendered, provided, twinName, twin.MobileType, &targetOrder, payloads)
		}
	}
	for _, targetName := range targetOrder {
		payload, _ := payloads.get(targetName)
		dropValuesContradictingDeclaredScalars(ctx, payload.typ, payload.values)
		if payload.values.count() == 0 {
			continue
		}
		if eqi(targetName, twinName) {
			var merged *jn
			if twin.Values == nil {
				merged = newObj()
			} else {
				merged = toJToken(twin.Values)
				if !merged.isObj() {
					panic(engineFault{"Error reading JObject from JsonReader."})
				}
			}
			for _, key := range payload.values.keys {
				merged.set(key, payload.values.props[key].clone())
			}
			twin.Values = toJsonNode(merged)
			continue
		}
		ctx.add(&mapEntry{WebType: nzp(webType), Operation: OperationMerge, Name: strp(targetName), MobileType: payload.typ,
			Values: toJsonNode(payload.values)})
	}
}

type twinPayload struct {
	typ    *string
	values *jn
}

func diffOntoProvided(ctx *walkContext, rendered, provided *jn, targetName string, targetType *string, targetOrder *[]string,
	payloads *dict[*twinPayload]) {
	for _, key := range rendered.keys {
		value := rendered.props[key]
		if twinTemplateExcludedProps.contains(key) {
			continue
		}
		have := provided.get(key)
		if value.isObj() && isComponentObjectJ(value) {
			childType := value.get("type").tstrp()
			if have.isObj() && isComponentObjectJ(have) && eqip(have.get("type").tstrp(), childType) {
				if childName := have.get("name").tstr(); childName != "" {
					diffOntoProvided(ctx, value, have, childName, childType, targetOrder, payloads)
				}
			}
			continue
		}
		if have != nil && jtokenEquals(have, value) {
			continue
		}
		payload, ok := payloads.get(targetName)
		if !ok {
			payload = &twinPayload{typ: targetType, values: newObj()}
			payloads.set(targetName, payload)
			*targetOrder = append(*targetOrder, targetName)
		}
		payload.values.set(key, coerceToDeclaredShape(ctx, targetType, key, value.clone()))
	}
}

func buildTwinMergeValues(ctx *walkContext, node *jn, rule *ComponentMappingRule, twinMobileType, webType *string) *jn {
	if len(rule.CarryProperties) > 0 {
		return buildCarriedTwinValues(ctx, node, rule, twinMobileType)
	}
	sameComponent := webType != nil && *webType != "" && eqip(twinMobileType, webType)
	if !sameComponent {
		return nil
	}
	return buildDeltaTwinMergeValues(ctx, node, node.get("name").tstrp(), twinMobileType, rule.Mobile)
}

func buildDeltaTwinMergeValues(ctx *walkContext, node *jn, webName, mobileType, mobileName *string) *jn {
	if webName == nil || *webName == "" {
		return nil
	}
	baseline, ok := ctx.webBaselineNodes.get(*webName)
	if !ok || baseline == nil {
		return nil
	}
	values := newObj()
	for _, key := range node.keys {
		value := node.props[key]
		if eqi(key, "items") && value.isArr() {
			continue
		}
		if isEventBindingJ(value) {
			if !jtokenEquals(baseline.get(key), value) {
				processOneEventBinding(ctx, deref(mobileName), key, value, values)
			}
			continue
		}
		if eqi(key, "caption") {
			sameNameTwin := eqip(webName, mobileName)
			if !sameNameTwin && captionValueChanged(ctx, value, baseline.get("caption")) {
				values.set(key, coerceToDeclaredShape(ctx, mobileType, key, value.clone()))
			}
			continue
		}
		if excludedSourceProps.contains(key) || twinMergeExcludedProps.contains(key) {
			continue
		}
		if baseValue := baseline.get(key); baseValue != nil && jtokenEquals(baseValue, value) {
			continue
		}
		values.set(key, coerceToDeclaredShape(ctx, mobileType, key, value.clone()))
	}
	if values.count() == 0 {
		return nil
	}
	return toJsonNode(values)
}

func buildCarriedTwinValues(ctx *walkContext, node *jn, rule *ComponentMappingRule, mobileType *string) *jn {
	if rule == nil || len(rule.CarryProperties) == 0 {
		return nil
	}
	values := newObj()
	for _, propName := range rule.CarryProperties {
		if propName == nil || isBlank(*propName) {
			continue
		}
		propValue := node.get(*propName)
		if propValue == nil {
			continue
		}
		cloned := propValue.clone()
		if mobileType == nil || *mobileType == "" {
			values.set(*propName, cloned)
		} else {
			values.set(*propName, coerceToDeclaredShape(ctx, mobileType, *propName, cloned))
		}
	}
	if values.count() == 0 {
		return nil
	}
	return toJsonNode(values)
}

// --- captions and resources ------------------------------------------------------------------------------

func resolveCaptionResource(ctx *walkContext, node *jn, mobileName string) *CaptionResource {
	caption := node.get("caption").tstr()
	if caption == "" {
		return nil
	}
	sourceKey := firstKey(caption)
	if sourceKey == "" {
		return nil
	}
	sourceValue, ok := tryResolveDeclaredResourceString(ctx.resources, sourceKey)
	if !ok {
		return nil
	}
	return &CaptionResource{Key: strp(mobileName + "_caption"), SourceValue: &sourceValue}
}

func tryResolveDeclaredResourceString(resources *jn, key string) (string, bool) {
	value := resources.get(key)
	if value == nil {
		return "", false
	}
	if value.isObj() {
		culture := value.get("en-US")
		if culture == nil && len(value.keys) > 0 {
			culture = value.props[value.keys[0]]
		}
		return culture.tstr(), true
	}
	return value.tstr(), true
}

func resolveResourceString(resources *jn, key string) *string {
	value := resources.get(key)
	if value == nil {
		return nil
	}
	if value.isObj() {
		culture := value.get("en-US")
		if culture == nil && len(value.keys) > 0 {
			culture = value.props[value.keys[0]]
		}
		return culture.tstrp()
	}
	return value.tstrp()
}

func captionValueChanged(ctx *walkContext, pageCaption, baselineCaption *jn) bool {
	if pageCaption == nil {
		return false
	}
	if baselineCaption == nil || !jtokenEquals(baselineCaption, pageCaption) {
		return true
	}
	key := firstKey(pageCaption.tstr())
	if key == "" {
		return false
	}
	pageText := resolveResourceString(ctx.resources, key)
	baseText := resolveResourceString(ctx.webBaselineResources, key)
	return pageText != nil && baseText != nil && *pageText != *baseText
}

func collectResourceStrings(elementMap []*mapEntry, modelConfig, viewModelConfig, resources *jn) *OrderedMap[*string] {
	result := &OrderedMap[*string]{}
	has := func(key string) bool { _, ok := result.Values[key]; return ok }
	scan := func(json string) {
		if json == "" {
			return
		}
		for _, key := range extractKeys(json) {
			if has(key) {
				continue
			}
			if text, ok := tryResolveDeclaredResourceString(resources, key); ok {
				result.Set(key, strp(text))
			}
		}
	}
	for _, entry := range elementMap {
		if capRes := entry.CaptionResource; capRes != nil && capRes.Key != nil && *capRes.Key != "" && !has(*capRes.Key) {
			text := ""
			if capRes.SourceValue != nil {
				text = *capRes.SourceValue
			}
			result.Set(*capRes.Key, strp(text))
		}
		if entry.Values != nil {
			scan(entry.Values.toJSON(styleSTJ))
		}
	}
	if modelConfig != nil {
		scan(modelConfig.toJSON(styleSTJ))
	}
	if viewModelConfig != nil {
		scan(viewModelConfig.toJSON(styleSTJ))
	}
	return result
}

func resolveFieldLabel(ctx *walkContext, node *jn, mobileName string, mobileType *string, caption *CaptionResource) string {
	if mobileType == nil || *mobileType == "" || !standardFieldComponentTypes.contains(*mobileType) {
		return ""
	}
	if caption != nil {
		return "$Resources.Strings." + deref(caption.Key)
	}
	if column := resolveBoundColumn(ctx, node); column != "" {
		return "$Resources.Strings." + column
	}
	return "$Resources.Strings." + mobileName
}

func resolveBoundColumn(ctx *walkContext, node *jn) string {
	for _, token := range extractDollarRefs(node) {
		if column, ok := ctx.attrToColumn.get(token); ok && column != "" {
			return column
		}
	}
	return ""
}

func withoutItemsJSON(node *jn) string {
	clone := node.clone()
	clone.remove("items")
	return clone.toJSON(styleNewtonsoft)
}

func extractDollarRefs(node *jn) []string {
	var refs []string
	for _, match := range dollarRefPattern.FindAllStringSubmatch(withoutItemsJSON(node), -1) {
		refs = append(refs, match[1])
	}
	return refs
}

func buildAttrToColumn(bundle *Bundle) *dict[string] {
	result := newCI[string]()
	if bundle.ViewModelConfig == nil {
		return result
	}
	vmc := toJToken(bundle.ViewModelConfig)
	if !vmc.isObj() {
		return result
	}
	if attributes := vmc.get("attributes"); attributes.isObj() {
		for _, key := range attributes.keys {
			attr := attributes.props[key]
			var path string
			if attr.isObj() {
				if modelConfig := attr.get("modelConfig"); modelConfig != nil {
					path = modelConfig.get("path").tstr()
				}
			}
			if path != "" {
				if dot := strings.LastIndex(path, "."); dot >= 0 {
					path = path[dot+1:]
				}
				result.set(key, path)
			}
		}
	}
	return result
}

func parseResources(bundle *Bundle) *jn {
	if bundle.ResourcesStrings == nil {
		return nil
	}
	resources := toJToken(bundle.ResourcesStrings)
	if !resources.isObj() {
		return nil
	}
	return resources
}
