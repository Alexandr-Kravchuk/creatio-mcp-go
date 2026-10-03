package creatio

import (
	"context"
	"net/http"
	"testing"
)

func TestCreateClientUnitSchemaPayloadAndRefusal(t *testing.T) {
	for _, refuse := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[refuse], func(t *testing.T) {
			saves := 0
			server := groupCServer(t, func(path string, body map[string]any) (int, string) {
				switch path {
				case "/0/DataService/json/SyncReply/SelectQuery":
					if body["rootSchemaName"] == "SysPackage" {
						return http.StatusOK, `{"success":true,"rows":[{"UId":"package"}]}`
					}
					return http.StatusOK, `{"success":true,"rows":[]}`
				case "/0/ServiceModel/ClientUnitSchemaDesignerService.svc/SaveSchema":
					saves++
					if body["managerName"] != "ClientUnitSchemaManager" || body["body"] != "" || body["extendParent"] != false || body["name"] != "UsrParityHelper" {
						t.Errorf("payload %#v", body)
					}
					caption := body["caption"].([]any)[0].(map[string]any)
					if caption["cultureName"] != "en-US" || caption["value"] != "Helper" {
						t.Errorf("caption %#v", caption)
					}
					if refuse {
						return http.StatusOK, `{"success":false,"errorInfo":{"message":"locked"}}`
					}
					return http.StatusOK, `{"success":true}`
				}
				t.Errorf("unexpected %s", path)
				return http.StatusNotFound, ""
			})
			defer server.Close()
			client := newFormsTestClient(t, server.URL)
			result := client.CreateClientUnitSchema(context.Background(), ClientUnitCreateRequest{SchemaName: "UsrParityHelper", PackageName: "Custom", Caption: "Helper", CaptionCulture: "en-US"})
			if saves != 1 || result.Success == refuse {
				t.Fatalf("result=%#v saves=%d", result, saves)
			}
		})
	}
}

func TestUpdateClientUnitSchemaPreservesTopLayerMetadata(t *testing.T) {
	saved := false
	server := groupCServer(t, func(path string, body map[string]any) (int, string) {
		switch path {
		case "/0/DataService/json/SyncReply/SelectQuery":
			return http.StatusOK, `{"success":true,"rows":[{"UId":"base","PackageName":"a","HierarchyLevel":1},{"UId":"top","PackageName":"b","HierarchyLevel":2}]}`
		case "/0/ServiceModel/ClientUnitSchemaDesignerService.svc/GetSchema":
			if body["schemaUId"] != "top" {
				t.Errorf("wrong layer %#v", body)
			}
			return http.StatusOK, `{"success":true,"schema":{"uId":"top","name":"UsrParityHelper","body":"old","messages":[{"name":"Keep"}]}}`
		case "/0/ServiceModel/ClientUnitSchemaDesignerService.svc/SaveSchema":
			saved = true
			if body["body"] != "define(𝒳)" || body["uId"] != "top" || len(body["messages"].([]any)) != 1 {
				t.Errorf("payload %#v", body)
			}
			return http.StatusOK, `{"success":true}`
		}
		t.Errorf("unexpected %s", path)
		return http.StatusNotFound, ""
	})
	defer server.Close()
	client := newFormsTestClient(t, server.URL)
	body := "define(𝒳)"
	result := client.UpdateClientUnitSchema(context.Background(), SchemaBodyUpdateRequest{SchemaName: "UsrParityHelper", Body: &body})
	if !result.Success || !saved || result.BodyLength != 10 {
		t.Fatalf("result %#v saved=%t", result, saved)
	}
}
