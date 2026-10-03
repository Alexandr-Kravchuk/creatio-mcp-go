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

func TestGetSourceCodeSchemaResolvesNameThenReadsDesignerBody(t *testing.T) {
	server := groupCServer(t, func(path string, body map[string]any) (int, string) {
		switch path {
		case "/0/DataService/json/SyncReply/SelectQuery":
			items := body["filters"].(map[string]any)["items"].(map[string]any)
			manager := items["byManager"].(map[string]any)["rightExpression"].(map[string]any)["parameter"].(map[string]any)["value"]
			if body["rootSchemaName"] != "SysSchema" || manager != "SourceCodeSchemaManager" || body["rowCount"] != float64(1) {
				t.Errorf("unexpected lookup %#v", body)
			}
			return http.StatusOK, `{"success":true,"rows":[{"UId":"11111111-1111-1111-1111-111111111111"}]}`
		case "/0/ServiceModel/SourceCodeSchemaDesignerService.svc/GetSchema":
			if body["schemaUId"] != "11111111-1111-1111-1111-111111111111" || body["useFullHierarchy"] != false {
				t.Errorf("unexpected designer request %#v", body)
			}
			return http.StatusOK, `{"success":true,"schema":{"name":"UsrHelper","body":"class 𝒳 {}","caption":[{"cultureName":"en-US","value":"Helper"}],"package":{"name":"Custom"}}}`
		}
		t.Errorf("unexpected route %s", path)
		return http.StatusNotFound, ""
	})
	defer server.Close()
	client := newFormsTestClient(t, server.URL)
	result := client.GetSourceCodeSchema(context.Background(), SourceCodeSchemaRequest{SchemaName: "UsrHelper"})
	if !result.Success || result.Body != "class 𝒳 {}" || result.BodyLength != 11 || result.Caption != "Helper" || result.PackageName != "Custom" {
		t.Fatalf("result = %#v", result)
	}
	output := filepath.Join(t.TempDir(), "out", "UsrHelper.cs")
	written := client.GetSourceCodeSchema(context.Background(), SourceCodeSchemaRequest{SchemaName: "UsrHelper", OutputFile: output})
	if data, err := os.ReadFile(output); err != nil || string(data) != "class 𝒳 {}" || written.Body != "" || !written.Success {
		t.Fatalf("written = %#v, file = %q, err = %v", written, data, err)
	}
	again := client.GetSourceCodeSchema(context.Background(), SourceCodeSchemaRequest{SchemaName: "UsrHelper", OutputFile: output})
	if again.Success || !strings.Contains(again.Error, "already exists; refusing to overwrite it") {
		t.Fatalf("second write = %#v", again)
	}
}

