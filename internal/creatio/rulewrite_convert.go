package creatio

import (
	"encoding/json"
	"strings"
)

// Simple-to-metadata conversion (clio's SimpleToFullBusinessRuleConverter). The trees are written in the
// order System.Text.Json writes clio's DTOs: a derived DTO's own properties first, then the base ones
// (typeName, uId, enabled); expressions follow their explicit JsonPropertyOrder, which the platform needs
// because it parses "value" by the already-read "dataValueTypeName".

type ruleWriteExpressionFields struct {
	typeName, uid, kind string
	dataValueType       *string
	referenceSchema     *string
	path                *string
	scopeID             *string
	value               any // nil: omitted
	sysValueName        *string
	sysSettingName      *string
	parameterMappings   []any
	expressionSchema    *orderedObject
	filterExpression    *string
}

func ruleWriteExpressionObject(fields ruleWriteExpressionFields) *orderedObject {
	object := ruleWriteObject(
		"typeName", fields.typeName, "uId", fields.uid, "type", fields.kind,
		"dataValueTypeName", fields.dataValueType, "referenceSchemaName", fields.referenceSchema,
		"path", fields.path, "scopeId", fields.scopeID, "value", fields.value,
		"sysValueName", fields.sysValueName, "sysSettingName", fields.sysSettingName)
	if fields.parameterMappings != nil {
		object.set("parameterMappings", fields.parameterMappings)
	}
	if fields.expressionSchema != nil {
		object.set("expressionSchema", fields.expressionSchema)
	}
	if fields.filterExpression != nil {
		object.set("filterExpression", *fields.filterExpression)
	}
	return object
}

// ruleWriteExisting is ExistingRuleIdentity: the uIds an update keeps from the rule it replaces.
type ruleWriteExisting struct {
	ruleUID      string
	caseUID      *string
	groupUID     *string
	unconsumedTr []ruleWriteExistingTrigger
}

type ruleWriteExistingTrigger struct {
	name    string
	kind    int
	scopeID string
	uid     string
}

func ruleWriteReadExisting(rule *orderedObject) *ruleWriteExisting {
	uid, ok := jsonString(rule, "uId")
	if !ok {
		ruleWriteFail("Existing business rule has no uId.")
	}
	identity := &ruleWriteExisting{ruleUID: uid}
	if cases, ok := jsonArrayAt(rule, "cases"); ok && len(cases) == 1 {
		if existingCase, ok := cases[0].(*orderedObject); ok {
			caseUID, _ := jsonString(existingCase, "uId")
			groupUID := ""
			if condition := jsonObjectAt(existingCase, "condition"); condition != nil {
				if typeName, _ := jsonString(condition, "typeName"); typeName == brGroupConditionTypeName {
					groupUID, _ = jsonString(condition, "uId")
				}
			}
			if strings.TrimSpace(caseUID) != "" {
				identity.caseUID = &caseUID
			}
			if strings.TrimSpace(groupUID) != "" {
				identity.groupUID = &groupUID
			}
		}
	}
	if triggers, ok := jsonArrayAt(rule, "triggers"); ok {
		for _, node := range triggers {
			trigger, ok := node.(*orderedObject)
			if !ok {
				continue
			}
			name, _ := jsonString(trigger, "name")
			scopeID, _ := jsonString(trigger, "scopeId")
			triggerUID, _ := jsonString(trigger, "uId")
			if strings.TrimSpace(triggerUID) == "" {
				continue
			}
			identity.unconsumedTr = append(identity.unconsumedTr, ruleWriteExistingTrigger{
				name: name, kind: jsonInt(trigger, "type", ruleWriteChangeTrigger), scopeID: scopeID, uid: triggerUID,
			})
		}
	}
	return identity
}

// ruleWriteTrigger is a trigger before it is written; uid is replaced by an existing trigger's uId on update.
type ruleWriteTrigger struct {
	uid     string
	name    string
	kind    int
	scopeID *string
}

func (t ruleWriteTrigger) object() *orderedObject {
	return ruleWriteObject("typeName", ruleWriteTriggerTypeName, "uId", t.uid, "name", t.name, "type", t.kind, "scopeId", t.scopeID)
}

