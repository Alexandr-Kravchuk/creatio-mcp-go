package mobileguide

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// Ported from clio.tests WebToMobilePageConversionRulesCatalogTests.cs (8.1.0.142).

func str(value *string) string {
	if value == nil {
		return "<null>"
	}
	return *value
}

func TestLoadBundledReturnsSeededTemplatesAndComponents(t *testing.T) {
	rules := BundledRules()
	if str(rules.Version) != "latest" {
		t.Fatalf("version %q", str(rules.Version))
	}
	found := false
	for _, template := range rules.Templates {
		if str(template.Web) == "PageWithTabsFreedomTemplate" && str(template.Mobile) == "MobilePageWithTabsFreedomTemplate" {
			found = true
		}
	}
	if !found {
		t.Fatal("tabbed record page mapping missing")
	}
	grid := false
	for _, component := range rules.Components {
		for _, filter := range component.Filters {
			if str(filter.Type) == "crt.DataGrid" && len(component.ViewConfigTemplates) > 0 {
				grid = true
			}
		}
	}
	if !grid {
		t.Fatal("grid mapping with viewConfigTemplates missing")
	}
}

func leftExpressions(filter *ComponentPropertyFilter) []string {
	switch filter.FilterType {
	case FilterTypeIsEmpty:
		return []string{str(filter.LeftExpression)}
	case FilterTypeGroup:
		var all []string
		for _, item := range filter.Items {
			all = append(all, leftExpressions(item)...)
		}
		return all
	}
	return nil
}

func TestLoadBundledComponentRemovalsAndActionComponents(t *testing.T) {
	rules := BundledRules()
	byType := map[string]*ComponentRemovalRule{}
	for _, rule := range rules.ComponentRemovals {
		byType[str(rule.Type)] = rule
		if str(rule.Reason) != "drop-unsupported-request" {
			t.Fatalf("reason %q", str(rule.Reason))
		}
	}
	if len(byType) != 2 || byType["crt.Button"] == nil || byType["crt.MenuItem"] == nil {
		t.Fatalf("removal types %v", byType)
	}
	if got := strings.Join(leftExpressions(byType["crt.Button"].Filters), ","); got != "clicked,menuItems" && got != "menuItems,clicked" {
		t.Fatalf("button expressions %s", got)
	}
	if got := strings.Join(leftExpressions(byType["crt.MenuItem"].Filters), ","); got != "clicked" {
		t.Fatalf("menu item expressions %s", got)
	}
	if len(rules.ActionComponents) != 2 {
		t.Fatalf("action components %d", len(rules.ActionComponents))
	}
	for _, rule := range rules.ActionComponents {
		if len(rule.ActionPropertyNames) != 1 || str(rule.ActionPropertyNames[0]) != "clicked" {
			t.Fatalf("action rule %s", str(rule.Type))
		}
	}
}

func TestParseRulesDefaultsAndNulls(t *testing.T) {
	rules, err := ParseRules([]byte(`{ "version": "8.3.3", "templates": [], "components": [] }`))
	if err != nil || rules == nil {
		t.Fatal(err)
	}
	if rules.ExcludedComponents == nil || len(rules.ExcludedComponents) != 0 || rules.TabAreaLayers != nil {
		t.Fatal("absent excludedComponents must be empty and absent tabAreaLayers null")
	}
	rules, err = ParseRules([]byte(`{ "tabAreaLayers": { "mainTabContainer": { "namePrefix": "X" } }, "templates": null }`))
	if err != nil {
		t.Fatal(err)
	}
	if str(rules.TabAreaLayers.TabComponentType) != "crt.TabContainer" {
		t.Fatal("tabComponentType falls back to the platform tab type")
	}
	if rules.Templates != nil {
		t.Fatal("explicit null templates stays null")
	}
	if _, err := ParseRules([]byte(`{ "componentPropertyOverrides": [ { "mergeNestedObjects": null } ] }`)); err == nil {
		t.Fatal("null for a non-nullable bool must fail as System.Text.Json fails it")
	}
	if rules, err := ParseRules([]byte("  \n")); err != nil || rules != nil {
		t.Fatal("blank document parses to nil")
	}
}

func TestParseRulesFindsTheDiscriminatorWhereverItSits(t *testing.T) {
	rules, err := ParseRules([]byte(`{ "componentRemovals": [ { "type": "crt.Button", "reason": "drop-unsupported-request",
	    "filters": { "items": [ { "leftExpression": "clicked", "filterType": "IsEmpty" } ],
	                 "logicalOperation": "and", "filterType": "Group" } } ] }`))
	if err != nil {
		t.Fatal(err)
	}
	group := rules.ComponentRemovals[0].Filters
	if group.FilterType != FilterTypeGroup || group.Items[0].FilterType != FilterTypeIsEmpty {
		t.Fatalf("%+v", group)
	}
}

