package creatio

import (
	"context"
	"encoding/json"
	"testing"
)

func TestProcessPageProjectFollowsTheDesignerRules(t *testing.T) {
	bundle := mustJNode(t, `{
		"viewConfig": [
			{"type": "crt.Button", "name": "SaveButton", "caption": "#ResourceString(SaveCaption)#"},
			{"type": "crt.FlexContainer", "items": [
				{"type": "crt.Button", "name": "SaveButton", "caption": "#ResourceString(SaveCaption)#", "clicked": {"request": "crt.SaveRecordRequest"}},
				{"type": "crt.Button", "name": "RunButton", "caption": "Run", "clicked": {"request": "crt.RunBusinessProcessRequest"}},
				{"type": "crt.Button", "caption": "$Resources.Strings.Actions", "clickMode": "menu", "menuItems": [
					{"name": "Leaf1", "caption": "One", "clicked": {"request": "crt.ClosePageRequest"}},
					{"caption": "Group", "items": [{"name": "Leaf2", "caption": "#ResourceString(Missing)#"}]}
				]}
			]}
		],
		"modelConfig": {"dataSources": {
			"PDS": {"type": "crt.EntityDataSource", "scope": "page", "config": {"entitySchemaName": "Account"}},
			"GridDS": {"type": "crt.EntityDataSource", "scope": "viewElement", "config": {"entitySchemaName": "Contact"}},
			"Other": {"type": "crt.ProxyDataSource", "scope": "page", "config": {"entitySchemaName": "Lead"}}
		}},
		"resources": {"strings": {"SaveCaption": {"en-US": "Save", "uk-UA": "Зберегти"}, "Actions": {"de-DE": "Aktionen"}}}
	}`)
	buttons, sources, err := processPageProject(bundle, "uk-UA")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := json.Marshal(buttons)
	want := `[{"name":"SaveButton","caption":"Зберегти | SaveButton","event":"clicked","requests":["crt.SaveRecordRequest"]},` +
		`{"name":"RunButton","caption":"Run | RunButton","event":"clicked","requests":["crt.RunBusinessProcessRequest"]},` +
		`{"name":"Leaf1","caption":"Aktionen | One | Leaf1","event":"clicked","requests":["crt.ClosePageRequest"]},` +
		`{"name":"Leaf2","caption":"Aktionen | Group | #ResourceString(Missing)# | Leaf2","event":"clicked","requests":[]}]`
	if string(got) != want {
		t.Fatalf("buttons = %s", got)
	}
	if processPageIsCandidate(buttons[1]) || !processPageIsCandidate(buttons[3]) {
		t.Fatalf("candidate rule broken: %#v", buttons)
	}
	if len(sources) != 1 || sources[0] != (ProcessPageDataSource{Name: "PDS", EntitySchemaName: "Account"}) {
		t.Fatalf("data sources = %#v", sources)
	}
}

func TestProcessPageTypeUsesLabelThenNumericThenBody(t *testing.T) {
	classic := 0
	cases := []struct {
		page PageGetResult
		want string
	}{
		{PageGetResult{fullPage: &PageMetadata{SchemaType: "web"}}, "web"},
		{PageGetResult{fullPage: &PageMetadata{SchemaType: "unknown", SchemaTypeValue: &classic}, rawBody: "viewConfigDiff"}, "unknown"},
		{PageGetResult{fullPage: &PageMetadata{SchemaType: "unknown"}, rawBody: ` {"viewConfigDiff":[]}`}, "mobile"},
		{PageGetResult{fullPage: &PageMetadata{SchemaType: "unknown"}, rawBody: `define("X", [], function() { return { viewConfigDiff: [] }; });`}, "web"},
	}
	for index, c := range cases {
		if got := processPageType(c.page); got != c.want {
			t.Errorf("case %d = %s, want %s", index, got, c.want)
		}
	}
}

func TestGetProcessPageFactsReportsAPageThatCannotBeRead(t *testing.T) {
	client, requests := groupIServer(t, func(request groupIRequest) (int, string) {
		return 200, `{"success":true,"rows":[]}`
	})
	result := client.GetProcessPageFacts(context.Background(), "UsrNoPage", "")
	if result.Success || result.SchemaName != "UsrNoPage" || result.Error == "" {
		t.Fatalf("result = %#v", result)
	}
	if len(*requests) == 0 || (*requests)[0].Root != "SysSchema" || (*requests)[0].Filters["Name"] != "UsrNoPage" {
		t.Fatalf("requests = %#v", *requests)
	}
	if missing := client.GetProcessPageFacts(context.Background(), " ", ""); missing.Error != "schema-name is required." {
		t.Fatalf("missing name = %#v", missing)
	}
}
