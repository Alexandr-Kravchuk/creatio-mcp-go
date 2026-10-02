package creatio

import (
	"fmt"
	"sort"
	"strconv"
)

// This file ports clio's JsonDiffApplier and JsonPathDiffApplier, which are themselves 1:1 ports of the
// Creatio client's JsonApplierService and JsonPathApplierService. Control flow and quirks are kept as in the
// source so the merged bundle matches clio's; refactoring them would make that unverifiable.

const (
	loopDependencyMessage        = "Cyclic dependency exists for object \"%s\". The parentName parameter cannot be equal to name"
	requiredParameterMessage     = "Required parameter \"%s\" not found in object"
	notContainerInsertMessage    = "Item \"%s\" is not a container for other items"
	nullReferenceMessage         = "Object reference not set to an instance of an object."
	invalidJArrayKeyMessage      = "Accessed JArray values with invalid key value: \"%s\". Int32 array index expected."
	nullPropertyNameArgumentText = "Value cannot be null. (Parameter 'propertyName')"
)

// applierFault aborts a merge. diff marks clio's JsonDiffApplierException; other faults stand for the
// runtime exceptions .NET would raise on the same input.
type applierFault struct {
	message string
	diff    bool
}

func diffFault(format string, args ...any) {
	panic(applierFault{message: fmt.Sprintf(format, args...), diff: true})
}

func runtimeFault(message string) { panic(applierFault{message: message}) }

type itemInfo struct {
	item         *jnode
	parentName   *string
	propertyName string
	parentPath   []any
}

type aliasInfo struct {
	name               *string
	excludeProperties  []string
	excludeOperations  []string
	hasExcludeProps    bool
	hasExcludeOperates bool
}

type iterationConfig struct {
	item                 *jnode
	propertyName         string
	parent               *jnode
	childIterationResult bool
	parentPath           []any
}

type applierOptions struct {
	applyMoveIfIndirectParentMoved bool
}

// jsonDiffApplier applies Creatio view-config diffs; pathMode switches it to the path applier used for
// view-model and model configs (items keyed by "_id", optional path addressing, deep merge).
type jsonDiffApplier struct {
	pathMode     bool
	memoryStore  map[string]*itemInfo
	source       *jnode
	rootWrapper  *jnode
	options      *applierOptions
	aliases      map[string]*aliasInfo
	aliasesIsSet bool
}

func newJSONDiffApplier(pathMode bool) *jsonDiffApplier {
	return &jsonDiffApplier{pathMode: pathMode, memoryStore: map[string]*itemInfo{}}
}

func (a *jsonDiffApplier) aliasName() string {
	if a.pathMode {
		return "_id"
	}
	return "name"
}

func (a *jsonDiffApplier) apply(source, operations *jnode, options *applierOptions) *jnode {
	a.source = source.clone()
	a.options = options
	a.rootWrapper = newObject()
	if a.source != nil {
		a.rootWrapper.set("items", a.source)
	}
	if isEmptyToken(source) {
		a.aliases, a.aliasesIsSet = nil, false
	}
	defer func() {
		a.source, a.options, a.rootWrapper = nil, nil, nil
		a.memoryStore = map[string]*itemInfo{}
	}()
	inner := newArray()
	if operations != nil {
		inner = operations.clone()
	}
	a.applyOperations(inner)
	return a.source
}

func (a *jsonDiffApplier) applyDiff(source *jnode, operations []*jnode, options []*applierOptions) *jnode {
	result := newArray()
	if source != nil {
		result = source.clone()
	}
	for index, operation := range operations {
		var option *applierOptions
		if index < len(options) {
			option = options[index]
		}
		result = a.apply(result, operation, option)
	}
	return result
}

// isEmptyToken mirrors IsEmpty for tokens: absent, null, empty string or empty array.
func isEmptyToken(n *jnode) bool {
	if n == nil || n.kind == jkNull {
		return true
	}
	if n.kind == jkString {
		return n.text == ""
	}
	return n.kind == jkArray && len(n.items) == 0
}

