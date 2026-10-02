package creatio

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

const pageBody = `define("Contacts_FormPage", /**SCHEMA_DEPS*/[]/**SCHEMA_DEPS*/, function/**SCHEMA_ARGS*/()/**SCHEMA_ARGS*/ {
	return {
		viewConfigDiff: /**SCHEMA_VIEW_CONFIG_DIFF*/[
			{"operation": "insert", "name": "Тест", "values": {"type": "crt.Button",}, "parentName": "Main"},
			// comment
			{operation: 'merge', name: 'Header', values: {visible: false}},
		]/**SCHEMA_VIEW_CONFIG_DIFF*/,
		viewModelConfigDiff: /**SCHEMA_VIEW_MODEL_CONFIG_DIFF*/[]/**SCHEMA_VIEW_MODEL_CONFIG_DIFF*/,
		modelConfigDiff: /**SCHEMA_MODEL_CONFIG_DIFF*/[{"operation": "merge"}]/**SCHEMA_MODEL_CONFIG_DIFF*/,
		handlers: /**SCHEMA_HANDLERS*/[{request: "crt.SaveRecordRequest", handler: async (r, next) => { return next?.handle(r); }}]/**SCHEMA_HANDLERS*/,
		converters: /**SCHEMA_CONVERTERS*/{}/**SCHEMA_CONVERTERS*/,
		validators: /**SCHEMA_VALIDATORS*/{}/**SCHEMA_VALIDATORS*/
	};
});`

func TestGetPageReadsHierarchyDesignPackageAndOwnBody(t *testing.T) {
	var hierarchyRequests []map[string]any
	server := groupCServer(t, func(path string, body map[string]any) (int, string) {
		switch path {
		case "/0/DataService/json/SyncReply/SelectQuery":
			items := body["filters"].(map[string]any)["items"].(map[string]any)
			switch {
			case items["filter0"] != nil:
				return http.StatusOK, `{"success":true,"rows":[{"Name":"Contacts_FormPage","UId":"leaf-uid","PackageName":"Leaf","PackageUId":"leaf-pkg","ParentSchemaName":"Contacts_FormPage"}]}`
			case items["byUId"] != nil:
				return http.StatusOK, `{"success":true,"rows":[{"Checksum":"abc","ModifiedOn":"2026-10-02T22:36:48.927"}]}`
			}
		case "/0/ServiceModel/ApplicationPackagesService.svc/GetDesignPackageUId":
			return http.StatusOK, `{"success":true,"uId":"design-pkg"}`
		case "/0/ServiceModel/ClientUnitSchemaDesignerService.svc/GetParentSchemas":
			hierarchyRequests = append(hierarchyRequests, body)
			encodedBody, _ := json.Marshal(pageBody)
			return http.StatusOK, `{"success":true,"values":[
				{"uId":"own-uid","name":"Contacts_FormPage","package":{"uId":"design-pkg","name":"Design"},"schemaType":9,"body":` + string(encodedBody) + `},
				{"uId":"root-uid","name":"Contacts_FormPage","package":{"uId":"base-pkg","name":"Base"},"schemaType":9,"body":""}]}`
		case "/0/ServiceModel/PackageService.svc/GetPackageProperties":
			if body != nil {
				t.Errorf("package properties body must be a JSON string, got object %#v", body)
			}
			return http.StatusOK, `{"success":true,"package":{"name":"Design"}}`
		}
		t.Errorf("unexpected request %s %#v", path, body)
		return http.StatusNotFound, ""
	})
	defer server.Close()
	result := newFormsTestClient(t, server.URL).GetPage(context.Background(), PageGetRequest{SchemaName: "Contacts_FormPage"})
	if !result.Success {
		t.Fatalf("result = %#v", result)
	}
	if len(hierarchyRequests) != 2 || hierarchyRequests[0]["schemaUId"] != "leaf-uid" || hierarchyRequests[1]["schemaUId"] != "root-uid" ||
		hierarchyRequests[0]["packageUId"] != "design-pkg" || hierarchyRequests[0]["useFullHierarchy"] != true {
		t.Fatalf("hierarchy requests = %#v", hierarchyRequests)
	}
	page := result.Page
	if page.SchemaUID != "own-uid" || page.RootSchemaUID != "root-uid" || derefString(page.DesignPackageName) != "Design" ||
		page.WillCreateReplacingInDesignPackage || page.SchemaType != "web" || page.CurrentLeafPackageName != "Design" {
		t.Fatalf("page = %#v", page)
	}
	summary := page.OwnBodySummary
	if summary.ViewConfigDiffOperations != 2 || summary.ModelConfigDiffOperations != 1 || summary.HandlerEntries != 1 ||
		len(summary.HandlerRequests) != 1 || summary.HandlerRequests[0] != "crt.SaveRecordRequest" ||
		derefString(summary.ViewConfigDiffOps[0].Type) != "crt.Button" || derefString(summary.ViewConfigDiffOps[1].Operation) != "merge" {
		t.Fatalf("summary = %#v", summary)
	}
	if summary.BodyLength != len([]rune(pageBody)) {
		t.Fatalf("bodyLength = %d, want UTF-16 length %d", summary.BodyLength, len([]rune(pageBody)))
	}
	if !result.Editable.EditableSchemaExists || derefString(result.Editable.Checksum) != "abc" {
		t.Fatalf("editable = %#v", result.Editable)
	}
}

func TestGetPageOperationCountsKeepFirstSeenOrder(t *testing.T) {
	counts := operationCounts([]PageOperation{{Operation: stringPointer("merge")}, {Operation: stringPointer("insert")}, {}, {Operation: stringPointer("merge")}})
	encoded, _ := counts.MarshalJSON()
	if string(encoded) != `{"merge":2,"insert":1,"unknown":1}` {
		t.Fatalf("counts = %s", encoded)
	}
}

func TestGetPageReportsUnknownSchema(t *testing.T) {
	server := groupCServer(t, func(path string, body map[string]any) (int, string) {
		return http.StatusOK, `{"success":true,"rows":[]}`
	})
	defer server.Close()
	result := newFormsTestClient(t, server.URL).GetPage(context.Background(), PageGetRequest{SchemaName: "ZzNope_FormPage"})
	if result.Success || result.Error != "Schema 'ZzNope_FormPage' not found" {
		t.Fatalf("result = %#v", result)
	}
}

func TestGetPageFailsOnUnparsableSection(t *testing.T) {
	_, err := parsePageBody(`define("X", [], function() { return { viewConfigDiff: /**SCHEMA_VIEW_CONFIG_DIFF*/[{"a": }]/**SCHEMA_VIEW_CONFIG_DIFF*/ }; });`)
	if err == nil || !strings.HasPrefix(err.Error(), "Failed to parse schema section 'SCHEMA_VIEW_CONFIG_DIFF'") {
		t.Fatalf("err = %v", err)
	}
}
