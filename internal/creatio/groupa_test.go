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

// groupARoute answers one Creatio route; the decoded request body is passed in for shape checks.
type groupARoute func(t *testing.T, body map[string]any) string

// newGroupAServer serves forms login plus the given routes. A SelectQuery route is chosen by its
// rootSchemaName ("SelectQuery:Contact"); every request body is recorded under the same key.
func newGroupAServer(t *testing.T, routes map[string]groupARoute) (*Client, map[string][]map[string]any) {
	t.Helper()
	seen := map[string][]map[string]any{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ServiceModel/AuthService.svc/Login" {
			_, _ = w.Write([]byte(`{"Code":0}`))
			return
		}
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		key := strings.TrimPrefix(r.URL.Path, "/0/")
		if key == "DataService/json/SyncReply/SelectQuery" {
			key = "SelectQuery:" + body["rootSchemaName"].(string)
		}
		seen[key] = append(seen[key], body)
		route, ok := routes[key]
		if !ok {
			t.Errorf("unexpected request %s", key)
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(route(t, body)))
	}))
	t.Cleanup(server.Close)
	return newFormsTestClient(t, server.URL), seen
}

func groupAFixed(response string) groupARoute {
	return func(*testing.T, map[string]any) string { return response }
}

func groupAFilterItems(body map[string]any) map[string]any {
	return body["filters"].(map[string]any)["items"].(map[string]any)
}

func TestFindAppBatchesSectionsInOneOrQueryAndSorts(t *testing.T) {
	client, seen := newGroupAServer(t, map[string]groupARoute{
		"SelectQuery:SysInstalledApp": groupAFixed(`{"success":true,"rows":[
			{"Id":"a2","Code":"Zeta","Name":"zeta app","Version":"","Description":null},
			{"Id":"a1","Code":"CrtSales","Name":"Sales","Version":"1.0","Description":"Customer deals"}]}`),
		"SelectQuery:ApplicationSection": groupAFixed(`{"success":true,"rows":[
			{"Id":"s2","ApplicationId":"A1","Caption":"Orders","Code":"Order","EntitySchemaName":"Order"},
			{"Id":"s1","ApplicationId":"a1","Caption":"Accounts","Code":"Account","EntitySchemaName":" "}]}`),
	})
	result := client.FindApp(context.Background(), FindAppRequest{})
	if !result.Success || len(result.Applications) != 2 || result.Applications[0].Name != "Sales" {
		t.Fatalf("result = %#v", result)
	}
	sales := result.Applications[0]
	if len(sales.Sections) != 2 || sales.Sections[0].Caption != "Accounts" || sales.Sections[0].EntitySchemaName != nil {
		t.Fatalf("sections are not grouped case-insensitively, sorted and blank-trimmed: %#v", sales.Sections)
	}
	if zeta := result.Applications[1]; zeta.Version != nil || zeta.Sections == nil {
		t.Fatalf("blank version must be absent and sections []: %#v", zeta)
	}
	sectionQuery := seen["SelectQuery:ApplicationSection"][0]
	if sectionQuery["filters"].(map[string]any)["logicalOperation"] != float64(1) || len(groupAFilterItems(sectionQuery)) != 2 {
		t.Fatalf("sections must be loaded by one OR-grouped query: %#v", sectionQuery["filters"])
	}
	filtered := client.FindApp(context.Background(), FindAppRequest{SearchPattern: "DEALS"})
	if len(filtered.Applications) != 1 || filtered.Applications[0].Code != "CrtSales" {
		t.Fatalf("pattern must match the description ignoring case: %#v", filtered)
	}
}

func TestFindAppKeepsAppsWhenSectionsFailAndReportsAppFailure(t *testing.T) {
	client, _ := newGroupAServer(t, map[string]groupARoute{
		"SelectQuery:SysInstalledApp":    groupAFixed(`{"success":true,"rows":[{"Id":"a1","Code":"App","Name":"App"}]}`),
		"SelectQuery:ApplicationSection": groupAFixed(`{"success":false,"errorInfo":{"message":"boom"}}`),
	})
	result := client.FindApp(context.Background(), FindAppRequest{Code: "app"})
	if !result.Success || len(result.Applications) != 1 || len(result.Applications[0].Sections) != 0 {
		t.Fatalf("section failure must degrade to no sections: %#v", result)
	}
	failing, _ := newGroupAServer(t, map[string]groupARoute{
		"SelectQuery:SysInstalledApp": groupAFixed(`{"success":false,"errorInfo":{"message":"denied"}}`),
	})
	if failed := failing.FindApp(context.Background(), FindAppRequest{}); failed.Success || !strings.Contains(failed.Error, "denied") || failed.Applications != nil {
		t.Fatalf("app query failure = %#v", failed)
	}
}

