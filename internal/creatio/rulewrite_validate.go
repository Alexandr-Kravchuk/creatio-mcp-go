package creatio

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ruleWriteScope carries what validating and converting one batch needs: the attribute map, the schema
// cache (static filters reuse it), the page element names, and the remote lookups clio makes per rule.
type ruleWriteScope struct {
	ctx           context.Context
	client        *Client
	page          bool
	entitySchema  string // root entity schema name (entity rules); "" for page rules
	attributes    ruleWriteAttributes
	elements      map[string]bool
	cache         *ruleWriteSchemaCache
	lookupIDs     map[[2]string]string
	lookupNames   map[[2]string]*string
	now           func() time.Time
	sysSettings   map[string]ruleWriteSysSetting
	staticFilters bool
}

type ruleWriteSysSetting struct {
	name            string
	dataValueType   string
	referenceSchema *string
}

// System-setting operands (clio's SysSettingConditionOperandResolver).

func (s *ruleWriteScope) resolveSysSettings(rule *RuleWriteRule) {
	s.sysSettings = map[string]ruleWriteSysSetting{}
	if rule.Condition == nil {
		return
	}
	for _, condition := range rule.Condition.Conditions {
		if condition == nil {
			continue
		}
		s.collectSysSetting(condition.Left)
		s.collectSysSetting(condition.Right)
	}
}

func (s *ruleWriteScope) collectSysSetting(expression *ruleWriteExpression) {
	if expression == nil || !strings.EqualFold(ruleWriteText(expression.Type), "SysSetting") {
		return
	}
	code := ruleWriteText(expression.SysSettingName)
	if strings.TrimSpace(code) == "" {
		return
	}
	if _, done := s.sysSettings[code]; done {
		return
	}
	valueType, reference, found := s.sysSettingType(code)
	if !found {
		ruleWriteFail("System setting '%s' referenced in rule.condition.conditions[*] does not exist on the target environment.", code)
	}
	normalized := valueType
	switch valueType {
	case "Decimal":
		normalized = "Float"
	case "Currency":
		normalized = "Money"
	}
	if strings.EqualFold(normalized, "SecureText") {
		ruleWriteFail("System setting '%s' is a SecureText setting and cannot be used as a business-rule condition operand.", code)
	}
	if strings.EqualFold(normalized, "Binary") {
		ruleWriteFail("System setting '%s' is a Binary setting and cannot be used as a business-rule condition operand.", code)
	}
	if normalized == "" || !ruleWriteIsFilterable(normalized) {
		ruleWriteFail("System setting '%s' has value type '%s', which is not supported as a business-rule condition operand.", code, valueType)
	}
	var referenceSchema *string
	if strings.EqualFold(normalized, "Lookup") {
		if ruleWriteBlank(reference) {
			ruleWriteFail("System setting '%s' is a Lookup setting whose reference schema could not be resolved on the target environment.", code)
		}
		referenceSchema = reference
	}
	s.sysSettings[code] = ruleWriteSysSetting{name: code, dataValueType: normalized, referenceSchema: referenceSchema}
}

// sysSettingType is clio's GetSysSettingTypeByCode: the setting's ValueTypeName and, when it has a
// reference schema, that schema's name.
func (s *ruleWriteScope) sysSettingType(code string) (string, *string, bool) {
	rows, err := s.client.selectRows(s.ctx, buildSelectQuery("SysSettings", map[string]string{
		"Id": "Id", "Code": "Code", "ValueTypeName": "ValueTypeName", "ReferenceSchemaUId": "ReferenceSchemaUId",
	}, map[string]any{"Code": comparisonFilter("Code", code, 1, 3)}, 1))
	if err != nil {
		ruleWriteFail("%s", err.Error())
	}
	if len(rows) == 0 {
		return "", nil, false
	}
	valueType := rowString(rows[0], "ValueTypeName")
	referenceUID := normalizeGUID(rowString(rows[0], "ReferenceSchemaUId"))
	if referenceUID == "" || referenceUID == emptyGUID {
		return valueType, nil, true
	}
	schemas, err := s.client.selectRows(s.ctx, buildSelectQuery("SysSchema", map[string]string{"Name": "Name"},
		map[string]any{"UId": comparisonFilter("UId", referenceUID, 0, 3)}, 1))
	if err != nil {
		ruleWriteFail("%s", err.Error())
	}
	if len(schemas) == 0 {
		return valueType, nil, true
	}
	name := rowString(schemas[0], "Name")
	return valueType, &name, true
}

// Rule validation (clio's BusinessRuleValidator and PageBusinessRuleValidator).

type ruleWriteOperandKind int

