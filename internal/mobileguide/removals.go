package mobileguide

// ExcludedComponentsPass.cs and WebToMobileAnalysisService.ComponentRemovals.cs.

// --- excludedComponents ----------------------------------------------------------------------------------

func removeExcludedComponents(elementMap *[]*mapEntry, rules *Rules) (*strSet, *strSet) {
	removedMobile := newSet(true)
	removedWeb := newSet(true)
	if rules == nil || len(rules.ExcludedComponents) == 0 {
		return removedWeb, removedMobile
	}
	var filters []*ExcludedComponentFilterRule
	for _, group := range rules.ExcludedComponents {
		if group == nil {
			continue
		}
		for _, filter := range group.Filters {
			if filter == nil || filter.ParentType == nil || isBlank(*filter.ParentType) {
				continue
			}
			if filter.IsAllowList() {
				if filter.PropertiesContainerName == nil || isBlank(*filter.PropertiesContainerName) {
					continue
				}
			} else if filter.Type == nil || isBlank(*filter.Type) {
				continue
			}
			filters = append(filters, filter)
		}
	}
	if len(filters) == 0 {
		return removedWeb, removedMobile
	}
	removeExcludedEntries(*elementMap, filters, removedWeb, removedMobile)
	dropOrphanedSubtrees(*elementMap, removedWeb, removedMobile)
	stripVerbatimCarriedComponents(elementMap, buildFiltersByParentType(filters), removedWeb)
	return removedWeb, removedMobile
}

func removeExcludedEntries(elementMap []*mapEntry, filters []*ExcludedComponentFilterRule, removedWeb, removedMobile *strSet) {
	byMobileName := indexByMobileName(elementMap)
	for index, entry := range elementMap {
		if !isInsert(entry) || entry.MobileType == nil || *entry.MobileType == "" {
			continue
		}
		for _, filter := range filters {
			if !filter.MatchesType(*entry.MobileType) {
				continue
			}
			host := findHostOnAncestorPath(entry, filter, byMobileName)
			if host == nil {
				continue
			}
			elementMap[index] = &mapEntry{WebName: entry.WebName, WebType: entry.WebType, Operation: OperationDrop,
				Reason: buildExcludedDropReason(filter, host)}
			recordRemoved(entry, removedWeb, removedMobile)
			break
		}
	}
}

func findHostOnAncestorPath(candidate *mapEntry, filter *ExcludedComponentFilterRule, byMobileName *dict[*mapEntry]) *string {
	visited := newSet(true)
	current := candidate
	for depth := 0; depth <= maxParseDepth; depth++ {
		parentName := current.ParentName
		if parentName == nil || *parentName == "" || !visited.add(*parentName) {
			return nil
		}
		parent, ok := byMobileName.get(*parentName)
		if !ok {
			return nil
		}
		if eqip(parent.MobileType, filter.ParentType) && slotMatches(current, filter) {
			return parent.Name
		}
		current = parent
	}
	return nil
}

func slotMatches(child *mapEntry, filter *ExcludedComponentFilterRule) bool {
	if filter.PropertiesContainerName == nil || isBlank(*filter.PropertiesContainerName) {
		return true
	}
	slot := itemsPropertyName
	if child.PropertyName != nil && *child.PropertyName != "" {
		slot = *child.PropertyName
	}
	return eqi(slot, *filter.PropertiesContainerName)
}

func dropOrphanedSubtrees(elementMap []*mapEntry, removedWeb, removedMobile *strSet) {
	if removedMobile.len() == 0 {
		return
	}
	byMobileName := indexByMobileName(elementMap)
	for index, entry := range elementMap {
		if !isInsert(entry) {
			continue
		}
		ancestor := findRemovedAncestor(entry, removedMobile, byMobileName)
		if ancestor == nil {
			continue
		}
		elementMap[index] = &mapEntry{WebName: entry.WebName, WebType: entry.WebType, Operation: OperationDrop,
			Reason: []*ReasonCode{reason(DropParentExcluded, rp("ancestor", nzp(ancestor)))}}
		recordRemoved(entry, removedWeb, removedMobile)
	}
}

func findRemovedAncestor(entry *mapEntry, removedMobile *strSet, byMobileName *dict[*mapEntry]) *string {
	visited := newSet(true)
	current := entry
	for depth := 0; depth <= maxParseDepth; depth++ {
		parentName := current.ParentName
		if parentName == nil || *parentName == "" || !visited.add(*parentName) {
			return nil
		}
		if removedMobile.contains(*parentName) {
			return parentName
		}
		parent, ok := byMobileName.get(*parentName)
		if !ok {
			return nil
		}
		current = parent
	}
	return nil
}