func TestGetClientUnitSchemaPicksTopLayerAndReportsMissingSchema(t *testing.T) {
	server := groupCServer(t, func(path string, body map[string]any) (int, string) {
		switch path {
		case "/0/DataService/json/SyncReply/SelectQuery":
			name := body["filters"].(map[string]any)["items"].(map[string]any)["byName"].(map[string]any)["rightExpression"].(map[string]any)["parameter"].(map[string]any)["value"]
			if body["rowCount"] != float64(-1) {
				t.Errorf("layer enumeration must not limit rows: %#v", body)
			}
			if name == "ZzNope" {
				return http.StatusOK, `{"success":true,"rows":[]}`
			}
			// Deliberately out of order: the client-side sort decides the top layer.
			return http.StatusOK, `{"success":true,"rows":[
				{"UId":"top","Name":"X","PackageName":"b","HierarchyLevel":9},
				{"UId":"base","Name":"X","PackageName":"a","HierarchyLevel":1},
				{"UId":"tie","Name":"X","PackageName":"A","HierarchyLevel":9}]}`
		case "/0/ServiceModel/ClientUnitSchemaDesignerService.svc/GetSchema":
			if body["schemaUId"] != "top" || body["useFullHierarchy"] != true {
				t.Errorf("unexpected designer request %#v", body)
			}
			return http.StatusOK, `{"schema":{"name":"X","body":"define()","localizableStrings":[{"name":"S","parentSchemaUId":"p","uId":"u","values":[{"cultureName":"en-US","value":"V"}]}]}}`
		}
		return http.StatusNotFound, ""
	})
	defer server.Close()
	client := newFormsTestClient(t, server.URL)
	result := client.GetClientUnitSchema(context.Background(), ClientUnitSchemaRequest{SchemaName: "X", FullHierarchy: true})
	if !result.Success || result.SchemaUID != "top" || result.LocalizableStringCount != 1 || result.LocalizableStrings == nil ||
		derefString((*result.LocalizableStrings)[0].ParentSchemaUID) != "p" {
		t.Fatalf("result = %#v", result)
	}
	missing := client.GetClientUnitSchema(context.Background(), ClientUnitSchemaRequest{SchemaName: "ZzNope"})
	if missing.Error != "Schema 'ZzNope' not found (ManagerName='ClientUnitSchemaManager')" {
		t.Fatalf("missing = %#v", missing)
	}
	if none := client.GetClientUnitSchema(context.Background(), ClientUnitSchemaRequest{}); none.Error != "schema-name or schema-uid is required" {
		t.Fatalf("none = %#v", none)
	}
}

func TestSchemaGetOutputRefusesPathsOutsideAllowedRoots(t *testing.T) {
	if _, refusal := schemaGetResolveOutput("/etc/creatio-mcp-test.cs"); !strings.Contains(refusal, "resolves outside the allowed locations") {
		t.Fatalf("refusal = %q", refusal)
	}
	inside := filepath.Join(os.TempDir(), "creatio-mcp-grouph-"+randomHex(4), "x.json")
	if path, refusal := schemaGetResolveOutput(inside); refusal != "" || path != inside {
		t.Fatalf("path = %q, refusal = %q", path, refusal)
	}
}

func TestStjIndentedJSONWritesSystemTextJSONLayout(t *testing.T) {
	node := toJNode(orderedFields{{"a", "<é>"}, {"list", []any{}}, {"nested", orderedFields{{"b", true}, {"c", (*string)(nil)}}}})
	want := "{\n  \"a\": \"\\u003C\\u00E9\\u003E\",\n  \"list\": [],\n  \"nested\": {\n    \"b\": true,\n    \"c\": null\n  }\n}"
	if got := string(node.stjIndentedJSON()); got != want {
		t.Fatalf("got %s", got)
	}
}

func TestPageHierarchyBuildOrdersRootFirstPagesAndOmitsLargeBodies(t *testing.T) {
	body := func(size int) *string { text := strings.Repeat("x", size); return &text }
	web := 9
	effectiveFirst := []pageLayer{
		{Name: "Leaf", Body: body(150_000), SchemaType: &web},
		{Name: "Middle", Body: body(60_000)},
		{Name: "Root", Body: body(10)},
	}
	whole := hierarchyGetBuild(PageHierarchyRequest{SchemaName: "Leaf"}, effectiveFirst)
	if whole.RootSchemaName != "Root" || !whole.BodiesOmittedForSize || whole.Warning == "" || (*whole.Schemas)[0].Body != nil ||
		(*whole.Schemas)[2].SchemaType != "web" || (*whole.Schemas)[0].SchemaType != "unknown" {
		t.Fatalf("whole = %#v", whole)
	}
	window := hierarchyGetBuild(PageHierarchyRequest{SchemaName: "Leaf", Offset: 1, Limit: 1}, effectiveFirst)
	entries := *window.Schemas
	if window.BodiesOmittedForSize || len(entries) != 1 || entries[0].HierarchyLevel != 1 || entries[0].Body == nil || !window.HasMore {
		t.Fatalf("window = %#v", window)
	}
	past := hierarchyGetBuild(PageHierarchyRequest{SchemaName: "Leaf", Offset: 99}, effectiveFirst)
	if past.Offset != 3 || past.ReturnedCount != 0 || past.HasMore {
		t.Fatalf("past = %#v", past)
	}
}

