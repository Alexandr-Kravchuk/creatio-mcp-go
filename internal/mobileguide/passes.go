package mobileguide

import (
	"crypto/sha256"
	"fmt"
	"math/big"
	"strconv"
	"strings"
)

// The passes that run over the finished element map, and the projections into the answer.

func intp(value int) *int { return &value }

func removeEmptyContainers(elementMap []*mapEntry, rules *Rules) (*strSet, *strSet) {
	removed := newSet(true)
	removedMobile := newSet(true)
	removable := newSet(true)
	if rules != nil && rules.EmptyContainerRemoval != nil {
		for _, typ := range rules.EmptyContainerRemoval.RemovableTypes {
			if typ != nil && !isBlank(*typ) {
				removable.add(*typ)
			}
		}
	}
	if removable.len() == 0 {
		return removed, removedMobile
	}
	for anyRemoved := true; anyRemoved; {
		anyRemoved = false
		occupied := newSet(true)
		for _, entry := range elementMap {
			if isInsert(entry) && entry.ParentName != nil && *entry.ParentName != "" {
				occupied.add(*entry.ParentName)
			}
		}
		for index := 0; index < len(elementMap); index++ {
			entry := elementMap[index]
			if !isEmptyRemovalCandidate(entry, removable) || occupied.contains(deref(entry.Name)) {
				continue
			}
			if entry.DeclaredByRule {
				elementMap[index] = &mapEntry{Name: entry.Name, MobileType: nzp(entry.MobileType), Operation: OperationDrop,
					DeclaredByRule: true, Reason: []*ReasonCode{reason(DropEmptyContainer)}}
				for twinIndex, twin := range elementMap {
					if isMerge(twin) && eqip(twin.Name, entry.Name) {
						elementMap[twinIndex] = drop(twin.WebName, twin.WebType, reason(DropTargetMissing, rp("missingParent", nzp(entry.Name))))
						if twin.WebName != nil && *twin.WebName != "" {
							removed.add(*twin.WebName)
						}
					}
				}
			} else {
				elementMap[index] = drop(entry.WebName, entry.WebType, reason(DropEmptyContainer))
			}
			if entry.WebName != nil && *entry.WebName != "" {
				removed.add(*entry.WebName)
			}
			removedMobile.add(deref(entry.Name))
			anyRemoved = true
		}
	}
	return removed, removedMobile
}

func isEmptyRemovalCandidate(entry *mapEntry, removable *strSet) bool {
	return isInsert(entry) && ((entry.WebName != nil && *entry.WebName != "") || entry.DeclaredByRule) &&
		entry.Name != nil && *entry.Name != "" && entry.MobileType != nil && *entry.MobileType != "" &&
		removable.contains(*entry.MobileType) && (!entry.Values.isObj() || entry.Values.get("items") == nil)
}

func compactPositionalIndexes(elementMap []*mapEntry) {
	groups := newCI[[]*mapEntry]()
	var order []string
	for _, entry := range elementMap {
		if isInsert(entry) && !entry.DeclaredByRule && entry.Index != nil && entry.ParentName != nil && *entry.ParentName != "" {
			list, ok := groups.get(*entry.ParentName)
			if !ok {
				order = append(order, *entry.ParentName)
			}
			groups.set(*entry.ParentName, append(list, entry))
		}
	}
	for _, parent := range order {
		group, _ := groups.get(parent)
		sorted := append([]*mapEntry{}, group...)
		stableSort(sorted, func(a, b *mapEntry) bool { return *a.Index < *b.Index })
		for next, entry := range sorted {
			entry.Index = intp(next)
		}
	}
}

const (
	mobileTabsElementName         = "Tabs"
	mobileTabComponentType        = "crt.TabContainer"
	mobileTabsPanelComponentType  = "crt.TabPanel"
	firstConvertedTabIndex        = 1
	mobileGridContainerType       = "crt.GridContainer"
	mobileGridMaxColumns          = 12
	maxWebPosition                = 1_000_000
	parentSourceTemplate          = "template"
	parentSourcePage              = "page"
	parentSourceConverter         = "converter"
	parentSourceUnknown           = "unknown"
	changedNamedElementConflict   = "changed-named-element"
	changedScalarConflict         = "changed-scalar"
	namelessChangedInPlaceConflct = "nameless-changed-in-place"
)

func assignConvertedTabIndexes(elementMap []*mapEntry) {
	tabsCreated := false
	for _, entry := range elementMap {
		if isInsert(entry) && eqi(deref(entry.Name), mobileTabsElementName) && eqi(deref(entry.MobileType), mobileTabsPanelComponentType) {
			tabsCreated = true
			break
		}
	}
	declaredIndexes := map[int]bool{}
	for _, entry := range elementMap {
		if entry.DeclaredByRule && entry.Index != nil && isInsert(entry) && eqi(deref(entry.ParentName), mobileTabsElementName) {
			declaredIndexes[*entry.Index] = true
		}
	}
	next := firstConvertedTabIndex
	if tabsCreated {
		next = 0
	}
	for _, entry := range elementMap {
		if isInsert(entry) && !entry.DeclaredByRule && entry.ParentName != nil && eqi(*entry.ParentName, mobileTabsElementName) &&
			entry.MobileType != nil && eqi(*entry.MobileType, mobileTabComponentType) {
			for declaredIndexes[next] {
				next++
			}
			entry.Index = intp(next)
			next++
		}
	}
}

