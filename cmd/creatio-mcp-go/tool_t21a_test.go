package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var t21aTools = []string{"create-package", "place-section-in-workplace", "localize-page", "trigger-smoke"}

// t21aSession is a server whose settings name an environment that must never be contacted, and a Safe one.
func t21aSession(t *testing.T) *mcp.ClientSession {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("a refused call contacted Creatio: %s", r.URL.Path)
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	clearCreatioVariables(t)
	path := filepath.Join(t.TempDir(), "appsettings.json")
	writeSettings(t, path, "t21", map[string]map[string]any{
		"t21":  {"Uri": server.URL, "Login": "example-user", "Password": "replace-me"},
		"safe": {"Uri": server.URL, "Login": "example-user", "Password": "replace-me", "Safe": true},
	})
	return connectTestClient(t, newMCPServerWithHiddenTools(newEnvironments(path), defaultHiddenToolServices()),
		mcp.NewClient(&mcp.Implementation{Name: "probe-client", Version: "test"}, nil))
}

func TestT21aToolsCarryClioAnnotationsAndContracts(t *testing.T) {
	want := map[string]toolAnnotations{
		"create-package":             {Destructive: true},
		"place-section-in-workplace": {Destructive: true, Idempotent: true},
		"localize-page":              {Destructive: true, Idempotent: true},
		"trigger-smoke":              {Destructive: true},
	}
	for name, annotations := range want {
		if got := annotationsOf(name); got != annotations {
			t.Errorf("%s annotations = %#v, want %#v", name, got, annotations)
		}
		contract, ok := contractFor(name)
		if !ok {
			t.Fatalf("%s has no contract", name)
		}
		encoded, _ := json.Marshal(contract)
		if !strings.Contains(string(encoded), `"name":"`+name+`"`) || !strings.Contains(string(encoded), `"input-schema"`) {
			t.Errorf("%s contract = %s", name, encoded)
		}
	}
	for _, name := range []string{"page-smoke", "get-mobile-page-conversion-guide"} {
		if _, ok := registeredTools[name]; ok {
			t.Errorf("%s is registered although it is not ported", name)
		}
	}
}

func TestT21aWritesAreGatedByRawName(t *testing.T) {
	session := t21aSession(t)
	for _, name := range t21aTools {
		result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name,
			Arguments: map[string]any{"args": map[string]any{"environment-name": "t21"}}})
		if err != nil {
			t.Fatal(err)
		}
		text := result.Content[0].(*mcp.TextContent).Text
		if !strings.HasPrefix(text, "Tool '"+name+"' can overwrite or delete existing state and was NOT executed") {
			t.Errorf("%s direct call: %s", name, text)
		}
	}
}