func TestGetPageHierarchyReportsMissingSchema(t *testing.T) {
	server := groupCServer(t, func(path string, body map[string]any) (int, string) {
		return http.StatusOK, `{"success":true,"rows":[]}`
	})
	defer server.Close()
	result := newFormsTestClient(t, server.URL).GetPageHierarchy(context.Background(), PageHierarchyRequest{SchemaName: "ZzNope"})
	if result.Success || result.Error != "Schema 'ZzNope' not found" {
		t.Fatalf("result = %#v", result)
	}
}

func TestClassicPageParseColumnsFollowsOverridesAndCallParent(t *testing.T) {
	base := `define("S", [], function() { return {
		entitySchemaName: "Contact",
		methods: {
			getGridDataColumns: function() { return { "Name": {path: "Name"}, "Account": {path: "Account.Name"} }; },
			nested: function() { var f = function() { return {path: "Ignored"}; }; }
		}
	}; });`
	composing := `define("S", [], function() { return {
		methods: { getGridDataColumns: function() { var c = this.callParent(arguments); delete c.Name; c.Owner = {path: "Owner"}; return c; } }
	}; });`
	override := `define("S", [], function() { return { initColumnsConfig() { return [{bindTo: 'Name'}, {bindTo: "Phone"}]; } }; });`
	parsed := classicPageParseColumns([]string{base, composing, override})
	if strings.Join(parsed.columns, ",") != "Name,Account.Name,Owner,Phone" || !parsed.declaresBoth || parsed.subtractive != 1 ||
		parsed.origins["name"] != "both" || parsed.origins["phone"] != "initColumnsConfig" {
		t.Fatalf("parsed = %#v", parsed)
	}
	if entity := classicPageParseEntityName([]string{base, composing}); entity != "Contact" {
		t.Fatalf("entity = %q", entity)
	}
	broken := classicPageParseColumns([]string{`define("S", [], function() { return { "a: 1 }; });`, `var x = {methods: {}};`})
	if broken.unparsed != 2 || broken.unanchored != 1 {
		t.Fatalf("broken = %#v", broken)
	}
	bare := classicPageParseColumns([]string{`entitySchemaName: "Lead", methods: { getGridDataColumns: function() { return {A: {path: "A"}}; } }`})
	if strings.Join(bare.columns, ",") != "A" || classicPageParseEntityName([]string{`entitySchemaName: "Lead"`}) != "Lead" {
		t.Fatalf("bare = %#v", bare)
	}
}

func TestClassicPageProfileColumnsPrefersDataGridFlag(t *testing.T) {
	var profile map[string]json.RawMessage
	_ = json.Unmarshal([]byte(`{"isTiled":true,"DataGrid":{"isTiled":false,
		"listedConfig":"{\"items\":[{\"bindTo\":\"Name\",\"caption\":\"Full name\"},{\"bindTo\":\"name\"},{\"metaPath\":\"Phone\"}]}",
		"tiledConfig":"{\"items\":[{\"bindTo\":\"Ignored\"}]}"}}`), &profile)
	notes := []string{}
	columns, viewType := classicPageProfileColumns(profile, &notes)
	if viewType != "listed" || len(columns) != 2 || columns[0].path != "Name" || derefString(columns[0].caption) != "Full name" || len(notes) != 0 {
		t.Fatalf("columns = %#v, viewType = %q, notes = %v", columns, viewType, notes)
	}
}

