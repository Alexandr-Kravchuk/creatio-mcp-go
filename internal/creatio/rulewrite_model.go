package creatio

import (
	"fmt"
	"strings"
)

// The create/update contract of clio's business-rule tools (BusinessRule, BusinessRuleConditionGroup,
// BusinessRuleCondition, BusinessRuleExpression and the polymorphic actions), bound the way clio's
// System.Text.Json binder binds them: property names match case-insensitively, unknown properties are
// ignored, a JSON null leaves a reference property null, and the action discriminator "type" must be
// spelled exactly and name a type the tool knows.

// RuleWriteRule is one rule of a create/update batch. A nil *RuleWriteRule is a JSON null array element.
type RuleWriteRule struct {
	Caption   *string
	Condition *ruleWriteGroup
	Actions   []*ruleWriteAction // nil: absent or null
	Name      *string
	Enabled   *bool
}

type ruleWriteGroup struct {
	LogicalOperation *string
	Conditions       []*ruleWriteCondition // nil: absent or null
}

type ruleWriteCondition struct {
	Left           *ruleWriteExpression
	ComparisonType *string
	Right          *ruleWriteExpression
	UID            *string
}

type ruleWriteExpression struct {
	Type           *string
	Path           *string
	Value          any // the JSON value (decoded); meaningful only when HasValue
	HasValue       bool
	Expression     *string
	SysValueName   *string
	SysSettingName *string
	UID            *string
}

type ruleWriteSetItem struct {
	Expression *ruleWriteExpression
	Value      *ruleWriteExpression
	UID        *string
}

// ruleWriteAction is clio's BusinessRuleAction family flattened: Type is the discriminator.
type ruleWriteAction struct {
	Type             string
	UID              *string
	Items            []*string // field/element selection; nil when absent or null
	SetItems         []*ruleWriteSetItem
	Target           string
	TargetFilterPath string
	Source           string
	SourceFilterPath *string
	ClearValue       bool
	PopulateValue    bool
	TargetAttribute  string
	Filter           any
	HasFilter        bool // false: absent or JSON null (JsonElement Undefined/Null)
}

var ruleWriteEntityActionTypes = map[string]bool{
	"make-editable": true, "make-read-only": true, "make-required": true, "make-optional": true,
	"set-values": true, "apply-filter": true, "apply-static-filter": true,
}

var ruleWritePageActionTypes = map[string]bool{
	"hide-element": true, "show-element": true, "make-editable": true, "make-read-only": true,
	"make-required": true, "make-optional": true, "apply-static-filter": true,
}

// ruleWriteBindError is clio's argument-binding refusal (raised before the tool runs).
type ruleWriteBindError struct{ message string }

func (e ruleWriteBindError) Error() string { return e.message }

// RuleWriteParseRules binds the rules argument. raw is the decoded JSON value; present tells an absent key
// from a JSON null. The result is nil for an absent or null array.
func RuleWriteParseRules(tool string, raw any, page bool) ([]*RuleWriteRule, error) {
	if raw == nil {
		return nil, nil
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, ruleWriteBindError{fmt.Sprintf("invalid-parameter-type: argument 'rules' for MCP tool '%s' must be an array. Received an incompatible JSON value.", tool)}
	}
	binder := ruleWriteBinder{tool: tool, page: page}
	rules := make([]*RuleWriteRule, 0, len(items))
	for _, item := range items {
		rule := binder.rule(item)
		if binder.err != nil {
			return nil, binder.err
		}
		rules = append(rules, rule)
	}
	return rules, nil
}

type ruleWriteBinder struct {
	tool string
	page bool
	err  error
}

func (b *ruleWriteBinder) shape() {
	if b.err == nil {
		b.err = ruleWriteBindError{fmt.Sprintf("invalid-parameter-type: argument 'rules' for MCP tool '%s' contains a value that does not match the documented shape. Received an incompatible JSON value.", b.tool)}
	}
}

