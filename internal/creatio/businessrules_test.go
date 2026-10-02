package creatio

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testAddonMetadata = `{"typeName":"Terrasoft.Core.BusinessRules.BusinessRules","rules":[
 {"uId":"11111111-1111-1111-1111-111111111111","name":"BusinessRule_ro","enabled":true,"cases":[{
  "condition":{"typeName":"Terrasoft.Core.BusinessRules.Models.Conditions.BusinessRuleGroupCondition","logicalOperation":2,"conditions":[
   {"typeName":"Terrasoft.Core.BusinessRules.Models.Conditions.BusinessRuleCondition","uId":"c1","comparisonType":2,
    "leftExpression":{"typeName":"Terrasoft.Core.BusinessRules.Models.Expressions.BusinessRuleAttributeExpression","scopeId":"null","path":"Type","uId":"l1"},
    "rightExpression":{"typeName":"Terrasoft.Core.BusinessRules.Models.Expressions.BusinessRuleValueExpression","value":{"b":1,"a":2},"uId":"r1"}}]},
  "actions":[{"typeName":"Terrasoft.Core.BusinessRules.Models.Actions.BusinessRuleActionReadonlyElement","items":"Name, Phone","uId":"a1"}]}]},
 {"uId":"22222222-2222-2222-2222-222222222222","name":"BusinessRule_filter","cases":[{
  "actions":[{"typeName":"Terrasoft.Core.BusinessRules.Models.Actions.BusinessRuleActionFilterLookup","uId":"a2",
   "leftExpression":{"path":"City","filterExpression":"Country"},"rightExpression":{"path":"Country","filterExpression":"null"}}]}]},
 {"uId":"33333333-3333-3333-3333-333333333333","parentUId":"22222222-2222-2222-2222-222222222222","name":"BusinessRule_filter_ClearValue","cases":[]}
]}`

func TestReadEntityBusinessRulesSendsAddonRequestAndConvertsRules(t *testing.T) {
	var designRequest, addonRequest map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		switch r.URL.Path {
		case "/ServiceModel/AuthService.svc/Login":
			_, _ = w.Write([]byte(`{"Code":0}`))
		case "/0/DataService/json/SyncReply/SelectQuery":
			_, _ = w.Write([]byte(`{"success":true,"rows":[{"Name":"Custom","UId":"{AAAAAAAA-0000-0000-0000-000000000001}"}]}`))
		case "/0/ServiceModel/EntitySchemaDesignerService.svc/GetSchemaDesignItem":
			_ = json.Unmarshal(body, &designRequest)
			_, _ = w.Write([]byte(`{"success":true,"schema":{"uId":"bbbbbbbb-0000-0000-0000-000000000002","parentSchema":null}}`))
		case "/0/ServiceModel/AddonSchemaDesignerService.svc/GetSchema":
			_ = json.Unmarshal(body, &addonRequest)
			metadata, _ := json.Marshal(testAddonMetadata)
			_, _ = w.Write([]byte(`{"success":true,"schema":{"metaData":` + string(metadata) + `,"resources":[` +
				`{"key":"AddonConfig.Rules.11111111-1111-1111-1111-111111111111.Caption","value":[{"key":"uk-UA","value":"x"},{"key":"en-US","value":"Read only"}]}]}}`))
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	result := newFormsTestClient(t, server.URL).ReadEntityBusinessRules(context.Background(), BusinessRulesReadRequest{PackageName: "custom", SchemaName: " Account "})
	if result.Error != "" || result.Count != 2 {
		t.Fatalf("result = %#v", result)
	}
	if designRequest["name"] != "Account" || designRequest["packageUId"] != "aaaaaaaa-0000-0000-0000-000000000001" || designRequest["useFullHierarchy"] != true {
		t.Fatalf("design request = %#v", designRequest)
	}
	if addonRequest["addonName"] != "BusinessRule" || addonRequest["targetSchemaUId"] != "bbbbbbbb-0000-0000-0000-000000000002" ||
		addonRequest["targetParentSchemaUId"] != emptyGUID || addonRequest["targetSchemaManagerName"] != "EntitySchemaManager" {
		t.Fatalf("addon request = %#v", addonRequest)
	}
	encoded, _ := json.Marshal(result.Rules)
	for _, want := range []string{
		`"caption":"Read only"`, `"logicalOperation":"OR"`, `"path":"null.Type"`, `"comparisonType":"equal"`,
		`"value":{"b":1,"a":2}`, `"type":"make-read-only","items":["Name","Phone"]`,
		`"type":"apply-filter","target":"City","targetFilterPath":"Country","source":"Country","clearValue":true,"populateValue":false`,
		`"conditions":[]`, `"enabled":true`,
	} {
		if !strings.Contains(string(encoded), want) {
			t.Errorf("rules missing %s:\n%s", want, encoded)
		}
	}
}

func TestReadEntityBusinessRulesReportsMissingPackageInsideEnvelope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/Login") {
			_, _ = w.Write([]byte(`{"Code":0}`))
			return
		}
		_, _ = w.Write([]byte(`{"success":true,"rows":[{"Name":"Other","UId":"aaaaaaaa-0000-0000-0000-000000000001"}]}`))
	}))
	defer server.Close()
	result := newFormsTestClient(t, server.URL).ReadEntityBusinessRules(context.Background(), BusinessRulesReadRequest{PackageName: "Missing", SchemaName: "Account"})
	encoded, _ := json.Marshal(result)
	if string(encoded) != `{"count":0,"rules":[],"error":"Package 'Missing' was not found."}` {
		t.Fatalf("envelope = %s", encoded)
	}
	if got := missingBusinessRuleFields(" ", "", "page-schema-name"); got != "package-name, page-schema-name are required." {
		t.Fatalf("missing fields = %q", got)
	}
}