func TestFindEntitySchemasFiltersAndCrossChecksEmptyPattern(t *testing.T) {
	calls := 0
	client, seen := newGroupAServer(t, map[string]groupARoute{
		"SelectQuery:SysSchema": func(t *testing.T, body map[string]any) string {
			calls++
			if calls == 1 {
				return `{"success":true,"rows":[]}`
			}
			return `{"success":true,"rows":[
				{"Name":"UsrTaskStatus","PackageName":"Custom","PackageMaintainer":"Me","ParentSchemaName":"BaseLookup"},
				{"Name":"Contact","PackageName":"CrtBase","PackageMaintainer":"Creatio","ParentSchemaName":""}]}`
		},
	})
	results, err := client.FindEntitySchemas(context.Background(), EntitySchemaSearchRequest{SearchPattern: " taskstat "})
	if err != nil || len(results) != 1 || results[0].SchemaName != "UsrTaskStatus" || results[0].PackageName != "Custom" {
		t.Fatalf("results = %#v, err = %v", results, err)
	}
	first, second := groupAFilterItems(seen["SelectQuery:SysSchema"][0]), groupAFilterItems(seen["SelectQuery:SysSchema"][1])
	if len(first) != 2 || len(second) != 1 {
		t.Fatalf("pattern query must add a contains filter and the cross-check must drop it: %v / %v", first, second)
	}
	contains := first["filter1"].(map[string]any)
	if contains["comparisonType"] != float64(11) || contains["rightExpression"].(map[string]any)["parameter"].(map[string]any)["value"] != "taskstat" {
		t.Fatalf("contains filter = %#v", contains)
	}
	if _, err := client.FindEntitySchemas(context.Background(), EntitySchemaSearchRequest{UID: "nope"}); err == nil || !strings.Contains(err.Error(), "is not a valid Guid") {
		t.Fatalf("invalid uid error = %v", err)
	}
	if _, err := client.FindEntitySchemas(context.Background(), EntitySchemaSearchRequest{}); err == nil {
		t.Fatal("a call without criteria must fail")
	}
}

const contactRuntimeSchema = `{"success":true,"schema":{"name":"Contact","primaryDisplayColumnName":"Name","columns":{"items":{
	"1":{"uId":"c1","name":"Owner","caption":{"en-US":"Owner"},"dataValueType":10,"isIndexed":true,"isValueCloneable":true,
		"referenceSchemaName":"Contact","usageType":0,
		"defValue":{"valueSourceType":3,"valueSource":"4F367CA9-549B-4A1A-B64E-A40123F52AC0","sequenceNumberOfChars":0}},
	"2":{"uId":"c2","name":"Confirmed","caption":{"en-US":"Confirmed"},"dataValueType":12,"usageType":2,
		"defValue":{"valueSourceType":1,"value":true}},
	"3":{"uId":"c3","name":"Type","caption":{"en-US":"Type"},"dataValueType":10,"isInherited":true,"referenceSchemaName":"ContactType",
		"defValue":{"valueSourceType":1,"value":"00000000-0000-0000-0000-00000000000a"}}}}}}`