func isItemConfig(n *jnode) bool { return n.isObject() && !isEmptyToken(n.get("name")) }

// isFalsy mirrors JavaScript falsiness for required-parameter checks.
func isFalsy(n *jnode) bool {
	if n == nil || n.kind == jkNull {
		return true
	}
	switch n.kind {
	case jkString:
		return n.text == ""
	case jkBool:
		return !n.flag
	case jkInteger, jkFloat:
		value, _ := strconv.ParseFloat(n.text, 64)
		return value == 0
	}
	return false
}

func optionalText(n *jnode, key string) *string { return n.get(key).stringValue() }

func textOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func sameText(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

// ----- tree traversal -----

func (a *jsonDiffApplier) iterateChildItems(config *jnode, iterator func(*iterationConfig) bool) bool {
	if a.pathMode {
		return a.walkWithPath(config, iterator, []any{})
	}
	result := true
	if !config.isObject() {
		return result
	}
	for _, key := range append([]string{}, config.keys...) {
		property := config.props[key]
		if property == nil {
			continue
		}
		if property.kind == jkArray || property.kind == jkObject {
			items := []*jnode{property}
			if property.kind == jkArray {
				items = property.items
			}
			for _, child := range items {
				if !isItemConfig(items[0]) {
					break
				}
				childResult := a.iterateChildItems(child, iterator)
				var item *jnode
				if child.isObject() {
					item = child
				}
				result = iterator(&iterationConfig{item: item, propertyName: key, parent: config, childIterationResult: childResult}) && childResult
				if !result {
					break
				}
			}
		}
		if !result {
			break
		}
	}
	return result
}

func (a *jsonDiffApplier) walkWithPath(config *jnode, iterator func(*iterationConfig) bool, parentPath []any) bool {
	result := true
	if !config.isObject() {
		return result
	}
	for _, key := range append([]string{}, config.keys...) {
		property := config.props[key]
		if property == nil {
			continue
		}
		if property.kind == jkArray || property.kind == jkObject {
			isParentArray := property.kind == jkArray
			items := []*jnode{property}
			if isParentArray {
				items = property.items
			}
			parentPath = append(parentPath, key)
			for index := 0; index < len(items); index++ {
				child := items[index]
				if isParentArray {
					parentPath = append(parentPath, index)
				}
				childResult := a.walkWithPath(child, iterator, append([]any{}, parentPath...))
				var item *jnode
				if child.isObject() {
					item = child
				}
				result = iterator(&iterationConfig{item: item, propertyName: key, childIterationResult: childResult,
					parentPath: append([]any{}, parentPath...), parent: config}) && childResult
				if isParentArray && result {
					parentPath = parentPath[:len(parentPath)-1]
				}
				if !result {
					break
				}
			}
			if result {
				parentPath = parentPath[:len(parentPath)-1]
			}
		}
		if !result {
			break
		}
	}
	return result
}

// ----- item lookup -----

func (a *jsonDiffApplier) findItemInfo(name *string, parent *jnode) *itemInfo {
	if name == nil || *name == "" {
		return nil
	}
	if cached, ok := a.memoryStore[*name]; ok {
		return cached
	}
	var aliasTarget *string
	if alias := a.aliases[*name]; alias != nil {
		aliasTarget = alias.name
	}
	var result *itemInfo
	a.iterateChildItems(parent, func(config *iterationConfig) bool {
		var itemName *string
		if config.item != nil {
			itemName = config.item.get(a.aliasName()).stringValue()
		}
		isAliasFound := aliasTarget != nil && *aliasTarget != "" && sameText(itemName, aliasTarget)
		isItemFound := sameText(itemName, name) || isAliasFound
		if !config.childIterationResult || isItemFound {
			var parentName *string
			if config.parent.isObject() {
				parentName = config.parent.get(a.aliasName()).stringValue()
			}
			current := &itemInfo{item: config.item, parentName: parentName, parentPath: config.parentPath, propertyName: config.propertyName}
			if itemName != nil {
				a.memoryStore[*itemName] = current
			}
			if isItemFound {
				result = current
			}
		}
		return !isItemFound
	})
	return result
}

func (a *jsonDiffApplier) findItemInfoInSourceObject(name *string) *itemInfo {
	if a.pathMode {
		return a.findItemInfo(name, a.source)
	}
	return a.findItemInfo(name, a.rootWrapper)
}

func (a *jsonDiffApplier) removeFromCache(item *jnode) {
	a.iterateChildItems(item, func(config *iterationConfig) bool {
		if config.item != nil {
			if childName := config.item.get(a.aliasName()).stringValue(); childName != nil {
				delete(a.memoryStore, *childName)
			}
		}
		return true
	})
	if item == nil {
		runtimeFault(nullReferenceMessage)
	}
	if itemName := item.get(a.aliasName()).stringValue(); itemName != nil {
		delete(a.memoryStore, *itemName)
	}
}

// ----- operation dispatch -----

type splitOperations struct {
	merge, set, removeProperties, remove, move, insert []*jnode
}

func (a *jsonDiffApplier) applyOperations(operations *jnode) {
	split := a.splitOperations(operations)
	for _, operation := range split.merge {
		a.merge(operation)
	}
	a.applyChangePositionOperationGroup(split.remove, split.insert, split.move, int(^uint(0)>>1))
	for _, operation := range split.removeProperties {
		a.removeOperation(operation)
	}
	for _, operation := range split.set {
		a.setOperation(operation)
	}
}

func (a *jsonDiffApplier) splitOperations(operations *jnode) splitOperations {
	var result splitOperations
	for _, operation := range operations.items {
		if !operation.isObject() {
			continue
		}
		a.checkOperation(operation)
		name := optionalText(operation, "name")
		kind := textOrEmpty(optionalText(operation, "operation"))
		if a.isExcludeAliasOperation(name, kind, operation.get("properties")) {
			continue
		}
		switch kind {
		case "merge":
			result.merge = append(result.merge, operation)
		case "set":
			result.set = append(result.set, operation)
		case "insert":
			result.insert = append(result.insert, operation)
		case "move":
			result.move = append(result.move, operation)
		case "remove":
			if operation.get("properties").isArray() {
				result.removeProperties = append(result.removeProperties, operation)
			} else {
				result.remove = append(result.remove, operation)
			}
		}
	}
	return result
}

func (a *jsonDiffApplier) applyChangePositionOperationGroup(removes, inserts, moves []*jnode, previousUnsuccessful int) {
	moves = filterMoveOperations(removes, moves)
	removeOperations := []*jnode{}
	for _, operation := range append(append([]*jnode{}, removes...), moves...) {
		if converted := a.convertMoveToRemove(operation); len(converted.keys) > 0 {
			removeOperations = append(removeOperations, converted)
		}
	}
	removeOperations = a.operationsSequenceByPath(removeOperations, false)
	insertOperations := []*jnode{}
	groups, _ := groupByName(moves)
	for _, operation := range removeOperations {
		if group, ok := groups[textOrEmpty(optionalText(operation, "name"))]; ok {
			for _, move := range group {
				if converted := a.convertMoveToInsert(move); len(converted.keys) > 0 {
					insertOperations = append(insertOperations, converted)
				}
			}
		}
		a.removeOperation(operation)
	}
	allInserts := a.operationsSequenceByPath(append(append([]*jnode{}, inserts...), insertOperations...), true)
	unsuccessful := []*jnode{}
	for _, operation := range allInserts {
		if !a.insert(operation) {
			unsuccessful = append(unsuccessful, operation)
		}
	}
	if len(unsuccessful) > 0 {
		a.applyUnsuccessfulInserts(unsuccessful, previousUnsuccessful)
	}
}

func (a *jsonDiffApplier) applyUnsuccessfulInserts(unsuccessful []*jnode, previousUnsuccessful int) {
	// The TS source mutates during forEach: a removal does not rewind the index, so the next entry is skipped.
	for index := 0; index < len(unsuccessful); index++ {
		unsuccessful[index].set("operation", newString("move"))
		if a.findItemInfoInSourceObject(optionalText(unsuccessful[index], "parentName")) == nil {
			unsuccessful = append(unsuccessful[:index], unsuccessful[index+1:]...)
		}
	}
	if len(unsuccessful) > 0 {
		if len(unsuccessful) >= previousUnsuccessful {
			return
		}
		a.applyChangePositionOperationGroup(nil, nil, unsuccessful, len(unsuccessful))
	}
}

func groupByName(operations []*jnode) (map[string][]*jnode, []string) {
	groups := map[string][]*jnode{}
	order := []string{}
	for _, operation := range operations {
		name := textOrEmpty(optionalText(operation, "name"))
		if _, ok := groups[name]; !ok {
			order = append(order, name)
		}
		groups[name] = append(groups[name], operation)
	}
	return groups, order
}

func filterMoveOperations(removes, moves []*jnode) []*jnode {
	filtered := moves
	for _, remove := range removes {
		removeName := optionalText(remove, "name")
		next := []*jnode{}
		for _, move := range filtered {
			if !sameText(removeName, optionalText(move, "name")) {
				next = append(next, move)
			}
		}
		filtered = next
	}
	return filtered
}

func (a *jsonDiffApplier) convertMoveToRemove(operation *jnode) *jnode {
	name := optionalText(operation, "name")
	result := newObject()
	if info := a.findItemInfoInSourceObject(name); info != nil {
		result.set("parentName", toJNode(info.parentName))
		result.set("propertyName", newString(info.propertyName))
		result.set("index", newString(info.propertyName)) // verbatim quirk of the TS source
		result.set("operation", newString("remove"))
		result.set("name", toJNode(name))
	}
	return result
}

func (a *jsonDiffApplier) convertMoveToInsert(operation *jnode) *jnode {
	name := optionalText(operation, "name")
	result := newObject()
	if info := a.findItemInfoInSourceObject(name); info != nil {
		if info.item == nil {
			runtimeFault(nullReferenceMessage)
		}
		values := info.item.clone()
		values.remove(a.aliasName())
		result.set("values", values)
		result.set("operation", newString(""))
		for _, key := range operation.keys {
			result.set(key, operation.props[key].clone())
		}
		result.set("operation", newString("insert"))
	}
	return result
}

// ----- path ordering -----

type hierarchyEntry struct{ parentName, propertyName string }

func operationHierarchy(operations []*jnode) map[string]hierarchyEntry {
	result := map[string]hierarchyEntry{}
	for _, operation := range operations {
		name := optionalText(operation, "name")
		if name == nil {
			continue
		}
		entry := hierarchyEntry{parentName: "_", propertyName: "_"}
		if parent := textOrEmpty(optionalText(operation, "parentName")); parent != "" {
			entry.parentName = parent
		}
		if property := textOrEmpty(optionalText(operation, "propertyName")); property != "" {
			entry.propertyName = property
		}
		result[*name] = entry
	}
	return result
}

func (a *jsonDiffApplier) operationItemPath(name *string, hierarchy map[string]hierarchyEntry, result string, visited map[string]bool) string {
	if name != nil {
		if visited[*name] {
			diffFault(loopDependencyMessage, *name)
		}
		visited[*name] = true
	}
	if a.options != nil && a.options.applyMoveIfIndirectParentMoved {
		return a.operationItemFullPath(name, hierarchy, result, visited)
	}
	if name != nil {
		if entry, ok := hierarchy[*name]; ok {
			result = entry.parentName + "=" + entry.propertyName + "==" + result
			parent := entry.parentName
			result = a.operationItemPath(&parent, hierarchy, result, visited)
		}
	}
	return result
}

func (a *jsonDiffApplier) operationItemFullPath(name *string, hierarchy map[string]hierarchyEntry, result string, visited map[string]bool) string {
	var parentName *string
	var propertyName string
	if entry, ok := hierarchy[textOrEmpty(name)]; name != nil && ok {
		parent := entry.parentName
		parentName, propertyName = &parent, entry.propertyName
	} else {
		info := a.findItemInfoInSourceObject(name)
		if info == nil {
			return result
		}
		parentName, propertyName = info.parentName, info.propertyName
	}
	if parentName != nil && *parentName != "" {
		result = *parentName + "=" + propertyName + "==" + result
		result = a.operationItemPath(parentName, hierarchy, result, visited)
	}
	return result
}

func (a *jsonDiffApplier) operationsSequenceByPath(operations []*jnode, ascending bool) []*jnode {
	hierarchy := operationHierarchy(operations)
	groups := map[string][]*jnode{}
	order := []string{}
	for _, operation := range operations {
		path := a.operationItemPath(optionalText(operation, "name"), hierarchy, "", map[string]bool{})
		if _, ok := groups[path]; !ok {
			order = append(order, path)
		}
		groups[path] = append(groups[path], operation)
	}
	sort.SliceStable(order, func(i, j int) bool {
		if ascending {
			return len(order[i]) < len(order[j])
		}
		return len(order[i]) > len(order[j])
	})
	result := []*jnode{}
	for _, path := range order {
		group := append([]*jnode{}, groups[path]...)
		sort.SliceStable(group, func(i, j int) bool { return compareIndex(group[i].get("index"), group[j].get("index")) < 0 })
		if !ascending {
			for left, right := 0, len(group)-1; left < right; left, right = left+1, right-1 {
				group[left], group[right] = group[right], group[left]
			}
		}
		result = append(result, group...)
	}
	return result
}

// compareIndex orders like lodash sortBy('index'): empty values last, numbers numerically, otherwise ordinal text.
func compareIndex(x, y *jnode) int {
	emptyX, emptyY := isEmptyToken(x), isEmptyToken(y)
	switch {
	case emptyX && emptyY:
		return 0
	case emptyX:
		return 1
	case emptyY:
		return -1
	}
	numeric := func(n *jnode) bool { return n.kind == jkInteger || n.kind == jkFloat }
	if numeric(x) && numeric(y) {
		left, _ := strconv.ParseFloat(x.text, 64)
		right, _ := strconv.ParseFloat(y.text, 64)
		switch {
		case left < right:
			return -1
		case left > right:
			return 1
		}
		return 0
	}
	left, right := textOrEmpty(x.tokenString()), textOrEmpty(y.tokenString())
	switch {
	case left < right:
		return -1
	case left > right:
		return 1
	}
	return 0
}

// ----- aliases -----

func (a *jsonDiffApplier) saveAlias(config *jnode) {
	if isEmptyToken(config.get("alias")) {
		return
	}
	clone := config.clone()
	alias := clone.get("alias")
	if !alias.isObject() {
		runtimeFault("Unable to cast object of type 'Newtonsoft.Json.Linq.JValue' to type 'Newtonsoft.Json.Linq.JObject'.")
	}
	if a.aliases == nil {
		a.aliases = map[string]*aliasInfo{}
	}
	info := &aliasInfo{name: optionalText(clone, "name")}
	if list := alias.get("excludeProperties"); list.isArray() {
		info.hasExcludeProps = true
		for _, item := range list.items {
			info.excludeProperties = append(info.excludeProperties, textOrEmpty(item.stringValue()))
		}
	}
	if list := alias.get("excludeOperations"); list.isArray() {
		info.hasExcludeOperates = true
		for _, item := range list.items {
			info.excludeOperations = append(info.excludeOperations, textOrEmpty(item.stringValue()))
		}
	}
	aliasName := optionalText(alias, "name")
	if aliasName == nil {
		runtimeFault("Value cannot be null. (Parameter 'key')")
	}
	a.aliases[*aliasName] = info
}

func (a *jsonDiffApplier) excludeAliasProperties(name *string, values *jnode) []string {
	if values == nil {
		runtimeFault(nullReferenceMessage)
	}
	properties := append([]string{}, values.keys...)
	if name != nil {
		if alias := a.aliases[*name]; alias != nil && alias.hasExcludeProps {
			kept := []string{}
			for _, property := range properties {
				if !containsText(alias.excludeProperties, property) {
					kept = append(kept, property)
				}
			}
			properties = kept
		}
	}
	return properties
}

func (a *jsonDiffApplier) isExcludeAliasOperation(name *string, operation string, properties *jnode) bool {
	if properties.isArray() && operation == "remove" {
		return false
	}
	if name == nil {
		return false
	}
	alias := a.aliases[*name]
	return alias != nil && alias.hasExcludeOperates && containsText(alias.excludeOperations, operation)
}

func containsText(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

// ----- insert / set / merge / remove -----

func (a *jsonDiffApplier) findInsertItemInfo(config *jnode) *itemInfo {
	parentName := optionalText(config, "parentName")
	info := a.findItemInfoInSourceObject(parentName)
	if !a.pathMode {
		return info
	}
	path := config.get("path")
	if info != nil && info.item != nil && path.isArray() {
		return &itemInfo{item: getByPath(info.item, pathSegments(path), nil)}
	}
	if path.isArray() {
		parts := pathSegments(path)
		info = &itemInfo{item: getByPath(a.source, parts, nil)}
		if info.item == nil {
			var rootName *string
			if len(parts) > 0 {
				text := fmt.Sprint(parts[0])
				rootName = &text
				parts = parts[1:]
			}
			info = a.findItemInfoInSourceObject(rootName)
			if info != nil && info.item != nil && len(parts) > 0 {
				info = &itemInfo{item: getByPath(info.item, parts, nil)}
			}
		}
	}
	return info
}

func (a *jsonDiffApplier) findInsertItemParent(info *itemInfo, config *jnode) *jnode {
	if info == nil {
		return a.source
	}
	if a.pathMode {
		return info.item
	}
	return indexByName(info.item, optionalText(config, "propertyName"))
}

// indexByName mirrors JToken[string]: a missing key is null, a null key or a non-object token throws.
func indexByName(item *jnode, key *string) *jnode {
	if item == nil {
		runtimeFault(nullReferenceMessage)
	}
	if key == nil {
		runtimeFault(nullPropertyNameArgumentText)
	}
	if item.kind == jkArray {
		runtimeFault(fmt.Sprintf(invalidJArrayKeyMessage, *key))
	}
	if !item.isObject() {
		runtimeFault(fmt.Sprintf("Cannot access child value on %s.", "Newtonsoft.Json.Linq.JValue"))
	}
	return item.get(*key)
}

func (a *jsonDiffApplier) insert(config *jnode) bool {
	parentName := optionalText(config, "parentName")
	name := optionalText(config, "name")
	if nameTo := textOrEmpty(optionalText(config, "nameTo")); nameTo != "" {
		parentName = &nameTo
	}
	info := a.findInsertItemInfo(config)
	parentExists := !(info == nil && textOrEmpty(optionalText(config, "parentName")) != "")
	item := newObject()
	if values := config.get("values"); values != nil && values.kind != jkNull {
		item = values.clone()
	}
	a.saveAlias(config)
	parent := a.findInsertItemParent(info, config)
	if name != nil && *name != "" && item.isObject() {
		item.set(a.aliasName(), newString(*name))
	}
	switch {
	case parent.isArray():
		length := len(parent.items)
		index := length
		if indexToken := config.get("index"); !isEmptyToken(indexToken) {
			index = tokenInt(indexToken)
		}
		parent.insertAt(normalizeSpliceStart(index, length), item)
	case parent.isObject() && info != nil:
		propertyName := optionalText(config, "propertyName")
		if info.item == nil {
			runtimeFault(nullReferenceMessage)
		}
		if propertyName == nil {
			runtimeFault(nullPropertyNameArgumentText)
		}
		if !info.item.isObject() {
			runtimeFault(fmt.Sprintf(invalidJArrayKeyMessage, *propertyName))
		}
		info.item.set(*propertyName, item)
	default:
		diffFault(notContainerInsertMessage, textOrEmpty(parentName))
	}
	return parentExists
}

func tokenInt(n *jnode) int {
	value, err := strconv.ParseFloat(n.text, 64)
	if err != nil {
		runtimeFault(fmt.Sprintf("Could not convert string to integer: %s.", n.text))
	}
	return int(value)
}

func normalizeSpliceStart(index, length int) int {
	if index < 0 {
		return max(length+index, 0)
	}
	return min(index, length)
}

func (a *jsonDiffApplier) setOperation(config *jnode) bool {
	parentName := optionalText(config, "parentName")
	if nameTo := textOrEmpty(optionalText(config, "nameTo")); nameTo != "" {
		parentName = &nameTo
	}
	removed := a.removeOperation(config)
	parentExists := removed != nil && removed.kind != jkNull && !(removed.isArray() && len(removed.items) == 0)
	if parentExists {
		config.set("index", removed.get("index").clone())
		config.set("nameTo", toJNode(parentName))
		config.set("parentName", toJNode(parentName))
		config.set("propertyName", removed.get("propertyName").clone())
	}
	a.insert(config)
	return parentExists
}

func (a *jsonDiffApplier) findMergeOrRemoveItemInfo(config *jnode) *itemInfo {
	info := a.findItemInfoInSourceObject(optionalText(config, "name"))
	if !a.pathMode {
		return info
	}
	path := config.get("path")
	if (info == nil || info.item == nil) && path.isArray() {
		parts := pathSegments(path)
		var fallback *jnode
		if len(parts) == 0 {
			fallback = a.source
		}
		info = &itemInfo{item: getByPath(a.source, parts, fallback)}
		if info.item == nil {
			var parentName *string
			if len(parts) > 0 {
				text := fmt.Sprint(parts[0])
				parentName = &text
				parts = parts[1:]
			}
			info = a.findItemInfoInSourceObject(parentName)
			if info != nil && info.item != nil && len(parts) > 0 {
				info = &itemInfo{item: getByPath(info.item, parts, nil)}
			}
		}
	}
	return info
}

func (a *jsonDiffApplier) merge(config *jnode) bool {
	info := a.findMergeOrRemoveItemInfo(config)
	if info == nil {
		return false
	}
	name := optionalText(config, "name")
	if a.pathMode {
		configValues := config.get("values")
		if configValues != nil && !configValues.isObject() {
			runtimeFault("Unable to cast object of type 'Newtonsoft.Json.Linq.JValue' to type 'Newtonsoft.Json.Linq.JObject'.")
		}
		values := newObject()
		for _, property := range a.excludeAliasProperties(name, configValues) {
			values.set(property, configValues.get(property).clone())
		}
		target := info.item
		if target == nil {
			runtimeFault(nullReferenceMessage)
		}
		if !target.isObject() {
			runtimeFault("Unable to cast object of type 'Newtonsoft.Json.Linq.JArray' to type 'Newtonsoft.Json.Linq.JObject'.")
		}
		merged := deepMergeReplaceArrays(target, values)
		for _, key := range merged.keys {
			target.set(key, merged.props[key])
		}
		return true
	}
	values := config.get("values")
	if !values.isObject() {
		return true
	}
	if info.item == nil || !info.item.isObject() {
		runtimeFault(nullReferenceMessage)
	}
	for _, key := range append([]string{}, info.item.keys...) {
		property := info.item.props[key]
		first := property
		if property.isArray() {
			first = nil
			if len(property.items) > 0 {
				first = property.items[0]
			}
		}
		if isItemConfig(first) && values.get(key) != nil {
			values.remove(key)
		}
	}
	for _, property := range a.excludeAliasProperties(name, values) {
		info.item.set(property, values.get(property).clone())
	}
	return true
}

func deepMergeReplaceArrays(target, source *jnode) *jnode {
	result := target.clone()
	for _, key := range source.keys {
		incoming := source.props[key]
		if existing := result.get(key); existing.isObject() && incoming.isObject() {
			result.set(key, deepMergeReplaceArrays(existing, incoming))
		} else {
			result.set(key, incoming.clone())
		}
	}
	return result
}

func (a *jsonDiffApplier) findRemoveParentItemInfo(parentName *string, info *itemInfo) *itemInfo {
	parent := a.findItemInfoInSourceObject(parentName)
	if !a.pathMode {
		return parent
	}
	if parent == nil && info != nil && info.parentPath != nil {
		path := append([]any{}, info.parentPath...)
		if len(path) > 0 {
			path = path[:len(path)-1]
		}
		if len(path) > 0 {
			path = path[:len(path)-1]
		}
		if len(path) == 0 {
			return nil
		}
		parent = &itemInfo{item: getByPath(a.source, path, nil)}
	}
	return parent
}

// removeOperation returns clio's remove result object, or nil when the item is not found.
func (a *jsonDiffApplier) removeOperation(config *jnode) *jnode {
	info := a.findMergeOrRemoveItemInfo(config)
	if info == nil {
		return nil
	}
	if properties := config.get("properties"); properties.isArray() {
		if info.item == nil || !info.item.isObject() {
			runtimeFault(nullReferenceMessage)
		}
		for _, property := range properties.items {
			info.item.remove(textOrEmpty(property.stringValue()))
		}
		return newObject()
	}
	removed := info.item
	parentName := info.parentName
	parentInfo := a.findRemoveParentItemInfo(parentName, info)
	items := a.source
	if parentInfo != nil {
		key := info.propertyName
		items = indexByName(parentInfo.item, &key)
	}
	index := 0
	if items.isArray() {
		index = -1
		for position, candidate := range items.items {
			if candidate == removed {
				index = position
				break
			}
		}
		if index >= 0 {
			items.removeAt(index)
		}
	} else {
		if parentInfo == nil || parentInfo.item == nil || !parentInfo.item.isObject() {
			runtimeFault(nullReferenceMessage)
		}
		parentInfo.item.remove(info.propertyName)
	}
	a.removeFromCache(removed)
	result := newObject()
	result.set("index", newInt(index))
	result.set("item", removed)
	result.set("nameTo", toJNode(parentName))
	result.set("parentName", toJNode(parentName))
	result.set("propertyName", newString(info.propertyName))
	return result
}

// ----- validation -----

func (a *jsonDiffApplier) checkOperation(operation *jnode) {
	name := optionalText(operation, "name")
	parentName := optionalText(operation, "parentName")
	if name != nil && *name != "" && sameText(parentName, name) {
		diffFault(loopDependencyMessage, *name)
	}
	checkRequiredParameters(operation, []string{"operation"})
	if !a.pathMode {
		required := map[string][]string{"set": {"name", "values"}, "merge": {"name", "values"}, "move": {"name"}, "remove": {"name"}}
		if kind := optionalText(operation, "operation"); kind != nil {
			checkRequiredParameters(operation, required[*kind])
		}
	}
}

func checkRequiredParameters(operation *jnode, required []string) {
	for _, parameter := range required {
		if isFalsy(operation.get(parameter)) {
			diffFault(requiredParameterMessage, parameter)
		}
	}
}

func pathSegments(path *jnode) []any {
	parts := make([]any, 0, len(path.items))
	for _, segment := range path.items {
		parts = append(parts, textOrEmpty(segment.stringValue()))
	}
	return parts
}

func getByPath(root *jnode, path []any, fallback *jnode) *jnode {
	if len(path) == 0 {
		return fallback
	}
	current := root
	for _, key := range path {
		text := fmt.Sprint(key)
		switch {
		case current.isObject() && current.get(text) != nil:
			current = current.get(text)
		case current.isArray():
			index, err := strconv.Atoi(text)
			if err != nil || index < 0 || index >= len(current.items) {
				return fallback
			}
			current = current.items[index]
		default:
			return fallback
		}
	}
	return current
}