func stampParentSource(elementMap []*mapEntry, mobileTemplateTypesByName *dict[*string]) {
	authoredHere := newCI[bool]()
	for _, entry := range elementMap {
		if isInsert(entry) && entry.Name != nil && *entry.Name != "" {
			authoredHere.set(*entry.Name, entry.WebName != nil && *entry.WebName != "")
		}
	}
	for _, entry := range elementMap {
		if !isInsert(entry) || entry.ParentName == nil || *entry.ParentName == "" {
			entry.ParentSource = nil
			continue
		}
		if fromPage, ok := authoredHere.get(*entry.ParentName); ok {
			if fromPage {
				entry.ParentSource = strp(parentSourcePage)
			} else {
				entry.ParentSource = strp(parentSourceConverter)
			}
			continue
		}
		if mobileTemplateTypesByName.len() > 0 && mobileTemplateTypesByName.has(*entry.ParentName) {
			entry.ParentSource = strp(parentSourceTemplate)
		} else {
			entry.ParentSource = strp(parentSourceUnknown)
		}
	}
}

// --- property prune --------------------------------------------------------------------------------------

type declaredPropertyIndex struct {
	enabled bool
	byType  *dict[*sortedSetOI]
}

func disabledPropertyIndex() *declaredPropertyIndex {
	return &declaredPropertyIndex{byType: newCI[*sortedSetOI]()}
}

func declaredPropertyIndexFromRegistry(mobileByType *dict[*RegistryEntry], baseInputs *dict[*jn]) *declaredPropertyIndex {
	if mobileByType.len() == 0 {
		return disabledPropertyIndex()
	}
	byType := newCI[*sortedSetOI]()
	for _, pair := range mobileByType.pairs() {
		if isBlank(pair.key) || pair.value == nil {
			continue
		}
		declared := buildAllowedPropertyNames(pair.value, nil)
		if len(declared.items) == 0 {
			continue
		}
		for _, key := range baseInputs.keys() {
			declared.add(key)
		}
		byType.set(pair.key, declared)
	}
	if byType.len() == 0 {
		return disabledPropertyIndex()
	}
	return &declaredPropertyIndex{enabled: true, byType: byType}
}

func (index *declaredPropertyIndex) declaresProperty(mobileType, propName string) bool {
	if !index.enabled || isBlank(mobileType) || isBlank(propName) {
		return true
	}
	declared, ok := index.byType.get(mobileType)
	return !ok || declared.contains(propName)
}

func buildAllowedPropertyNames(entry *RegistryEntry, baseInputs *dict[*jn]) *sortedSetOI {
	allowed := &sortedSetOI{}
	if entry == nil {
		return allowed
	}
	for _, key := range entry.Properties.keys() {
		allowed.add(key)
	}
	for _, key := range entry.Inputs.keys() {
		allowed.add(key)
	}
	for _, key := range entry.Outputs.keys() {
		allowed.add(key)
	}
	for _, key := range baseInputs.keys() {
		allowed.add(key)
	}
	return allowed
}

func pruneUndeclaredProperties(elementMap []*mapEntry, declared *declaredPropertyIndex, collectors *requestCollectors) []*PrunedPropertyEntry {
	if !declared.enabled || len(elementMap) == 0 {
		return nil
	}
	var entries []*PrunedPropertyEntry
	for _, entry := range elementMap {
		if entry == nil || entry.DeclaredByRule || !entry.Values.isObj() || entry.MobileType == nil || isBlank(*entry.MobileType) ||
			((entry.WebName == nil || isBlank(*entry.WebName)) && (entry.WebType == nil || isBlank(*entry.WebType))) {
			continue
		}
		values := entry.Values
		var removed, removedBindings []string
		for _, propName := range values.keyList() {
			if excludedSourceProps.contains(propName) || declared.declaresProperty(*entry.MobileType, propName) {
				continue
			}
			wasBinding := isEventBindingNode(values.get(propName))
			values.remove(propName)
			removed = append(removed, propName)
			if !wasBinding {
				continue
			}
			removedBindings = append(removedBindings, propName)
			reclassifyPrunedBinding(collectors, isInsert(entry), deref(entry.Name), propName, *entry.MobileType)
		}
		if len(removed) > 0 {
			pruned := &PrunedPropertyEntry{Name: entry.Name, Type: entry.MobileType, WebName: entry.WebName, Properties: removed}
			if len(removedBindings) > 0 {
				pruned.Bindings = removedBindings
			}
			entries = append(entries, pruned)
		}
	}
	return entries
}

func isEventBindingNode(value *jn) bool {
	if !value.isObj() {
		return false
	}
	request, ok := value.props["request"]
	return ok && request != nil && request.k == jsString && !isBlank(request.s)
}

func reclassifyPrunedBinding(collectors *requestCollectors, isInsertEntry bool, elementName, binding, mobileType string) {
	matches := func(name, boundTo *string) bool { return eqi(deref(name), elementName) && eqi(deref(boundTo), binding) }
	var webRequest *string
	for _, record := range collectors.converted {
		if matches(record.ElementName, record.Binding) {
			webRequest = record.WebRequest
			break
		}
	}
	keptConverted := collectors.converted[:0:0]
	for _, record := range collectors.converted {
		if !matches(record.ElementName, record.Binding) {
			keptConverted = append(keptConverted, record)
		}
	}
	collectors.converted = keptConverted
	if webRequest == nil {
		for _, record := range collectors.flagged {
			if matches(record.ElementName, record.Binding) {
				webRequest = record.Request
				break
			}
		}
	}
	keptFlagged := collectors.flagged[:0:0]
	for _, record := range collectors.flagged {
		if !matches(record.ElementName, record.Binding) {
			keptFlagged = append(keptFlagged, record)
		}
	}
	collectors.flagged = keptFlagged
	keptUnresolved := collectors.unresolved[:0:0]
	for _, record := range collectors.unresolved {
		if !matches(record.ElementName, record.Binding) {
			keptUnresolved = append(keptUnresolved, record)
		}
	}
	collectors.unresolved = keptUnresolved
	if webRequest == nil || !isInsertEntry {
		return
	}
	collectors.dropped = append(collectors.dropped, &DroppedRequest{ElementName: strp(elementName), Binding: strp(binding),
		WebRequest: webRequest, Reason: []*ReasonCode{reason(DropRequestPropertyNotDeclared, rp("mobileType", strp(mobileType)))}})
}