func (e *ruleWriteExisting) applyTriggers(triggers []ruleWriteTrigger) {
	if e == nil {
		return
	}
	for index := range triggers {
		trigger := &triggers[index]
		for candidate, existing := range e.unconsumedTr {
			if existing.kind == trigger.kind && strings.EqualFold(existing.name, trigger.name) &&
				strings.EqualFold(existing.scopeID, ruleWriteText(trigger.scopeID)) {
				trigger.uid = existing.uid
				e.unconsumedTr = append(e.unconsumedTr[:candidate], e.unconsumedTr[candidate+1:]...)
				break
			}
		}
	}
}

func ruleWriteTriggerObjects(triggers []ruleWriteTrigger) []any {
	result := make([]any, len(triggers))
	for index, trigger := range triggers {
		result[index] = trigger.object()
	}
	return result
}

func (e *ruleWriteExisting) ruleUIDOrNew() string {
	if e == nil {
		return newGUID()
	}
	return e.ruleUID
}

func (e *ruleWriteExisting) caseUIDOrNew() string {
	if e == nil || e.caseUID == nil {
		return newGUID()
	}
	return *e.caseUID
}

func (e *ruleWriteExisting) groupUIDOrNew() string {
	if e == nil || e.groupUID == nil {
		return newGUID()
	}
	return *e.groupUID
}

// ruleWriteBlockUID is ResolveBlockUId: a caller uId must be a GUID and is written in lower-case D form.
func ruleWriteBlockUID(requested *string) string {
	if ruleWriteBlank(requested) {
		return newGUID()
	}
	parsed := ruleWriteParseGUID(*requested)
	if parsed == "" {
		ruleWriteFail("Block uId '%s' is not a valid GUID.", *requested)
	}
	return parsed
}

// ruleWriteRuleName is ResolveRuleName: the trimmed caller name or "BusinessRule_" plus seven hex digits.
func ruleWriteRuleName(rule *RuleWriteRule) string {
	if !ruleWriteBlank(rule.Name) {
		return strings.TrimSpace(*rule.Name)
	}
	return ("BusinessRule_" + strings.ReplaceAll(newGUID(), "-", ""))[:20]
}

func ruleWriteEnabled(rule *RuleWriteRule) bool {
	if rule.Enabled == nil {
		return true
	}
	return *rule.Enabled
}

// ruleWriteMetadataRule is BusinessRuleMetadataDto.
type ruleWriteMetadataRule struct {
	uid, name, caption string
	object             *orderedObject
}

func ruleWriteRuleObject(uid string, cases []any, triggers []any, name string, enabled bool, caption string, parentUID, parentActionUID *string) ruleWriteMetadataRule {
	object := ruleWriteObject(
		"typeName", ruleWriteRuleTypeName, "uId", uid, "cases", cases, "triggers", triggers, "name", name,
		"enabled", enabled, "caption", caption, "parentUId", parentUID, "parentActionUId", parentActionUID)
	return ruleWriteMetadataRule{uid: uid, name: name, caption: caption, object: object}
}

func ruleWriteCaseObject(uid string, condition *orderedObject, actions []any) *orderedObject {
	return ruleWriteObject("typeName", ruleWriteCaseTypeName, "uId", uid, "condition", condition, "actions", actions)
}

// toEntityMetadata is ToEntityMetadata; existing is the rule an update replaces (nil on create).
func (s *ruleWriteScope) toEntityMetadata(rule *RuleWriteRule, existing *orderedObject) []ruleWriteMetadataRule {
	var identity *ruleWriteExisting
	if existing != nil {
		identity = ruleWriteReadExisting(existing)
	}
	if len(rule.Actions) == 1 && rule.Actions[0].Type == "apply-filter" {
		return s.applyFilterRules(rule, rule.Actions[0], identity)
	}
	if len(rule.Actions) == 1 && rule.Actions[0].Type == "apply-static-filter" {
		return []ruleWriteMetadataRule{s.staticFilterRule(rule, rule.Actions[0], identity)}
	}
	return []ruleWriteMetadataRule{s.toMetadata(rule, true, identity)}
}

func (s *ruleWriteScope) toPageMetadata(rule *RuleWriteRule, existing *orderedObject) ruleWriteMetadataRule {
	var identity *ruleWriteExisting
	if existing != nil {
		identity = ruleWriteReadExisting(existing)
	}
	return s.toMetadata(rule, false, identity)
}

