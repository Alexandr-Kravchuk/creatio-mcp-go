package creatio

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func relatedAddonTestStand(t *testing.T, saveAnswer string, saved *map[string]any, calls *[]string) func(string, map[string]any) (int, string) {
	return func(path string, body map[string]any) (int, string) {
		*calls = append(*calls, path)
		switch path {
		case "/0/DataService/json/SyncReply/SelectQuery":
			if body["rootSchemaName"] == "SysPackage" {
				return http.StatusOK, `{"success":true,"rows":[{"UId":"11111111-1111-1111-1111-111111111111"}]}`
			}
			filters, _ := json.Marshal(body["filters"])
			if strings.Contains(string(filters), "SysPackage.UId") {
				return http.StatusOK, `{"success":true,"rows":[{"UId":"22222222-2222-2222-2222-222222222222"}]}`
			}
			return http.StatusOK, `{"success":true,"rows":[]}`
		case "/0/ServiceModel/EntitySchemaDesignerService.svc/GetSchemaDesignItem":
			return http.StatusOK, `{"success":true,"schema":{"uId":"33333333-3333-3333-3333-333333333333","name":"UsrEntity",` +
				`"parentSchema":{"uId":"44444444-4444-4444-4444-444444444444"},"columns":[{"uId":"55555555-5555-5555-5555-555555555555"}]}}`
		case "/0/ServiceModel/ClientUnitSchemaDesignerService.svc/GetParentSchemas":
			return http.StatusOK, `{"success":true,"values":[{"uId":"22222222-2222-2222-2222-222222222222","name":"UsrPage","package":{"uId":"11111111-1111-1111-1111-111111111111"}}]}`
		case "/0/ServiceModel/AddonSchemaDesignerService.svc/GetSchema":
			if body["addonName"] != "RelatedPage" || body["targetSchemaUId"] != "33333333-3333-3333-3333-333333333333" ||
				body["targetParentSchemaUId"] != "44444444-4444-4444-4444-444444444444" || body["useFullHierarchy"] != true {
				t.Errorf("addon read %#v", body)
			}
			return http.StatusOK, `{"success":true,"schema":{"metaData":"{\"Keep\":1,\"Pages\":[]}","resources":[],` +
				`"targetSchemaUId":"66666666-6666-6666-6666-666666666666","uId":"addon"}}`
		case "/0/ServiceModel/AddonSchemaDesignerService.svc/SaveSchema":
			*saved = body
			return http.StatusOK, saveAnswer
		case "/0/rest/WorkplaceService/ResetScriptCache":
			return http.StatusOK, `{}`
		case "/0/ServiceModel/WorkspaceExplorerService.svc/BuildConfiguration":
			return http.StatusOK, `{"success":true}`
		}
		t.Errorf("unexpected %s", path)
		return http.StatusNotFound, ""
	}
}

func TestCreateRelatedPageAddonSavesPagesAndRebuilds(t *testing.T) {
	var saved map[string]any
	var calls []string
	server := groupCServer(t, relatedAddonTestStand(t, `{"success":true}`, &saved, &calls))
	defer server.Close()
	yes := true
	result := newFormsTestClient(t, server.URL).CreateRelatedPageAddon(context.Background(), CreateRelatedPageAddonRequest{
		EntitySchemaName: "UsrEntity", PackageName: "Pkg", TypeColumnUID: "55555555555555555555555555555555",
		Pages: []*RelatedPageSpec{{PageSchemaName: "UsrPage", IsDefault: &yes, RoleName: "All employees"},
			{PageSchemaUID: "77777777-7777-7777-7777-777777777777", IsDefault: &yes, RoleName: "all external users"}}})
	if !result.Success || result.PageCount != 2 || result.EntitySchemaUID != "66666666-6666-6666-6666-666666666666" || result.AddonName != "RelatedPage" {
		t.Fatalf("result %#v", result)
	}
	var metadata map[string]any
	if err := json.Unmarshal([]byte(saved["metaData"].(string)), &metadata); err != nil {
		t.Fatal(err)
	}
	pages := metadata["Pages"].([]any)
	first, second := pages[0].(map[string]any), pages[1].(map[string]any)
	if metadata["Keep"] != float64(1) || metadata["TypeColumnUId"] != "55555555-5555-5555-5555-555555555555" ||
		first["PageSchemaUId"] != "22222222-2222-2222-2222-222222222222" || first["Role"] != "a29a3ba5-4b0d-de11-9a51-005056c00008" ||
		second["Role"] != "720b771c-e7a7-4f31-9cfb-52cd21c3739f" || saved["uId"] != "addon" {
		t.Fatalf("saved %#v", saved)
	}
	if last := calls[len(calls)-1]; last != "/0/ServiceModel/WorkspaceExplorerService.svc/BuildConfiguration" {
		t.Fatalf("calls %v", calls)
	}
}

func TestCreateRelatedPageAddonReportsSaveFailureAndValidation(t *testing.T) {
	var saved map[string]any
	var calls []string
	server := groupCServer(t, relatedAddonTestStand(t, `{"success":true,"value":false,"errorInfo":{"message":"denied"}}`, &saved, &calls))
	defer server.Close()
	yes := true
	client := newFormsTestClient(t, server.URL)
	result := client.CreateRelatedPageAddon(context.Background(), CreateRelatedPageAddonRequest{EntitySchemaName: "UsrEntity",
		PackageName: "Pkg", Pages: []*RelatedPageSpec{{PageSchemaName: "UsrPage", IsDefault: &yes}}})
	if result.Success || result.Error != "denied" {
		t.Fatalf("result %#v", result)
	}
	unknownColumn := client.CreateRelatedPageAddon(context.Background(), CreateRelatedPageAddonRequest{EntitySchemaName: "UsrEntity",
		PackageName: "Pkg", TypeColumnUID: "88888888-8888-8888-8888-888888888888", Pages: []*RelatedPageSpec{{PageSchemaName: "UsrPage", IsDefault: &yes}}})
	if !strings.HasPrefix(unknownColumn.Error, "type-column-uid '88888888-8888-8888-8888-888888888888' is not a column of object 'UsrEntity'") {
		t.Fatalf("type column %#v", unknownColumn)
	}
	if problem := RelatedPageAddonArgumentFailure(CreateRelatedPageAddonRequest{EntitySchemaName: "E", PackageName: "P", PagesMissing: true}); problem != "pages is required (send an empty list to clear all bindings / reset to inline)" {
		t.Fatal(problem)
	}
}