// --- request conversions ---------------------------------------------------------------------------------

func buildRequestConversionInfo(collectors *requestCollectors, emptyRemovedMobile, excludedRemovedMobile *strSet,
	probe *ActionTargetsProbeResult) *RequestConversionInfo {
	reclassifyRemovedBindings(collectors, emptyRemovedMobile, reason(DropRequestElementEmptyContainer))
	if excludedRemovedMobile.len() > 0 {
		reclassifyRemovedBindings(collectors, excludedRemovedMobile, reason(DropRequestElementExcluded))
	}
	reclassifyRemovedTargetFindings(collectors, emptyRemovedMobile)
	reclassifyRemovedTargetFindings(collectors, excludedRemovedMobile)
	targetsProbed := probe != nil && probe.ProbeOk
	if len(collectors.converted) == 0 && len(collectors.dropped) == 0 && len(collectors.flagged) == 0 && len(collectors.unresolved) == 0 {
		return nil
	}
	info := &RequestConversionInfo{
		ConvertedRequests:        orEmpty(collectors.converted),
		DroppedRequests:          orEmpty(collectors.dropped),
		FlaggedRequests:          orEmpty(collectors.flagged),
		UnresolvedTargetRequests: orEmpty(collectors.unresolved),
		TargetsProbed:            targetsProbed,
		MissingTargetPages:       buildMissingTargetPages(collectors.unresolved),
	}
	if probe != nil {
		info.TargetsNote = probe.Note
	}
	return info
}

func orEmpty[T any](items []T) []T {
	if items == nil {
		return []T{}
	}
	return items
}

func reclassifyRemovedTargetFindings(collectors *requestCollectors, removed *strSet) {
	if removed.len() == 0 {
		return
	}
	kept := collectors.unresolved[:0:0]
	for _, record := range collectors.unresolved {
		if !removed.contains(deref(record.ElementName)) {
			kept = append(kept, record)
		}
	}
	collectors.unresolved = kept
}

func reclassifyRemovedBindings(collectors *requestCollectors, removed *strSet, code *ReasonCode) {
	for index := len(collectors.converted) - 1; index >= 0; index-- {
		record := collectors.converted[index]
		if record.ElementName != nil && removed.contains(*record.ElementName) {
			collectors.dropped = append(collectors.dropped, &DroppedRequest{ElementName: record.ElementName, Binding: record.Binding,
				WebRequest: record.WebRequest, Reason: []*ReasonCode{code}})
			collectors.converted = append(collectors.converted[:index:index], collectors.converted[index+1:]...)
		}
	}
	for index := len(collectors.flagged) - 1; index >= 0; index-- {
		record := collectors.flagged[index]
		if record.ElementName != nil && removed.contains(*record.ElementName) {
			collectors.dropped = append(collectors.dropped, &DroppedRequest{ElementName: record.ElementName, Binding: record.Binding,
				WebRequest: record.Request, Reason: []*ReasonCode{code}})
			collectors.flagged = append(collectors.flagged[:index:index], collectors.flagged[index+1:]...)
		}
	}
}

// isValidSchemaName is PageSchemaMetadataHelper.IsValidSchemaName.
func isValidSchemaName(name string) bool {
	if name == "" {
		return false
	}
	for index, r := range name {
		if index == 0 && !isLetter(r) {
			return false
		}
		if !isLetter(r) && !isDigit(r) && r != '_' {
			return false
		}
	}
	return true
}

func buildMissingTargetPages(unresolved []*UnresolvedTargetRequest) []*MissingTargetPage {
	type keyed struct {
		key, kind string
		resolved  bool
		request   *UnresolvedTargetRequest
	}
	var entries []keyed
	for _, record := range unresolved {
		switch {
		case eqi(deref(record.TargetKind), KindWebPage) && isValidSchemaName(deref(record.Target)):
			entries = append(entries, keyed{deref(record.Target), KindWebPage, false, record})
		case eqi(deref(record.TargetKind), KindEntityDefaultMobilePage) && deref(record.State) == StateMissing:
			resolved := record.ResolvedCandidateSchemaName != nil && !isBlank(*record.ResolvedCandidateSchemaName)
			key := deref(record.Target)
			if resolved {
				key = *record.ResolvedCandidateSchemaName
			}
			if !isBlank(key) {
				entries = append(entries, keyed{key, KindEntityDefaultMobilePage, resolved, record})
			}
		}
	}
	groups := newCI[[]keyed]()
	var order []string
	for _, entry := range entries {
		list, ok := groups.get(entry.key)
		if !ok {
			order = append(order, entry.key)
		}
		groups.set(entry.key, append(list, entry))
	}
	pages := []*MissingTargetPage{}
	for _, key := range order {
		group, _ := groups.get(key)
		kind := group[0].kind
		anyResolved := false
		for _, entry := range group {
			if entry.kind == KindWebPage {
				kind = KindWebPage
			}
			if entry.resolved {
				anyResolved = true
			}
		}
		page := &MissingTargetPage{Target: strp(key), TargetKind: strp(kind), References: []*MissingTargetPageReference{}}
		if kind == KindEntityDefaultMobilePage && anyResolved {
			page.ResolvedCandidateSchemaName = strp(key)
		}
		seen := map[[2]string]bool{}
		for _, entry := range group {
			pair := [2]string{deref(entry.request.ElementName), deref(entry.request.Binding)}
			if entry.request.ElementName == nil {
				pair[0] = "\x00null"
			}
			if entry.request.Binding == nil {
				pair[1] = "\x00null"
			}
			if seen[pair] {
				continue
			}
			seen[pair] = true
			page.References = append(page.References, &MissingTargetPageReference{ElementName: entry.request.ElementName,
				Binding: entry.request.Binding})
		}
		pages = append(pages, page)
	}
	return pages
}