func (s *ruleWriteScope) toMetadata(rule *RuleWriteRule, includeReference bool, identity *ruleWriteExisting) ruleWriteMetadataRule {
	ruleUID := identity.ruleUIDOrNew()
	caseObject := s.buildCase(rule, includeReference, identity)
	triggers := s.buildTriggers(rule, identity)
	return ruleWriteRuleObject(ruleUID, []any{caseObject}, ruleWriteTriggerObjects(triggers), ruleWriteRuleName(rule),
		ruleWriteEnabled(rule), strings.TrimSpace(ruleWriteText(rule.Caption)), nil, nil)
}

func (s *ruleWriteScope) buildCase(rule *RuleWriteRule, includeReference bool, identity *ruleWriteExisting) *orderedObject {
	caseUID := identity.caseUIDOrNew()
	condition := s.buildConditionGroup(rule.Condition, includeReference, identity)
	actions := []any{}
	for _, action := range rule.Actions {
		actions = append(actions, s.buildAction(action, includeReference))
	}
	return ruleWriteCaseObject(caseUID, condition, actions)
}

func (s *ruleWriteScope) buildConditionGroup(group *ruleWriteGroup, includeReference bool, identity *ruleWriteExisting) *orderedObject {
	uid := identity.groupUIDOrNew()
	logical := 1
	if strings.EqualFold(ruleWriteText(group.LogicalOperation), "OR") {
		logical = 2
	}
	conditions := []any{}
	for _, condition := range group.Conditions {
		conditions = append(conditions, s.buildCondition(condition, includeReference))
	}
	return ruleWriteObject("logicalOperation", logical, "conditions", conditions, "typeName", brGroupConditionTypeName, "uId", uid)
}

func (s *ruleWriteScope) operandType(expression *ruleWriteExpression) *ruleWriteOperandType {
	kind := ruleWriteText(expression.Type)
	switch {
	case strings.EqualFold(kind, "AttributeValue"):
		attribute := ruleWriteMustGet(s.attributes, ruleWriteText(expression.Path))
		typed := attribute.operand()
		return &typed
	case strings.EqualFold(kind, "SysValue"):
		if variable, ok := ruleWriteSystemVariableByName(ruleWriteText(expression.SysValueName)); ok {
			return &ruleWriteOperandType{dataValueType: variable.dataValueType, referenceSchema: ruleWriteOptional(variable.referenceSchema)}
		}
	case strings.EqualFold(kind, "SysSetting"):
		if setting, ok := s.sysSettings[ruleWriteText(expression.SysSettingName)]; ok {
			return &ruleWriteOperandType{dataValueType: setting.dataValueType, referenceSchema: setting.referenceSchema}
		}
	}
	return nil
}

func (s *ruleWriteScope) buildCondition(condition *ruleWriteCondition, includeReference bool) *orderedObject {
	hasRight := !ruleWriteIsUnary(ruleWriteText(condition.ComparisonType))
	leftType := s.operandType(condition.Left)
	var rightType *ruleWriteOperandType
	if hasRight {
		rightType = s.operandType(condition.Right)
	}
	fallback := ruleWriteTextOperand
	if leftType != nil {
		fallback = leftType.asValueType()
	} else if rightType != nil {
		fallback = rightType.asValueType()
	}
	uid := ruleWriteBlockUID(condition.UID)
	comparison, ok := ruleWriteComparison(ruleWriteText(condition.ComparisonType))
	if !ok {
		ruleWriteFail("Unsupported business-rule comparisonType '%s'.", ruleWriteText(condition.ComparisonType))
	}
	leftConst := fallback
	if rightType != nil {
		leftConst = rightType.asValueType()
	}
	left := s.buildOperand(condition.Left, leftConst, includeReference)
	var right *orderedObject
	if hasRight {
		rightConst := fallback
		if leftType != nil {
			rightConst = leftType.asValueType()
		}
		right = s.buildOperand(condition.Right, rightConst, includeReference)
	}
	return ruleWriteObject("leftExpression", left, "rightExpression", right, "comparisonType", comparison,
		"typeName", brConditionTypeName, "uId", uid)
}