const (
	ruleWriteOperandAttribute ruleWriteOperandKind = iota
	ruleWriteOperandConst
	ruleWriteOperandSysValue
	ruleWriteOperandSysSetting
)

type ruleWriteOperand struct {
	field      string
	kind       ruleWriteOperandKind
	label      string
	typed      *ruleWriteOperandType
	expression *ruleWriteExpression
}

func ruleWriteIsApplyFilterOnly(rule *RuleWriteRule) bool {
	return len(rule.Actions) == 1 && rule.Actions[0] != nil && strings.EqualFold(rule.Actions[0].Type, "apply-filter")
}

func ruleWriteIsStaticFilterOnly(rule *RuleWriteRule) bool {
	return len(rule.Actions) == 1 && rule.Actions[0] != nil && strings.EqualFold(rule.Actions[0].Type, "apply-static-filter")
}

func (s *ruleWriteScope) validate(rule *RuleWriteRule) {
	if !s.page {
		s.validateRule(rule)
		return
	}
	message, failed := ruleWriteCatch(func() { s.validateRule(rule) })
	if failed {
		ruleWriteFail("%s", s.pageHint(message))
	}
}

// pageHint is PageBusinessRuleValidator.AppendCandidateHint. clio applies it to ArgumentException only;
// every validator failure here is one, except a remote lookup failure surfacing as another exception type.
func (s *ruleWriteScope) pageHint(message string) string {
	result := strings.ReplaceAll(message, "Unknown attribute", "Unknown or unsupported datasource-bound page attribute")
	if strings.Contains(result, "rule.condition.conditions") {
		result += " Available condition attributes: " + ruleWriteCandidates(s.attributes.keys()) + "."
	}
	if strings.Contains(result, "rule.actions") {
		names := make([]string, 0, len(s.elements))
		for name := range s.elements {
			names = append(names, name)
		}
		result += " Available page elements: " + ruleWriteCandidates(names) + "."
	}
	return result
}

func ruleWriteCandidates(candidates []string) string {
	values := []string{}
	for _, candidate := range candidates {
		if strings.TrimSpace(candidate) != "" {
			values = append(values, candidate)
		}
	}
	sort.SliceStable(values, func(i, j int) bool {
		left, right := strings.ToUpper(values[i]), strings.ToUpper(values[j])
		if left != right {
			return left < right
		}
		return false
	})
	if len(values) > 20 {
		values = values[:20]
	}
	if len(values) == 0 {
		return "<none>"
	}
	return strings.Join(values, ", ")
}

func (s *ruleWriteScope) validateRule(rule *RuleWriteRule) {
	applyFilter := ruleWriteIsApplyFilterOnly(rule)
	staticFilter := ruleWriteIsStaticFilterOnly(rule)
	if ruleWriteBlank(rule.Caption) {
		ruleWriteFail("rule.caption is required.")
	}
	if rule.Condition == nil {
		ruleWriteFail("rule.condition is required.")
	}
	if len(rule.Actions) == 0 {
		ruleWriteFail("rule.actions must contain at least one action.")
	}
	for _, action := range rule.Actions {
		if action != nil && strings.EqualFold(action.Type, "apply-filter") && !applyFilter {
			ruleWriteFail("apply-filter rules support exactly one action and cannot be combined with other entity business-rule actions.")
		}
	}
	for _, action := range rule.Actions {
		if action != nil && strings.EqualFold(action.Type, "apply-static-filter") && !staticFilter {
			ruleWriteFail("apply-static-filter rules support exactly one action and cannot be combined with other entity business-rule actions.")
		}
	}
	logical := rule.Condition.LogicalOperation
	if ruleWriteBlank(logical) {
		ruleWriteFail("rule.condition.logicalOperation is required.")
	}
	if !strings.EqualFold(*logical, "AND") && !strings.EqualFold(*logical, "OR") {
		ruleWriteFail("Unsupported rule.condition.logicalOperation '%s'. Use AND or OR.", *logical)
	}
	if rule.Condition.Conditions == nil {
		ruleWriteFail("rule.condition.conditions is required.")
	}
	if !applyFilter && !staticFilter && len(rule.Condition.Conditions) == 0 {
		ruleWriteFail("rule.condition.conditions must contain at least one condition.")
	}
	for _, condition := range rule.Condition.Conditions {
		if condition == nil {
			ruleWriteFail("rule.condition.conditions[*] is required.")
		}
		s.validateCondition(condition)
	}
	for _, action := range rule.Actions {
		if action == nil {
			ruleWriteFail("rule.actions[*].type is required.")
		}
		if s.page {
			s.validatePageAction(action)
		} else {
			s.validateEntityAction(action)
		}
	}
	s.validateLookupReferences(rule)
}