// --- tab area layers -------------------------------------------------------------------------------------

func isUsableLayer(container *SynthesizedContainerRule) bool {
	if container == nil || container.NamePrefix == nil || isBlank(*container.NamePrefix) || container.Values == nil {
		return false
	}
	raw, ok := container.Values.Values["type"]
	if !ok {
		return false
	}
	typ := rawNode(raw)
	return typ != nil && typ.k == jsString && !isBlank(typ.s)
}

func synthesizedLayerEntry(container *SynthesizedContainerRule, name, parentName string) *mapEntry {
	values := newObj()
	for _, key := range container.Values.Keys {
		values.set(key, rawNode(container.Values.Values[key]))
	}
	return &mapEntry{Operation: OperationInsert, Name: strp(name), MobileType: strp(values.get("type").getStringValue()),
		ParentName: strp(parentName), PropertyName: strp(itemsPropertyName), Values: values}
}

func buildTabAreaLayers(elementMap *[]*mapEntry, rules *Rules, sourcePage *string) []*TabAreaLayerGroup {
	if rules == nil || rules.TabAreaLayers == nil {
		return nil
	}
	rule := rules.TabAreaLayers
	mainRule := rule.MainTabContainer
	var areaRule *SynthesizedContainerRule
	if mainRule != nil {
		areaRule = mainRule.AreaContainer
	}
	if rule.TabComponentType == nil || isBlank(*rule.TabComponentType) || !isUsableLayer(mainRule) || !isUsableLayer(areaRule) {
		return nil
	}
	taken := newSet(true)
	for _, entry := range *elementMap {
		if entry.WebName != nil && *entry.WebName != "" {
			taken.add(*entry.WebName)
		}
		if entry.Name != nil && *entry.Name != "" {
			taken.add(*entry.Name)
		}
	}
	var groups []*TabAreaLayerGroup
	var tabs []*mapEntry
	for _, entry := range *elementMap {
		if isInsert(entry) && eqip(entry.MobileType, rule.TabComponentType) && entry.Name != nil && *entry.Name != "" {
			tabs = append(tabs, entry)
		}
	}
	for _, tab := range tabs {
		var content []*mapEntry
		for _, entry := range *elementMap {
			if eqip(entry.ParentName, tab.Name) && (isInsert(entry) || isRelocateChildren(entry)) {
				content = append(content, entry)
			}
		}
		if len(content) == 0 {
			continue
		}
		suffix := stableSuffix(sourcePage, tab.Name, func(candidate string) bool {
			return taken.contains(*mainRule.NamePrefix+candidate) || taken.contains(*areaRule.NamePrefix+candidate)
		})
		mainName := *mainRule.NamePrefix + suffix
		taken.add(mainName)
		insertAt := indexOfEntry(*elementMap, tab)
		insertAt++
		*elementMap = insertEntry(*elementMap, insertAt, synthesizedLayerEntry(mainRule, mainName, *tab.Name))
		var areaName *string
		for _, child := range content {
			if isInsert(child) {
				areaName = strp(*areaRule.NamePrefix + suffix)
				taken.add(*areaName)
				*elementMap = insertEntry(*elementMap, insertAt+1, synthesizedLayerEntry(areaRule, *areaName, mainName))
				break
			}
		}
		ordered := append([]*mapEntry{}, content...)
		stableSort(ordered, func(a, b *mapEntry) bool { return isHeaderSlot(a.PropertyName) && !isHeaderSlot(b.PropertyName) })
		moved := []string{}
		row := 1
		for _, child := range ordered {
			if areaName != nil {
				child.ParentName = strp(*areaName)
			} else {
				child.ParentName = strp(mainName)
			}
			if !isInsert(child) {
				continue
			}
			child.PropertyName = strp(itemsPropertyName)
			moved = append(moved, deref(child.Name))
			placeInSingleColumn(child, row)
			row++
		}
		groups = append(groups, &TabAreaLayerGroup{TabName: tab.Name, MainTabContainerName: strp(mainName), AreaName: areaName,
			MovedChildren: moved})
	}
	return groups
}

func indexOfEntry(elementMap []*mapEntry, target *mapEntry) int {
	for index, entry := range elementMap {
		if entry == target {
			return index
		}
	}
	return -1
}

func insertEntry(elementMap []*mapEntry, at int, entry *mapEntry) []*mapEntry {
	elementMap = append(elementMap, nil)
	copy(elementMap[at+1:], elementMap[at:])
	elementMap[at] = entry
	return elementMap
}

func isHeaderSlot(propertyName *string) bool {
	return propertyName != nil && *propertyName != "" && !eqi(*propertyName, itemsPropertyName)
}

func placeInSingleColumn(child *mapEntry, row int) {
	if values := child.Values; values.isObj() {
		layout := values.get("layoutConfig")
		if !layout.isObj() || layout.get("adaptive") == nil {
			values.set("layoutConfig", siblingSlot(row))
		}
	}
}