func (s *ruleWriteScope) buildOperand(expression *ruleWriteExpression, constType ruleWriteOperandType, includeReference bool) *orderedObject {
	kind := ruleWriteText(expression.Type)
	switch {
	case strings.EqualFold(kind, "AttributeValue"):
		path := ruleWriteText(expression.Path)
		attribute := ruleWriteMustGet(s.attributes, path)
		return ruleWriteAttributeExpression(attribute, path, attribute.DataValueType, includeReference, expression.UID)
	case strings.EqualFold(kind, "SysSetting"):
		setting, ok := s.sysSettings[ruleWriteText(expression.SysSettingName)]
		if !ok {
			ruleWriteFail("System setting '%s' was not resolved for the business rule.", ruleWriteText(expression.SysSettingName))
		}
		return ruleWriteExpressionObject(ruleWriteExpressionFields{
			typeName: brSysSettingExpression, uid: ruleWriteBlockUID(expression.UID), kind: "SysSetting",
			dataValueType: ruleWriteOptional(setting.dataValueType), referenceSchema: setting.referenceSchema,
			sysSettingName: expression.SysSettingName,
		})
	case strings.EqualFold(kind, "SysValue"):
		valueType := ruleWriteTextOperand
		if typed := s.operandType(expression); typed != nil {
			valueType = *typed
		}
		name := expression.SysValueName
		if variable, ok := ruleWriteSystemVariableByName(ruleWriteText(name)); ok {
			name = &variable.name
		}
		return ruleWriteExpressionObject(ruleWriteExpressionFields{
			typeName: brSysValueExpression, uid: ruleWriteBlockUID(expression.UID), kind: "SysValue",
			dataValueType: ruleWriteOptional(valueType.dataValueType), referenceSchema: valueType.referenceSchema, sysValueName: name,
		})
	}
	value := ruleWriteConvertValue(expression.Value, constType.dataValueType)
	return ruleWriteExpressionObject(ruleWriteExpressionFields{
		typeName: brValueExpression, uid: ruleWriteBlockUID(expression.UID), kind: "Const",
		dataValueType: ruleWriteOptional(constType.dataValueType), referenceSchema: constType.referenceSchema, value: value,
	})
}

func ruleWriteAttributeExpression(attribute ruleWriteAttribute, path, dataValueType string, includeReference bool, requested *string) *orderedObject {
	var reference *string
	if includeReference {
		reference = attribute.ReferenceSchema
	}
	written := path
	if attribute.scoped() {
		written = attribute.Path
	}
	return ruleWriteExpressionObject(ruleWriteExpressionFields{
		typeName: brAttributeExpression, uid: ruleWriteBlockUID(requested), kind: "AttributeValue",
		dataValueType: ruleWriteOptional(dataValueType), referenceSchema: reference, path: &written,
		scopeID: ruleWriteOptional(attribute.Scope),
	})
}

func ruleWriteMinimalAttributeExpression(path string) *orderedObject {
	return ruleWriteExpressionObject(ruleWriteExpressionFields{typeName: brAttributeExpression, uid: newGUID(), kind: "AttributeValue", path: &path})
}

// ruleWriteConvertValue is ConvertJsonElement: date/time constants become UTC instants, numeric ones keep
// the number, and anything else is written as the caller sent it.
func ruleWriteConvertValue(value any, dataValueType string) any {
	if dataValueType != "" && ruleWriteIsDateTime(dataValueType) {
		if converted, ok := ruleWriteDateTimeConstant(value, dataValueType); ok {
			return converted
		}
	}
	if dataValueType != "" && ruleWriteIsNumeric(dataValueType) {
		number, ok := value.(float64)
		if !ok {
			encoded, _ := json.Marshal(value)
			ruleWriteFail("Numeric constant '%s' is not supported for data value type '%s'.", ruleWriteElementText(value, encoded), dataValueType)
		}
		return json.Number(ruleWriteNumberText(number))
	}
	return ruleWriteToOrdered(value)
}

// ruleWriteElementText is JsonElement.ToString(): a string's text, otherwise the raw JSON.
func ruleWriteElementText(value any, encoded []byte) string {
	if text, ok := value.(string); ok {
		return text
	}
	return string(encoded)
}

func (s *ruleWriteScope) buildAction(action *ruleWriteAction, includeReference bool) *orderedObject {
	if strings.EqualFold(action.Type, "set-values") {
		items := []any{}
		for _, item := range action.setValueItems() {
			items = append(items, s.buildSetValueItem(item, includeReference))
		}
		return ruleWriteObject("items", items, "typeName", ruleWriteSetValuesAction, "uId", ruleWriteBlockUID(action.UID), "enabled", true)
	}
	typeName, ok := ruleWriteActionTypeNames[action.Type]
	if !ok {
		typeName, ok = ruleWritePageActionTypeNames[action.Type]
	}
	if !ok {
		ruleWriteFail("Unsupported business-rule action type '%s'.", action.Type)
	}
	uid := ruleWriteBlockUID(action.UID)
	return ruleWriteObject("items", strings.Join(action.fieldSelectionItems(), ","), "typeName", typeName, "uId", uid, "enabled", true)
}

