package creatio

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const pageWriteTestBody = `define("UsrP", /**SCHEMA_DEPS*/[]/**SCHEMA_DEPS*/, function/**SCHEMA_ARGS*/()/**SCHEMA_ARGS*/ {
	return {
		viewConfigDiff: /**SCHEMA_VIEW_CONFIG_DIFF*/%s/**SCHEMA_VIEW_CONFIG_DIFF*/,
		viewModelConfigDiff: /**SCHEMA_VIEW_MODEL_CONFIG_DIFF*/[]/**SCHEMA_VIEW_MODEL_CONFIG_DIFF*/,
		modelConfigDiff: /**SCHEMA_MODEL_CONFIG_DIFF*/[]/**SCHEMA_MODEL_CONFIG_DIFF*/,
		handlers: /**SCHEMA_HANDLERS*/[]/**SCHEMA_HANDLERS*/,
		converters: /**SCHEMA_CONVERTERS*/{}/**SCHEMA_CONVERTERS*/,
		validators: /**SCHEMA_VALIDATORS*/{}/**SCHEMA_VALIDATORS*/
	};
});`

func pageWriteTestPage(diff string) string { return strings.Replace(pageWriteTestBody, "%s", diff, 1) }

const pageWriteLabel = `[{"operation":"insert","name":"Label1","values":{"type":"crt.Label","caption":"#ResourceString(UsrTitle)#"},"parentName":"MainContainer","propertyName":"items","index":0}]`

func TestCreatePageSavesTemplateChildAndReportsSaveFailure(t *testing.T) {
	for _, refuse := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[refuse], func(t *testing.T) {
			var saved map[string]any
			server := groupCServer(t, func(path string, body map[string]any) (int, string) {
				switch path {
				case "/0/rest/schema.template.api/templates":
					return http.StatusOK, `{"success":true,"items":[{"uId":"tpl","name":"BlankPageTemplate","title":"Blank","groupName":"Pages"}]}`
				case "/0/DataService/json/SyncReply/SelectQuery":
					switch body["rootSchemaName"] {
					case "SysPackage":
						return http.StatusOK, `{"success":true,"rows":[{"UId":"pkg"}]}`
					}
					filters, _ := json.Marshal(body["filters"])
					if strings.Contains(string(filters), "EntitySchemaManager") {
						return http.StatusOK, `{"success":true,"rows":[{"UId":"entity"}]}`
					}
					return http.StatusOK, `{"success":true,"rows":[]}`
				case "/0/ServiceModel/ClientUnitSchemaDesignerService.svc/GetSchema":
					if body["schemaUId"] != "tpl" || body["useFullHierarchy"] != false {
						t.Errorf("template read %#v", body)
					}
					return http.StatusOK, `{"success":true,"schema":{"localizableStrings":[{"name":"Tpl"}]}}`
				case "/0/ServiceModel/ClientUnitSchemaDesignerService.svc/SaveSchema":
					saved = body
					if refuse {
						return http.StatusOK, `{"success":false,"errorInfo":{"message":"locked"}}`
					}
					return http.StatusOK, `{"success":true}`
				case "/0/ServiceModel/ApplicationPackagesService.svc/GetDesignPackageUId":
					return http.StatusOK, `{"success":true,"uId":"design"}`
				}
				t.Errorf("unexpected %s", path)
				return http.StatusNotFound, ""
			})
			defer server.Close()
			result := newFormsTestClient(t, server.URL).CreatePage(context.Background(), PageCreateRequest{SchemaName: "UsrP",
				Template: "blankpagetemplate", PackageName: "Pkg", EntitySchemaName: "UsrEntity", CaptionCulture: "en-us",
				OptionalProperties: `[{"key":"entitySchemaName","value":"UsrEntity"}]`})
			if refuse {
				if result.Success || result.Error != "locked" {
					t.Fatalf("result %#v", result)
				}
				return
			}
			if !result.Success || result.TemplateName != "BlankPageTemplate" || result.SchemaType != 9 || result.EntitySchemaUID != "entity" ||
				result.DesignPackageUID != "design" || result.WillCreateReplacingInDesignPackage == nil || !strings.Contains(result.Note, "target-schema-uid=") {
				t.Fatalf("result %#v", result)
			}
			parent := saved["parent"].(map[string]any)
			caption := saved["caption"].([]any)[0].(map[string]any)
			if parent["uId"] != "tpl" || saved["group"] != "Pages" || saved["schemaType"] != float64(9) || caption["cultureName"] != "en-US" ||
				caption["value"] != "UsrP" || len(saved["localizableStrings"].([]any)) != 1 || len(saved["optionalProperties"].([]any)) != 1 ||
				saved["dependsOn"].([]any)[0].(map[string]any)["uId"] != "entity" || saved["extendParent"] != false {
				t.Fatalf("payload %#v", saved)
			}
		})
	}
}