func indexByMobileName(elementMap []*mapEntry) *dict[*mapEntry] {
	result := newCI[*mapEntry]()
	for _, entry := range elementMap {
		if entry.Name != nil && *entry.Name != "" && (isInsert(entry) || isMerge(entry)) {
			result.tryAdd(*entry.Name, entry)
		}
	}
	return result
}

func recordRemoved(removed *mapEntry, removedWeb, removedMobile *strSet) {
	if removed.WebName != nil && *removed.WebName != "" {
		removedWeb.add(*removed.WebName)
	}
	if removed.Name != nil && *removed.Name != "" {
		removedMobile.add(*removed.Name)
	}
}

func stripVerbatimCarriedComponents(elementMap *[]*mapEntry, filtersByParentType *dict[[]*ExcludedComponentFilterRule],
	removedWeb *strSet) {
	if filtersByParentType.len() == 0 {
		return
	}
	var dropped []*mapEntry
	for _, entry := range *elementMap {
		if !entry.Values.isObj() {
			continue
		}
		if entry.MobileType != nil && *entry.MobileType != "" {
			if rootFilters, ok := filtersByParentType.get(*entry.MobileType); ok {
				applyFiltersToHost(entry.Values, entry.Name, rootFilters, &dropped)
			}
		}
		findNestedHosts(entry.Values, filtersByParentType, entry.Name, &dropped, 0)
	}
	for _, entry := range dropped {
		if entry.WebName != nil && *entry.WebName != "" {
			removedWeb.add(*entry.WebName)
		}
	}
	*elementMap = append(*elementMap, dropped...)
}

func buildFiltersByParentType(filters []*ExcludedComponentFilterRule) *dict[[]*ExcludedComponentFilterRule] {
	result := newCI[[]*ExcludedComponentFilterRule]()
	for _, filter := range filters {
		list, _ := result.get(*filter.ParentType)
		result.set(*filter.ParentType, append(list, filter))
	}
	return result
}

func findNestedHosts(node *jn, filtersByParentType *dict[[]*ExcludedComponentFilterRule], fallbackHost *string,
	dropped *[]*mapEntry, depth int) {
	if depth > maxParseDepth {
		return
	}
	switch {
	case node.isArr():
		for index := 0; index < len(node.arr); index++ {
			candidate := node.arr[index]
			if candidate.isObj() {
				if typ, ok := candidate.get("type").nodeString(); ok && typ != "" {
					if filters, found := filtersByParentType.get(typ); found {
						host := fallbackHost
						if hostName, ok := candidate.get("name").nodeString(); ok && hostName != "" {
							host = strp(hostName)
						}
						applyFiltersToHost(candidate, host, filters, dropped)
					}
				}
			}
			findNestedHosts(node.arr[index], filtersByParentType, fallbackHost, dropped, depth+1)
		}
	case node.isObj():
		for _, key := range node.keyList() {
			findNestedHosts(node.get(key), filtersByParentType, fallbackHost, dropped, depth+1)
		}
	}
}

func applyFiltersToHost(hostValues *jn, hostName *string, filters []*ExcludedComponentFilterRule, dropped *[]*mapEntry) {
	for _, filter := range filters {
		scoped := filter.PropertiesContainerName != nil && !isBlank(*filter.PropertiesContainerName)
		var scopeKey *string
		if scoped {
			scopeKey = resolveScopeKey(hostValues, *filter.PropertiesContainerName)
			if scopeKey == nil {
				continue
			}
		}
		scope := hostValues
		if scoped {
			scope = hostValues.get(*scopeKey)
		}
		if scope == nil {
			continue
		}
		wasOccupied := scope.isArr() && len(scope.arr) > 0
		stripComponentsOfType(scope, filter, hostName, dropped, 0)
		if scopeKey != nil && wasOccupied && scope.isArr() && len(scope.arr) == 0 {
			hostValues.remove(*scopeKey)
		}
	}
}

func resolveScopeKey(hostValues *jn, propertyName string) *string {
	if hostValues.has(propertyName) {
		return &propertyName
	}
	for _, key := range hostValues.keys {
		if eqi(key, propertyName) {
			return strp(key)
		}
	}
	return nil
}