func (s *ruleWriteScope) buildSetValueItem(item *ruleWriteSetItem, includeReference bool) *orderedObject {
	targetPath := ruleWriteText(item.Expression.Path)
	target := ruleWriteMustGet(s.attributes, targetPath)
	var value *orderedObject
	switch {
	case ruleWriteIsFormula(item.Value):
		if strings.TrimSpace(s.entitySchema) == "" {
			ruleWriteFail("Formula set-values items are only supported for entity business rules.")
		}
		value = ruleWriteFormulaValueExpression(s.entitySchema, s.attributes, targetPath, ruleWriteFormulaText(item.Value), target.DataValueType)
	case strings.EqualFold(ruleWriteText(item.Value.Type), "AttributeValue"):
		sourcePath := ruleWriteText(item.Value.Path)
		source := ruleWriteMustGet(s.attributes, sourcePath)
		value = ruleWriteAttributeExpression(source, sourcePath, source.DataValueType, includeReference, item.Value.UID)
	default:
		converted := ruleWriteConvertValue(item.Value.Value, target.DataValueType)
		value = ruleWriteExpressionObject(ruleWriteExpressionFields{
			typeName: brValueExpression, uid: ruleWriteBlockUID(item.Value.UID), kind: "Const",
			dataValueType: ruleWriteOptional(target.DataValueType), referenceSchema: target.ReferenceSchema, value: converted,
		})
	}
	uid := ruleWriteBlockUID(item.UID)
	expression := ruleWriteAttributeExpression(target, targetPath, target.DataValueType, includeReference, item.Expression.UID)
	return ruleWriteObject("expression", expression, "value", value, "typeName", ruleWriteSetValueItem, "uId", uid, "enabled", true)
}

// Triggers (BuildTriggers): attribute operands of the conditions, formula sources and attribute set-value
// sources raise a change trigger; scoped attributes add a DataLoaded trigger per scope.
func (s *ruleWriteScope) buildTriggers(rule *RuleWriteRule, identity *ruleWriteExisting) []ruleWriteTrigger {
	names := []string{}
	for _, condition := range rule.Condition.Conditions {
		names = append(names, ruleWriteConditionTriggerNames(condition)...)
	}
	if strings.TrimSpace(s.entitySchema) != "" {
		for _, action := range rule.Actions {
			if !strings.EqualFold(action.Type, "set-values") {
				continue
			}
			for _, item := range action.setValueItems() {
				if ruleWriteIsFormula(item.Value) {
					names = append(names, ruleWriteFormulaSources(ruleWriteFormulaText(item.Value), s.attributes)...)
				}
			}
		}
	}
	for _, action := range rule.Actions {
		if !strings.EqualFold(action.Type, "set-values") {
			continue
		}
		for _, item := range action.setValueItems() {
			if item.Value != nil && strings.EqualFold(ruleWriteText(item.Value.Type), "AttributeValue") && !ruleWriteBlank(item.Value.Path) {
				path := *item.Value.Path
				if separator := strings.Index(path, "."); separator > 0 {
					path = path[:separator]
				}
				names = append(names, path)
			}
		}
	}
	scopes := []string{}
	unscoped := false
	triggers := []ruleWriteTrigger{}
	for _, name := range ruleWriteDistinctFold(names) {
		emitted := name
		var scope *string
		if attribute, ok := s.attributes.lookup(name); ok && attribute.scoped() {
			emitted = attribute.Path
			scopeName := attribute.Scope
			scope = &scopeName
		}
		if scope == nil {
			unscoped = true
		} else {
			scopes = append(scopes, *scope)
		}
		triggers = append(triggers, ruleWriteTrigger{uid: newGUID(), name: emitted, kind: ruleWriteChangeTrigger, scopeID: scope})
	}
	empty := ""
	scopes = ruleWriteDistinctFold(scopes)
	for _, scope := range scopes {
		triggers = append(triggers, ruleWriteTrigger{uid: newGUID(), name: scope, kind: ruleWriteDataLoadedTrigger, scopeID: &empty})
	}
	if unscoped || len(scopes) == 0 {
		triggers = append(triggers, ruleWriteTrigger{uid: newGUID(), name: "", kind: ruleWriteDataLoadedTrigger})
	}
	identity.applyTriggers(triggers)
	return triggers
}