// missingDiscriminator is what clio's binder reports for an action object without "type": the abstract
// action type cannot be created, which surfaces as a refusal of the whole args object.
func (b *ruleWriteBinder) missingDiscriminator() {
	if b.err == nil {
		b.err = ruleWriteBindError{fmt.Sprintf("invalid-parameter-type: argument 'args' for MCP tool '%s' must be an object. Received an incompatible JSON value.", b.tool)}
	}
}

// field finds a property case-insensitively; the last matching key wins, as a repeated key does in STJ.
func ruleWriteField(object map[string]any, name string) (any, bool) {
	if value, ok := object[name]; ok {
		return value, true
	}
	for key, value := range object {
		if strings.EqualFold(key, name) {
			return value, true
		}
	}
	return nil, false
}

func (b *ruleWriteBinder) object(value any) (map[string]any, bool) {
	if value == nil {
		return nil, false
	}
	object, ok := value.(map[string]any)
	if !ok {
		b.shape()
		return nil, false
	}
	return object, true
}

func (b *ruleWriteBinder) str(object map[string]any, name string) *string {
	value, ok := ruleWriteField(object, name)
	if !ok || value == nil {
		return nil
	}
	text, ok := value.(string)
	if !ok {
		b.shape()
		return nil
	}
	return &text
}

func (b *ruleWriteBinder) plain(object map[string]any, name string) string {
	if value := b.str(object, name); value != nil {
		return *value
	}
	return ""
}

func (b *ruleWriteBinder) nullableBool(object map[string]any, name string) *bool {
	value, ok := ruleWriteField(object, name)
	if !ok || value == nil {
		return nil
	}
	flag, ok := value.(bool)
	if !ok {
		b.shape()
		return nil
	}
	return &flag
}

// boolean binds a non-nullable bool: JSON null is a binding failure, absence keeps false.
func (b *ruleWriteBinder) boolean(object map[string]any, name string) bool {
	value, ok := ruleWriteField(object, name)
	if !ok {
		return false
	}
	flag, ok := value.(bool)
	if !ok {
		b.shape()
		return false
	}
	return flag
}

func (b *ruleWriteBinder) array(object map[string]any, name string) ([]any, bool) {
	value, ok := ruleWriteField(object, name)
	if !ok || value == nil {
		return nil, false
	}
	items, ok := value.([]any)
	if !ok {
		b.shape()
		return nil, false
	}
	return items, true
}

func (b *ruleWriteBinder) rule(value any) *RuleWriteRule {
	object, ok := b.object(value)
	if !ok {
		return nil
	}
	rule := &RuleWriteRule{Caption: b.str(object, "caption"), Name: b.str(object, "name"), Enabled: b.nullableBool(object, "enabled")}
	if raw, ok := ruleWriteField(object, "condition"); ok {
		if group, ok := b.object(raw); ok {
			rule.Condition = b.group(group)
		}
	}
	if items, ok := b.array(object, "actions"); ok {
		rule.Actions = make([]*ruleWriteAction, 0, len(items))
		for _, item := range items {
			rule.Actions = append(rule.Actions, b.action(item))
		}
	}
	return rule
}

func (b *ruleWriteBinder) group(object map[string]any) *ruleWriteGroup {
	group := &ruleWriteGroup{LogicalOperation: b.str(object, "logicalOperation")}
	if items, ok := b.array(object, "conditions"); ok {
		group.Conditions = make([]*ruleWriteCondition, 0, len(items))
		for _, item := range items {
			var condition *ruleWriteCondition
			if entry, ok := b.object(item); ok {
				condition = &ruleWriteCondition{
					Left: b.expressionField(entry, "leftExpression"), ComparisonType: b.str(entry, "comparisonType"),
					Right: b.expressionField(entry, "rightExpression"), UID: b.str(entry, "uId"),
				}
			}
			group.Conditions = append(group.Conditions, condition)
		}
	}
	return group
}

