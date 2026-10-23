package mobileguide

import (
	"encoding/json"
	"strings"
	"unicode"
)

// The data sections: web-only sections, data sources, the model configs and their targeted diffs.

func rawOf(node *jn) json.RawMessage {
	if node == nil {
		return nil
	}
	return json.RawMessage(node.toJSON(styleRelaxed))
}

func isLetter(r rune) bool { return unicode.IsLetter(r) }
func isDigit(r rune) bool  { return unicode.IsDigit(r) }

func hasContent(section *string, empty string) bool {
	return section != nil && !isBlank(*section) && dotnetTrim(*section) != empty
}

func collectWebOnlySections(bundle *Bundle) []string {
	sections := []string{}
	if hasContent(bundle.Handlers, "[]") {
		sections = append(sections, "handlers")
	}
	if hasContent(bundle.Validators, "{}") {
		sections = append(sections, "validators")
	}
	if hasContent(bundle.Converters, "{}") {
		sections = append(sections, "converters")
	}
	return sections
}

func collectDataSources(bundle *Bundle) []string {
	names := []string{}
	if bundle.ModelConfig == nil {
		return names
	}
	modelConfig := toJToken(bundle.ModelConfig)
	if !modelConfig.isObj() {
		return names
	}
	if dataSources := modelConfig.get("dataSources"); dataSources.isObj() {
		names = append(names, dataSources.keys...)
	}
	return names
}

func passthroughModelConfig(bundle *Bundle) *jn {
	if bundle.ModelConfig.count() > 0 {
		return bundle.ModelConfig.clone()
	}
	return nil
}

// buildTargetedDiff is BuildTargetedDiff(pageConfig, templateBase, section, out conflicts).
func buildTargetedDiff(pageConfig, templateBase *jn, section *string) (*jn, []*DataSectionConflict) {
	if !pageConfig.isObj() {
		return nil, nil
	}
	if !templateBase.isObj() {
		return buildRootMergeDiff(pageConfig), nil
	}
	operations := newArr()
	var conflicts []*DataSectionConflict
	diffObject(pageConfig, templateBase, []string{}, operations, &conflicts, false)
	for _, conflict := range conflicts {
		conflict.Section = section
	}
	return operations, conflicts
}

type childDiff struct {
	page, base   *jn
	key          string
	inCollection bool
}

type arrayInsert struct {
	key      string
	elements []*jn
}

func diffObject(page, base *jn, path []string, operations *jn, conflicts *[]*DataSectionConflict, insideCollection bool) {
	mergeValues := newObj()
	var recurse []childDiff
	var inserts []arrayInsert
	for _, key := range page.keys {
		value := page.props[key]
		baseValue := base.get(key)
		switch {
		case value.isArr() && baseValue.isArr():
			diffArray(value, baseValue, path, key, &inserts, conflicts)
		case value.isObj() && baseValue.isObj():
			recurse = append(recurse, childDiff{value, baseValue, key, insideCollection || isCollectionNode(baseValue) || isCollectionNode(value)})
		case value.isObj() || value.isArr():
			mergeValues.set(key, value.clone())
		default:
			changed := baseValue != nil && !nodeEquals(baseValue, value)
			if baseValue == nil || (!insideCollection && changed) {
				mergeValues.set(key, value.clone())
			} else if insideCollection && changed {
				*conflicts = append(*conflicts, &DataSectionConflict{Path: append(append([]string{}, path...), key),
					Kind: strp(changedScalarConflict)})
			}
		}
	}
	if len(mergeValues.keys) > 0 {
		operation := newObj()
		operation.set("operation", newStr("merge"))
		operation.set("path", pathArray(path))
		operation.set("values", mergeValues)
		operations.arr = append(operations.arr, operation)
	}
	for _, child := range recurse {
		diffObject(child.page, child.base, append(append([]string{}, path...), child.key), operations, conflicts, child.inCollection)
	}
	for _, insert := range inserts {
		insertPath := pathArray(append(append([]string{}, path...), insert.key))
		for _, element := range insert.elements {
			operation := newObj()
			operation.set("operation", newStr("insert"))
			operation.set("path", insertPath.clone())
			operation.set("values", element.clone())
			operations.arr = append(operations.arr, operation)
		}
	}
}

func diffArray(page, base *jn, path []string, key string, inserts *[]arrayInsert, conflicts *[]*DataSectionConflict) {
	baseByName := map[string]*jn{}
	baseJSON := map[string]bool{}
	for _, element := range base.arr {
		if named := namedIdentity(element); named != nil {
			baseByName[*named] = element
		} else {
			baseJSON[element.toJSON(styleSTJ)] = true
		}
	}
	var newElements []*jn
	namelessInserted := false
	for _, element := range page.arr {
		if named := namedIdentity(element); named != nil {
			if match, ok := baseByName[*named]; ok {
				if !nodeEquals(match, element) {
					*conflicts = append(*conflicts, &DataSectionConflict{Path: append(append([]string{}, path...), key),
						Entry: namedValue(element), Kind: strp(changedNamedElementConflict)})
				}
			} else {
				newElements = append(newElements, element)
			}
		} else if !baseJSON[element.toJSON(styleSTJ)] {
			newElements = append(newElements, element)
			namelessInserted = true
		}
	}
	if namelessInserted && hasUnreproducedNameless(base, page) {
		*conflicts = append(*conflicts, &DataSectionConflict{Path: append(append([]string{}, path...), key),
			Kind: strp(namelessChangedInPlaceConflct)})
	}
	if len(newElements) > 0 {
		*inserts = append(*inserts, arrayInsert{key, newElements})
	}
}

