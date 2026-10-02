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

func TestListPrintablesQueriesMSWordTypeAndFiltersEitherEntityPath(t *testing.T) {
	var query map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ServiceModel/AuthService.svc/Login":
			_, _ = w.Write([]byte(`{"Code":0}`))
		case "/0/DataService/json/SyncReply/SelectQuery":
			body, _ := io.ReadAll(r.Body)
			if err := json.Unmarshal(body, &query); err != nil {
				t.Errorf("decode SelectQuery: %v", err)
			}
			_, _ = w.Write([]byte(`{"success":true,"rows":[
				{"Id":"p3","Caption":"zeta","ConvertInPDF":true,"ShowInCard":true,"ShowInSection":false,"SysEntitySchemaName":"Contact","SysModuleEntityName":""},
				{"Id":"p1","Caption":"Alpha","ConvertInPDF":false,"ShowInCard":false,"ShowInSection":true,"SysEntitySchemaName":"","SysModuleEntityName":"contact"},
				{"Id":"p2","Caption":"Beta","SysEntitySchemaName":"Account","SysModuleEntityName":"Account"}]}`))
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	result := newFormsTestClient(t, server.URL).ListPrintables(context.Background(), " CONTACT ")
	if !result.Success || result.Count != 2 || result.Printables[0].TemplateID != "p1" || result.Printables[1].TemplateID != "p3" {
		t.Fatalf("result = %#v", result)
	}
	first, second := result.Printables[0], result.Printables[1]
	if first.EntitySchemaName != nil || first.ModuleEntitySchemaName == nil || !first.ShowInSection || first.ConvertInPDF {
		t.Fatalf("first = %#v", first)
	}
	if second.EntitySchemaName == nil || *second.EntitySchemaName != "Contact" || !second.ConvertInPDF || !second.ShowInCard {
		t.Fatalf("second = %#v", second)
	}
	if query["rootSchemaName"] != "SysModuleReport" {
		t.Fatalf("root = %#v", query["rootSchemaName"])
	}
	columns := query["columns"].(map[string]any)["items"].(map[string]any)
	module := columns["SysModuleEntityName"].(map[string]any)["expression"].(map[string]any)["columnPath"]
	if module != "SysModule.SysModuleEntity.[SysSchema:UId:SysEntitySchemaUId].Name" {
		t.Fatalf("module column = %#v", module)
	}
	filter := query["filters"].(map[string]any)["items"].(map[string]any)["filter0"].(map[string]any)
	parameter := filter["rightExpression"].(map[string]any)["parameter"].(map[string]any)
	if filter["leftExpression"].(map[string]any)["columnPath"] != "Type.Id" || parameter["value"] != msWordPrintableTypeID || parameter["dataValueType"] != float64(0) {
		t.Fatalf("type filter = %#v", filter)
	}
}

func TestListPrintablesReportsSelectQueryFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/Login") {
			_, _ = w.Write([]byte(`{"Code":0}`))
			return
		}
		_, _ = w.Write([]byte(`{"success":false,"errorInfo":{"message":"denied"}}`))
	}))
	defer server.Close()
	result := newFormsTestClient(t, server.URL).ListPrintables(context.Background(), "")
	if result.Success || result.Error != "SelectQuery failed: denied" || result.Count != 0 || result.Printables == nil {
		t.Fatalf("result = %#v", result)
	}
}