func TestMergedColumnPropertiesEnrichesSystemValueDefault(t *testing.T) {
	client, seen := newGroupAServer(t, map[string]groupARoute{
		"DataService/json/SyncReply/RuntimeEntitySchemaRequest": groupAFixed(contactRuntimeSchema),
		"ServiceModel/EntitySchemaDesignerService.svc/GetSystemValues": groupAFixed(
			`{"success":true,"items":[{"value":"4f367ca9-549b-4a1a-b64e-a40123f52ac0","displayValue":"Current user contact"}]}`),
	})
	result, err := client.GetEntitySchemaColumnProperties(context.Background(), EntitySchemaColumnPropertiesRequest{SchemaName: "Contact", ColumnName: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	if result.PackageName != mergedSchemaPackageName || result.Source != "own" || result.Type != "Lookup" || result.TrackChanges != nil {
		t.Fatalf("result = %#v", result)
	}
	config := result.DefaultValueConfig
	if config == nil || config.Source != "SystemValue" || config.DisplayValue == nil || *config.DisplayValue != "Current user contact" ||
		*config.ValueSource != "4F367CA9-549B-4A1A-B64E-A40123F52AC0" || *result.DefaultValue != *config.ValueSource {
		t.Fatalf("default value config = %#v", config)
	}
	if request := seen["ServiceModel/EntitySchemaDesignerService.svc/GetSystemValues"][0]; request["dataValueTypeUId"] != "b295071f-7ea9-4e62-8d1a-919bf3732ff2" {
		t.Fatalf("GetSystemValues request = %#v", request)
	}
	if request := seen["DataService/json/SyncReply/RuntimeEntitySchemaRequest"][0]; request["Name"] != "Contact" {
		t.Fatalf("runtime request = %#v", request)
	}
	boolean, err := client.GetEntitySchemaColumnProperties(context.Background(), EntitySchemaColumnPropertiesRequest{SchemaName: "Contact", ColumnName: "Confirmed"})
	if err != nil || *boolean.DefaultValue != "True" || string(boolean.DefaultValueConfig.Value) != "true" || *boolean.UsageType != "None" {
		t.Fatalf("boolean Const default = %#v, err = %v", boolean, err)
	}
}

func TestMergedColumnPropertiesMarksUnresolvedLookupAndMissingColumn(t *testing.T) {
	client, _ := newGroupAServer(t, map[string]groupARoute{
		"DataService/json/SyncReply/RuntimeEntitySchemaRequest": func(t *testing.T, body map[string]any) string {
			if body["Name"] == "ContactType" {
				return `{"success":true,"schema":{"name":"ContactType","primaryDisplayColumnName":"Name","columns":{"items":{}}}}`
			}
			return contactRuntimeSchema
		},
		"SelectQuery:ContactType": groupAFixed(`{"success":true,"rows":[]}`),
	})
	result, err := client.GetEntitySchemaColumnProperties(context.Background(), EntitySchemaColumnPropertiesRequest{SchemaName: "Contact", ColumnName: "Type"})
	if err != nil || result.Source != "inherited" || result.DefaultValueConfig.RecordResolution == nil ||
		*result.DefaultValueConfig.RecordResolution != recordResolutionNotFound {
		t.Fatalf("lookup Const default = %#v, err = %v", result.DefaultValueConfig, err)
	}
	if _, err := client.GetEntitySchemaColumnProperties(context.Background(), EntitySchemaColumnPropertiesRequest{SchemaName: "Contact", ColumnName: "Nope"}); err == nil ||
		err.Error() != "Column 'Nope' was not found in merged schema 'Contact'." {
		t.Fatalf("missing column error = %v", err)
	}
	if _, err := client.GetEntitySchemaColumnProperties(context.Background(), EntitySchemaColumnPropertiesRequest{SchemaName: "Contact"}); err == nil {
		t.Fatal("column-name must be required")
	}
}

func TestPackageColumnPropertiesReadsDesignLayer(t *testing.T) {
	client, seen := newGroupAServer(t, map[string]groupARoute{
		"SelectQuery:SysPackage": groupAFixed(`{"success":true,"rows":[{"UId":"p1","Name":"CrtBase"}]}`),
		"ServiceModel/EntitySchemaDesignerService.svc/GetSchemaDesignItem": groupAFixed(`{"success":true,"schema":{"name":"Contact","package":{"name":"CrtBase"},
			"columns":[],"inheritedColumns":[{"name":"Name","caption":[{"cultureName":"en-US","value":"Full name"}],"type":28,
			"requirementType":1,"indexed":true,"isValueCloneable":true}]}}`),
	})
	result, err := client.GetEntitySchemaColumnProperties(context.Background(), EntitySchemaColumnPropertiesRequest{PackageName: "crtbase", SchemaName: "Contact", ColumnName: "Name"})
	if err != nil || result.Source != "inherited" || result.Type != "MediumText" || !result.Required || result.TrackChanges == nil || *result.Title != "Full name" || result.PackageName != "CrtBase" {
		t.Fatalf("package column = %#v, err = %v", result, err)
	}
	request := seen["ServiceModel/EntitySchemaDesignerService.svc/GetSchemaDesignItem"][0]
	if request["packageUId"] != "p1" || request["useFullHierarchy"] != false {
		t.Fatalf("design request = %#v", request)
	}
	missing, _ := newGroupAServer(t, map[string]groupARoute{"SelectQuery:SysPackage": groupAFixed(`{"success":true,"rows":[]}`)})
	if _, err := missing.GetEntitySchemaColumnProperties(context.Background(), EntitySchemaColumnPropertiesRequest{PackageName: "Nope", SchemaName: "Contact", ColumnName: "Name"}); err == nil ||
		err.Error() != "Package 'Nope' was not found." {
		t.Fatalf("missing package error = %v", err)
	}
}

func TestGetAppInfoReadsPackageEntitiesPagesAndPrefix(t *testing.T) {
	client, seen := newGroupAServer(t, map[string]groupARoute{
		"SelectQuery:SysInstalledApp": groupAFixed(`{"success":true,"rows":[{"Id":"app-1","Code":"UsrApp","Name":"My App","Version":"1.0"}]}`),
		"ServiceModel/ApplicationPackagesService.svc/GetApplicationPackages": groupAFixed(
			`{"success":true,"packages":[{"uId":"other","name":"Dep"},{"uId":"11111111-1111-1111-1111-111111111111","name":"UsrApp","isApplicationPrimaryPackage":true}]}`),
		"SelectQuery:ApplicationEntity": groupAFixed(`{"success":true,"rows":[{"UId":"e1","Name":"UsrApp","Caption":""},{"UId":"E1","Name":"UsrApp"}]}`),
		"DataService/json/SyncReply/RuntimeEntitySchemaRequest": groupAFixed(`{"success":true,"schema":{"name":"UsrApp","caption":{"en-US":"Base object"},
			"columns":{"items":{"a":{"name":"UsrName","caption":{"en-US":"Name"},"dataValueType":28,"isRequired":true},
			"b":{"name":"Id","dataValueType":0,"isInherited":true},
			"c":{"name":"UsrCount","caption":{},"dataValueType":4,"defValue":{"valueSourceType":1,"value":5}}}}}}`),
		"ServiceModel/EntitySchemaDesignerService.svc/GetSchemaDesignItem": groupAFixed(`{"success":true,"schema":{"caption":[{"cultureName":"en-US","value":"My entity"}],
			"columns":[{"name":"UsrCount","caption":[{"cultureName":"en-US","value":"Count"}]}]}}`),
		"SelectQuery:SysSchema":        groupAFixed(`{"success":true,"rows":[{"Name":"Usr_ListPage","UId":"p2"},{"Name":"Usr_FormPage","UId":"p1"}]}`),
		"SelectQuery:SysSettingsValue": groupAFixed(`{"success":true,"rows":[{"TextValue":" \"Usr\" "}]}`),
	})
	result := client.GetAppInfo(context.Background(), AppInfoRequest{Code: "UsrApp"})
	if !result.Success || result.PackageName != "UsrApp" || result.CanonicalMainEntityName != "UsrApp" || *result.SchemaNamePrefix != "Usr" {
		t.Fatalf("result = %#v", result)
	}
	if len(result.Entities) != 1 || result.Entities[0].Caption != "My entity" {
		t.Fatalf("entities must be de-duplicated by UId and prefer the design caption over Base object: %#v", result.Entities)
	}
	columns := result.Entities[0].Columns
	if len(columns) != 2 || columns[0].Name != "UsrCount" || columns[0].Caption != "Count" || columns[0].Type != "Integer" ||
		string(columns[0].DefaultValueConfig.Value) != "5" || columns[1].Type != "MEDIUM_TEXT" || !columns[1].Required {
		t.Fatalf("own columns = %#v", columns)
	}
	if len(result.Pages) != 2 || result.Pages[0].SchemaName != "Usr_FormPage" {
		t.Fatalf("pages = %#v", result.Pages)
	}
	if request := seen["DataService/json/SyncReply/RuntimeEntitySchemaRequest"][0]; request["uId"] != "e1" {
		t.Fatalf("runtime schema must be read by uId: %#v", request)
	}
	entityFilters := groupAFilterItems(seen["SelectQuery:ApplicationEntity"][0])
	if len(entityFilters) != 2 {
		t.Fatalf("entity filters = %#v", entityFilters)
	}
}

func TestGetAppInfoReportsFailuresInsideEnvelope(t *testing.T) {
	client, _ := newGroupAServer(t, map[string]groupARoute{
		"SelectQuery:SysInstalledApp": groupAFixed(`{"success":true,"rows":[]}`),
	})
	if result := client.GetAppInfo(context.Background(), AppInfoRequest{Code: "Nope"}); result.Success || result.Error != "Application 'Nope' not found." || result.Entities != nil {
		t.Fatalf("unknown app = %#v", result)
	}
	if result := client.GetAppInfo(context.Background(), AppInfoRequest{ID: "x", Code: "y"}); result.Error != "Provide exactly one identifier: id or code." {
		t.Fatalf("two identifiers = %#v", result)
	}
}