func TestParseRulesRefusals(t *testing.T) {
	cases := []struct{ json, names string }{
		{`{ "componentRemovals": [ { "type": "crt.Button", "filters": { "filterType": "Contains", "leftExpression": "clicked" } } ] }`, "Contains"},
		{`{ "componentRemovals": [ { "filters": { "filterType": "IsEmpty", "leftExpression": "clicked" } } ] }`, "type"},
		{`{ "componentRemovals": [ { "type": "crt.Button" } ] }`, "filters"},
		{`{ "componentRemovals": [ { "type": "crt.Button", "filters": { "filterType": "Group", "items": [] } } ] }`, "items"},
		{`{ "componentRemovals": [ { "type": "crt.Button", "filters": { "filterType": "Group", "items": [ null ] } } ] }`, "null item"},
		{`{ "componentRemovals": [ { "type": "crt.Button", "filters": { "filterType": "IsEmpty" } } ] }`, "leftExpression"},
		{`{ "componentRemovals": [ { "type": "crt.Button", "filters": { "filterType": "Group", "logicalOperation": "any", "items": [ { "filterType": "IsEmpty", "leftExpression": "clicked" } ] } } ] }`, "any"},
		{`{ "actionComponents": [ { "actionPropertyNames": [ "clicked" ] } ] }`, "type"},
		{`{ "actionComponents": [ { "type": "   " } ] }`, "type"},
		{`{ "actionComponents": [ { "type": "crt.Button", "actionPropertyNames": [ "" ] } ] }`, "crt.Button"},
		{`{ "componentRemovals": [ { "type": "crt.Button", "filters": { "leftExpression": "clicked" } } ] }`, "filterType"},
		{`{ "componentRemovals": [ { "type": "crt.Button", "filters": { "FilterType": "IsEmpty", "leftExpression": "clicked" } } ] }`, "filterType"},
		{`{ "componentRemovals": [ { "type": "crt.Button", "filters": { "filterType": "Regex", "leftExpression": "clicked" } } ] }`, "Regex"},
		{`{ "componentRemovals": [ { "type": "crt.Button", "reason": "drop-because-i-said-so", "filters": { "filterType": "IsEmpty", "leftExpression": "clicked" } } ] }`, "drop-because-i-said-so"},
	}
	for _, code := range []string{"drop-excluded-by-rule", "drop-inherited-chrome", "flag-request-unmapped", "drop-request-target-missing"} {
		cases = append(cases, struct{ json, names string }{
			`{ "componentRemovals": [ { "type": "crt.Button", "reason": "` + code + `", "filters": { "filterType": "IsEmpty", "leftExpression": "clicked" } } ] }`, code})
	}
	for _, c := range cases {
		_, err := ParseRules([]byte(c.json))
		if err == nil || !strings.Contains(err.Error(), c.names) {
			t.Errorf("%s: error %v, want mention of %q", c.json, err, c.names)
		}
	}
	if _, err := ParseRules([]byte(`{ "componentRemovals": [ { "type": "crt.Button", "reason": "drop-unsupported-request", "filters": { "filterType": "IsEmpty", "leftExpression": "clicked" } } ] }`)); err != nil {
		t.Fatal(err)
	}
}

func TestGetRulesFallsBackToBundled(t *testing.T) {
	served := func(body string, err error) RulesFetcher {
		return func(context.Context, string) ([]byte, error) { return []byte(body), err }
	}
	rules, err := GetRules(context.Background(), "9.9.9", served("", errors.New("unavailable")))
	if err != nil || rules != BundledRules() {
		t.Fatal("unavailable client must fall back to bundled")
	}
	rules, _ = GetRules(context.Background(), "9.9.9", served(`{ "version": "9.9.9", "templates": [], "components": [] }`, nil))
	if str(rules.Version) != "9.9.9" {
		t.Fatal("served rules must be returned")
	}
	rules, _ = GetRules(context.Background(), "9.9.9", served(`{ "version": "9.9.9", "componentRemovals": [ { "type": "crt.Button", "reason": "drop-invented", "filters": { "filterType": "IsEmpty", "leftExpression": "clicked" } } ] }`, nil))
	if str(rules.Version) == "9.9.9" {
		t.Fatal("an invalid reason code must fall back to bundled")
	}
	if _, err := GetRules(context.Background(), "", served("", context.Canceled)); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation propagates")
	}
}