func TestLenientJSONFollowsJSONHAsClioReadsIt(t *testing.T) {
	// Expected values were produced by JsonhCs 7.8's JsonhReader.ParseElement, the reader clio's get-page uses.
	for input, want := range map[string]string{
		`[1, 2],`:                          `[1,2]`,
		"[{\"a\": 1\n\"b\": Terrasoft.X}]": `[{"a":1,"b":"Terrasoft.X"}]`,
		`[{"a": b c, d: 'e'}]`:             `[{"a":"b c","d":"e"}]`,
		`[{"a": 0x1F, "b": 1.50, "c": 1_000, "d": .5, "e": True}]`: `[{"a":31,"b":1.5,"c":1000,"d":0.5,"e":"True"}]`,
		`[{"a": [1 2]}]`:     `[{"a":["1 2"]}]`,
		`a: 1, b: 2`:         `{"a":1,"b":2}`,
		`[{"a": /* c */ 1}]`: `[{"a":1}]`,
	} {
		got, err := lenientJSON(input)
		if err != nil || string(got) != want {
			t.Errorf("lenientJSON(%q) = %s, %v; want %s", input, got, err, want)
		}
	}
	for input, want := range map[string]string{
		`[{"a": "x" + "y"}]`:                `Result was error: "Expected ` + "`:`" + ` after property name in object"`,
		`[{"a": function() { return 1; }}]`: `Result was error: "Empty quoteless string"`,
		``:                                  `Result was error: "Expected token, got end of input"`,
		`[{"a": a/b}]`:                      "Result was error: \"Unexpected `/`\"",
	} {
		if _, err := lenientJSON(input); err == nil || err.Error() != want {
			t.Errorf("lenientJSON(%q) error = %v; want %s", input, err, want)
		}
	}
}

func TestValidatePageRunsMarkerAndSectionChecks(t *testing.T) {
	template := `define("X", /**SCHEMA_DEPS*/[]/**SCHEMA_DEPS*/, function/**SCHEMA_ARGS*/()/**SCHEMA_ARGS*/ { return {
		viewConfigDiff: /**SCHEMA_VIEW_CONFIG_DIFF*/%s/**SCHEMA_VIEW_CONFIG_DIFF*/,
		viewModelConfigDiff: /**SCHEMA_VIEW_MODEL_CONFIG_DIFF*/[]/**SCHEMA_VIEW_MODEL_CONFIG_DIFF*/,
		modelConfigDiff: /**SCHEMA_MODEL_CONFIG_DIFF*/[]/**SCHEMA_MODEL_CONFIG_DIFF*/,
		handlers: /**SCHEMA_HANDLERS*/[]/**SCHEMA_HANDLERS*/,
		converters: /**SCHEMA_CONVERTERS*/%s/**SCHEMA_CONVERTERS*/,
		validators: /**SCHEMA_VALIDATORS*/{}/**SCHEMA_VALIDATORS*/ }; });`
	body := func(diff, converters string) string {
		return strings.Replace(strings.Replace(template, "%s", diff, 1), "%s", converters, 1)
	}
	if ok := ValidatePage(PageValidateRequest{Body: body(`[{"a": 1},]`, "{}")}); !ok.Valid || len(ok.Validation.Warnings) != 1 {
		t.Fatalf("ok = %#v", ok)
	}
	converters := ValidatePage(PageValidateRequest{Body: body("[]", "[]")})
	if converters.Valid || converters.Validation.ContentOK ||
		converters.Validation.Errors[0] != "Invalid JavaScript object section in SCHEMA_CONVERTERS: section must remain an object literal." {
		t.Fatalf("converters = %#v", converters)
	}
	syntax := ValidatePage(PageValidateRequest{Body: "define(\"X\", [], function() { return { "})
	if syntax.Validation.Errors[0] != "JavaScript syntax error at line 1, column 39: Unexpected end of input. The body was NOT sent to Creatio." {
		t.Fatalf("syntax = %#v", syntax)
	}
	markers := ValidatePage(PageValidateRequest{Body: "define(\"X\", [], function() { return {}; });"})
	if markers.Validation.MarkersOK || !markers.Validation.JSSyntaxOK || len(markers.Validation.Errors) != 8 {
		t.Fatalf("markers = %#v", markers)
	}
	for request, want := range map[PageValidateRequest]string{
		{}:                                 "Either 'body' or 'body-file' must provide page body content.",
		{BodyFile: "relative.js"}:          "body-file must be an absolute local path.",
		{BodyFile: "/nonexistent/body.js"}: "body-file was not found.",
		{Body: `{"viewConfigDiff": []}`}:   PageValidateMobileUnsupported,
	} {
		if result := ValidatePage(request); result.Valid || result.Validation.Errors[0] != want {
			t.Errorf("%#v = %#v", request, result)
		}
	}
}

