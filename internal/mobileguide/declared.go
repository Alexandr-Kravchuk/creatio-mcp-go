package mobileguide

// declaredElements admission and emission (SelectDeclaredElements, WithDeclaredElements, EmitDeclaredElements).

type declaredSelection struct {
	accepted         []*DeclaredElementRule
	containerNameMap *dict[string]
}

func isValidDeclaredElementShape(declared *DeclaredElementRule) bool {
	if declared.Type == nil || isBlank(*declared.Type) || declared.ParentName == nil || isBlank(*declared.ParentName) {
		return false
	}
	if !safeIdentifierPattern.MatchString(deref(declared.Name)) || !safeIdentifierPattern.MatchString(*declared.ParentName) {
		return false
	}
	if declared.PropertyName != nil && *declared.PropertyName != "" && !safeIdentifierPattern.MatchString(*declared.PropertyName) {
		return false
	}
	if declared.CaptionResource != nil && declared.CaptionResource.Property != nil && *declared.CaptionResource.Property != "" &&
		!safeIdentifierPattern.MatchString(*declared.CaptionResource.Property) {
		return false
	}
	return true
}

func selectDeclaredElements(rule *TemplateMappingRule, mobileTypes *strSet, pageViewConfig *jn,
	containerNameMap *dict[string], webTemplateNames *strSet, probedTypes *dict[*string], probeAvailable bool) declaredSelection {
	var accepted []*DeclaredElementRule
	removedNames := newSet(true)
	seen := newSet(true)
	var pageNames *strSet
	if pageViewConfig == nil {
		pageNames = newSet(true)
	} else {
		pageNames = collectComponentNames(pageViewConfig)
	}
	if webTemplateNames.len() > 0 {
		pageNames.exceptWith(webTemplateNames.keys())
	}
	if rule != nil {
		for _, declared := range rule.DeclaredElements {
			if declared == nil || declared.Name == nil || isBlank(*declared.Name) {
				continue
			}
			name := *declared.Name
			if !seen.add(name) {
				continue
			}
			if !isValidDeclaredElementShape(declared) {
				removedNames.add(name)
				continue
			}
			if probeAvailable && probedTypes.has(name) {
				continue
			}
			if !mobileTypes.contains(*declared.Type) {
				removedNames.add(name)
				continue
			}
			mappedTo, ok := containerNameMap.get(name)
			pairedOntoItself := ok && eqi(mappedTo, name)
			if pageNames.contains(name) && !pairedOntoItself {
				removedNames.add(name)
				continue
			}
			accepted = append(accepted, declared)
		}
	}
	if len(accepted) > 1 {
		byName := newCI[*DeclaredElementRule]()
		for _, declared := range accepted {
			if !byName.tryAdd(*declared.Name, declared) {
				panic(engineFault{"An item with the same key has already been added. Key: " + *declared.Name})
			}
		}
		state := newCI[int]()
		cyclic := newSet(true)
		for _, start := range accepted {
			if state.value(*start.Name) != 0 {
				continue
			}
			var chain []string
			current := start
			for current != nil && state.value(*current.Name) == 0 {
				state.set(*current.Name, 1)
				chain = append(chain, *current.Name)
				selfParented := eqi(deref(current.ParentName), *current.Name)
				var next *DeclaredElementRule
				if !selfParented {
					if parent, ok := byName.get(deref(current.ParentName)); ok {
						next = parent
					}
				}
				current = next
			}
			if current != nil && state.value(*current.Name) == 1 {
				start := 0
				for index, name := range chain {
					if name == *current.Name {
						start = index
						break
					}
				}
				for _, name := range chain[start:] {
					cyclic.add(name)
				}
			}
			for _, name := range chain {
				state.set(name, 2)
			}
		}
		if cyclic.len() > 0 {
			for index := len(accepted) - 1; index >= 0; index-- {
				if cyclic.contains(*accepted[index].Name) {
					removedNames.add(*accepted[index].Name)
					accepted = append(accepted[:index:index], accepted[index+1:]...)
				}
			}
		}
	}
	for changed := true; changed; {
		changed = false
		known := newSet(true)
		if probeAvailable {
			for _, name := range probedTypes.keys() {
				known.add(name)
			}
		}
		for _, declared := range accepted {
			known.add(*declared.Name)
		}
		for index := len(accepted) - 1; index >= 0; index-- {
			declared := accepted[index]
			selfParented := eqi(deref(declared.ParentName), *declared.Name)
			if !selfParented {
				if probeAvailable {
					if known.contains(*declared.ParentName) {
						continue
					}
				} else if !removedNames.contains(*declared.ParentName) {
					continue
				}
			}
			removedNames.add(*declared.Name)
			accepted = append(accepted[:index:index], accepted[index+1:]...)
			changed = true
		}
	}
	result := containerNameMap
	if removedNames.len() > 0 {
		result = newCI[string]()
		for _, pair := range containerNameMap.pairs() {
			if !removedNames.contains(pair.value) {
				if !result.tryAdd(pair.key, pair.value) {
					panic(engineFault{"An item with the same key has already been added. Key: " + pair.key})
				}
			}
		}
	}
	return declaredSelection{accepted: accepted, containerNameMap: result}
}

