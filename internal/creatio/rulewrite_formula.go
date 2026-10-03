package creatio

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
)

// Formula set-values (clio's BusinessRuleFormulaBuilder and BusinessRuleFormulaValidationService).

var ruleWriteFormulaSyntax = regexp.MustCompile(`^(?:#[\p{L}\p{Mn}\p{Nd}\p{Pc}]+(?:\.[\p{L}\p{Mn}\p{Nd}\p{Pc}]+)*#|[\p{Nd}\s+\-*/.()])*$`)

var ruleWriteFormulaKnownIdentifiers = []string{"Blank", "false", "Self", "true"}

func ruleWriteIsFormula(expression *ruleWriteExpression) bool {
	return expression != nil && strings.EqualFold(ruleWriteText(expression.Type), "Formula")
}

func ruleWriteFormulaText(expression *ruleWriteExpression) string {
	if expression == nil || ruleWriteBlank(expression.Expression) {
		ruleWriteFail("rule.actions[*].items[*].value.expression must be a non-empty string when value.type is 'Formula'.")
	}
	return *expression.Expression
}

func ruleWriteFormulaTarget(targetPath, targetType string) {
	if !ruleWriteIsNumeric(targetType) {
		ruleWriteFail("Formula target attribute '%s' has type %s. Formula set-values supports only numeric target attributes.", targetPath, targetType)
	}
}

// ruleWriteFormulaScope is ValidateFormulaScope: a numeric target, then a translatable formula.
func ruleWriteFormulaScope(formula string, attributes ruleWriteAttributes, targetPath, targetType string) {
	ruleWriteFormulaTarget(targetPath, targetType)
	ruleWriteTranslateFormula(formula, "Record", attributes, &targetPath)
}

// ruleWriteFormulaAttributesByName is BuildAttributesByName: the map's own keys, first one wins
// case-insensitively.
func ruleWriteFormulaAttributesByName(attributes ruleWriteAttributes) map[string]ruleWriteAttribute {
	result := map[string]ruleWriteAttribute{}
	for _, key := range attributes.keys() {
		if strings.TrimSpace(key) == "" {
			continue
		}
		lower := strings.ToLower(key)
		if _, seen := result[lower]; seen {
			continue
		}
		result[lower] = ruleWriteMustGet(attributes, key)
	}
	return result
}

func ruleWriteTranslateFormula(formula, recordVariable string, attributes ruleWriteAttributes, targetPath *string) (string, []string) {
	byName := ruleWriteFormulaAttributesByName(attributes)
	sources := []string{}
	runes := []rune(formula)
	var result strings.Builder
	for index := 0; index < len(runes); {
		current := runes[index]
		if current == '"' {
			result.WriteRune(current)
			index++
			for index < len(runes) {
				char := runes[index]
				result.WriteRune(char)
				index++
				if char != '"' {
					continue
				}
				if index < len(runes) && runes[index] == '"' {
					result.WriteRune(runes[index])
					index++
					continue
				}
				break
			}
			continue
		}
		if unicode.IsLetter(current) || current == '_' {
			start := index
			index++
			for index < len(runes) && (unicode.IsLetter(runes[index]) || unicode.IsDigit(runes[index]) || runes[index] == '_') {
				index++
			}
			identifier := string(runes[start:index])
			if attribute, ok := byName[strings.ToLower(identifier)]; ok {
				if !ruleWriteIsNumeric(attribute.DataValueType) {
					ruleWriteFail("Formula source attribute '%s' has type %s. Formula set-values supports only numeric source attributes.", attribute.Path, attribute.DataValueType)
				}
				result.WriteString("#" + recordVariable + "." + attribute.Path + "#")
				sources = append(sources, attribute.Path)
				continue
			}
			known := false
			for _, name := range ruleWriteFormulaKnownIdentifiers {
				if strings.EqualFold(name, identifier) {
					known = true
				}
			}
			if known {
				result.WriteString(identifier)
				continue
			}
			lookahead := index
			for lookahead < len(runes) && unicode.IsSpace(runes[lookahead]) {
				lookahead++
			}
			if lookahead < len(runes) && runes[lookahead] == '(' {
				ruleWriteFail("Formula functions are not supported in rule.actions[*].items[*].value.expression. Use a simple direct-field expression instead of '%s(...)'.", identifier)
			}
			ruleWriteFail("Unknown attribute '%s' in rule.actions[*].items[*].value.expression formula.", identifier)
		}
		result.WriteRune(current)
		index++
	}
	expression := result.String()
	if !ruleWriteFormulaSyntax.MatchString(expression) {
		ruleWriteFail("Formula expression supports only direct entity fields, numbers, arithmetic operators (+, -, *, /), dots, parentheses, and whitespace.")
	}
	if len(sources) == 0 {
		if targetPath == nil {
			ruleWriteFail("Formula must reference at least one entity attribute.")
		}
		ruleWriteFail("Formula for '%s' must reference at least one entity attribute.", *targetPath)
	}
	return expression, sources
}

