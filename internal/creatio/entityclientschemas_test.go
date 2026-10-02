package creatio

import (
	"context"
	"net/http"
	"testing"
)

func TestListEntityClientSchemasResolvesSectionsEditPagesAndTypeNames(t *testing.T) {
	const typeColumn = "11111111-1111-1111-1111-111111111111"
	var inFilterValues []any
	server := groupCServer(t, func(path string, body map[string]any) (int, string) {
		switch path {
		case "/0/DataService/json/SyncReply/RuntimeEntitySchemaRequest":
			if body["Name"] == "Activity" {
				return http.StatusOK, `{"success":true,"schema":{"name":"Activity","columns":{"items":{"t":{"uId":"` + typeColumn + `","name":"Type","referenceSchemaName":"ActivityType"}}}}}`
			}
			return http.StatusOK, `{"success":true,"schema":{"name":"ActivityType","primaryDisplayColumnName":"Name","columns":{"items":{}}}}`
		case "/0/DataService/json/SyncReply/SelectQuery":
		default:
			t.Errorf("unexpected route %s", path)
			return http.StatusNotFound, ""
		}
		switch body["rootSchemaName"] {
		case "SysSchema":
			items := body["filters"].(map[string]any)["items"].(map[string]any)
			if byUID, ok := items["byUId"].(map[string]any); ok {
				if byUID["filterType"] != float64(4) {
					t.Errorf("schema meta filter = %#v", byUID)
				}
				return http.StatusOK, `{"success":true,"rows":[{"UId":"card-uid","Name":"ActivityPageV2","ParentName":"BaseModulePageV2"},{"UId":"form-uid","Name":"Tasks_FormPage","ParentName":"PageWithTabsFreedomTemplate"}]}`
			}
			return http.StatusOK, `{"success":true,"rows":[{"UId":"layer-uid","ExtendParent":true},{"UId":"entity-uid","ExtendParent":false}]}`
		case "SysModule":
			return http.StatusOK, `{"success":true,"rows":[{"Caption":"Tasks","Code":"Activity","SectionSchemaUId":"00000000-0000-0000-0000-000000000000","CardSchemaUId":"FORM-UID","TypeColumnUId":"` + typeColumn + `"}]}`
		case "SysModuleEdit":
			return http.StatusOK, `{"success":true,"rows":[{"TypeColumnValue":"fbe0acdc-cfc0-df11-b00f-001d60e938c6","CardSchemaUId":"card-uid","MiniPageSchemaUId":"","MiniPageModes":"","TypeColumnUId":"` + typeColumn + `"}]}`
		case "ActivityType":
			inFilterValues = body["filters"].(map[string]any)["items"].(map[string]any)["Id"].(map[string]any)["rightExpressions"].([]any)
			return http.StatusOK, `{"success":true,"rows":[{"Id":"FBE0ACDC-CFC0-DF11-B00F-001D60E938C6","DisplayValue":"Task"}]}`
		}
		t.Errorf("unexpected root %v", body["rootSchemaName"])
		return http.StatusNotFound, ""
	})
	defer server.Close()
	result := newFormsTestClient(t, server.URL).ListEntityClientSchemas(context.Background(), "Activity")
	if !result.Success || result.EntityUID != "entity-uid" || len(result.Sections) != 1 || len(result.EditPages) != 1 {
		t.Fatalf("result = %#v", result)
	}
	section := result.Sections[0]
	if derefString(section.CardSchema) != "Tasks_FormPage" || section.Kind != "freedom" || !section.IsTyped || section.SectionSchema != nil {
		t.Fatalf("section = %#v", section)
	}
	page := result.EditPages[0]
	if page.Kind != "classic" || page.MiniPageKind != "unknown" || derefString(page.TypeColumnDisplayValue) != "Task" {
		t.Fatalf("edit page = %#v", page)
	}
	if len(inFilterValues) != 1 || result.Note != entityClientSchemaNote {
		t.Fatalf("lookup values = %#v, note = %q", inFilterValues, result.Note)
	}
}

func TestListEntityClientSchemasReportsUnknownEntity(t *testing.T) {
	server := groupCServer(t, func(path string, body map[string]any) (int, string) {
		return http.StatusOK, `{"success":true,"rows":[]}`
	})
	defer server.Close()
	result := newFormsTestClient(t, server.URL).ListEntityClientSchemas(context.Background(), "ZzNope")
	if result.Success || result.Error != "Entity 'ZzNope' not found (ManagerName='EntitySchemaManager')" {
		t.Fatalf("result = %#v", result)
	}
}
