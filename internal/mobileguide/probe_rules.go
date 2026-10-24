package mobileguide

import (
	"strconv"
	"strings"
)

// PageBusinessRuleProbe.ParseRules: the add-on metadata of a page's business rules read into the conversion's
// read-model. The HTTP half of the probe (schema context, AddonSchemaDesignerService.GetSchema) is not ported yet.

// AddonResource is an AddonResourceDto: the key and its first localized value.
type AddonResource struct {
	Key        string
	FirstValue *string
}

const logicalOr = 2

var comparisonNameByValue = map[int]string{0: "is-not-filled-in", 1: "is-filled-in", 2: "equal", 3: "not-equal",
	5: "less-than", 6: "less-than-or-equal", 7: "greater-than", 8: "greater-than-or-equal", 11: "contain", 12: "not-contain"}

var supportedComparisonNames = newSet(true, "is-not-filled-in", "is-filled-in", "equal", "not-equal", "less-than",
	"less-than-or-equal", "greater-than", "greater-than-or-equal", "contain", "not-contain")

var pascalComparisonToKebab = func() *dict[string] {
	result := newCI[string]()
	for _, pair := range [][2]string{{"IsNotFilledIn", "is-not-filled-in"}, {"IsFilledIn", "is-filled-in"}, {"Equal", "equal"},
		{"NotEqual", "not-equal"}, {"Less", "less-than"}, {"LessOrEqual", "less-than-or-equal"}, {"Greater", "greater-than"},
		{"GreaterOrEqual", "greater-than-or-equal"}, {"Contain", "contain"}, {"NotContain", "not-contain"}} {
		result.set(pair[0], pair[1])
	}
	return result
}()

var actionShortByTypeName = func() *dict[string] {
	const prefix = "Terrasoft.Core.BusinessRules.Models.Actions.BusinessRuleAction"
	result := newCI[string]()
	for _, pair := range [][2]string{{"HideElement", "hide-element"}, {"ShowElement", "show-element"},
		{"EditableElement", "make-editable"}, {"ReadonlyElement", "make-read-only"}, {"RequiredElement", "make-required"},
		{"OptionalElement", "make-optional"}} {
		result.set(prefix+pair[0], pair[1])
	}
	return result
}()

// ParsePageBusinessRules is PageBusinessRuleProbe.ParseRules over an add-on schema's metadata and resources.
func ParsePageBusinessRules(metaData string, resources []AddonResource) []*SourcePageBusinessRule {
	result := []*SourcePageBusinessRule{}
	if isBlank(metaData) {
		return result
	}
	root, err := parseSTJ([]byte(metaData))
	if err != nil || !root.isObj() {
		return result
	}
	ruleNodes := root.get("rules")
	if !ruleNodes.isArr() {
		return result
	}
	captionByUID := buildCaptionResources(resources)
	for _, rule := range ruleNodes.arr {
		if !rule.isObj() {
			continue
		}
		uid := stringOf(rule.get("uId"))
		caption := stringOf(rule.get("caption"))
		if (caption == nil || isBlank(*caption)) && uid != nil && !isBlank(*uid) {
			if value, ok := captionByUID.get(*uid); ok {
				caption = strp(value)
			} else {
				caption = nil
			}
		}
		if caption == nil || isBlank(*caption) {
			caption = stringOf(rule.get("name"))
		}
		cases := rule.get("cases")
		if !cases.isArr() || len(cases.arr) == 0 {
			continue
		}
		caseIndex := 0
		for _, ruleCase := range cases.arr {
			if !ruleCase.isObj() {
				continue
			}
			caseIndex++
			var actionNodes *jn
			if value := ruleCase.get("actions"); value.isArr() {
				actionNodes = value
			}
			actions := parseRuleActions(actionNodes)
			if len(actions) == 0 {
				continue
			}
			caseCaption := caption
			if len(cases.arr) > 1 && caption != nil && !isBlank(*caption) {
				caseCaption = strp(*caption + " (case " + strconv.Itoa(caseIndex) + ")")
			}
			condition, issue := convertRuleCondition(ruleCase.get("condition"))
			result = append(result, &SourcePageBusinessRule{Caption: caseCaption, Condition: condition, ConditionIssue: issue,
				Actions: actions})
		}
	}
	return result
}

func parseRuleActions(actionNodes *jn) []*SourcePageRuleAction {
	actions := []*SourcePageRuleAction{}
	if actionNodes == nil {
		return actions
	}
	for _, action := range actionNodes.arr {
		if !action.isObj() {
			continue
		}
		typeName := stringOf(action.get("typeName"))
		if typeName == nil || *typeName == "" {
			continue
		}
		actionType, ok := actionShortByTypeName.get(*typeName)
		if !ok {
			continue
		}
		actions = append(actions, &SourcePageRuleAction{ActionType: strp(actionType), ElementItems: splitRuleItems(action.get("items"))})
	}
	return actions
}

func convertRuleCondition(node *jn) (*jn, int) {
	if !node.isObj() {
		return nil, ConditionIssueNone
	}
	activeIsOr := map[bool]bool{}
	var order []bool
	collectActiveGroupOperators(node, activeIsOr, &order)
	if len(activeIsOr) > 1 {
		return nil, ConditionIssueMixedAndOr
	}
	if hasUnrecognizedComparison(node) {
		return nil, ConditionIssueUnrecognizedComparison
	}
	conditions := newArr()
	collectLeafConditions(node, conditions)
	isOr := intOf(node.get("logicalOperation")) == logicalOr
	if len(order) == 1 {
		isOr = order[0]
	}
	operation := "AND"
	if isOr {
		operation = "OR"
	}
	result := newObj()
	result.set("logicalOperation", newStr(operation))
	result.set("conditions", conditions)
	return result, ConditionIssueNone
}