func TestReadPageBusinessRulesPassesPageAsAddonParent(t *testing.T) {
	var addonRequest map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		switch r.URL.Path {
		case "/ServiceModel/AuthService.svc/Login":
			_, _ = w.Write([]byte(`{"Code":0}`))
		case "/0/DataService/json/SyncReply/SelectQuery":
			if strings.Contains(string(body), `"SysPackage"`) {
				_, _ = w.Write([]byte(`{"success":true,"rows":[{"Name":"Custom","UId":"aaaaaaaa-0000-0000-0000-000000000001"}]}`))
				return
			}
			_, _ = w.Write([]byte(`{"success":true,"rows":[{"UId":"cccccccc-0000-0000-0000-000000000003"}]}`))
		case "/0/ServiceModel/ClientUnitSchemaDesignerService.svc/GetParentSchemas":
			_, _ = w.Write([]byte(`{"success":true,"values":[{"uId":"dddddddd-0000-0000-0000-000000000004","name":"Page"}]}`))
		case "/0/ServiceModel/AddonSchemaDesignerService.svc/GetSchema":
			_ = json.Unmarshal(body, &addonRequest)
			_, _ = w.Write([]byte(`{"success":true,"schema":{"metaData":"","resources":[]}}`))
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	result := newFormsTestClient(t, server.URL).ReadPageBusinessRules(context.Background(), BusinessRulesReadRequest{PackageName: "Custom", SchemaName: "Page"})
	if result.Error != "" || result.Count != 0 || result.Rules == nil {
		t.Fatalf("result = %#v", result)
	}
	if addonRequest["targetParentSchemaUId"] != "dddddddd-0000-0000-0000-000000000004" ||
		addonRequest["targetSchemaManagerName"] != "ClientUnitSchemaManager" || normalizeGUID(addonRequest["targetSchemaUId"].(string)) == "" {
		t.Fatalf("addon request = %#v", addonRequest)
	}
}

func TestDecompileStaticFilterKeepsItemOrderAndShapes(t *testing.T) {
	envelope := `{"filterType":6,"logicalOperation":1,"items":{
	 "z":{"filterType":2,"comparisonType":2,"leftExpression":{"columnPath":"Email"}},
	 "a":{"filterType":4,"comparisonType":3,"leftExpression":{"columnPath":"Type"},"rightExpressions":[{"parameter":{"value":{"Name":"Customer","Id":"1"}}}]},
	 "m":{"filterType":1,"comparisonType":7,"leftExpression":{"expressionType":0,"columnPath":"Amount"},"rightExpression":{"expressionType":2,"parameter":{"value":10.50}}},
	 "d":{"filterType":1,"comparisonType":3,"leftExpression":{"expressionType":0,"columnPath":"CreatedOn"},"rightExpression":{"expressionType":1,"macrosType":24,"functionArgument":{"parameter":{"value":3}}}},
	 "e":{"filterType":5,"comparisonType":15,"leftExpression":{"columnPath":"[Activity:Account].Id"}},
	 "g":{"filterType":6,"logicalOperation":0,"items":{}}}}`
	filter, err := decompileStaticFilter(envelope)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(filter)
	want := `{"logicalOperation":"OR","filters":[` +
		`{"columnPath":"Email","comparisonType":"IS_NOT_NULL"},` +
		`{"columnPath":"Type","comparisonType":"EQUAL","value":"Customer"},` +
		`{"columnPath":"Amount","comparisonType":"GREATER","value":10.50},` +
		`{"columnPath":"CreatedOn","comparisonType":"EQUAL","valueMacros":"NextNDays","valueMacrosArgument":3}],` +
		`"groups":[{"logicalOperation":"AND"}],` +
		`"backwardReferenceFilters":[{"referenceColumnPath":"[Activity:Account]","comparisonType":"EXISTS"}]}`
	if string(encoded) != want {
		t.Fatalf("filter =\n%s\nwant\n%s", encoded, want)
	}
	if _, err := decompileStaticFilter(`{"items":{"x":{"filterType":9}}}`); err == nil || !strings.Contains(err.Error(), "unsupported filterType '9'") {
		t.Fatalf("unsupported filter error = %v", err)
	}
}

func TestConvertBusinessRulesRejectsMultiCaseRule(t *testing.T) {
	_, err := convertBusinessRules(`{"rules":[{"name":"R1","cases":[{},{}]}]}`, nil)
	if err == nil || err.Error() != "Business rule 'R1' cannot be represented in the rule contract: only single-case rules are supported." {
		t.Fatalf("error = %v", err)
	}
}