func TestClassicPageSourcesHelpersMatchClio(t *testing.T) {
	page := newObject()
	page.set("body", newString(`details: { Files: { schemaName: "FileDetailV2", entitySchemaName: "AccountFile" },
		Contacts: { entitySchemaName: "Contact", schemaName: "ContactDetailV2" }, masterEntitySchemaName: "No" }`))
	overrides := classicPageDetailOverrides(nil, []*jnode{page})
	if overrides["filedetailv2"] != "AccountFile" || overrides["contactdetailv2"] != "Contact" {
		t.Fatalf("overrides = %#v", overrides)
	}
	if names := classicPageDetailNames([]*jnode{page}); strings.Join(names, ",") != "FileDetailV2,ContactDetailV2" {
		t.Fatalf("names = %v", names)
	}
	tables, warnings := classicPageParseEnums(`Terrasoft.ViewItemType = { GRID: 0, /* x: 9 */ "s": 1, LABEL: 6, LABEL: 7 };
		Terrasoft.ContentTypeX = {A: 1}; Terrasoft.ContentType = {}`)
	if len(tables) != 1 || tables[0].name != "ViewItemType" || len(tables[0].members) != 2 || tables[0].members[1].value != 7 || len(warnings) != 2 {
		t.Fatalf("tables = %#v, warnings = %v", tables, warnings)
	}
	indented := string(classicPageNewtonsoftIndented(toJNode(orderedFields{{"a", "é "}, {"b", []any{}}, {"c", false}})))
	if indented != "{\n  \"a\": \"é\\u2028\",\n  \"b\": [],\n  \"c\": false\n}" {
		t.Fatalf("indented = %s", indented)
	}
}

func TestGetClassicPageSourcesReportsMissingPage(t *testing.T) {
	server := groupCServer(t, func(path string, body map[string]any) (int, string) {
		if path == "/0/DataService/json/SyncReply/SelectQuery" {
			return http.StatusOK, `{"success":true,"rows":[]}`
		}
		t.Errorf("unexpected route %s", path)
		return http.StatusNotFound, ""
	})
	defer server.Close()
	result := newFormsTestClient(t, server.URL).GetClassicPageSources(context.Background(), ClassicPageSourcesRequest{SchemaName: "ZzNope"})
	if result.Success || result.Error != "Schema 'ZzNope' not found (ManagerName='ClientUnitSchemaManager')" {
		t.Fatalf("result = %#v", result)
	}
}

func TestClassicPageTokenizerReadsDivisionAfterPostfixUpdate(t *testing.T) {
	for _, source := range []string{
		"var ratio = count++ / total; var next = /x/g;",
		"var ratio = items[0]-- / 2;",
		"var ratio = (a)++ / 2;",
	} {
		tokens, err := classicPageTokenize([]rune(source))
		if err != nil {
			t.Fatalf("%q: %v", source, err)
		}
		regexes := 0
		for _, token := range tokens {
			if token.kind == classicPageRegex {
				regexes++
			}
		}
		if want := strings.Count(source, "/x/"); regexes != want {
			t.Fatalf("%q: %d regex tokens, want %d", source, regexes, want)
		}
	}
	// A slash after a punctuator that is not a postfix update still opens a regex.
	if _, err := classicPageTokenize([]rune("var r = x ? /a/ : /b/;")); err != nil {
		t.Fatal(err)
	}
}