func ruleWriteConditionTriggerNames(condition *ruleWriteCondition) []string {
	names := []string{}
	isAttribute := func(expression *ruleWriteExpression) bool {
		return expression != nil && strings.EqualFold(ruleWriteText(expression.Type), "AttributeValue") && !ruleWriteBlank(expression.Path)
	}
	if isAttribute(condition.Left) {
		names = append(names, *condition.Left.Path)
	}
	if isAttribute(condition.Right) {
		names = append(names, *condition.Right.Path)
	}
	return names
}

func ruleWriteChangeTriggers(names []string) []ruleWriteTrigger {
	triggers := []ruleWriteTrigger{}
	for _, name := range ruleWriteDistinctFold(names) {
		triggers = append(triggers, ruleWriteTrigger{uid: newGUID(), name: name, kind: ruleWriteChangeTrigger})
	}
	return append(triggers, ruleWriteTrigger{uid: newGUID(), name: "", kind: ruleWriteDataLoadedTrigger})
}

// apply-filter: the parent rule plus the optional ClearValue / PopulateValue child rules.
func (s *ruleWriteScope) applyFilterRules(rule *RuleWriteRule, action *ruleWriteAction, identity *ruleWriteExisting) []ruleWriteMetadataRule {
	targetFilterPath := strings.TrimSpace(action.TargetFilterPath)
	var sourceFilterPath *string
	if !ruleWriteBlank(action.SourceFilterPath) {
		trimmed := strings.TrimSpace(*action.SourceFilterPath)
		sourceFilterPath = &trimmed
	}
	parentUID := identity.ruleUIDOrNew()
	parentActionUID := ruleWriteBlockUID(action.UID)
	target := ruleWriteMustGet(s.attributes, action.Target)
	source := ruleWriteMustGet(s.attributes, action.Source)
	caseUID := identity.caseUIDOrNew()
	condition := s.buildConditionGroup(rule.Condition, true, identity)
	filterLookup := func(attribute ruleWriteAttribute, path string, filter *string) *orderedObject {
		expression := "null"
		if !ruleWriteBlank(filter) {
			expression = *filter
		}
		return ruleWriteExpressionObject(ruleWriteExpressionFields{
			typeName: ruleWriteFilterLookupExpression, uid: newGUID(), kind: "Const", dataValueType: ruleWriteOptional("Lookup"),
			referenceSchema: attribute.ReferenceSchema, path: &path, filterExpression: &expression,
		})
	}
	filterAction := ruleWriteObject(
		"leftExpression", filterLookup(target, action.Target, &targetFilterPath),
		"rightExpression", filterLookup(source, action.Source, sourceFilterPath),
		"clearValue", action.ClearValue, "populateValue", action.PopulateValue,
		"typeName", ruleWriteFilterLookupAction, "uId", parentActionUID, "enabled", true)
	names := []string{}
	for _, item := range rule.Condition.Conditions {
		names = append(names, ruleWriteConditionTriggerNames(item)...)
	}
	names = append(names, action.Source)
	triggers := ruleWriteChangeTriggers(names)
	identity.applyTriggers(triggers)
	parent := ruleWriteRuleObject(parentUID, []any{ruleWriteCaseObject(caseUID, condition, []any{filterAction})},
		ruleWriteTriggerObjects(triggers), ruleWriteRuleName(rule), ruleWriteEnabled(rule), strings.TrimSpace(ruleWriteText(rule.Caption)), nil, nil)
	rules := []ruleWriteMetadataRule{parent}
	targetRelated := action.Target + "." + targetFilterPath
	if action.ClearValue {
		comparisonPath := action.Source
		if sourceFilterPath != nil {
			comparisonPath = action.Source + "." + *sourceFilterPath
		}
		childCondition := ruleWriteObject(
			"leftExpression", ruleWriteMinimalAttributeExpression(comparisonPath),
			"rightExpression", ruleWriteMinimalAttributeExpression(targetRelated),
			"comparisonType", 3, "typeName", brConditionTypeName, "uId", newGUID())
		ruleUID, caseID, actionUID, itemUID := newGUID(), newGUID(), newGUID(), newGUID()
		emptyValue := ruleWriteExpressionObject(ruleWriteExpressionFields{typeName: brEmptyValueExpression, uid: newGUID(), kind: "Const", dataValueType: ruleWriteOptional("Lookup"), value: emptyGUID})
		setItem := ruleWriteObject("expression", ruleWriteAttributeExpression(target, action.Target, target.DataValueType, true, nil),
			"value", emptyValue, "typeName", ruleWriteSetValueItem, "uId", itemUID, "enabled", true)
		setAction := ruleWriteObject("items", []any{setItem}, "typeName", ruleWriteSetValuesAction, "uId", actionUID, "enabled", true)
		rules = append(rules, ruleWriteRuleObject(ruleUID, []any{ruleWriteCaseObject(caseID, childCondition, []any{setAction})},
			ruleWriteTriggerObjects([]ruleWriteTrigger{{uid: newGUID(), name: action.Source, kind: ruleWriteChangeTrigger}}),
			"Autogenerated_"+parentUID+"_ClearValue", true, "ChildRule-"+parentUID+"-ClearValue", &parentUID, &parentActionUID))
	}
	if action.PopulateValue {
		childCondition := ruleWriteObject(
			"leftExpression", ruleWriteMinimalAttributeExpression(action.Target),
			"comparisonType", 1, "typeName", brConditionTypeName, "uId", newGUID())
		ruleUID, caseID, actionUID, itemUID := newGUID(), newGUID(), newGUID(), newGUID()
		related := ruleWriteMustGet(s.attributes, targetRelated)
		setItem := ruleWriteObject("expression", ruleWriteAttributeExpression(source, action.Source, source.DataValueType, true, nil),
			"value", ruleWriteAttributeExpression(related, targetRelated, related.DataValueType, true, nil),
			"typeName", ruleWriteSetValueItem, "uId", itemUID, "enabled", true)
		setAction := ruleWriteObject("items", []any{setItem}, "typeName", ruleWriteSetValuesAction, "uId", actionUID, "enabled", true)
		rules = append(rules, ruleWriteRuleObject(ruleUID, []any{ruleWriteCaseObject(caseID, childCondition, []any{setAction})},
			ruleWriteTriggerObjects([]ruleWriteTrigger{{uid: newGUID(), name: action.Target, kind: ruleWriteChangeTrigger}}),
			"Autogenerated_"+parentUID+"_PopulateValue", true, "ChildRule-"+parentUID+"-PopulateValue", &parentUID, &parentActionUID))
	}
	return rules
}

