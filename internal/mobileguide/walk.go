package mobileguide

// The element-map walk (BuildElementMap, WalkElements and the branches it takes).

type requestCollectors struct {
	converted  []*ConvertedRequest
	dropped    []*DroppedRequest
	flagged    []*FlaggedRequest
	unresolved []*UnresolvedTargetRequest
}

// walkContext is ElementMapContext.
type walkContext struct {
	containerMap                *dict[string]
	componentMap                *dict[*ComponentMappingRule]
	mobileTypes                 *strSet
	mobileByType                *dict[*RegistryEntry]
	webByType                   *dict[*RegistryEntry]
	rules                       *Rules
	attrToColumn                *dict[string]
	resources                   *jn
	relocateTarget              string
	out                         []*mapEntry
	requestMap                  *dict[*RequestMappingRule]
	collectors                  *requestCollectors
	sourceLayouts               *dict[*jn]
	gridContainerColumns        *dict[int]
	positionalParentByAnchor    *dict[string]
	positionalAnchorByWebAnchor *dict[string]
	mobileTypesByName           *dict[*string]
	mobileNodesByName           *dict[*jn]
	webBaselineNodes            *dict[*jn]
	webBaselineResources        *jn
	scopeContainerNames         *strSet
	contentContainerTypes       *strSet
	actionComponentProperties   *dict[[]string]
	actionTargets               *dict[*ActionTargetResolution]
	declaredTypesByName         *dict[*string]
	mobileRequestTypes          *strSet
}

func collectScopeContainerNames(rules *Rules) *strSet {
	set := newSet(true)
	if rules != nil {
		for _, name := range rules.NonConvertingScopeContainers {
			if name != nil && !isBlank(*name) {
				set.add(*name)
			}
		}
	}
	return set
}

func buildElementMap(tree *jn, containerMap *dict[string], componentMap *dict[*ComponentMappingRule], mobileTypes *strSet,
	mobileByType, webByType *dict[*RegistryEntry], rules *Rules, attrToColumn *dict[string], resources *jn,
	requestMap *dict[*RequestMappingRule], collectors *requestCollectors, sourceLayouts *dict[*jn],
	gridContainerColumns *dict[int], actionComponentProperties *dict[[]string], positionalParentByAnchor,
	positionalAnchorByWebAnchor *dict[string], mobileTypesByName *dict[*string], mobileNodesByName,
	webBaselineNodes *dict[*jn], webBaselineResources *jn, declared []*DeclaredElementRule,
	actionTargets *dict[*ActionTargetResolution], mobileRequestTypes *strSet) []*mapEntry {
	declaredTypes := newCI[*string]()
	for _, element := range declared {
		declaredTypes.tryAdd(*element.Name, element.Type)
	}
	if componentMap == nil {
		componentMap = newCI[*ComponentMappingRule]()
	}
	if mobileByType == nil {
		mobileByType = newDict[*RegistryEntry](false)
	}
	if webByType == nil {
		webByType = newDict[*RegistryEntry](false)
	}
	if positionalParentByAnchor == nil {
		positionalParentByAnchor = newCI[string]()
	}
	if positionalAnchorByWebAnchor == nil {
		positionalAnchorByWebAnchor = newCI[string]()
	}
	if mobileTypesByName == nil {
		mobileTypesByName = newCI[*string]()
	}
	if mobileNodesByName == nil {
		mobileNodesByName = newCI[*jn]()
	}
	if webBaselineNodes == nil {
		webBaselineNodes = newCI[*jn]()
	}
	ctx := &walkContext{
		containerMap: containerMap, componentMap: componentMap, mobileTypes: mobileTypes, mobileByType: mobileByType,
		webByType: webByType, rules: rules, attrToColumn: attrToColumn, resources: resources,
		relocateTarget: relocateTargetFor(containerMap), out: []*mapEntry{}, requestMap: requestMap,
		collectors: collectors, sourceLayouts: sourceLayouts, gridContainerColumns: gridContainerColumns,
		positionalParentByAnchor: positionalParentByAnchor, positionalAnchorByWebAnchor: positionalAnchorByWebAnchor,
		mobileTypesByName: mobileTypesByName, mobileNodesByName: mobileNodesByName, webBaselineNodes: webBaselineNodes,
		webBaselineResources: webBaselineResources, scopeContainerNames: collectScopeContainerNames(rules),
		contentContainerTypes: contentContainerTypesOf(rules), actionComponentProperties: actionComponentProperties,
		actionTargets: actionTargets, declaredTypesByName: declaredTypes, mobileRequestTypes: mobileRequestTypes,
	}
	emitDeclaredElements(ctx, declared)
	walkElements(ctx, tree, nil, itemsPropertyName, nil, false, nil)
	return ctx.out
}