func siblingSlot(row int) *jn {
	slot := newObj()
	slot.set("row", newIntNode(row))
	slot.set("column", newIntNode(1))
	slot.set("colSpan", newIntNode(1))
	slot.set("rowSpan", newIntNode(1))
	return slot
}

const base36Digits = "0123456789abcdefghijklmnopqrstuvwxyz"

func stableSuffix(sourcePage, tabName *string, isTaken func(string) bool) string {
	hash := sha256.Sum256([]byte(deref(sourcePage) + ":" + deref(tabName)))
	value := new(big.Int).SetBytes(hash[:])
	base36 := "0"
	if value.Sign() != 0 {
		var digits []byte
		thirtySix := big.NewInt(36)
		remainder := new(big.Int)
		for value.Sign() != 0 {
			value.DivMod(value, thirtySix, remainder)
			digits = append([]byte{base36Digits[remainder.Int64()]}, digits...)
		}
		base36 = string(digits)
	}
	for len(base36) < 7 {
		base36 = "0" + base36
	}
	for length := 7; length <= len(base36); length++ {
		if candidate := base36[:length]; isTaken == nil || !isTaken(candidate) {
			return candidate
		}
	}
	for index := 0; index < 1000; index++ {
		if candidate := base36 + strconv.Itoa(index); isTaken == nil || !isTaken(candidate) {
			return candidate
		}
	}
	panic(engineFault{fmt.Sprintf("Cannot derive a free name suffix for tab '%s' on page '%s': every candidate is reported as taken.",
		deref(tabName), deref(sourcePage))})
}

// --- child slots, overrides, placements ------------------------------------------------------------------

func initializeContainerChildSlots(elementMap []*mapEntry, mobileByType *dict[*RegistryEntry]) {
	occupied := newCI[*strSet]()
	for _, entry := range elementMap {
		if !isInsert(entry) || entry.ParentName == nil || *entry.ParentName == "" {
			continue
		}
		slot := itemsPropertyName
		if entry.PropertyName != nil && *entry.PropertyName != "" {
			slot = *entry.PropertyName
		}
		slots, ok := occupied.get(*entry.ParentName)
		if !ok {
			slots = newSet(false)
			occupied.set(*entry.ParentName, slots)
		}
		slots.add(slot)
	}
	for _, entry := range elementMap {
		if !isInsert(entry) || entry.Name == nil || *entry.Name == "" || !entry.Values.isObj() {
			continue
		}
		slots, ok := occupied.get(*entry.Name)
		if !ok {
			continue
		}
		ordered := slots.keys()
		stableSort(ordered, func(a, b string) bool {
			rank := func(slot string) int {
				if eqi(slot, itemsPropertyName) {
					return 0
				}
				return 1
			}
			if rank(a) != rank(b) {
				return rank(a) < rank(b)
			}
			return compareOI(a, b) < 0
		})
		for _, slot := range ordered {
			if declaresObjectShapedSlot(mobileByType, entry.MobileType, slot) {
				continue
			}
			if entry.Values.get(slot) == nil {
				entry.Values.set(slot, newArr())
			}
		}
	}
}

func declaresObjectShapedSlot(mobileByType *dict[*RegistryEntry], mobileType *string, slot string) bool {
	if mobileByType == nil || mobileType == nil || *mobileType == "" {
		return false
	}
	entry, ok := mobileByType.get(*mobileType)
	return ok && entry != nil && resolveExpectedShape(entry, slot) == shapeObject
}

type overrideGroup struct {
	normalized []*NormalizationEntry
	skipped    []*NormalizationSkip
}

type overrideResult struct {
	groups *dict[*overrideGroup]
	order  []string
}

func (r *overrideResult) add(group string, name, typ *string, properties, skipped []string) {
	accumulator, ok := r.groups.get(group)
	if !ok {
		accumulator = &overrideGroup{}
		r.groups.set(group, accumulator)
		r.order = append(r.order, group)
	}
	if len(properties) > 0 {
		accumulator.normalized = append(accumulator.normalized, &NormalizationEntry{Name: name, Type: typ, Properties: properties})
	}
	if len(skipped) > 0 {
		accumulator.skipped = append(accumulator.skipped, &NormalizationSkip{Name: name, Type: typ, Properties: skipped,
			Reason: []*ReasonCode{reason(SkipNormalizationPathBlocked)}})
	}
}

func applyComponentPropertyOverrides(elementMap []*mapEntry, rules *Rules) *overrideResult {
	result := &overrideResult{groups: newCI[*overrideGroup]()}
	if rules == nil || len(rules.ComponentPropertyOverrides) == 0 {
		return result
	}
	var declared []*ComponentPropertyOverrideRule
	for _, rule := range rules.ComponentPropertyOverrides {
		if rule != nil && rule.Values != nil && len(rule.Values.Keys) > 0 && rule.Filters != nil {
			declared = append(declared, rule)
		}
	}
	if len(declared) == 0 {
		return result
	}
	for _, entry := range elementMap {
		if !isInsert(entry) || entry.MobileType == nil || *entry.MobileType == "" || !entry.Values.isObj() {
			continue
		}
		values := entry.Values
		var matched []*ComponentPropertyOverrideRule
		for _, rule := range declared {
			if matchesOverrideFilters(values, rule.Filters) {
				matched = append(matched, rule)
			}
		}
		var properties, skipped []string
		for _, rule := range matched {
			for _, key := range rule.Values.Keys {
				if eqi(key, "name") || eqi(key, "type") {
					continue
				}
				stampOverrideValue(values, key, rule.Values.Values[key], bool(rule.MergeNestedObjects), &properties, &skipped)
			}
		}
		if len(properties) > 0 || len(skipped) > 0 {
			result.add(resolveReportGroup(*entry.MobileType), entry.Name, entry.MobileType, distinct(properties), distinct(skipped))
		}
	}
	return result
}

