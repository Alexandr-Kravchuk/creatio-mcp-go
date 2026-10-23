package mobileguide

import "testing"

// Cases from clio's PageBusinessRuleProbeTests.cs (ParseRules).

const actionPrefix = "Terrasoft.Core.BusinessRules.Models.Actions.BusinessRuleAction"

func leaf(comparison, path string) string {
	return `{"comparisonType":` + comparison + `,"leftExpression":{"type":"AttributeValue","path":"` + path + `"},"rightExpression":null}`
}

func TestParsePageBusinessRulesReverseMapsConditionAndActions(t *testing.T) {
	meta := `{"rules":[{"uId":"rule-1","name":"BusinessRule_1","caption":"Hide warnings when status filled","cases":[{
		"condition":{"logicalOperation":1,"conditions":[` + leaf("1", "PDS_Status") + `]},
		"actions":[{"typeName":"` + actionPrefix + `HideElement","items":"Warn1,Warn2"},{"typeName":"` + actionPrefix + `ShowElement","items":"Hint"}]}]}]}`
	rules := ParsePageBusinessRules(meta, nil)
	if len(rules) != 1 || deref(rules[0].Caption) != "Hide warnings when status filled" {
		t.Fatalf("rules = %+v", rules)
	}
	want := `{"logicalOperation":"AND","conditions":[{"leftExpression":{"type":"AttributeValue","path":"PDS_Status"},"comparisonType":"is-filled-in"}]}`
	if got := rules[0].Condition.toJSON(styleRelaxed); got != want {
		t.Errorf("condition = %s", got)
	}
	actions := rules[0].Actions
	if len(actions) != 2 || deref(actions[0].ActionType) != "hide-element" || len(actions[0].ElementItems) != 2 ||
		deref(actions[0].ElementItems[1]) != "Warn2" || deref(actions[1].ActionType) != "show-element" {
		t.Errorf("actions = %+v", actions)
	}
}

func TestParsePageBusinessRulesConditionShapes(t *testing.T) {
	hide := `"actions":[{"typeName":"` + actionPrefix + `HideElement","items":"X"}]`
	nested := `{"rules":[{"caption":"c","cases":[{"condition":{"logicalOperation":1,"conditions":[{"logicalOperation":1,"conditions":[` +
		leaf("0", "Parameter_3pxm4wn") + `]}]},` + hide + `}]}]}`
	rules := ParsePageBusinessRules(nested, nil)
	if got := rules[0].Condition.toJSON(styleRelaxed); got != `{"logicalOperation":"AND","conditions":[{"leftExpression":{"type":"AttributeValue","path":"Parameter_3pxm4wn"},"comparisonType":"is-not-filled-in"}]}` {
		t.Errorf("nested group = %s", got)
	}
	mixed := `{"rules":[{"caption":"c","cases":[{"condition":{"logicalOperation":1,"conditions":[` + leaf("1", "A") +
		`,{"logicalOperation":2,"conditions":[` + leaf("1", "B") + `,` + leaf("1", "C") + `]}]},` + hide + `}]}]}`
	if rules := ParsePageBusinessRules(mixed, nil); rules[0].ConditionIssue != ConditionIssueMixedAndOr || rules[0].Condition != nil {
		t.Errorf("mixed AND/OR = %+v", rules[0])
	}
	unknown := `{"rules":[{"caption":"c","cases":[{"condition":{"logicalOperation":1,"conditions":[` + leaf("99", "A") + `]},` + hide + `}]}]}`
	if rules := ParsePageBusinessRules(unknown, nil); rules[0].ConditionIssue != ConditionIssueUnrecognizedComparison {
		t.Errorf("unknown comparison = %+v", rules[0])
	}
	twoCases := `{"rules":[{"uId":"u1","cases":[{` + hide + `},{` + hide + `}]}]}`
	rules = ParsePageBusinessRules(twoCases, []AddonResource{{Key: "BusinessRules.u1.Caption", FirstValue: strp("From resources")}})
	if len(rules) != 2 || deref(rules[1].Caption) != "From resources (case 2)" {
		t.Errorf("cases = %+v", rules)
	}
	for _, empty := range []string{"", "not json", `{"typeName":"x"}`} {
		if len(ParsePageBusinessRules(empty, nil)) != 0 {
			t.Errorf("%q gave rules", empty)
		}
	}
}