func TestCreatePageValidatesBeforeAnyCall(t *testing.T) {
	client := newFormsTestClient(t, "http://127.0.0.1:1")
	for request, want := range map[PageCreateRequest]string{
		{SchemaName: "1bad", Template: "t", PackageName: "p"}:                                      pageWriteSchemaNameError,
		{SchemaName: "UsrP", PackageName: "p"}:                                                     "template is required",
		{SchemaName: "UsrP", Template: "t", PackageName: "p", OptionalProperties: `{"key":"a"}`}:   pageWriteOptionalPropertiesError,
		{SchemaName: "UsrP", Template: "t", PackageName: "p", OptionalProperties: `[{"value":1}]`}: pageWriteOptionalPropertiesError,
	} {
		if result := client.CreatePage(context.Background(), request); result.Error != want {
			t.Errorf("%#v: %q", request, result.Error)
		}
	}
}

// pageWriteStand is a mock Creatio serving one page in its design package (or, with replacing, a page whose
// design package does not own it yet).
type pageWriteStand struct {
	t          *testing.T
	replacing  bool
	checksums  []string
	ownBody    string
	saved      map[string]any
	resets     int
	designCall int
}

func (s *pageWriteStand) handle(path string, body map[string]any) (int, string) {
	switch path {
	case "/0/DataService/json/SyncReply/SelectQuery":
		columns, _ := json.Marshal(body["columns"])
		if strings.Contains(string(columns), "Checksum") {
			checksum := s.checksums[0]
			if len(s.checksums) > 1 {
				s.checksums = s.checksums[1:]
			}
			return http.StatusOK, `{"success":true,"rows":[{"Checksum":"` + checksum + `","ModifiedOn":"2026-10-03T10:00:00"}]}`
		}
		filters, _ := json.Marshal(body["filters"])
		if strings.Contains(string(filters), "SysPackage.UId") {
			return http.StatusOK, `{"success":true,"rows":[]}`
		}
		return http.StatusOK, `{"success":true,"rows":[{"UId":"own"}]}`
	case "/0/ServiceModel/ApplicationPackagesService.svc/GetDesignPackageUId":
		s.designCall++
		return http.StatusOK, `{"success":true,"uId":"design"}`
	case "/0/ServiceModel/ClientUnitSchemaDesignerService.svc/GetParentSchemas":
		headPackage := "design"
		if s.replacing {
			headPackage = "base"
		}
		own, _ := json.Marshal(s.ownBody)
		template, _ := json.Marshal(pageWriteTestPage(`[{"operation":"insert","name":"MainContainer","values":{"type":"crt.FlexContainer","items":[]}}]`))
		return http.StatusOK, `{"success":true,"values":[{"uId":"own","name":"UsrP","schemaType":9,"package":{"uId":"` + headPackage +
			`","name":"Pkg"},"body":` + string(own) + `},{"uId":"tpl","name":"BlankPageTemplate","schemaType":9,"schemaVersion":1,"package":{"uId":"crt","name":"Crt"},"body":` +
			string(template) + `}]}`
	case "/0/ServiceModel/ClientUnitSchemaDesignerService.svc/GetSchema":
		own, _ := json.Marshal(s.ownBody)
		return http.StatusOK, `{"success":true,"schema":{"uId":"` + body["schemaUId"].(string) + `","name":"UsrP","body":` + string(own) +
			`,"localizableStrings":[{"uId":"k","name":"Keep","values":[{"cultureName":"en-US","value":"old"}]}],"optionalProperties":[{"key":"A","value":"1"}]}}`
	case "/0/ServiceModel/ClientUnitSchemaDesignerService.svc/SaveSchema":
		s.saved = body
		return http.StatusOK, `{"success":true}`
	case "/0/rest/WorkplaceService/ResetScriptCache":
		s.resets++
		return http.StatusOK, `{}`
	}
	s.t.Errorf("unexpected %s", path)
	return http.StatusNotFound, ""
}