func distinct(items []string) []string {
	seen := map[string]bool{}
	result := []string{}
	for _, item := range items {
		if !seen[item] {
			seen[item] = true
			result = append(result, item)
		}
	}
	return result
}

func matchesOverrideFilters(values *jn, filters []*ElementFilterRule) bool {
	if len(filters) == 0 {
		return true
	}
	typ := stringProp(values, "type")
	for _, filter := range filters {
		if !declaresFilter(filter) {
			continue
		}
		if filter.Type != nil && !isBlank(*filter.Type) && !eqip(filter.Type, typ) {
			continue
		}
		if matchesValueConstraints(filter, func(key string) *jn { return values.get(key) }) {
			return true
		}
	}
	return false
}

func stampOverrideValue(values *jn, key string, raw []byte, merge bool, stamped, skipped *[]string) {
	incoming := rawNode(raw)
	if merge && incoming.isObj() {
		if !hasLeaf(incoming) {
			return
		}
		if !values.has(key) {
			values.set(key, newObj())
		}
		existing := values.get(key)
		if !existing.isObj() {
			*skipped = append(*skipped, key)
			return
		}
		mergeJSONObject(existing, incoming, key, stamped, skipped)
		return
	}
	values.set(key, incoming)
	*stamped = append(*stamped, key)
}

func hasLeaf(source *jn) bool {
	for _, key := range source.keys {
		child := source.props[key]
		if !child.isObj() || hasLeaf(child) {
			return true
		}
	}
	return false
}

func mergeJSONObject(target, source *jn, prefix string, stamped, skipped *[]string) {
	for _, key := range source.keys {
		path := prefix + "." + key
		sourceChild := source.props[key]
		if sourceChild.isObj() {
			if len(sourceChild.keys) == 0 {
				continue
			}
			if !target.has(key) {
				target.set(key, newObj())
			}
			if existing := target.get(key); existing.isObj() {
				mergeJSONObject(existing, sourceChild, path, stamped, skipped)
			} else {
				*skipped = append(*skipped, path)
			}
			continue
		}
		incoming := sourceChild.clone()
		if nodeEquals(target.get(key), incoming) {
			continue
		}
		target.set(key, incoming)
		*stamped = append(*stamped, path)
	}
}

func resolveReportGroup(mobileType string) string {
	switch {
	case eqi(mobileType, "crt.GridContainer"), eqi(mobileType, "crt.FlexContainer"):
		return "spacing"
	case eqi(mobileType, "crt.IndicatorWidget"):
		return "metricStyle"
	}
	return mobileType
}

func buildNormalizations(result *overrideResult) *OrderedMap[*NormalizationInfo] {
	if len(result.order) == 0 {
		return nil
	}
	sections := &OrderedMap[*NormalizationInfo]{}
	for _, group := range result.order {
		accumulator, _ := result.groups.get(group)
		info := &NormalizationInfo{Normalized: orEmpty(accumulator.normalized)}
		if len(accumulator.skipped) > 0 {
			info.Skipped = accumulator.skipped
		}
		sections.Set(group, info)
	}
	return sections
}

func normalizePlacements(elementMap []*mapEntry) {
	for _, entry := range elementMap {
		if entry.Values != nil {
			normalizePlacementsIn(entry.Values)
		}
	}
}

func normalizePlacementsIn(node *jn) {
	switch {
	case node.isObj():
		if placement, ok := node.props["layoutConfig"]; ok {
			node.set("layoutConfig", completePlacement(placement))
		}
		for _, key := range node.keyList() {
			if key != "layoutConfig" && node.props[key] != nil {
				normalizePlacementsIn(node.props[key])
			}
		}
	case node.isArr():
		for _, item := range append([]*jn{}, node.arr...) {
			if item != nil {
				normalizePlacementsIn(item)
			}
		}
	}
}

func completePlacement(placement *jn) *jn {
	if !placement.isObj() {
		return siblingSlot(1)
	}
	if adaptive := placement.get("adaptive"); adaptive.isObj() {
		for _, key := range adaptive.keyList() {
			if slot := adaptive.props[key]; slot.isObj() {
				fillPlacementKeys(slot)
			}
		}
		for _, key := range []string{"row", "column", "colSpan", "rowSpan"} {
			if placement.get(key) != nil {
				fillPlacementKeys(placement)
				break
			}
		}
		return placement
	}
	fillPlacementKeys(placement)
	return placement
}

func fillPlacementKeys(cell *jn) {
	for _, key := range []string{"row", "column"} {
		if cell.get(key) == nil {
			cell.set(key, newIntNode(1))
		}
	}
	for _, key := range []string{"colSpan", "rowSpan"} {
		cell.set(key, newIntNode(1))
	}
}

// --- projections -----------------------------------------------------------------------------------------

func resolveTemplateMatch(rule *TemplateMappingRule) *string {
	if rule == nil {
		return nil
	}
	if rule.Web == nil || isBlank(*rule.Web) {
		return strp("generic-fallback")
	}
	return strp("matched")
}

func buildContainerMap(rule *TemplateMappingRule) []*ContainerMapEntry {
	entries := []*ContainerMapEntry{}
	if rule == nil {
		return entries
	}
	for _, container := range rule.Containers {
		if container != nil && isSafeRuleIdentifier(container.Web) && isSafeRuleIdentifier(container.Mobile) {
			entries = append(entries, &ContainerMapEntry{Web: container.Web, Mobile: container.Mobile})
		}
	}
	return entries
}