// ruleWriteFormulaSources is GetFormulaSourcePaths: distinct case-insensitively, in first-seen order.
func ruleWriteFormulaSources(formula string, attributes ruleWriteAttributes) []string {
	_, sources := ruleWriteTranslateFormula(formula, "Record", attributes, nil)
	return ruleWriteDistinctFold(sources)
}

func ruleWriteDistinctFold(values []string) []string {
	seen := map[string]bool{}
	result := []string{}
	for _, value := range values {
		key := strings.ToLower(value)
		if seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, value)
	}
	return result
}

type ruleWriteFormulaContext struct {
	recordVariable, idParameter, fieldValuesParameter string
	expression                                        string
}

func ruleWriteBuildFormulaContext(entity string, attributes ruleWriteAttributes, targetPath, formula, targetType string) ruleWriteFormulaContext {
	context := ruleWriteFormulaContext{recordVariable: entity + "Record", idParameter: entity + "IdParameter", fieldValuesParameter: entity + "fieldValuesParameter"}
	ruleWriteFormulaTarget(targetPath, targetType)
	context.expression, _ = ruleWriteTranslateFormula(formula, context.recordVariable, attributes, &targetPath)
	return context
}

// ruleWriteFormulaValueExpression is BuildValueExpression: the persisted Formula value expression.
func ruleWriteFormulaValueExpression(entity string, attributes ruleWriteAttributes, targetPath, formula, targetType string) *orderedObject {
	context := ruleWriteBuildFormulaContext(entity, attributes, targetPath, formula, targetType)
	schema := ruleWriteObject(
		"uId", newGUID(), "name", "", "engineType", "PowerFx", "expression", context.expression, "resultDataValueType", targetType,
		"expressionVariables", []any{ruleWriteObject(
			"typeName", ruleWriteSchemaVariableTypeName, "uId", newGUID(), "name", context.recordVariable,
			"variableType", "Record", "dataValueType", "Lookup",
			"config", ruleWriteObject(
				"typeName", ruleWriteRecordVariableConfig, "uId", newGUID(), "name", "VariableConfig", "value", entity,
				"recordType", "Entity",
				"primaryValue", ruleWriteObject("type", "Parameter", "value", context.idParameter),
				"fieldValues", ruleWriteObject("type", "Parameter", "value", context.fieldValuesParameter),
				"columns", []any{}))},
		"parameters", []any{
			ruleWriteObject("typeName", ruleWriteSchemaParameter, "uId", newGUID(), "name", context.idParameter, "dataValueType", "Guid"),
			ruleWriteObject("typeName", ruleWriteSchemaParameter, "uId", newGUID(), "name", context.fieldValuesParameter, "dataValueType", "Text"),
		})
	return ruleWriteExpressionObject(ruleWriteExpressionFields{
		typeName: brFormulaExpression, uid: newGUID(), kind: "Formula",
		parameterMappings: []any{
			ruleWriteObject("typeName", ruleWriteParameterMapping, "uId", newGUID(), "parameterName", context.idParameter,
				"expression", ruleWriteExpressionObject(ruleWriteExpressionFields{typeName: brAttributeExpression, uid: newGUID(), kind: "AttributeValue", dataValueType: ruleWriteOptional("Guid"), path: ruleWriteOptional("Id")})),
			ruleWriteObject("typeName", ruleWriteParameterMapping, "uId", newGUID(), "parameterName", context.fieldValuesParameter,
				"expression", ruleWriteExpressionObject(ruleWriteExpressionFields{typeName: ruleWriteContextExpression, uid: newGUID()})),
		},
		expressionSchema: schema,
	})
}