func (s *ruleWriteScope) validateCondition(condition *ruleWriteCondition) {
	if condition.Left == nil {
		ruleWriteFail("rule.condition.conditions[*].leftExpression is required.")
	}
	comparison := ruleWriteText(condition.ComparisonType)
	if _, ok := ruleWriteComparison(comparison); !ok || strings.TrimSpace(comparison) == "" {
		ruleWriteFail("Unsupported rule.condition.conditions[*].comparisonType '%s'. Supported values: %s.", comparison, ruleWriteSupportedComparisons)
	}
	comparison = strings.TrimSpace(comparison)
	left := s.resolveOperand(condition.Left, "rule.condition.conditions[*].leftExpression")
	if ruleWriteIsUnary(comparison) {
		if condition.Right != nil {
			ruleWriteFail("rule.condition.conditions[*].rightExpression must be omitted when comparisonType is '%s'.", comparison)
		}
		return
	}
	if condition.Right == nil {
		ruleWriteFail("rule.condition.conditions[*].rightExpression is required when comparisonType is '%s'.", comparison)
	}
	right := s.resolveOperand(condition.Right, "rule.condition.conditions[*].rightExpression")
	s.validateComparison(comparison, left, right)
}

func (s *ruleWriteScope) resolveOperand(expression *ruleWriteExpression, field string) ruleWriteOperand {
	kind := ruleWriteText(expression.Type)
	switch {
	case strings.EqualFold(kind, "AttributeValue"):
		if ruleWriteBlank(expression.Path) {
			ruleWriteFail("%s.path is required when %s.type is 'AttributeValue'.", field, field)
		}
		path := *expression.Path
		if !s.isScoped(path) {
			ruleWriteDirectPath(path, field+".path")
		}
		attribute := s.resolveAttribute(path, field+".path")
		typed := attribute.operand()
		return ruleWriteOperand{field: field, kind: ruleWriteOperandAttribute, label: fmt.Sprintf("attribute '%s'", path), typed: &typed, expression: expression}
	case strings.EqualFold(kind, "SysValue"):
		if ruleWriteBlank(expression.SysValueName) {
			ruleWriteFail("%s.sysValueName is required when %s.type is 'SysValue'.", field, field)
		}
		variable, ok := ruleWriteSystemVariableByName(*expression.SysValueName)
		if !ok {
			ruleWriteFail("Unsupported %s.sysValueName '%s'. Supported values: %s.", field, *expression.SysValueName, ruleWriteSupportedSystemValues)
		}
		typed := ruleWriteOperandType{dataValueType: variable.dataValueType, referenceSchema: ruleWriteOptional(variable.referenceSchema)}
		return ruleWriteOperand{field: field, kind: ruleWriteOperandSysValue, label: fmt.Sprintf("system variable '%s'", variable.name), typed: &typed, expression: expression}
	case strings.EqualFold(kind, "SysSetting"):
		if ruleWriteBlank(expression.SysSettingName) {
			ruleWriteFail("%s.sysSettingName is required when %s.type is 'SysSetting'.", field, field)
		}
		setting, ok := s.sysSettings[*expression.SysSettingName]
		if !ok {
			ruleWriteFail("System setting '%s' in %s does not exist on the target environment.", *expression.SysSettingName, field)
		}
		typed := ruleWriteOperandType{dataValueType: setting.dataValueType, referenceSchema: setting.referenceSchema}
		return ruleWriteOperand{field: field, kind: ruleWriteOperandSysSetting, label: fmt.Sprintf("system setting '%s'", setting.name), typed: &typed, expression: expression}
	case strings.EqualFold(kind, "Const"):
		if !expression.HasValue {
			ruleWriteFail("%s.value is required when %s.type is 'Const'.", field, field)
		}
		return ruleWriteOperand{field: field, kind: ruleWriteOperandConst, label: "constant value", expression: expression}
	}
	ruleWriteFail("%s.type must be 'AttributeValue', 'Const', 'SysValue', or 'SysSetting'.", field)
	return ruleWriteOperand{}
}