func hasUnreproducedNameless(base, page *jn) bool {
	for _, baseElement := range base.arr {
		if namedIdentity(baseElement) != nil {
			continue
		}
		reproduced := false
		for _, pageElement := range page.arr {
			if nodeEquals(baseElement, pageElement) {
				reproduced = true
				break
			}
		}
		if !reproduced {
			return true
		}
	}
	return false
}

func namedValue(node *jn) *string {
	if !node.isObj() {
		return nil
	}
	if name, ok := node.get("name").stjStringValue(); ok && !isBlank(name) {
		return &name
	}
	return nil
}

func namedIdentity(node *jn) *string {
	if name := namedValue(node); name != nil {
		return strp("name:" + *name)
	}
	return nil
}

func isCollectionNode(node *jn) bool {
	value, ok := node.get("isCollection").tryBool()
	return ok && value
}

func pathArray(path []string) *jn {
	array := newArr()
	for _, segment := range path {
		array.arr = append(array.arr, newStr(segment))
	}
	return array
}

func buildRootMergeDiff(config *jn) *jn {
	if config == nil {
		return nil
	}
	operation := newObj()
	operation.set("operation", newStr("merge"))
	operation.set("path", newArr())
	operation.set("values", config.clone())
	return newArr(operation)
}

func buildMobileViewModelConfig(bundle *Bundle, tree *jn, elementMap []*mapEntry, layoutRemoved *strSet) *jn {
	if bundle.ViewModelConfig.count() == 0 {
		return nil
	}
	vmc := toJToken(bundle.ViewModelConfig)
	if !vmc.isObj() {
		return nil
	}
	if attributes := vmc.get("attributes"); attributes.isObj() && len(attributes.keys) > 0 {
		dropped := newSet(true)
		for _, entry := range elementMap {
			if isDrop(entry) && entry.WebName != nil && *entry.WebName != "" {
				dropped.add(*entry.WebName)
			}
		}
		if layoutRemoved.len() > 0 {
			dropped.exceptWith(layoutRemoved.keys())
		}
		consumers := buildAttrConsumers(tree)
		referenced := collectAttributesReferencedBySurvivors(elementMap)
		for _, key := range attributes.keyList() {
			users, ok := consumers.get(key)
			if !ok || users.len() == 0 {
				continue
			}
			allDropped := true
			for _, user := range users.keys() {
				if !dropped.contains(user) {
					allDropped = false
					break
				}
			}
			if allDropped && !referenced.contains(key) {
				attributes.remove(key)
			}
		}
	}
	return toJsonNode(vmc)
}

func buildAttrConsumers(tree *jn) *dict[*strSet] {
	consumers := newCI[*strSet]()
	walkConsumers(tree, consumers)
	return consumers
}

func walkConsumers(nodes *jn, consumers *dict[*strSet]) {
	for _, node := range nodes.arr {
		if !node.isObj() {
			continue
		}
		if name := node.get("name").tstr(); name != "" {
			for _, attr := range extractConsumedAttributes(node) {
				users, ok := consumers.get(attr)
				if !ok {
					users = newSet(true)
					consumers.set(attr, users)
				}
				users.add(name)
			}
		}
		if items := node.get("items"); items.isArr() {
			walkConsumers(items, consumers)
		}
	}
}

func extractConsumedAttributes(node *jn) []string {
	json := withoutItemsJSON(node)
	var attributes []string
	for _, match := range resourceStringsRefPattern.FindAllStringSubmatch(json, -1) {
		attributes = append(attributes, match[1])
	}
	for _, match := range dollarRefPattern.FindAllStringSubmatch(json, -1) {
		attributes = append(attributes, match[1])
	}
	return attributes
}

func collectAttributesReferencedBySurvivors(elementMap []*mapEntry) *strSet {
	referenced := newSet(true)
	for _, entry := range elementMap {
		if entry.Values == nil || (!isInsert(entry) && !isMerge(entry)) {
			continue
		}
		json := entry.Values.toJSON(styleSTJ)
		for _, match := range resourceStringsRefPattern.FindAllStringSubmatch(json, -1) {
			referenced.add(match[1])
		}
		for _, match := range dollarRefPattern.FindAllStringSubmatch(json, -1) {
			referenced.add(match[1])
		}
	}
	return referenced
}

var _ = strings.TrimSpace