// validateFormulas is EntityBusinessRuleService.ValidateFormulas: ExpressionService.svc/Validate for every
// formula set-values item.
func (s *ruleWriteScope) validateFormulas(rule *RuleWriteRule) {
	type pending struct {
		targetPath, formula, targetType string
		context                         ruleWriteFormulaContext
	}
	contexts := []pending{}
	for _, action := range rule.Actions {
		if !strings.EqualFold(action.Type, "set-values") {
			continue
		}
		for _, item := range action.setValueItems() {
			if !ruleWriteIsFormula(item.Value) {
				continue
			}
			targetPath := ruleWriteText(item.Expression.Path)
			target, ok := s.attributes.lookup(targetPath)
			if !ok {
				ruleWriteFail("Unknown attribute '%s' in rule.actions[*].items[*].expression.path.", targetPath)
			}
			formula := ruleWriteFormulaText(item.Value)
			context := ruleWriteBuildFormulaContext(s.entitySchema, s.attributes, targetPath, formula, target.DataValueType)
			contexts = append(contexts, pending{targetPath, formula, target.DataValueType, context})
		}
	}
	for _, entry := range contexts {
		s.validateFormula(entry.targetPath, entry.formula, entry.context, entry.targetType)
	}
}

func (s *ruleWriteScope) validateFormula(targetPath, formula string, context ruleWriteFormulaContext, targetType string) {
	metadata := ruleWriteObject(
		"engineType", "PowerFx", "expression", context.expression, "resultDataValueType", targetType,
		"parameters", []any{
			ruleWriteObject("name", context.idParameter, "dataValueType", "Guid"),
			ruleWriteObject("name", context.fieldValuesParameter, "dataValueType", "Text"),
		},
		"expressionVariables", []any{ruleWriteObject(
			"name", context.recordVariable, "variableType", "Record", "dataValueType", "Lookup",
			"config", ruleWriteObject("value", s.entitySchema, "recordType", "Entity",
				"primaryValue", ruleWriteObject("type", "Parameter", "value", context.idParameter)))},
	)
	request := ruleWriteSTJ(ruleWriteObject("metadata", ruleWriteSTJ(metadata, true)), true)
	payload, err := s.client.callService(s.ctx, serviceCall{Route: "ServiceModel/ExpressionService.svc/Validate", Body: []byte(request), Timeout: 45 * time.Second, Limit: maxResponseBytes})
	if err != nil {
		ruleWriteFail("%s", err.Error())
	}
	var errors []struct {
		Message string `json:"message"`
		From    *int   `json:"from"`
		To      *int   `json:"to"`
	}
	if strings.TrimSpace(string(payload)) != "null" {
		if err := json.Unmarshal(payload, &errors); err != nil {
			ruleWriteFail("%s", err.Error())
		}
	}
	if len(errors) == 0 {
		return
	}
	parts := make([]string, 0, len(errors))
	for _, item := range errors {
		text := item.Message
		if item.From != nil && item.To != nil {
			text += fmt.Sprintf(" [%d-%d]", *item.From, *item.To)
		}
		parts = append(parts, text)
	}
	ruleWriteFail("Formula validation failed for '%s' ('%s'): %s", targetPath, formula, strings.Join(parts, "; "))
}