func pageWriteTestMeta(t *testing.T, dir, checksum string) string {
	t.Helper()
	metaPath := filepath.Join(dir, ".clio-pages", "UsrP", "meta.json")
	if err := os.MkdirAll(filepath.Dir(metaPath), 0o755); err != nil {
		t.Fatal(err)
	}
	meta := `{"fetchedAt":"2026-10-03T00:00:00.0000000Z","page":{"schemaName":"UsrP"},"baseline":{"schemaName":"UsrP","environmentName":"dev",` +
		`"editableSchemaExists":true,"editableSchemaUId":"own","checksum":"` + checksum + `","capturedAt":"x"}}`
	if err := os.WriteFile(metaPath, []byte(meta), 0o644); err != nil {
		t.Fatal(err)
	}
	return metaPath
}

func TestUpdatePageSavesOnTheArmedBaselineAndRefreshesIt(t *testing.T) {
	dir := t.TempDir()
	metaPath := pageWriteTestMeta(t, dir, "c1")
	stand := &pageWriteStand{t: t, checksums: []string{"c1", "c2"}, ownBody: pageWriteTestPage("[]")}
	server := groupCServer(t, stand.handle)
	defer server.Close()
	client := newFormsTestClient(t, server.URL)
	request := &PageUpdateRequest{SchemaName: "UsrP", Body: pageWriteTestPage(pageWriteLabel), Validate: true, EnvironmentName: "DEV",
		Resources: `{"UsrTitle":"Title"}`, OptionalProperties: `[{"key":"a","value":"2"},{"key":"B","value":"3"}]`}
	path, refresh, warning := client.ArmPageBaseline(request, dir)
	if path != metaPath || !refresh || warning != "" || request.ExpectedChecksum != "c1" {
		t.Fatalf("arm %q %t %q %#v", path, refresh, warning, request)
	}
	response := client.UpdatePage(context.Background(), request)
	if !response.Success || response.NewChecksum != "c2" || response.SavedSchemaUID != "own" || response.ResourcesRegistered != 1 ||
		len(response.Warnings) != 1 || stand.resets != 1 {
		t.Fatalf("response %#v resets=%d", response, stand.resets)
	}
	strings_ := stand.saved["localizableStrings"].([]any)
	optional := stand.saved["optionalProperties"].([]any)
	if stand.saved["body"] != request.Body || len(strings_) != 2 || strings_[1].(map[string]any)["name"] != "UsrTitle" ||
		len(optional) != 2 || optional[0].(map[string]any)["value"] != "2" {
		t.Fatalf("saved %#v", stand.saved)
	}
	if warning := RefreshPageBaseline(metaPath, request, &response); warning != "" {
		t.Fatal(warning)
	}
	meta, _ := os.ReadFile(metaPath)
	if !strings.Contains(string(meta), `"checksum":"c2"`) || !strings.Contains(string(meta), `"environmentName":"DEV"`) ||
		!strings.Contains(string(meta), `"page":{"schemaName":"UsrP"}`) {
		t.Fatalf("meta %s", meta)
	}
}

func TestUpdatePageRefusesAnExternalModification(t *testing.T) {
	dir := t.TempDir()
	pageWriteTestMeta(t, dir, "c1")
	stand := &pageWriteStand{t: t, checksums: []string{"other"}, ownBody: pageWriteTestPage("[]")}
	server := groupCServer(t, stand.handle)
	defer server.Close()
	client := newFormsTestClient(t, server.URL)
	request := &PageUpdateRequest{SchemaName: "UsrP", Body: pageWriteTestPage("[]"), EnvironmentName: "dev", Validate: true}
	client.ArmPageBaseline(request, dir)
	response := client.UpdatePage(context.Background(), request)
	if response.Success || !response.Conflict || response.ConflictDetails.Reason != "checksum-mismatch" ||
		response.ConflictDetails.ActualChecksum != "other" || stand.saved != nil {
		t.Fatalf("response %#v", response)
	}
	// A baseline of another environment is not armed.
	other := &PageUpdateRequest{SchemaName: "UsrP", Body: pageWriteTestPage("[]"), EnvironmentName: "prod"}
	if _, refresh, _ := client.ArmPageBaseline(other, dir); refresh || other.ExpectedChecksum != "" {
		t.Fatalf("armed another environment %#v", other)
	}
}