func stripComponentsOfType(scope *jn, filter *ExcludedComponentFilterRule, hostName *string, dropped *[]*mapEntry, depth int) {
	if depth > maxParseDepth {
		return
	}
	switch {
	case scope.isArr():
		for index := len(scope.arr) - 1; index >= 0; index-- {
			child := scope.arr[index]
			if child.isObj() {
				typ, _ := child.get("type").nodeString()
				if filter.MatchesType(typ) {
					*dropped = append(*dropped, buildExcludedDropEntry(child, filter, hostName))
					scope.removeAt(index)
					continue
				}
			}
			stripComponentsOfType(scope.arr[index], filter, hostName, dropped, depth+1)
		}
	case scope.isObj():
		for _, key := range scope.keyList() {
			if !shouldDescendInto(key, filter) {
				continue
			}
			wasOccupied := scope.get(key).isArr() && len(scope.get(key).arr) > 0
			stripComponentsOfType(scope.get(key), filter, hostName, dropped, depth+1)
			if wasOccupied && scope.get(key).isArr() && len(scope.get(key).arr) == 0 {
				scope.remove(key)
			}
		}
	}
}

func shouldDescendInto(propertyName string, filter *ExcludedComponentFilterRule) bool {
	if len(filter.ChildSlots) == 0 {
		return true
	}
	for _, slot := range filter.ChildSlots {
		if slot != nil && eqi(*slot, propertyName) {
			return true
		}
	}
	return false
}

func buildExcludedDropEntry(removed *jn, filter *ExcludedComponentFilterRule, hostName *string) *mapEntry {
	name, _ := removed.get("name").nodeString()
	typ, _ := removed.get("type").nodeString()
	return &mapEntry{WebName: nz(name), WebType: nz(typ), Operation: OperationDrop, Reason: buildExcludedDropReason(filter, hostName)}
}

func buildExcludedDropReason(filter *ExcludedComponentFilterRule, hostName *string) []*ReasonCode {
	return []*ReasonCode{reason(DropExcludedByRule, rp("hostType", nzp(filter.ParentType)), rp("host", nzp(hostName)),
		rp("slot", nzp(filter.PropertiesContainerName)))}
}

// --- componentRemovals -----------------------------------------------------------------------------------

type removalScope struct {
	occupiedSlots       *strSet
	parentsWithChildren *strSet
	valuesByName        *dict[[]*jn]
}

type carriedRemovalContext struct {
	requestMap         *dict[*RequestMappingRule]
	mobileRequestTypes *strSet
	actionComponents   *dict[[]string]
	removalRules       []*ComponentRemovalRule
	collectors         *requestCollectors
}

func applyComponentRemovals(elementMap *[]*mapEntry, requestMap *dict[*RequestMappingRule], mobileRequestTypes *strSet,
	actionComponents *dict[[]string], removalRules []*ComponentRemovalRule, collectors *requestCollectors) {
	pruneCarriedComponents(elementMap, &carriedRemovalContext{requestMap, mobileRequestTypes, actionComponents, removalRules, collectors})
	removeMatchingEntries(*elementMap, removalRules)
}

func pruneCarriedComponents(elementMap *[]*mapEntry, ctx *carriedRemovalContext) {
	index := 0
	for index < len(*elementMap) {
		host := (*elementMap)[index]
		if host.Values == nil || (!isInsert(host) && !isMerge(host)) {
			index++
			continue
		}
		var removed []*mapEntry
		pruneCarriedComponentsIn(host.Values, ctx, &removed, 0)
		if len(removed) == 0 {
			index++
			continue
		}
		rest := append([]*mapEntry{}, (*elementMap)[index+1:]...)
		*elementMap = append(append((*elementMap)[:index+1], removed...), rest...)
		index += len(removed) + 1
	}
}

func pruneCarriedComponentsIn(node *jn, ctx *carriedRemovalContext, removed *[]*mapEntry, depth int) {
	if depth > maxParseDepth {
		return
	}
	switch {
	case node.isObj():
		for _, key := range node.keyList() {
			value := node.get(key)
			if value == nil {
				continue
			}
			if value.isArr() {
				if pruneCarriedComponentsFrom(value, ctx, removed, depth) && len(value.arr) == 0 {
					node.remove(key)
				}
				continue
			}
			pruneCarriedComponentsIn(value, ctx, removed, depth)
			if !value.isObj() || !isComponentObjectS(value) {
				continue
			}
			if code := carriedRemovalReason(value, ctx); code != nil {
				if name := stringProp(value, "name"); name != nil && *name != "" {
					*removed = append(*removed, drop(name, stringProp(value, "type"), code))
					node.remove(key)
					continue
				}
			}
			processCarriedEventBindings(value, ctx)
		}
	case node.isArr():
		pruneCarriedComponentsFrom(node, ctx, removed, depth)
	}
}