func withDeclaredTypes(probed *dict[*string], extras []*DeclaredElementRule) *dict[*string] {
	result := newCI[*string]()
	for _, pair := range probed.pairs() {
		result.set(pair.key, pair.value)
	}
	for _, extra := range extras {
		result.tryAdd(*extra.Name, extra.Type)
	}
	return result
}

func withDeclaredParents(probed *dict[string], extras []*DeclaredElementRule) *dict[string] {
	result := newCI[string]()
	for _, pair := range probed.pairs() {
		result.set(pair.key, pair.value)
	}
	for _, extra := range extras {
		result.tryAdd(*extra.Name, deref(extra.ParentName))
	}
	return result
}

func withDeclaredElementLayouts(probed *dict[*jn], extras []*DeclaredElementRule) *dict[*jn] {
	result := newCI[*jn]()
	for _, pair := range probed.pairs() {
		result.set(pair.key, pair.value)
	}
	for _, extra := range extras {
		if extra.Values == nil {
			continue
		}
		raw, ok := extra.Values.Values["layoutConfig"]
		if !ok {
			continue
		}
		if placement := rawNode(raw); placement.isObj() {
			result.tryAdd(*extra.Name, placement)
		}
	}
	return result
}

const maxDeclaredElementsChainDepth = 32

func orderDeclaredElementsParentFirst(extras []*DeclaredElementRule) []*DeclaredElementRule {
	if len(extras) < 2 {
		return extras
	}
	byName := newCI[*DeclaredElementRule]()
	for _, extra := range extras {
		byName.tryAdd(*extra.Name, extra)
	}
	ordered := make([]*DeclaredElementRule, 0, len(extras))
	visited := newSet(true)
	var visit func(extra *DeclaredElementRule, depth int)
	visit = func(extra *DeclaredElementRule, depth int) {
		if !visited.add(*extra.Name) {
			return
		}
		if depth < maxDeclaredElementsChainDepth {
			if parent, ok := byName.get(deref(extra.ParentName)); ok && !eqi(*parent.Name, *extra.Name) {
				visit(parent, depth+1)
			}
		}
		ordered = append(ordered, extra)
	}
	for _, extra := range extras {
		visit(extra, 0)
	}
	return ordered
}

func emitDeclaredElements(ctx *walkContext, extras []*DeclaredElementRule) {
	extras = orderDeclaredElementsParentFirst(extras)
	frontInsertAt := 0
	for _, extra := range extras {
		values := newObj()
		values.set("type", stjString(extra.Type))
		if extra.Values != nil {
			for _, key := range extra.Values.Keys {
				if eqi(key, "type") || eqi(key, itemsPropertyName) {
					continue
				}
				values.set(key, rawNode(extra.Values.Values[key]))
			}
		}
		var caption *CaptionResource
		if capRule := extra.CaptionResource; capRule != nil && capRule.Key != nil && !isBlank(*capRule.Key) &&
			safeIdentifierPattern.MatchString(*capRule.Key) {
			property := "caption"
			if capRule.Property != nil && !isBlank(*capRule.Property) {
				property = *capRule.Property
			}
			if !eqi(property, "type") && !eqi(property, itemsPropertyName) {
				caption = &CaptionResource{Key: capRule.Key, SourceValue: capRule.Value}
				values.set(property, newStr("#ResourceString("+*capRule.Key+")#"))
			}
		}
		propertyName := itemsPropertyName
		if extra.PropertyName != nil && !isBlank(*extra.PropertyName) {
			propertyName = *extra.PropertyName
		}
		entry := &mapEntry{
			Operation: OperationInsert, Name: extra.Name, MobileType: extra.Type, ParentName: extra.ParentName,
			PropertyName: strp(propertyName), Index: extra.Index, CaptionResource: caption, Values: values,
			DeclaredByRule: true,
		}
		parentAt := -1
		for index, existing := range ctx.out {
			if isInsert(existing) && eqip(existing.Name, extra.ParentName) {
				parentAt = index
				break
			}
		}
		var at int
		if parentAt >= 0 {
			at = parentAt + 1
			for at < len(ctx.out) && ctx.out[at].DeclaredByRule && eqip(ctx.out[at].ParentName, extra.ParentName) {
				at++
			}
		} else {
			at = frontInsertAt
			frontInsertAt++
		}
		ctx.out = append(ctx.out, nil)
		copy(ctx.out[at+1:], ctx.out[at:])
		ctx.out[at] = entry
	}
}