func TestUpdatePageAppendCreatesReplacingSchemaInTheDesignPackage(t *testing.T) {
	stand := &pageWriteStand{t: t, replacing: true, checksums: []string{""}, ownBody: pageWriteTestPage("[]")}
	server := groupCServer(t, stand.handle)
	defer server.Close()
	client := newFormsTestClient(t, server.URL)
	fragment := "/**SCHEMA_VIEW_CONFIG_DIFF*/" + pageWriteLabel + "/**SCHEMA_VIEW_CONFIG_DIFF*/"
	response := client.UpdatePage(context.Background(), &PageUpdateRequest{SchemaName: "UsrP", Mode: "append", Body: fragment, Validate: true})
	if !response.Success || response.AppendProjection == nil || response.AppendProjection.AddedOperationCount != 1 {
		t.Fatalf("response %#v", response)
	}
	pkg := stand.saved["package"].(map[string]any)
	parent := stand.saved["parent"].(map[string]any)
	body := stand.saved["body"].(string)
	if stand.saved["extendParent"] != true || pkg["uId"] != "design" || parent["uId"] != "own" || stand.saved["uId"] == "own" ||
		!strings.Contains(body, `"name": "Label1"`) || !strings.HasPrefix(body, `define("UsrP"`) {
		t.Fatalf("saved %#v", stand.saved)
	}
}

func TestUpdatePageRejectsAnUnresolvedParent(t *testing.T) {
	stand := &pageWriteStand{t: t, checksums: []string{""}, ownBody: pageWriteTestPage("[]")}
	server := groupCServer(t, stand.handle)
	defer server.Close()
	client := newFormsTestClient(t, server.URL)
	body := pageWriteTestPage(strings.Replace(pageWriteLabel, `"MainContainer"`, `"MainContainr"`, 1))
	response := client.UpdatePage(context.Background(), &PageUpdateRequest{SchemaName: "UsrP", Body: body, Validate: false})
	want := "Element 'Label1' has unresolved parentName 'MainContainr'. Closest known element: 'MainContainer'."
	if response.Success || response.Error != want || stand.saved != nil {
		t.Fatalf("response %#v", response)
	}
}

func TestPageUpdateCheckOrdersItsGates(t *testing.T) {
	cases := []struct {
		request PageUpdateRequest
		want    string
		syntax  bool
	}{
		{PageUpdateRequest{}, "Either 'body' or 'body-file' must provide page body content.", false},
		{PageUpdateRequest{BodyFile: "/no/such/file.js"}, "File not found: /no/such/file.js", false},
		{PageUpdateRequest{Mode: "append", Body: "/**SCHEMA_MODEL_CONFIG*/{}/**SCHEMA_MODEL_CONFIG*/"}, "Append merge cannot use this body: " + pageWriteWebIncomingFullConfig + " See docs://mcp/guides/page-modification for the append diff-form contract.", false},
		{PageUpdateRequest{Body: "define(", Validate: true}, "JavaScript syntax error at line 1, column 8: Unexpected end of input. The body was NOT sent to Creatio.", true},
		{PageUpdateRequest{Body: "define(", Validate: true, Resources: "[1]"}, pageWriteInvalidResources, false},
	}
	for _, item := range cases {
		request := item.request
		result := PageUpdateCheck(&request)
		if result.Failure == nil || result.Failure.Error != item.want || result.SyntaxOnly != item.syntax {
			t.Errorf("%#v: %#v", item.request, result)
		}
	}
	request := PageUpdateRequest{Body: pageWriteTestPage("[]"), Force: true}
	if result := PageUpdateCheck(&request); result.Failure != nil || len(result.Warnings) != 1 || result.Warnings[0] != PageWriteForceValidateAdvisory {
		t.Fatalf("advisory %#v", result)
	}
}