func pruneCarriedComponentsFrom(array *jn, ctx *carriedRemovalContext, removed *[]*mapEntry, depth int) bool {
	var doomed []int
	for index := 0; index < len(array.arr); index++ {
		member := array.arr[index]
		if !member.isObj() {
			if member != nil {
				pruneCarriedComponentsIn(member, ctx, removed, depth+1)
			}
			continue
		}
		pruneCarriedComponentsIn(member, ctx, removed, depth+1)
		code := carriedRemovalReason(member, ctx)
		name := stringProp(member, "name")
		if code == nil || name == nil || *name == "" {
			processCarriedEventBindings(member, ctx)
			continue
		}
		*removed = append(*removed, drop(name, stringProp(member, "type"), code))
		doomed = append(doomed, index)
	}
	for index := len(doomed) - 1; index >= 0; index-- {
		array.removeAt(doomed[index])
	}
	return len(doomed) > 0
}

func processCarriedEventBindings(member *jn, ctx *carriedRemovalContext) {
	elementName := stringProp(member, "name")
	if elementName == nil || *elementName == "" || !isComponentObjectS(member) {
		return
	}
	for _, binding := range member.keyList() {
		source := member.get(binding)
		if !source.isObj() || !isEventBindingS(source) {
			continue
		}
		request := stringProp(source, "request")
		if request == nil || *request == "" {
			continue
		}
		rule, ok := ctx.requestMap.get(*request)
		if !ok {
			ctx.collectors.flagged = append(ctx.collectors.flagged, &FlaggedRequest{ElementName: elementName,
				Binding: strp(binding), Request: request, Reason: []*ReasonCode{reason(FlagRequestUnmapped)}})
			continue
		}
		if rule.Mobile != nil && !isBlank(*rule.Mobile) {
			continue
		}
		member.remove(binding)
		var note *string
		if rule.Note != nil && !isBlank(*rule.Note) {
			note = rule.Note
		}
		ctx.collectors.dropped = append(ctx.collectors.dropped, &DroppedRequest{ElementName: elementName, Binding: strp(binding),
			WebRequest: request, Reason: []*ReasonCode{reason(DropRequestUnsupported, rp("note", note))}})
	}
}

func carriedRemovalReason(member *jn, ctx *carriedRemovalContext) *ReasonCode {
	typ := stringProp(member, "type")
	if isActionOnlyType(ctx.actionComponents, typ) && !carriesComponents(member, 0) {
		if dead := unsupportedCarriedRequest(member, ctx.requestMap, ctx.mobileRequestTypes); dead != "" {
			return unsupportedRequestDropReason(ctx.requestMap, dead, nil)
		}
	}
	return matchingRemovalReason(ctx.removalRules, typ, nil, nil, member)
}

func unsupportedCarriedRequest(node *jn, requestMap *dict[*RequestMappingRule], mobileRequestTypes *strSet) string {
	for _, key := range node.keys {
		value := node.props[key]
		if !isEventBindingS(value) {
			continue
		}
		if request := stringProp(value, "request"); request != nil && *request != "" &&
			!isRequestSupported(requestMap, mobileRequestTypes, *request) {
			return *request
		}
	}
	return ""
}

func removeMatchingEntries(elementMap []*mapEntry, removalRules []*ComponentRemovalRule) {
	if len(removalRules) == 0 {
		return
	}
	for removedThisRound := true; removedThisRound; {
		removedThisRound = false
		scope := buildRemovalScope(elementMap)
		for index, entry := range elementMap {
			if !isRemovalCandidate(entry) {
				continue
			}
			code := matchingRemovalReason(removalRules, entry.MobileType, scope, entry.Name, entry.Values)
			if code == nil {
				continue
			}
			elementMap[index] = drop(entry.WebName, entry.WebType, code)
			removedThisRound = true
		}
	}
}

func buildRemovalScope(elementMap []*mapEntry) *removalScope {
	scope := &removalScope{occupiedSlots: newSet(true), parentsWithChildren: newSet(true), valuesByName: newCI[[]*jn]()}
	for _, entry := range elementMap {
		if !isInsert(entry) && !isMerge(entry) {
			continue
		}
		if entry.ParentName != nil && *entry.ParentName != "" {
			scope.parentsWithChildren.add(*entry.ParentName)
			scope.occupiedSlots.add(slotKey(*entry.ParentName, slotOf(entry)))
		}
		if entry.Name != nil && *entry.Name != "" && entry.Values.isObj() {
			written, _ := scope.valuesByName.get(*entry.Name)
			scope.valuesByName.set(*entry.Name, append(written, entry.Values))
		}
	}
	return scope
}