func (b *ruleWriteBinder) expressionField(object map[string]any, name string) *ruleWriteExpression {
	raw, ok := ruleWriteField(object, name)
	if !ok {
		return nil
	}
	entry, ok := b.object(raw)
	if !ok {
		return nil
	}
	expression := &ruleWriteExpression{
		Type: b.str(entry, "type"), Path: b.str(entry, "path"), Expression: b.str(entry, "expression"),
		SysValueName: b.str(entry, "sysValueName"), SysSettingName: b.str(entry, "sysSettingName"), UID: b.str(entry, "uId"),
	}
	if value, ok := ruleWriteField(entry, "value"); ok && value != nil {
		expression.Value, expression.HasValue = value, true
	}
	return expression
}

func (b *ruleWriteBinder) action(value any) *ruleWriteAction {
	object, ok := b.object(value)
	if !ok {
		return nil
	}
	discriminator, ok := object["type"]
	if !ok {
		b.missingDiscriminator()
		return nil
	}
	kind, ok := discriminator.(string)
	known := ruleWriteEntityActionTypes
	if b.page {
		known = ruleWritePageActionTypes
	}
	if !ok || !known[kind] {
		b.shape()
		return nil
	}
	action := &ruleWriteAction{Type: kind, UID: b.str(object, "uId")}
	switch kind {
	case "set-values":
		if items, ok := b.array(object, "items"); ok {
			action.SetItems = make([]*ruleWriteSetItem, 0, len(items))
			for _, item := range items {
				var setItem *ruleWriteSetItem
				if entry, ok := b.object(item); ok {
					setItem = &ruleWriteSetItem{Expression: b.expressionField(entry, "expression"), Value: b.expressionField(entry, "value"), UID: b.str(entry, "uId")}
				}
				action.SetItems = append(action.SetItems, setItem)
			}
		}
	case "apply-filter":
		action.Target = b.plain(object, "target")
		action.TargetFilterPath = b.plain(object, "targetFilterPath")
		action.Source = b.plain(object, "source")
		action.SourceFilterPath = b.str(object, "sourceFilterPath")
		action.ClearValue = b.boolean(object, "clearValue")
		action.PopulateValue = b.boolean(object, "populateValue")
	case "apply-static-filter":
		action.TargetAttribute = b.plain(object, "targetAttribute")
		if filter, ok := ruleWriteField(object, "filter"); ok && filter != nil {
			action.Filter, action.HasFilter = filter, true
		}
	default:
		if items, ok := b.array(object, "items"); ok {
			action.Items = make([]*string, 0, len(items))
			for _, item := range items {
				if item == nil {
					action.Items = append(action.Items, nil)
					continue
				}
				text, ok := item.(string)
				if !ok {
					b.shape()
					return nil
				}
				action.Items = append(action.Items, &text)
			}
		}
	}
	return action
}

// fieldSelectionItems is clio's FieldSelectionItems: the items of a field/element action, empty otherwise.
// A JSON null item stays an empty name (clio keeps the null; every consumer treats it as blank).
func (a *ruleWriteAction) fieldSelectionItems() []string {
	switch a.Type {
	case "set-values", "apply-filter", "apply-static-filter":
		return []string{}
	}
	items := make([]string, 0, len(a.Items))
	for _, item := range a.Items {
		if item == nil {
			items = append(items, "")
			continue
		}
		items = append(items, *item)
	}
	return items
}

func (a *ruleWriteAction) setValueItems() []*ruleWriteSetItem {
	if a.Type != "set-values" {
		return nil
	}
	if a.SetItems == nil {
		return []*ruleWriteSetItem{}
	}
	return a.SetItems
}

func ruleWriteText(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func ruleWriteBlank(value *string) bool {
	return value == nil || strings.TrimSpace(*value) == ""
}