func TestPageWriteMergeKeepsOperationsHandlersAndKeyedEntries(t *testing.T) {
	current := strings.Replace(strings.Replace(pageWriteTestPage(`[{"operation":"insert","name":"A","values":{"x":1}},{"operation":"merge","name":"B","values":{}}]`),
		"/**SCHEMA_HANDLERS*/[]", `/**SCHEMA_HANDLERS*/[{request: "crt.A", handler: async () => {}}, {request: "crt.B", handler: async () => {}}]`, 1),
		"/**SCHEMA_CONVERTERS*/{}", `/**SCHEMA_CONVERTERS*/{"usr.Keep": function(v) { return v; }, "usr.Swap": function(v) { return 1; }}`, 1)
	incoming := `/**SCHEMA_VIEW_CONFIG_DIFF*/[{"operation":"insert","name":"A","values":{"x":2}},{"operation":"insert","name":"C"}]/**SCHEMA_VIEW_CONFIG_DIFF*/` +
		`/**SCHEMA_HANDLERS*/[{request: "crt.B", handler: async () => { return 2; }}]/**SCHEMA_HANDLERS*/` +
		`/**SCHEMA_CONVERTERS*/{"usr.Swap": function(v) { return 2; }}/**SCHEMA_CONVERTERS*/`
	merged, projection, problem, _ := pageWriteMergeBodies(current, incoming)
	if problem != "" {
		t.Fatal(problem)
	}
	diff, _ := readPageSection(merged, "SCHEMA_VIEW_CONFIG_DIFF")
	handlers, _ := readPageSection(merged, "SCHEMA_HANDLERS")
	converters, _ := readPageSection(merged, "SCHEMA_CONVERTERS")
	if !strings.Contains(diff, "\"x\": 2") || strings.Contains(diff, "\"x\": 1") || projection.ReplacedOperationCount != 1 ||
		projection.AddedOperationCount != 1 || projection.ProjectedOperationCount != 3 || !projection.ViewConfigDiffApplied {
		t.Fatalf("diff %s projection %#v", diff, projection)
	}
	if strings.Count(handlers, "crt.B") != 1 || !strings.Contains(handlers, "return 2") || !strings.Contains(handlers, "crt.A") {
		t.Fatalf("handlers %s", handlers)
	}
	if converters != `{"usr.Keep": function(v) { return v; },"usr.Swap": function(v) { return 2; }}` {
		t.Fatalf("converters %s", converters)
	}
}

func TestSyncPagesAnswersOfflineFailuresAndSavesTheRest(t *testing.T) {
	stand := &pageWriteStand{t: t, checksums: []string{""}, ownBody: pageWriteTestPage("[]")}
	server := groupCServer(t, stand.handle)
	defer server.Close()
	request := PageSyncRequest{Validate: true, Pages: []PageSyncPageInput{
		{SchemaName: "UsrBad", Body: "define("},
		{SchemaName: "UsrNoMarkers", Body: "define('x', [], function() { return {}; });"},
		{SchemaName: "UsrP", Body: pageWriteTestPage("[]")},
	}}
	results := PageSyncPrepass(request)
	if results[0] == nil || results[1] == nil || results[2] != nil || !strings.HasPrefix(results[1].Error, "Client-side validation failed: SCHEMA_DEPS") {
		t.Fatalf("prepass %#v %#v", results[0], results[1])
	}
	response := newFormsTestClient(t, server.URL).SyncPages(context.Background(), request, results)
	if response.Success || len(response.Pages) != 3 || !response.Pages[2].Success || stand.saved == nil ||
		response.Pages[2].Validation.Warnings[0] != PageWriteValidationGapWarning {
		t.Fatalf("response %#v", response)
	}
	failed := PageSyncFillPending(PageSyncPrepass(request), request, "Environment 'x' not found")
	if failed.Pages[2].Error != "Environment 'x' not found" || failed.Success {
		t.Fatalf("fill %#v", failed)
	}
}