type positionalPlace struct {
	parent string
	index  *int
	anchor *string
}

func (ctx *walkContext) add(entry *mapEntry) { ctx.out = append(ctx.out, entry) }

func appendAncestor(ancestors []string, name *string) []string {
	if name == nil || *name == "" {
		if ancestors == nil {
			return []string{}
		}
		return ancestors
	}
	result := make([]string, 0, len(ancestors)+1)
	result = append(result, ancestors...)
	return append(result, *name)
}

func walkElements(ctx *walkContext, nodes *jn, mobileParentName *string, parentPropertyName string,
	sourceAncestors []string, inScope bool, hostableParentName *string) {
	positional := resolvePositionalSiblings(ctx, nodes)
	for _, node := range nodes.arr {
		if !node.isObj() {
			continue
		}
		name := node.get("name").tstrp()
		typ := node.get("type").tstrp()
		items := node.get("items")
		if !items.isArr() {
			items = nil
		}
		if name == nil || *name == "" {
			if items != nil {
				walkElements(ctx, items, mobileParentName, itemsPropertyName, sourceAncestors, inScope, hostableParentName)
			}
			continue
		}

		if !inScope && ctx.scopeContainerNames.contains(*name) {
			ctx.add(drop(name, typ, reason(DropNonConvertingScope)))
			scopeAncestors := appendAncestor(sourceAncestors, name)
			if items != nil {
				walkElements(ctx, items, mobileParentName, itemsPropertyName, scopeAncestors, true, hostableParentName)
			}
			recurseChildArrays(ctx, node, name, typ, scopeAncestors, true)
			continue
		}

		if inScope {
			walkScoped(ctx, node, name, typ, items, mobileParentName, sourceAncestors, hostableParentName)
			continue
		}

		place, isPositional := positional[*name]
		isContainer := (items != nil && len(items.arr) > 0) || isLayoutContainer(typ, name, nil, ctx.mobileByType)
		captureSource(ctx, *name, node)

		if isActionOnlyType(ctx.actionComponentProperties, typ) && !holdsChildComponents(node) {
			if unsupported := unsupportedRequestOf(ctx, node); unsupported != nil {
				ctx.add(drop(name, typ, unsupportedRequestDropReason(ctx.requestMap, *unsupported, nil)))
				continue
			}
		}

		if twinMobileName, ok := ctx.containerMap.get(*name); ok {
			declaredTwinType, twinIsDeclared := ctx.declaredTypesByName.get(twinMobileName)
			var mobileType *string
			if probed, ok := ctx.mobileTypesByName.get(twinMobileName); ok && probed != nil && *probed != "" {
				mobileType = probed
			} else if twinIsDeclared && declaredTwinType != nil && *declaredTwinType != "" {
				mobileType = declaredTwinType
			} else if ctx.mobileTypes.contains(deref(typ)) {
				mobileType = typ
			}
			twin := &mapEntry{WebName: name, WebType: nzp(typ), Operation: OperationMerge, Name: strp(twinMobileName),
				MobileType: mobileType, MergeParentName: strp(resolveParent(ctx, mobileParentName))}
			ctx.add(twin)
			applyTemplatesToTwin(ctx, node, twin, typ, sourceAncestors)
			if items != nil {
				walkElements(ctx, items, strp(twinMobileName), itemsPropertyName, appendAncestor(sourceAncestors, name), false,
					nearestHostable(ctx, strp(twinMobileName), hostableParentName))
			}
			continue
		}

		if compRule, ok := ctx.componentMap.get(*name); ok {
			var twinMobileType *string
			if compRule.MobileType != nil && !isBlank(*compRule.MobileType) {
				twinMobileType = compRule.MobileType
			} else if probed, found := ctx.mobileTypesByName.get(deref(compRule.Mobile)); compRule.Mobile != nil &&
				*compRule.Mobile != "" && found && probed != nil && *probed != "" {
				twinMobileType = probed
			} else if ctx.mobileTypes.contains(deref(typ)) {
				twinMobileType = typ
			}
			twinValues := buildTwinMergeValues(ctx, node, compRule, twinMobileType, typ)
			componentTwin := &mapEntry{WebName: name, WebType: nzp(typ), Operation: OperationMerge, Name: compRule.Mobile,
				MobileType: twinMobileType, Values: twinValues}
			ctx.add(componentTwin)
			applyTemplatesToTwin(ctx, node, componentTwin, typ, sourceAncestors)
			if items != nil {
				walkElements(ctx, items, compRule.Mobile, itemsPropertyName, appendAncestor(sourceAncestors, name), false, nil)
			}
			continue
		}

		if !isContainer && ctx.webBaselineNodes.has(*name) {
			if autoTwinType, ok := ctx.mobileTypesByName.get(*name); ok && typ != nil && *typ != "" && autoTwinType != nil &&
				eqi(*autoTwinType, *typ) {
				delta := buildDeltaTwinMergeValues(ctx, node, name, typ, name)
				autoTwin := &mapEntry{WebName: name, WebType: nzp(typ), Operation: OperationMerge, Name: name, MobileType: typ,
					Values: delta}
				ctx.add(autoTwin)
				applyTemplatesToTwin(ctx, node, autoTwin, typ, sourceAncestors)
				continue
			}
		}

		if isContainer {
			typeSupported := typ != nil && *typ != "" && ctx.mobileTypes.contains(*typ)
			if !typeSupported {
				target := resolveParent(ctx, mobileParentName)
				if isPositional {
					target = place.parent
				}
				ctx.add(&mapEntry{WebName: name, WebType: nzp(typ), Operation: OperationRelocateChildren, ParentName: strp(target)})
				if items != nil {
					walkElements(ctx, items, strp(target), itemsPropertyName, appendAncestor(sourceAncestors, name), false,
						nearestHostable(ctx, strp(target), hostableParentName))
				}
				hoistChildArraysInto(ctx, node, strp(target), typ, appendAncestor(sourceAncestors, name))
				continue
			}
			containerCaption := resolveCaptionResource(ctx, node, *name)
			var containerParent string
			if isPositional {
				containerParent = place.parent
			} else {
				containerParent = resolveHostingParent(ctx, resolveParent(ctx, mobileParentName), hostableParentName, typ,
					parentPropertyName)
			}
			containerProperty := parentPropertyName
			var containerIndex *int
			if isPositional {
				containerIndex = place.index
			}
			retargeted := false
			if target, ok := resolveTemplatePlacement(ctx, node, typ, *name, containerParent, containerProperty, sourceAncestors); ok {
				if retargetSourceIsInheritedChrome(ctx, *name) {
					ctx.add(drop(name, typ, reason(DropInheritedChrome, rp("scope", nil), rp("targetParent", nz(target.parent)),
						rp("targetSlot", nz(target.property)))))
					if items != nil {
						walkElements(ctx, items, strp(resolveParent(ctx, mobileParentName)), itemsPropertyName,
							appendAncestor(sourceAncestors, name), false, hostableParentName)
					}
					hoistChildArraysInto(ctx, node, strp(resolveParent(ctx, mobileParentName)), typ, appendAncestor(sourceAncestors, name))
					continue
				}
				if retargetTargetMissing(ctx, target.parent) {
					ctx.add(drop(name, typ, reason(DropTargetMissing, rp("missingParent", nz(target.parent)), rp("scope", nil))))
					if items != nil {
						walkElements(ctx, items, strp(resolveParent(ctx, mobileParentName)), itemsPropertyName,
							appendAncestor(sourceAncestors, name), false, hostableParentName)
					}
					hoistChildArraysInto(ctx, node, strp(resolveParent(ctx, mobileParentName)), typ, appendAncestor(sourceAncestors, name))
					continue
				}
				parentPtr, propertyPtr := strp(target.parent), strp(target.property)
				containerValues := buildMobileValues(ctx, node, *name, typ, containerCaption, parentPtr, propertyPtr, sourceAncestors)
				ctx.add(&mapEntry{WebName: name, WebType: nzp(typ), Operation: OperationInsert, Name: name, MobileType: typ,
					ParentName: parentPtr, PropertyName: propertyPtr, CaptionResource: containerCaption, Values: containerValues})
				retargeted = true
			}
			if !retargeted {
				containerValues := buildMobileValues(ctx, node, *name, typ, containerCaption, strp(containerParent),
					strp(containerProperty), sourceAncestors)
				entry := &mapEntry{WebName: name, WebType: nzp(typ), Operation: OperationInsert, Name: name, MobileType: typ,
					ParentName: strp(containerParent), PropertyName: strp(containerProperty), Index: containerIndex,
					CaptionResource: containerCaption, Values: containerValues}
				if isPositional {
					entry.PositionalAnchor = place.anchor
				}
				ctx.add(entry)
			}
			childAncestors := appendAncestor(sourceAncestors, name)
			if items != nil {
				hostable := hostableParentName
				if canHostChildrenOfType(ctx, typ) {
					hostable = name
				}
				walkElements(ctx, items, name, itemsPropertyName, childAncestors, false, hostable)
			}
			recurseChildArrays(ctx, node, name, typ, childAncestors, false)
			continue
		}

		leafMobileType := resolveConvertedMobileType(ctx, node, sourceAncestors)
		if leafMobileType == nil || *leafMobileType == "" {
			ctx.add(drop(name, typ, reason(DropTypeNotInMobileRegistry)))
			continue
		}
		leafCaption := resolveCaptionResource(ctx, node, *name)
		var leafParent string
		if isPositional {
			leafParent = place.parent
		} else {
			leafParent = resolveHostingParent(ctx, resolveParent(ctx, mobileParentName), hostableParentName, leafMobileType,
				parentPropertyName)
		}
		leafProperty := parentPropertyName
		var leafIndex *int
		if isPositional {
			leafIndex = place.index
		}
		parentPtr, propertyPtr := strp(leafParent), strp(leafProperty)
		leafRetargeted := false
		if target, ok := resolveTemplatePlacement(ctx, node, leafMobileType, *name, leafParent, leafProperty, sourceAncestors); ok {
			if retargetSourceIsInheritedChrome(ctx, *name) {
				ctx.add(drop(name, typ, reason(DropInheritedChrome, rp("scope", nil), rp("targetParent", nz(target.parent)),
					rp("targetSlot", nz(target.property)))))
				_, nativeRequest := classifyClicked(ctx, node)
				if nativeRequest != nil {
					ctx.collectors.dropped = append(ctx.collectors.dropped, &DroppedRequest{ElementName: name,
						Binding: strp("clicked"), WebRequest: nativeRequest, Reason: []*ReasonCode{reason(DropRequestChromeNative)}})
				}
				recurseChildArrays(ctx, node, name, leafMobileType, appendAncestor(sourceAncestors, name), true)
				continue
			}
			if retargetTargetMissing(ctx, target.parent) {
				missingReason := reason(DropTargetMissing, rp("missingParent", nz(target.parent)), rp("scope", nil))
				ctx.add(drop(name, typ, missingReason))
				_, missingRequest := classifyClicked(ctx, node)
				if missingRequest != nil {
					ctx.collectors.dropped = append(ctx.collectors.dropped, &DroppedRequest{ElementName: name,
						Binding: strp("clicked"), WebRequest: missingRequest, Reason: []*ReasonCode{missingReason}})
				}
				recurseChildArrays(ctx, node, name, leafMobileType, appendAncestor(sourceAncestors, name), true)
				continue
			}
			parentPtr, propertyPtr = strp(target.parent), strp(target.property)
			leafIndex = nil
			leafRetargeted = true
		}
		leafValues := buildMobileValues(ctx, node, *name, leafMobileType, leafCaption, parentPtr, propertyPtr, sourceAncestors)
		entry := &mapEntry{WebName: name, WebType: nzp(typ), Operation: OperationInsert, Name: name, MobileType: leafMobileType,
			ParentName: parentPtr, PropertyName: propertyPtr, Index: leafIndex, CaptionResource: leafCaption, Values: leafValues}
		if isPositional && !leafRetargeted {
			entry.PositionalAnchor = place.anchor
		}
		ctx.add(entry)
		recurseChildArrays(ctx, node, name, leafMobileType, appendAncestor(sourceAncestors, name), leafRetargeted)
	}
}