// TestT21aArgumentRefusalsMatchClio holds clio 8.1.0.142's answers, measured through clio-run on an environment
// that is never contacted (a refusal happens before any request).
func TestT21aArgumentRefusalsMatchClio(t *testing.T) {
	session := t21aSession(t)
	for _, test := range []struct {
		tool    string
		args    map[string]any
		want    string // the envelope's error, or the raised text when raised
		raised  bool
		extra   map[string]any
		absents []string
	}{
		{tool: "create-package", args: map[string]any{"package-name": "X"}, want: "environment-name is required and cannot be empty.",
			extra: map[string]any{"package-created": false}},
		{tool: "create-package", args: map[string]any{"environment-name": "t21"}, want: "package-name is required and cannot be empty."},
		{tool: "create-package", args: map[string]any{"environment-name": "t21", "packageName": "X", "zzz": 1, "Package": 2},
			want: "Rename: 'packageName' -> 'package-name'. Unknown args: 'Package', 'zzz'. Valid: environment-name, package-name, description, dependencies, application-code."},
		{tool: "create-package", args: map[string]any{"environmentName": "t21", "package-name": "X"}, want: "Rename: 'environmentName' -> 'environment-name'."},
		{tool: "create-package", args: map[string]any{"environment-name": "t21", "package-name": "X", "dependencies": "CrtBase"}, raised: true,
			want: "invalid-parameter-type: argument 'dependencies' for MCP tool 'create-package' must be an array. Received an incompatible JSON value."},
		{tool: "create-package", args: map[string]any{"environment-name": "t21", "package-name": "X", "dependencies": []any{1}}, raised: true,
			want: "invalid-parameter-type: argument 'dependencies' for MCP tool 'create-package' contains a value that does not match the documented shape. Received an incompatible JSON value."},
		{tool: "create-package", args: map[string]any{"environment-name": "safe", "package-name": "X"}, raised: true,
			want: "Error: tool 'create-package' failed: Safe environment confirmation required but it was declined or the context is non-interactive. Environment: '[redacted-uri]'."},
		{tool: "place-section-in-workplace", args: map[string]any{"section-code": "X", "workplace-name": "W"}, want: "environment-name is required and cannot be empty."},
		{tool: "place-section-in-workplace", args: map[string]any{"environment-name": "t21", "workplace-name": "W"}, want: "section-code is required and cannot be empty."},
		{tool: "place-section-in-workplace", args: map[string]any{"environment-name": "t21", "section-code": "X"}, want: "workplace-name is required unless mode is 'none'."},
		{tool: "place-section-in-workplace", args: map[string]any{"environment-name": "t21", "section-code": "X", "workplace-name": "W", "mode": "maybe"},
			want: "mode must be 'existing', 'new' or 'none'."},
		{tool: "place-section-in-workplace", args: map[string]any{"environment-name": "t21", "section-code": "X", "mode": "none", "roles": []any{"A"}},
			want: "mode=none writes nothing, so roles cannot be granted; omit roles."},
		{tool: "place-section-in-workplace", args: map[string]any{"environment-name": "t21", "section-code": "X", "workplace-name": "W", "client-type": "desktop"},
			want: "client-type must be 'web' or 'mobile'."},
		{tool: "place-section-in-workplace", args: map[string]any{"environment-name": "t21", "section-code": "X", "workplace-name": "W", "roles": []any{"A", nil}},
			want: "roles must not contain blank entries."},
		{tool: "place-section-in-workplace", args: map[string]any{"environment-name": "t21", "section-code": "X", "workplace-name": " my applications "},
			want: "The target is 'My applications' itself; pass remove-from-my-applications=false."},
		{tool: "place-section-in-workplace", args: map[string]any{"environment-name": "t21", "sectionCode": "X", "Workplace": "W", "code": "y", "zz": 1},
			want: "Rename: 'code' -> 'section-code', 'sectionCode' -> 'section-code'. Unknown args: 'Workplace', 'zz'. Valid: environment-name, package-name, section-code, workplace-name, client-type, roles, remove-from-my-applications, position, mode."},
		{tool: "place-section-in-workplace", args: map[string]any{"environment-name": "t21", "section-code": "X", "workplace-name": "W", "position": 3.5}, raised: true,
			want: "invalid-parameter-type: argument 'position' for MCP tool 'place-section-in-workplace' must be a number. Received an incompatible JSON value."},
		{tool: "localize-page", args: map[string]any{"environment-name": "t21", "culture": "es-ES"}, want: "schema-name is required", absents: []string{"schemaName"},
			extra: map[string]any{"saved": false}},
		{tool: "localize-page", args: map[string]any{"environment-name": "t21", "schema-name": "P"}, want: "culture is required"},
		{tool: "localize-page", args: map[string]any{"environment-name": "t21", "schema-name": "P", "culture": " EN-us "},
			want: "localize-page does not write the default culture 'en-US'. Change en-US values with update-page `resources` (it registers keys and writes en-US); localize-page adds the other cultures."},
		{tool: "localize-page", args: map[string]any{"environment-name": "t21", "schema-name": "P", "culture": "es-ES", "resources": `{"a":{"b":1}}`},
			want: "resources must be a JSON object of key → value strings: Unexpected character encountered while parsing value: {. Path 'a', line 1, position 6."},
		{tool: "localize-page", args: map[string]any{"environment-name": "t21", "schema-name": "P", "culture": "es-ES", "resources": `{"a":null,"b":"x","c":null}`},
			want: "resources must map every key to a string value; key(s) with a null value: a, c."},
		{tool: "localize-page", args: map[string]any{"environment-name": "t21", "schema-name": "P", "culture": "es-ES", "caption": "  "},
			want: "The caption is whitespace-only; pass the page title text, or omit caption. Nothing was saved."},
		{tool: "localize-page", args: map[string]any{"environment-name": "t21", "schema-name": "P", "culture": "es-ES", "caption": "x\u0007"},
			want: "The caption contains a character Creatio cannot store in a schema resource (a control character other than tab, LF or CR, U+FFFE, U+FFFF or a lone surrogate half). Remove it and retry; nothing was saved."},
		{tool: "localize-page", args: map[string]any{"environment-name": "t21", "schemaName": "P", "culture": "es-ES", "environment": "x", "zz": 1},
			want: "Rename: 'schemaName' -> 'schema-name'. Unknown args: 'environment', 'zz'. Valid: schema-name, culture, resources, caption, output-directory, environment-name, uri, login, password."},
		{tool: "trigger-smoke", args: map[string]any{"entity": "UsrRequest", "values": map[string]any{"Name": "x"}, "watch": []any{"Activity"}}, raised: true,
			want: "invalid-parameter-type: argument 'args' for MCP tool 'trigger-smoke' must be an object. Received an incompatible JSON value."},
		{tool: "trigger-smoke", args: map[string]any{"environment-name": " ", "entity": "UsrRequest"}, want: "environment-name is required."},
		{tool: "trigger-smoke", args: map[string]any{"environment-name": "t21", "entity": "Usr Request"},
			want: "entity must be an entity schema name (letters, digits, underscore)."},
		{tool: "trigger-smoke", args: map[string]any{"environment-name": "t21", "entity": "UsrRequest", "values": map[string]any{}},
			want: "values is required: the column values of the triggering record, as OData property names (a lookup as <Column>Id with the record GUID), for example {\"Name\": \"Smoke\", \"AccountId\": \"…\"}."},
		{tool: "trigger-smoke", args: map[string]any{"environment-name": "t21", "entity": "UsrRequest", "values": map[string]any{"Account/Id": "x"}},
			want: "values keys must be plain property names; 'Account/Id' is not one."},
		{tool: "trigger-smoke", args: map[string]any{"environment-name": "t21", "entity": "UsrRequest", "values": map[string]any{"Name": "x"}},
			want: "watch is required: list the entities the started process writes, for example [\"Activity\"]. The platform keeps no list of what a run created, so clio reports only what appears in watched entities."},
		{tool: "trigger-smoke", args: map[string]any{"environment-name": "t21", "entity": "UsrRequest", "values": map[string]any{"Name": "x"},
			"watch": []any{"A", "B", "C", "D", "E", "F"}}, want: "watch takes 1-5 entity schema names."},
		{tool: "trigger-smoke", args: map[string]any{"environment-name": "t21", "entity": "UsrRequest", "values": map[string]any{"Name": "x"},
			"watch": []any{"Activity"}, "timeout": 31}, want: "timeout must be between 1 and 30 seconds."},
		{tool: "trigger-smoke", args: map[string]any{"environment-name": "t21", "entity": "UsrRequest", "values": map[string]any{"Name": "x"},
			"watch": []any{"Activity"}, "Environment": "x", "zz": 1},
			want: "Rename: 'Environment' -> 'environment-name'. Unknown args: 'zz'. Valid: environment-name, entity, values, watch, process, timeout, cleanup."},
		{tool: "trigger-smoke", args: map[string]any{"environment-name": "safe", "entity": "UsrRequest", "values": map[string]any{"Name": "x"},
			"watch": []any{"Activity"}}, want: "Safe environment confirmation required but it was declined or the context is non-interactive. Environment: '[redacted-uri]'."},
	} {
		result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "clio-run",
			Arguments: map[string]any{"command": test.tool, "args": test.args}})
		if err != nil {
			t.Fatal(err)
		}
		text := result.Content[0].(*mcp.TextContent).Text
		if test.raised {
			if text != test.want || !result.IsError {
				t.Errorf("%s %v:\n got  %s\n want %s", test.tool, test.args, text, test.want)
			}
			continue
		}
		var envelope map[string]any
		if err := json.Unmarshal([]byte(text), &envelope); err != nil {
			t.Fatalf("%s: %s", test.tool, text)
		}
		if envelope["success"] != false || envelope["error"] != test.want {
			t.Errorf("%s %v:\n got  %v\n want %s", test.tool, test.args, envelope["error"], test.want)
		}
		for key, value := range test.extra {
			if envelope[key] != value {
				t.Errorf("%s %v: %s = %v, want %v", test.tool, test.args, key, envelope[key], value)
			}
		}
		for _, key := range test.absents {
			if _, ok := envelope[key]; ok {
				t.Errorf("%s %v: %s should be absent: %s", test.tool, test.args, key, text)
			}
		}
		if test.tool == "trigger-smoke" && envelope["correlation-id"] == nil {
			t.Errorf("trigger-smoke answered without a correlation-id: %s", text)
		}
	}
}

// TestT21aFailureTextIsRedactedAsClioSpellsIt: clio redacts a failed answer in System.Text.Json's default
// spelling, where "d:'POST'" reads as a drive path; the answer text carries that spelling for these tools.
func TestT21aFailureTextIsRedactedAsClioSpellsIt(t *testing.T) {
	result := t21aResult(map[string]any{"success": false, "error": "x", "next-step": "fetch('u', {method:'POST'})"})
	redactFailureContent(result)
	text := result.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(text, "metho[redacted-path]") {
		t.Fatalf("text = %s", text)
	}
}