// apply-static-filter: a SetFilter action whose value is the compiled ESQ envelope.
func (s *ruleWriteScope) staticFilterRule(rule *RuleWriteRule, action *ruleWriteAction, identity *ruleWriteExisting) ruleWriteMetadataRule {
	target := ruleWriteMustGet(s.attributes, action.TargetAttribute)
	root := ruleWriteText(target.ReferenceSchema)
	group := ruleWriteDeserializeFilter(action.Filter)
	envelope := s.buildFilterEnvelope(group, root)
	targetPath := action.TargetAttribute
	setFilter := ruleWriteObject(
		"expression", ruleWriteExpressionObject(ruleWriteExpressionFields{typeName: brAttributeExpression, uid: newGUID(), kind: "AttributeValue", path: &targetPath}),
		"value", ruleWriteExpressionObject(ruleWriteExpressionFields{typeName: brValueExpression, uid: newGUID(), kind: "Const", value: envelope}),
		"typeName", ruleWriteSetFilterAction, "uId", ruleWriteBlockUID(action.UID), "enabled", true)
	caseUID := identity.caseUIDOrNew()
	condition := s.buildConditionGroup(rule.Condition, true, identity)
	names := []string{}
	for _, item := range rule.Condition.Conditions {
		names = append(names, ruleWriteConditionTriggerNames(item)...)
	}
	triggers := ruleWriteChangeTriggers(names)
	identity.applyTriggers(triggers)
	return ruleWriteRuleObject(identity.ruleUIDOrNew(), []any{ruleWriteCaseObject(caseUID, condition, []any{setFilter})},
		ruleWriteTriggerObjects(triggers), ruleWriteRuleName(rule), ruleWriteEnabled(rule), strings.TrimSpace(ruleWriteText(rule.Caption)), nil, nil)
}