func collectActiveGroupOperators(group *jn, activeIsOr map[bool]bool, order *[]bool) {
	inner := group.get("conditions")
	if !inner.isArr() {
		return
	}
	operands := 0
	for _, child := range inner.arr {
		if !child.isObj() {
			continue
		}
		if countLeaves(child) > 0 {
			operands++
		}
		collectActiveGroupOperators(child, activeIsOr, order)
	}
	if operands >= 2 {
		value := intOf(group.get("logicalOperation")) == logicalOr
		if !activeIsOr[value] {
			activeIsOr[value] = true
			*order = append(*order, value)
		}
	}
}

func countLeaves(node *jn) int {
	if inner := node.get("conditions"); inner.isArr() {
		sum := 0
		for _, child := range inner.arr {
			if child.isObj() {
				sum += countLeaves(child)
			}
		}
		return sum
	}
	if node.get("leftExpression") == nil && node.get("comparisonType") == nil {
		return 0
	}
	return 1
}

func hasUnrecognizedComparison(node *jn) bool {
	if inner := node.get("conditions"); inner.isArr() {
		for _, child := range inner.arr {
			if child.isObj() && hasUnrecognizedComparison(child) {
				return true
			}
		}
		return false
	}
	isLeaf := node.get("leftExpression") != nil || node.get("comparisonType") != nil
	return isLeaf && resolveComparisonName(node.get("comparisonType")) == nil
}

func collectLeafConditions(node, conditions *jn) {
	if inner := node.get("conditions"); inner.isArr() {
		for _, child := range inner.arr {
			if child.isObj() {
				collectLeafConditions(child, conditions)
			}
		}
		return
	}
	if node.get("leftExpression") == nil && node.get("comparisonType") == nil {
		return
	}
	result := newObj()
	result.set("leftExpression", convertRuleExpression(node.get("leftExpression")))
	result.set("comparisonType", stjString(resolveComparisonName(node.get("comparisonType"))))
	if right := convertRuleExpression(node.get("rightExpression")); right != nil {
		result.set("rightExpression", right)
	}
	conditions.arr = append(conditions.arr, result)
}

func resolveComparisonName(node *jn) *string {
	const defaultComparison = "is-not-filled-in"
	if node == nil || node.isObj() || node.isArr() {
		return strp(defaultComparison)
	}
	if numeric, ok := node.tryInt(); ok {
		if name, found := comparisonNameByValue[numeric]; found {
			return strp(name)
		}
		return nil
	}
	text, _ := node.nodeString()
	if isBlank(text) {
		return strp(defaultComparison)
	}
	if parsed, err := strconv.Atoi(strings.TrimSpace(text)); err == nil {
		if name, found := comparisonNameByValue[parsed]; found {
			return strp(name)
		}
		return nil
	}
	if supportedComparisonNames.contains(text) {
		return strp(text)
	}
	if mapped, ok := pascalComparisonToKebab.get(text); ok {
		return strp(mapped)
	}
	return nil
}

func convertRuleExpression(node *jn) *jn {
	if !node.isObj() {
		return nil
	}
	result := newObj()
	typ := stringOf(node.get("type"))
	if typ == nil {
		typ = strp("AttributeValue")
	}
	result.set("type", newStr(*typ))
	if path := stringOf(node.get("path")); path != nil && !isBlank(*path) {
		result.set("path", newStr(*path))
	}
	if value := node.get("value"); value != nil {
		result.set("value", value.clone())
	}
	if formula := stringOf(node.get("expression")); formula != nil && !isBlank(*formula) {
		result.set("expression", newStr(*formula))
	}
	return result
}

func buildCaptionResources(resources []AddonResource) *dict[string] {
	result := newCI[string]()
	for _, resource := range resources {
		if isBlank(resource.Key) || !hasSuffixFold(resource.Key, "Caption") {
			continue
		}
		parts := strings.Split(resource.Key, ".")
		if len(parts) < 2 {
			continue
		}
		uid := parts[len(parts)-2]
		if !isBlank(uid) && resource.FirstValue != nil && !isBlank(*resource.FirstValue) && !result.has(uid) {
			result.set(uid, *resource.FirstValue)
		}
	}
	return result
}

func splitRuleItems(node *jn) []*string {
	items := []*string{}
	switch {
	case node == nil:
	case node.isArr():
		for _, element := range node.arr {
			if value := stringOf(element); value != nil && !isBlank(*value) {
				items = append(items, strp(dotnetTrim(*value)))
			}
		}
	case !node.isObj():
		if csv := stringOf(node); csv != nil && !isBlank(*csv) {
			for _, part := range strings.Split(*csv, ",") {
				if trimmed := dotnetTrim(part); trimmed != "" {
					items = append(items, strp(trimmed))
				}
			}
		}
	}
	return items
}

// stringOf is PageBusinessRuleProbe.StringOf: a string value, else the node's ToString().
func stringOf(node *jn) *string {
	if node == nil {
		return nil
	}
	if node.k == jsString {
		return strp(node.s)
	}
	text, _ := node.nodeString()
	return &text
}

// intOf is PageBusinessRuleProbe.IntOf (int.MinValue when absent or not a number).
func intOf(node *jn) int {
	if node == nil || node.isObj() || node.isArr() {
		return -1 << 31
	}
	if value, ok := node.tryInt(); ok {
		return value
	}
	text, _ := node.nodeString()
	if parsed, err := strconv.Atoi(strings.TrimSpace(text)); err == nil {
		return parsed
	}
	return -1 << 31
}