func ruleWriteOptional(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func ruleWriteDescribe(typed ruleWriteOperandType) string {
	if ruleWriteBlank(typed.referenceSchema) {
		return typed.dataValueType
	}
	return typed.dataValueType + " -> " + *typed.referenceSchema
}

func (s *ruleWriteScope) validateComparison(comparison string, left, right ruleWriteOperand) {
	comparisonType := ruleWriteTextOperand
	if left.typed != nil {
		comparisonType = left.typed.asValueType()
	} else if right.typed != nil {
		comparisonType = right.typed.asValueType()
	}
	if left.typed != nil && right.typed != nil {
		leftValue, rightValue := left.typed.asValueType(), right.typed.asValueType()
		same := strings.EqualFold(leftValue.dataValueType, rightValue.dataValueType) ||
			(ruleWriteIsText(leftValue.dataValueType) && ruleWriteIsText(rightValue.dataValueType)) ||
			(ruleWriteIsNumeric(leftValue.dataValueType) && ruleWriteIsNumeric(rightValue.dataValueType))
		if !same {
			ruleWriteFail("rule.condition.conditions[*] compares %s (%s) to %s (%s). Both operands must resolve to the same data value type.",
				left.label, ruleWriteDescribe(*left.typed), right.label, ruleWriteDescribe(*right.typed))
		}
		if strings.EqualFold(leftValue.dataValueType, "Lookup") && !strings.EqualFold(ruleWriteText(leftValue.referenceSchema), ruleWriteText(rightValue.referenceSchema)) {
			ruleWriteFail("rule.condition.conditions[*] compares %s (%s) to %s (%s). Both lookup operands must reference the same schema.",
				left.label, ruleWriteText(leftValue.referenceSchema), right.label, ruleWriteText(rightValue.referenceSchema))
		}
		comparisonType = leftValue
	}
	if ruleWriteIsComparisonIn(comparison, "contain", "not-contain") {
		leftRaw := ""
		if left.typed != nil {
			leftRaw = left.typed.dataValueType
		}
		if left.typed == nil || !(strings.EqualFold(leftRaw, "ObjectList") || ruleWriteIsText(leftRaw)) {
			suffix := ""
			if left.typed != nil {
				suffix = " (" + leftRaw + ")"
			}
			ruleWriteFail("rule.condition.conditions[*].comparisonType '%s' is only supported when the left operand is a collection (ObjectList, for example CurrentUserRoles) or a text type. Left operand is %s%s.", comparison, left.label, suffix)
		}
	} else {
		if ruleWriteIsComparisonIn(comparison, "greater-than", "greater-than-or-equal", "less-than", "less-than-or-equal") && !ruleWriteIsRelational(comparisonType.dataValueType) {
			ruleWriteFail("rule.condition.conditions[*].comparisonType '%s' is only supported for numeric and date/time operands. The compared value type is %s.", comparison, comparisonType.dataValueType)
		}
		if ruleWriteIsComparisonIn(comparison, "equal", "not-equal") && ruleWriteUnsupportedForEquality(comparisonType.dataValueType) {
			ruleWriteFail("rule.condition.conditions[*].comparisonType '%s' is not supported for %s operands. RichText and Image operands do not support equal or not-equal business-rule conditions.", comparison, comparisonType.dataValueType)
		}
	}
	if left.kind == ruleWriteOperandConst {
		against := ruleWriteTextOperand
		if right.typed != nil {
			against = right.typed.asValueType()
		}
		ruleWriteValidateConstant(left.expression.Value, against, left.field)
	}
	if right.kind == ruleWriteOperandConst {
		against := ruleWriteTextOperand
		if left.typed != nil {
			against = left.typed.asValueType()
		}
		ruleWriteValidateConstant(right.expression.Value, against, right.field)
	}
}

func ruleWriteIsGUIDString(value any) bool {
	text, ok := value.(string)
	return ok && ruleWriteParseGUID(text) != ""
}

func ruleWriteValidateConstant(value any, against ruleWriteOperandType, field string) {
	name := against.dataValueType
	switch name {
	case "Lookup":
		if !ruleWriteIsGUIDString(value) {
			ruleWriteFail("%s.value must be a GUID string when compared against a Lookup operand.", field)
		}
		return
	case "Guid":
		if !ruleWriteIsGUIDString(value) {
			ruleWriteFail("%s.value must be a GUID string when compared against a Guid operand.", field)
		}
		return
	case "Boolean":
		if _, ok := value.(bool); !ok {
			ruleWriteFail("%s.value must be a JSON boolean when compared against a Boolean operand.", field)
		}
		return
	}
	if ruleWriteIsText(name) {
		if _, ok := value.(string); !ok {
			ruleWriteFail("%s.value must be a JSON string when compared against a text operand.", field)
		}
		return
	}
	if ruleWriteIsNumeric(name) {
		if _, ok := value.(float64); !ok {
			ruleWriteFail("%s.value must be a JSON number representable as Int64 or Decimal when compared against a numeric operand.", field)
		}
		return
	}
	if ruleWriteIsDateTime(name) {
		if _, ok := ruleWriteDateTimeConstant(value, name); !ok {
			ruleWriteFail("%s", ruleWriteDateTimeMessage(name))
		}
		return
	}
	ruleWriteFail("%s.value (Const) is not supported when compared against a '%s' operand.", field, name)
}

func ruleWriteDateTimeMessage(name string) string {
	switch name {
	case "Date":
		return "rule.condition.conditions[*].rightExpression.value must be a JSON string in 'yyyy-MM-dd' format when the left attribute is Date."
	case "DateTime":
		return "rule.condition.conditions[*].rightExpression.value must be a JSON string in ISO 8601 date-time format with a timezone suffix ('Z' or '+/-HH:mm') when the left attribute is DateTime."
	case "Time":
		return "rule.condition.conditions[*].rightExpression.value must be a JSON string in ISO 8601 time format with a timezone suffix ('Z' or '+/-HH:mm') when the left attribute is Time."
	}
	return "rule.condition.conditions[*].rightExpression.value must be a valid JSON string date/time constant."
}

func (s *ruleWriteScope) resolveAttribute(path, field string) ruleWriteAttribute {
	attribute, ok := s.attributes.lookup(path)
	if !ok {
		ruleWriteFail("Unknown attribute '%s' in %s.", path, field)
	}
	return attribute
}

func (s *ruleWriteScope) isScoped(path string) bool {
	attribute, ok := s.attributes.lookup(path)
	return ok && attribute.scoped()
}

func ruleWriteDirectPath(path, field string) {
	if strings.Contains(path, ".") {
		ruleWriteFail("%s must reference a direct entity attribute. Forward reference paths are supported only in rule.actions[*].items[*].value.path.", field)
	}
}

func (s *ruleWriteScope) validatePageAction(action *ruleWriteAction) {
	if action.Type == "" {
		ruleWriteFail("rule.actions[*].type is required.")
	}
	if _, ok := ruleWritePageActionTypeNames[action.Type]; !ok {
		ruleWriteFail("Unsupported rule.actions[*].type '%s'. Supported values: %s.", action.Type, ruleWriteSupportedPageActions)
	}
	items := action.fieldSelectionItems()
	if len(items) == 0 {
		ruleWriteFail("rule.actions[*].items must contain at least one page element name.")
	}
	for _, target := range items {
		if strings.TrimSpace(target) == "" {
			ruleWriteFail("rule.actions[*].items cannot contain empty page element names.")
		}
		if !s.elements[target] {
			ruleWriteFail("Unknown page element '%s' in rule.actions[*].items.", target)
		}
	}
}

func (s *ruleWriteScope) validateEntityAction(action *ruleWriteAction) {
	if action.Type == "" {
		ruleWriteFail("rule.actions[*].type is required.")
	}
	switch {
	case strings.EqualFold(action.Type, "apply-filter"):
		s.validateApplyFilter(action)
		return
	case strings.EqualFold(action.Type, "apply-static-filter"):
		s.validateStaticFilter(action)
		return
	case strings.EqualFold(action.Type, "set-values"):
		items := action.setValueItems()
		if len(items) == 0 {
			ruleWriteFail("rule.actions[*].items must contain at least one set-values item.")
		}
		for _, item := range items {
			if item == nil {
				ruleWriteFail("rule.actions[*].items[*] is required.")
			}
			s.validateSetValueItem(item)
		}
		return
	}
	if _, ok := ruleWriteActionTypeNames[action.Type]; !ok {
		ruleWriteFail("Unsupported rule.actions[*].type '%s'. Supported values: %s.", action.Type, ruleWriteSupportedActions)
	}
	items := action.fieldSelectionItems()
	if len(items) == 0 {
		ruleWriteFail("rule.actions[*].items must contain at least one attribute.")
	}
	for _, target := range items {
		if strings.TrimSpace(target) == "" {
			ruleWriteFail("rule.actions[*].items cannot contain empty attribute names.")
		}
		ruleWriteDirectPath(target, "rule.actions[*].items")
		s.resolveAttribute(target, "rule.actions[*].items")
	}
}

func (s *ruleWriteScope) validateSetValueItem(item *ruleWriteSetItem) {
	if item.Expression == nil || !strings.EqualFold(ruleWriteText(item.Expression.Type), "AttributeValue") {
		ruleWriteFail("rule.actions[*].items[*].expression.type must be 'AttributeValue'.")
	}
	if ruleWriteBlank(item.Expression.Path) {
		ruleWriteFail("rule.actions[*].items[*].expression.path is required.")
	}
	targetPath := *item.Expression.Path
	ruleWriteDirectPath(targetPath, "rule.actions[*].items[*].expression.path")
	target := s.resolveAttribute(targetPath, "rule.actions[*].items[*].expression.path")
	if item.Value == nil {
		ruleWriteFail("rule.actions[*].items[*].value is required.")
	}
	valueType := ruleWriteText(item.Value.Type)
	switch {
	case strings.EqualFold(valueType, "Const"):
		if !item.Value.HasValue {
			ruleWriteFail("rule.actions[*].items[*].value.value is required when value.type is 'Const'.")
		}
		ruleWriteValidateSetConstant(item.Value.Value, target.DataValueType)
	case strings.EqualFold(valueType, "Formula"):
		formula := ruleWriteFormulaText(item.Value)
		ruleWriteFormulaScope(formula, s.attributes, targetPath, target.DataValueType)
	case strings.EqualFold(valueType, "AttributeValue"):
		if ruleWriteBlank(item.Value.Path) {
			ruleWriteFail("rule.actions[*].items[*].value.path is required when value.type is 'AttributeValue'.")
		}
		sourcePath := *item.Value.Path
		source := s.resolveAttribute(sourcePath, "rule.actions[*].items[*].value.path")
		if !strings.EqualFold(target.DataValueType, source.DataValueType) {
			ruleWriteFail("rule.actions[*].items[*] assigns source attribute '%s' (%s) to target attribute '%s' (%s). Both attributes must have the same data value type.",
				sourcePath, source.DataValueType, targetPath, target.DataValueType)
		}
	default:
		ruleWriteFail("rule.actions[*].items[*].value.type must be 'Const', 'Formula', or 'AttributeValue'.")
	}
}

func ruleWriteValidateSetConstant(value any, target string) {
	switch {
	case target == "Lookup":
		if !ruleWriteIsGUIDString(value) {
			ruleWriteFail("rule.actions[*].items[*].value.value must be a GUID string when the target attribute is a Lookup.")
		}
	case target == "Boolean":
		if _, ok := value.(bool); !ok {
			ruleWriteFail("rule.actions[*].items[*].value.value must be a JSON boolean when the target attribute is Boolean.")
		}
	case ruleWriteIsDateTime(target):
		if _, ok := ruleWriteDateTimeConstant(value, target); ok {
			return
		}
		switch target {
		case "DateTime":
			ruleWriteFail("rule.actions[*].items[*].value.value must be a JSON string in ISO 8601 date-time format with a timezone suffix ('Z' or '+/-HH:mm') when the target attribute is DateTime.")
		case "Date":
			ruleWriteFail("rule.actions[*].items[*].value.value must be a JSON string in 'yyyy-MM-dd' format when the target attribute is Date.")
		case "Time":
			ruleWriteFail("rule.actions[*].items[*].value.value must be a JSON string in ISO 8601 time format with a timezone suffix ('Z' or '+/-HH:mm') when the target attribute is Time.")
		}
		ruleWriteFail("Const set-values is not supported for target attribute type '%s'.", target)
	case ruleWriteIsText(target):
		if _, ok := value.(string); !ok {
			ruleWriteFail("rule.actions[*].items[*].value.value must be a JSON string when the target attribute is a text type.")
		}
	case ruleWriteIsNumeric(target):
		if _, ok := value.(float64); !ok {
			ruleWriteFail("rule.actions[*].items[*].value.value must be a JSON number when the target attribute is a numeric type.")
		}
	default:
		ruleWriteFail("Const set-values is not supported for target attribute type '%s'.", target)
	}
}

func (s *ruleWriteScope) validateApplyFilter(action *ruleWriteAction) {
	if strings.TrimSpace(action.Target) == "" {
		ruleWriteFail("rule.actions[*].target is required when type is 'apply-filter'.")
	}
	ruleWriteDirectPath(action.Target, "rule.actions[*].target")
	target := s.resolveAttribute(action.Target, "rule.actions[*].target")
	ruleWriteEnsureLookup(target, action.Target, "rule.actions[*].target")
	if strings.TrimSpace(action.Source) == "" {
		ruleWriteFail("rule.actions[*].source is required when type is 'apply-filter'.")
	}
	ruleWriteDirectPath(action.Source, "rule.actions[*].source")
	source := s.resolveAttribute(action.Source, "rule.actions[*].source")
	ruleWriteEnsureLookup(source, action.Source, "rule.actions[*].source")
	if strings.TrimSpace(action.TargetFilterPath) == "" {
		ruleWriteFail("rule.actions[*].targetFilterPath is required when type is 'apply-filter'.")
	}
	left := s.resolveAttribute(action.Target+"."+strings.TrimSpace(action.TargetFilterPath), "rule.actions[*].targetFilterPath")
	ruleWriteEnsureLookup(left, left.Path, "rule.actions[*].targetFilterPath")
	right := source
	if !ruleWriteBlank(action.SourceFilterPath) {
		right = s.resolveAttribute(action.Source+"."+strings.TrimSpace(*action.SourceFilterPath), "rule.actions[*].sourceFilterPath")
		ruleWriteEnsureLookup(right, right.Path, "rule.actions[*].sourceFilterPath")
	}
	if !strings.EqualFold(left.DataValueType, right.DataValueType) {
		ruleWriteFail("apply-filter compares target path '%s' (%s) to source path '%s' (%s). Both sides must have the same data value type.",
			left.Path, left.DataValueType, right.Path, right.DataValueType)
	}
	if strings.EqualFold(left.DataValueType, "Lookup") && !strings.EqualFold(ruleWriteText(left.ReferenceSchema), ruleWriteText(right.ReferenceSchema)) {
		ruleWriteFail("apply-filter compares lookup path '%s' (%s) to '%s' (%s). Both lookup sides must reference the same schema.",
			left.Path, ruleWriteText(left.ReferenceSchema), right.Path, ruleWriteText(right.ReferenceSchema))
	}
	if !ruleWriteBlank(action.SourceFilterPath) && action.PopulateValue {
		ruleWriteFail("rule.actions[*].populateValue is not supported when rule.actions[*].sourceFilterPath is set for apply-filter.")
	}
}

func ruleWriteEnsureLookup(attribute ruleWriteAttribute, path, field string) {
	if !strings.EqualFold(attribute.DataValueType, "Lookup") {
		ruleWriteFail("Attribute '%s' in %s must be a Lookup.", path, field)
	}
	if ruleWriteBlank(attribute.ReferenceSchema) {
		ruleWriteFail("Lookup attribute '%s' in %s must declare a reference schema.", path, field)
	}
}

func (s *ruleWriteScope) validateStaticFilter(action *ruleWriteAction) {
	if strings.TrimSpace(action.TargetAttribute) == "" {
		ruleWriteFail("rule.actions[*].targetAttribute is required when type is 'apply-static-filter'.")
	}
	ruleWriteDirectPath(action.TargetAttribute, "rule.actions[*].targetAttribute")
	target, ok := s.attributes.lookup(action.TargetAttribute)
	if !ok {
		ruleWriteFail("filter.target-attribute-unknown: targetAttribute '%s' was not found on the entity schema.", action.TargetAttribute)
	}
	ruleWriteEnsureLookup(target, action.TargetAttribute, "rule.actions[*].targetAttribute")
	if !action.HasFilter {
		ruleWriteFail("rule.actions[*].filter is required when type is 'apply-static-filter'.")
	}
	group := ruleWriteDeserializeFilter(action.Filter)
	ruleWriteValidateFilterStructure(group, "filter")
	if s.staticFilters {
		s.validateFilterSchema(group, *target.ReferenceSchema, "filter")
	}
}

// Lookup constants must name existing records (clio's BusinessRuleLookupReferenceValidator).

func (s *ruleWriteScope) validateLookupReferences(rule *RuleWriteRule) {
	type reference struct {
		attributePath, schema, recordID, source string
	}
	references := []reference{}
	build := func(attribute ruleWriteAttribute, value any, source string) {
		if !strings.EqualFold(attribute.DataValueType, "Lookup") {
			return
		}
		if ruleWriteBlank(attribute.ReferenceSchema) {
			ruleWriteFail("%s references lookup attribute '%s', but its reference schema cannot be resolved.", source, attribute.Path)
		}
		text, ok := value.(string)
		if !ok {
			return
		}
		id := ruleWriteParseGUID(text)
		if id == "" {
			return
		}
		references = append(references, reference{attribute.Path, *attribute.ReferenceSchema, id, source})
	}
	if rule.Condition != nil {
		for _, condition := range rule.Condition.Conditions {
			if condition == nil || condition.Right == nil || !strings.EqualFold(ruleWriteText(condition.Right.Type), "Const") ||
				!condition.Right.HasValue || condition.Left == nil || ruleWriteBlank(condition.Left.Path) {
				continue
			}
			build(ruleWriteMustGet(s.attributes, *condition.Left.Path), condition.Right.Value, "rule.condition.conditions[*].rightExpression.value")
		}
	}
	for _, action := range rule.Actions {
		if action == nil || !strings.EqualFold(action.Type, "set-values") {
			continue
		}
		for _, item := range action.setValueItems() {
			if item == nil || item.Value == nil || !strings.EqualFold(ruleWriteText(item.Value.Type), "Const") ||
				!item.Value.HasValue || item.Expression == nil || ruleWriteBlank(item.Expression.Path) {
				continue
			}
			build(ruleWriteMustGet(s.attributes, *item.Expression.Path), item.Value.Value, "rule.actions[*].items[*].value.value")
		}
	}
	for _, ref := range references {
		rows, err := s.client.ruleWriteSelect(s.ctx, ruleWriteSelectQuery(ref.schema, [][2]string{{"Id", "Id"}}, "Id", ref.recordID, 0, 1), 30*time.Second, false)
		if err != nil {
			ruleWriteFail("%s references lookup attribute '%s', but record existence in lookup schema '%s' could not be verified: %s", ref.source, ref.attributePath, ref.schema, err.Error())
		}
		if len(rows) == 0 {
			ruleWriteFail("%s references lookup attribute '%s', but record '%s' was not found in lookup schema '%s'. Use odata-read or execute-esq to find the lookup record Id before creating the business rule.", ref.source, ref.attributePath, ref.recordID, ref.schema)
		}
	}
}

// ruleWriteSelectQuery is clio's SelectQueryHelper.BuildSelectQuery with one equality filter.
func ruleWriteSelectQuery(root string, columns [][2]string, filterColumn string, value any, dataValueType, rowCount int) *orderedObject {
	items := newOrdered()
	for _, column := range columns {
		items.set(column[0], ruleWriteObject(
			"expression", ruleWriteObject("expressionType", 0, "columnPath", column[1]),
			"orderDirection", 0, "orderPosition", -1, "isVisible", true))
	}
	filter := ruleWriteObject(
		"filterType", 1, "comparisonType", 3, "isEnabled", true, "trimDateTimeParameterToDate", false,
		"leftExpression", ruleWriteObject("expressionType", 0, "columnPath", filterColumn),
		"rightExpression", ruleWriteObject("expressionType", 2, "parameter", ruleWriteObject("value", value, "dataValueType", dataValueType)))
	return ruleWriteObject(
		"rootSchemaName", root, "operationType", 0, "allColumns", false, "isDistinct", false, "ignoreDisplayValues", false,
		"rowCount", rowCount, "rowsOffset", -1, "isPageable", false,
		"columns", ruleWriteObject("items", items),
		"filters", ruleWriteObject("filterType", 6, "isEnabled", true, "trimDateTimeParameterToDate", false, "logicalOperation", 0,
			"items", ruleWriteObject("filter0", filter)))
}

var ruleWriteTransientMarkers = []string{"collection was modified", "deadlock", "timeout", "timed out"}

// ruleWriteSelect is clio's SelectQueryHelper.ExecuteSelectQuery: success:false is a failure carrying the
// server's message (or the body); a missing rows array is no rows. An unbounded call retries a transient
// server failure up to three times.
func (c *Client) ruleWriteSelect(ctx context.Context, query *orderedObject, timeout time.Duration, retry bool) ([]map[string]json.RawMessage, error) {
	body, err := json.Marshal(query)
	if err != nil {
		return nil, err
	}
	attempts := 1
	if retry {
		attempts = 3
	}
	for attempt := 1; ; attempt++ {
		payload, err := c.postDataServiceJSON(ctx, "SelectQuery", body, timeout, maxResponseBytes)
		if err != nil {
			return nil, err
		}
		var response struct {
			Success   bool                         `json:"success"`
			Rows      []map[string]json.RawMessage `json:"rows"`
			ErrorInfo *struct {
				Message *string `json:"message"`
			} `json:"errorInfo"`
		}
		if err := json.Unmarshal(payload, &response); err != nil {
			return nil, fmt.Errorf("SelectQuery returned invalid JSON: %w", err)
		}
		if response.Success {
			return response.Rows, nil
		}
		detail := string(payload)
		if response.ErrorInfo != nil && response.ErrorInfo.Message != nil {
			detail = *response.ErrorInfo.Message
		}
		transient := false
		for _, marker := range ruleWriteTransientMarkers {
			if strings.Contains(strings.ToLower(detail), marker) {
				transient = true
			}
		}
		if attempt >= attempts || !transient {
			return nil, fmt.Errorf("SelectQuery failed: %s", detail)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// ruleWriteParseGUID is Guid.TryParse for the D, N, B and P formats; it returns the D form in lower case.
func ruleWriteParseGUID(text string) string {
	value := strings.TrimSpace(text)
	if len(value) == 38 && ((value[0] == '{' && value[37] == '}') || (value[0] == '(' && value[37] == ')')) {
		value = value[1:37]
	}
	if len(value) == 32 && !strings.Contains(value, "-") {
		value = value[0:8] + "-" + value[8:12] + "-" + value[12:16] + "-" + value[16:20] + "-" + value[20:]
	}
	if len(value) != 36 {
		return ""
	}
	value = strings.ToLower(value)
	for index, char := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			if char != '-' {
				return ""
			}
			continue
		}
		if !strings.ContainsRune("0123456789abcdef", char) {
			return ""
		}
	}
	return value
}