func isRemovalCandidate(entry *mapEntry) bool {
	return isInsert(entry) && entry.WebName != nil && *entry.WebName != "" && entry.Name != nil && *entry.Name != "" &&
		entry.Values.isObj()
}

func matchingRemovalReason(removalRules []*ComponentRemovalRule, typ *string, scope *removalScope, mobileName *string,
	values *jn) *ReasonCode {
	for _, rule := range removalRules {
		if rule == nil || rule.Type == nil || *rule.Type == "" || rule.Filters == nil || !eqip(rule.Type, typ) {
			continue
		}
		if !matchesRemovalFilter(rule.Filters, scope, mobileName, values) {
			continue
		}
		if stillHasSomethingToLose(scope, mobileName, values) {
			return nil
		}
		code := DropUnsupportedRequest
		if rule.Reason != nil && *rule.Reason != "" {
			code = *rule.Reason
		}
		return reason(code)
	}
	return nil
}

func stillHasSomethingToLose(scope *removalScope, mobileName *string, values *jn) bool {
	if values != nil && (carriesEventBinding(values) || carriesComponents(values, 0)) {
		return true
	}
	return scope != nil && mobileName != nil && *mobileName != "" && scope.parentsWithChildren.contains(*mobileName)
}

func carriesEventBinding(values *jn) bool {
	for _, key := range values.keys {
		if isEventBindingS(values.props[key]) {
			return true
		}
	}
	return false
}

func carriesComponents(values *jn, depth int) bool {
	if depth > maxParseDepth {
		return false
	}
	switch {
	case values.isArr():
		for _, item := range values.arr {
			if (item.isObj() && isComponentObjectS(item)) || (item != nil && carriesComponents(item, depth+1)) {
				return true
			}
		}
	case values.isObj():
		for _, key := range values.keys {
			value := values.props[key]
			if value != nil && ((value.isObj() && isComponentObjectS(value)) || carriesComponents(value, depth)) {
				return true
			}
		}
	}
	return false
}

func matchesRemovalFilter(filter *ComponentPropertyFilter, scope *removalScope, mobileName *string, values *jn) bool {
	switch filter.FilterType {
	case FilterTypeIsEmpty:
		return filter.LeftExpression != nil && *filter.LeftExpression != "" &&
			isEmptyExpression(scope, mobileName, values, *filter.LeftExpression)
	case FilterTypeGroup:
		if len(filter.Items) == 0 {
			return false
		}
		if filter.LogicalOperation != nil && eqi(*filter.LogicalOperation, LogicalOr) {
			for _, item := range filter.Items {
				if matchesRemovalFilter(item, scope, mobileName, values) {
					return true
				}
			}
			return false
		}
		for _, item := range filter.Items {
			if !matchesRemovalFilter(item, scope, mobileName, values) {
				return false
			}
		}
		return true
	}
	return false
}

func isEmptyExpression(scope *removalScope, mobileName *string, values *jn, expression string) bool {
	if scope != nil && mobileName != nil && *mobileName != "" {
		if scope.occupiedSlots.contains(slotKey(*mobileName, expression)) {
			return false
		}
		if written, ok := scope.valuesByName.get(*mobileName); ok {
			var folded *jn
			addressed := false
			for _, candidate := range written {
				if value, found := tryGetProperty(candidate, expression); found {
					folded = value
					addressed = true
				}
			}
			if addressed {
				return isEmptyValue(folded)
			}
		}
	}
	own, found := tryGetProperty(values, expression)
	return !found || isEmptyValue(own)
}

func slotOf(entry *mapEntry) string {
	if entry.PropertyName != nil && *entry.PropertyName != "" {
		return *entry.PropertyName
	}
	return itemsPropertyName
}

func slotKey(parent, slot string) string { return parent + "\n" + slot }

func isEmptyValue(value *jn) bool {
	switch {
	case value == nil || value.k == jsNull:
		return true
	case value.isArr():
		return len(value.arr) == 0
	case value.isObj():
		return len(value.keys) == 0
	case value.k == jsString:
		return value.s == ""
	}
	return false
}

func tryGetProperty(values *jn, propertyName string) (*jn, bool) {
	if values == nil {
		return nil, false
	}
	if value, ok := values.props[propertyName]; ok {
		return value, true
	}
	for _, key := range values.keys {
		if eqi(key, propertyName) {
			return values.props[key], true
		}
	}
	return nil, false
}
