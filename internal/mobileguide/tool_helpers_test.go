package mobileguide

import (
	"strings"
	"testing"
)

// Cases from clio's MobilePageConversionGuideToolTests.cs plus the helpers' own branches.

func TestDefaultTemplateRuleFallsBackWithoutCorrespondence(t *testing.T) {
	rules := &Rules{DefaultMobileTemplate: strp("BaseMobilePageTemplate"),
		Templates: []*TemplateMappingRule{{Web: strp("ListPageV3Template"), Mobile: strp("BaseMobileListTemplate")}}}
	if ResolveTemplateRule(rules, strp("UsrCustomTemplate")) != nil {
		t.Fatal("an unmatched web template must resolve to no rule")
	}
	fallback := DefaultTemplateRule(rules)
	if fallback == nil || deref(fallback.Mobile) != "BaseMobilePageTemplate" || fallback.Web != nil ||
		len(fallback.Containers) != 0 || len(fallback.Components) != 0 || fallback.Note != nil {
		t.Fatalf("fallback = %+v", fallback)
	}
	if DefaultTemplateRule(&Rules{}) != nil {
		t.Fatal("no declared default must give no rule")
	}
	if got := ResolveTemplateRule(rules, strp("listpagev3template")); got == nil || deref(got.Mobile) != "BaseMobileListTemplate" {
		t.Fatal("rule match ignores case")
	}
}

func TestResolveEffectiveTemplateName(t *testing.T) {
	rules := &Rules{Templates: []*TemplateMappingRule{{Web: strp("PageWithTabsFreedomTemplate"), Mobile: strp("M")}}}
	chain := []*string{strp("UsrX_FormPage"), strp("UsrX_FormPage"), strp("PageWithTabsAndProgressBarTemplate"),
		strp("PageWithTabsFreedomTemplate"), strp("BasePageFreedomTemplate")}
	cases := []struct {
		name, own, parent string
		chain             []*string
		want              string
	}{
		{"matched parent", "UsrX_FormPage", "PageWithTabsFreedomTemplate", nil, "PageWithTabsFreedomTemplate"},
		{"self parent climbs", "UsrX_FormPage", "UsrX_FormPage", chain, "PageWithTabsFreedomTemplate"},
		{"unmapped intermediate climbs", "UsrX_FormPage", "PageWithTabsAndProgressBarTemplate", chain, "PageWithTabsFreedomTemplate"},
		{"first distinct when nothing matches", "UsrX_FormPage", "", []*string{strp("UsrX_FormPage"), strp("Other"), strp("Base")}, "Other"},
		{"raw parent without a chain", "UsrX_FormPage", "Custom", nil, "Custom"},
	}
	for _, c := range cases {
		var parent *string
		if c.parent != "" {
			parent = strp(c.parent)
		}
		if got := deref(ResolveEffectiveTemplateName(strp(c.own), parent, c.chain, rules)); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestContainerAndPositionalMaps(t *testing.T) {
	rule := &TemplateMappingRule{Containers: []*ContainerMappingRule{
		{Web: strp("CardContentWrapper"), Mobile: strp("GeneralTabContainer")},
		{Web: strp("CardContentWrapper:top"), Mobile: strp("Tabs:top")},
		{Web: strp("CardContentWrapper:bottom"), Mobile: strp("Tabs:bottom")},
		{Web: strp(" "), Mobile: strp("X")},
	}, Components: []*ComponentMappingRule{{Web: strp("DataTable"), Mobile: strp("List")}, {Web: strp("Nope")}}}
	names := BuildContainerNameMap(rule)
	if names.len() != 1 || names.value("cardcontentwrapper") != "GeneralTabContainer" {
		t.Fatalf("container map = %v", names.pairs())
	}
	placements := BuildPositionalPlacements(rule)
	if len(placements) != 1 || deref(placements[0].WebAnchor) != "CardContentWrapper" || deref(placements[0].MobileAnchor) != "Tabs" {
		t.Fatalf("placements = %+v", placements)
	}
	components := BuildComponentNameMap(rule)
	if components.len() != 1 || !components.has("datatable") {
		t.Fatal("component map")
	}
	if BuildContainerNameMap(&TemplateMappingRule{}) != nil || BuildPositionalPlacements(nil) != nil || BuildComponentNameMap(nil) != nil {
		t.Fatal("empty rules give nil maps")
	}
}

func TestIsFormPageAndDerivedName(t *testing.T) {
	rules := &Rules{MobileFormPageTemplates: []*string{strp("MobilePageWithTabsFreedomTemplate")}}
	if !IsFormPage(strp("Usr_FormPage"), nil, nil, rules) {
		t.Error("a *FormPage name is a form")
	}
	if !IsFormPage(strp("UsrX"), nil, &TemplateMappingRule{Mobile: strp("mobilepagewithtabsfreedomtemplate")}, rules) {
		t.Error("a listed mobile template is a form")
	}
	if IsFormPage(strp("UsrX"), strp("PageWithTabsFreedomTemplate"), &TemplateMappingRule{Mobile: strp("BaseMobileListTemplate")}, rules) {
		t.Error("a matched rule decides, not the web template name")
	}
	if !IsFormPage(strp("UsrX"), strp("BasePageTemplate"), nil, rules) || IsFormPage(strp("UsrX"), strp("ListPage"), nil, rules) {
		t.Error("the name fallback applies only without a rule")
	}
	for input, want := range map[string]string{"Usr_FormPage": "Usr_MobileFormPage", "Usr_ListPage": "Usr_MobileListPage",
		"UsrX": "UsrX_Mobile", " ": "Mobile_Page", "usr_formpage": "usr_formpage_Mobile"} {
		if got := DeriveMobileSchemaName(strp(input)); got != want {
			t.Errorf("DeriveMobileSchemaName(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestRulesWarningNamesUnpublishedTargets(t *testing.T) {
	rules := &Rules{Requests: []*RequestMappingRule{{Web: strp("crt.A"), Mobile: strp("crt.MA")}, {Web: strp("crt.B"), Mobile: strp("crt.MB")}}}
	if RulesWarning(rules, newSet(true)) != nil {
		t.Error("no registry, no warning")
	}
	warning := RulesWarning(rules, newSet(true, "crt.ma"))
	if warning == nil || !strings.HasPrefix(*warning, "The conversion rules map crt.B -> crt.MB onto mobile request types") {
		t.Errorf("warning = %v", warning)
	}
}