func isSafeRuleIdentifier(value *string) bool {
	return value != nil && !isBlank(*value) && safeContainerNamePattern.MatchString(*value)
}

func projectDroppedElements(elementMap []*mapEntry) []*DroppedElement {
	var dropped []*DroppedElement
	for _, entry := range elementMap {
		if !((isDrop(entry) && entry.WebName != nil && *entry.WebName != "") || isRelocateChildren(entry)) {
			continue
		}
		element := &DroppedElement{WebName: entry.WebName, WebType: entry.WebType, Reason: entry.Reason}
		if isRelocateChildren(entry) {
			element.Reason = []*ReasonCode{reason(DropContainerNoMobileEquivalent, rp("newParent", nzp(entry.ParentName)))}
		}
		dropped = append(dropped, element)
	}
	return dropped
}

func projectViewConfigDiff(elementMap []*mapEntry) []*ViewConfigDiffOperation {
	withPayload := newSet(true)
	seen := newCI[int]()
	for _, entry := range elementMap {
		if (!isInsert(entry) && !isMerge(entry)) || entry.Name == nil || *entry.Name == "" {
			continue
		}
		seen.set(*entry.Name, seen.value(*entry.Name)+1)
		if values := entry.Values; values != nil && (!values.isObj() || len(values.keys) > 0) {
			withPayload.add(*entry.Name)
		}
	}
	payloadFreeKept := newSet(true)
	operations := []*ViewConfigDiffOperation{}
	for _, entry := range elementMap {
		if !isInsert(entry) && !isMerge(entry) {
			continue
		}
		if isRedundantPayloadFreeTwin(entry, seen, withPayload, payloadFreeKept) {
			continue
		}
		operation := &ViewConfigDiffOperation{Operation: strp(entry.Operation), Name: entry.Name, ParentName: entry.ParentName,
			PropertyName: entry.PropertyName, Index: entry.Index}
		values := entry.Values
		if values == nil && isMerge(entry) {
			values = newObj()
		}
		operation.Values = rawOf(values)
		operations = append(operations, operation)
	}
	return operations
}

func isRedundantPayloadFreeTwin(entry *mapEntry, seen *dict[int], withPayload, payloadFreeKept *strSet) bool {
	if !isMerge(entry) || (entry.Values != nil && !(entry.Values.isObj() && len(entry.Values.keys) == 0)) ||
		entry.Name == nil || *entry.Name == "" {
		return false
	}
	if count, ok := seen.get(*entry.Name); !ok || count <= 1 {
		return false
	}
	return withPayload.contains(*entry.Name) || !payloadFreeKept.add(*entry.Name)
}

func projectNameMap(elementMap []*mapEntry) *OrderedMap[*string] {
	renames := &OrderedMap[*string]{}
	for _, entry := range elementMap {
		if isDrop(entry) || isRelocateChildren(entry) || entry.WebName == nil || *entry.WebName == "" || entry.Name == nil ||
			*entry.Name == "" || *entry.WebName == *entry.Name {
			continue
		}
		renames.Set(*entry.WebName, entry.Name)
	}
	if renames.Len() == 0 {
		return nil
	}
	return renames
}

func projectUnresolvedParents(elementMap []*mapEntry) []*UnresolvedParent {
	var unresolved []*UnresolvedParent
	for _, entry := range elementMap {
		if deref(entry.ParentSource) == parentSourceUnknown && entry.ParentSource != nil && entry.Name != nil && *entry.Name != "" {
			unresolved = append(unresolved, &UnresolvedParent{Name: entry.Name, ParentName: entry.ParentName})
		}
	}
	return unresolved
}

// --- suggestions and contracts ---------------------------------------------------------------------------

func buildComponentSuggestions(namesByType *dict[[]string], rules *Rules, webTypes *strSet, elementMap []*mapEntry) []*ComponentSuggestion {
	presentTypes := newSet(true, namesByType.keys()...)
	emitted := newCI[*sortedSetOI]()
	entried := newSet(true)
	for _, entry := range elementMap {
		if entry == nil || entry.WebType == nil || *entry.WebType == "" {
			continue
		}
		webType := *entry.WebType
		entried.add(webType)
		if !isInsert(entry) && !isMerge(entry) {
			continue
		}
		types, ok := emitted.get(webType)
		if !ok {
			types = &sortedSetOI{}
			emitted.set(webType, types)
		}
		if entry.MobileType != nil && *entry.MobileType != "" {
			types.add(*entry.MobileType)
		}
	}
	nested := collectNestedComponentTypes(elementMap)
	suggestions := []*ComponentSuggestion{}
	for _, pair := range namesByType.pairs() {
		typ := pair.key
		emittedTypes, converted := emitted.get(typ)
		if !entried.contains(typ) && nested.contains(typ) {
			continue
		}
		rule := findRule(rules, &typ)
		suggested := &sortedSetOI{}
		if emittedTypes != nil {
			for _, item := range emittedTypes.items {
				suggested.add(item)
			}
		}
		if rule != nil {
			for _, mobile := range rule.Mobile {
				if mobile != nil && !isBlank(*mobile) {
					suggested.add(*mobile)
				}
			}
		}
		var note *string
		if rule != nil {
			note = rule.Note
		}
		suggestions = append(suggestions, &ComponentSuggestion{
			SourceType: strp(typ), SourceNames: pair.value,
			Category:             strp(classifyFromOutcome(typ, converted, suggested, rule, webTypes)),
			SuggestedMobileTypes: append([]string{}, suggested.items...),
			PrimaryWebMerge:      buildPrimaryWebMergeNote(rule, presentTypes),
			Note:                 note,
		})
	}
	stableSort(suggestions, func(a, b *ComponentSuggestion) bool { return compareOI(*a.SourceType, *b.SourceType) < 0 })
	return suggestions
}

var categoryNames = []string{"DirectMapping", "WithAdaptation", "AlternativeAvailable", "Unsupported", "RequiresManualDecision"}

func classifyFromOutcome(webType string, converted bool, suggested *sortedSetOI, rule *ComponentEquivalenceRule, webTypes *strSet) string {
	if converted && len(suggested.items) > 0 {
		if len(suggested.items) == 1 && suggested.contains(webType) {
			return "DirectMapping"
		}
		return "AlternativeAvailable"
	}
	if rule != nil && len(rule.Mobile) > 0 {
		if rule.Category != nil && *rule.Category != "" {
			return parseCategory(*rule.Category)
		}
		return "AlternativeAvailable"
	}
	if webTypes.contains(webType) {
		return "Unsupported"
	}
	return "RequiresManualDecision"
}

// parseCategory is Enum.TryParse(ignoreCase) over ComponentMappingCategory (names or their numbers).
func parseCategory(category string) string {
	trimmed := dotnetTrim(category)
	for _, name := range categoryNames {
		if eqi(name, trimmed) {
			return name
		}
	}
	if number, err := strconv.Atoi(trimmed); err == nil {
		if number >= 0 && number < len(categoryNames) {
			return categoryNames[number]
		}
		return strconv.Itoa(number)
	}
	return "RequiresManualDecision"
}

func collectNestedComponentTypes(elementMap []*mapEntry) *strSet {
	nested := newSet(true)
	for _, entry := range elementMap {
		if isDrop(entry) || !entry.Values.isObj() {
			continue
		}
		for _, key := range entry.Values.keys {
			if eqi(key, "type") {
				continue
			}
			collectComponentTypes(entry.Values.props[key], nested)
		}
	}
	return nested
}

func collectComponentTypes(node *jn, into *strSet) {
	switch {
	case node.isObj():
		if typ := stringProp(node, "type"); typ != nil && *typ != "" {
			into.add(*typ)
		}
		for _, key := range node.keys {
			collectComponentTypes(node.props[key], into)
		}
	case node.isArr():
		for _, item := range node.arr {
			collectComponentTypes(item, into)
		}
	}
}

func buildPrimaryWebMergeNote(rule *ComponentEquivalenceRule, presentTypes *strSet) *string {
	if rule == nil || rule.Web == nil || rule.PrimaryWeb == nil || isBlank(*rule.PrimaryWeb) {
		return nil
	}
	var present []string
	for _, web := range rule.Web {
		if web != nil && presentTypes.contains(*web) {
			present = append(present, *web)
		}
	}
	if len(present) <= 1 {
		return nil
	}
	mobile := "the mobile component"
	if len(rule.Mobile) > 0 {
		mobile = deref(rule.Mobile[0])
	}
	var secondary []string
	for _, web := range present {
		if !eqi(web, *rule.PrimaryWeb) {
			secondary = append(secondary, web)
		}
	}
	return strp(fmt.Sprintf("Many→one: build a single mobile %s from the primary web component %s; merge in properties from %s (do not emit them as separate components).",
		mobile, *rule.PrimaryWeb, strings.Join(secondary, ", ")))
}

func buildMobileContracts(suggestions []*ComponentSuggestion, mobileByType *dict[*RegistryEntry], baseInputs *dict[*jn]) []*MobileComponentContract {
	contracts := []*MobileComponentContract{}
	if mobileByType == nil {
		return contracts
	}
	seen := newSet(true)
	for _, suggestion := range suggestions {
		for _, mobileType := range suggestion.SuggestedMobileTypes {
			if isBlank(mobileType) || !seen.add(mobileType) {
				continue
			}
			entry, ok := mobileByType.get(mobileType)
			if !ok {
				continue
			}
			contract := &MobileComponentContract{ComponentType: strp(mobileType)}
			if entry != nil {
				contract.Container = entry.Container
				contract.Description = entry.Description
				contract.Example = entry.Example
				contract.DesignerDefaults = entry.DesignerDefaults
			}
			contract.AllowedProperties = append([]string{}, buildAllowedPropertyNames(entry, baseInputs).items...)
			contracts = append(contracts, contract)
		}
	}
	return contracts
}

func resolvePositionalParents(placements []*PositionalPlacement, mobileContainerParents *dict[string]) *dict[string] {
	result := newCI[string]()
	for _, placement := range placements {
		if placement == nil || placement.WebAnchor == nil || isBlank(*placement.WebAnchor) || placement.MobileAnchor == nil ||
			isBlank(*placement.MobileAnchor) {
			continue
		}
		parent := positionalFallbackParent
		if resolved, ok := mobileContainerParents.get(*placement.MobileAnchor); ok && !isBlank(resolved) {
			parent = resolved
		}
		result.set(*placement.WebAnchor, parent)
	}
	return result
}

func resolvePositionalAnchors(placements []*PositionalPlacement) *dict[string] {
	result := newCI[string]()
	for _, placement := range placements {
		if placement != nil && placement.WebAnchor != nil && !isBlank(*placement.WebAnchor) && placement.MobileAnchor != nil &&
			!isBlank(*placement.MobileAnchor) {
			result.set(*placement.WebAnchor, *placement.MobileAnchor)
		}
	}
	return result
}
