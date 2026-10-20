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

var t19Tools = []string{"get-object-rights", "set-object-rights", "set-default-record-rights", "apply-default-record-rights",
	"get-entity-connections", "update-entity-connections"}

// t19Session is a server whose settings name a reachable environment that must never be contacted, and a Safe one.
func t19Session(t *testing.T) *mcp.ClientSession {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("a refused call contacted Creatio: %s", r.URL.Path)
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	clearCreatioVariables(t)
	path := filepath.Join(t.TempDir(), "appsettings.json")
	writeSettings(t, path, "t19", map[string]map[string]any{
		"t19":  {"Uri": server.URL, "Login": "example-user", "Password": "replace-me"},
		"safe": {"Uri": server.URL, "Login": "example-user", "Password": "replace-me", "Safe": true},
	})
	return connectTestClient(t, newMCPServerWithHiddenTools(newEnvironments(path), defaultHiddenToolServices()),
		mcp.NewClient(&mcp.Implementation{Name: "probe-client", Version: "test"}, nil))
}

func t19Call(t *testing.T, session *mcp.ClientSession, tool string, args map[string]any) (string, bool) {
	t.Helper()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "clio-run", Arguments: map[string]any{"command": tool, "args": args}})
	if err != nil {
		t.Fatal(err)
	}
	return result.Content[0].(*mcp.TextContent).Text, result.IsError
}

func TestT19ToolsCarryClioAnnotationsAndContracts(t *testing.T) {
	want := map[string]toolAnnotations{
		"get-object-rights":           {ReadOnly: true, Idempotent: true},
		"set-object-rights":           {Destructive: true, Idempotent: true},
		"set-default-record-rights":   {Destructive: true, Idempotent: true},
		"apply-default-record-rights": {Destructive: true},
		"get-entity-connections":      {ReadOnly: true, Idempotent: true},
		"update-entity-connections":   {Destructive: true, Idempotent: true},
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
}

func TestT19WritesAreGatedByRawNameAndReadsAreNot(t *testing.T) {
	session := t19Session(t)
	for _, name := range t19Tools {
		result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name,
			Arguments: map[string]any{"args": map[string]any{"environment-name": "t19", "entity-schema-name": " "}}})
		if err != nil {
			t.Fatal(err)
		}
		text := result.Content[0].(*mcp.TextContent).Text
		gated := strings.HasPrefix(text, "Tool '"+name+"' can overwrite or delete existing state and was NOT executed")
		if wantGated := !strings.HasPrefix(name, "get-"); gated != wantGated || gated != requiresConfirmation(name) {
			t.Errorf("%s direct call: %s", name, text)
		}
	}
}

func TestT19ArgumentRefusalsMatchClio(t *testing.T) {
	session := t19Session(t)
	for _, test := range []struct {
		tool    string
		args    map[string]any
		want    string
		isError bool
	}{
		{"get-object-rights", map[string]any{"environment-name": "t19", "entity-schema-name": "A", "bogus": 1},
			`{"success":false,"error":"Unknown args: 'bogus'. Valid: environment-name, entity-schema-name, grantee, author, include-connected."}`, false},
		{"set-object-rights", map[string]any{"environment": "t19", "entity-schema-name": "A"},
			`{"success":false,"error":"Rename: 'environment' -\u003e 'environment-name'."}`, false},
		{"set-object-rights", map[string]any{"environment-name": "t19", "entity-schema-name": "A", "revoke": "true", "bogus": 1},
			"invalid-parameter-type: argument 'revoke' for MCP tool 'set-object-rights' must be a boolean. Received an incompatible JSON value.", true},
		{"set-object-rights", map[string]any{"environment-name": "t19", "entity-schema-name": "A", "grantee": "x", "operations": "read"},
			`{"success":false,"error":"Error: --grantee must be a SysAdminUnit id (GUID)."}`, false},
		{"set-object-rights", map[string]any{"environment-name": "t19", "entity-schema-name": "A", "disable-operation-permissions": true, "revoke": true},
			`{"success":false,"error":"Error: --disable-operation-permissions is a call of its own: it turns operation permissions OFF and keeps every row as it is, so --grantee, --operations and --revoke do not go with it. To clear a role's operations, revoke them in a separate call."}`, false},
		{"set-object-rights", map[string]any{"environment-name": "safe", "entity-schema-name": "A"},
			`{"success":false,"error":"Safe environment confirmation required but it was declined or the context is non-interactive. Environment: '[redacted-uri]'."}`, false},
		{"set-default-record-rights", map[string]any{"environment-name": "t19", "entity-schema-name": "Bad name", "enable-record-permissions": true},
			`{"success":false,"error":"Error: --entity-schema-name 'Bad name' is not a schema name (letters, digits and '_' only)."}`, false},
		{"set-default-record-rights", map[string]any{"environment-name": "t19", "entity-schema-name": "A", "author": "11111111-1111-1111-1111-111111111111",
			"grantee": "11111111-1111-1111-1111-111111111111", "operations": "read", "revoke": true, "level": "granted"},
			`{"success":false,"error":"Error: --level and --do-not-apply-for-manager apply to a grant; a revoke sets the named operations to 'not set'."}`, false},
		{"apply-default-record-rights", map[string]any{"environment-name": "t19", "entity-schema-name": "A", "timeout-seconds": "0"},
			`{"success":false,"error":"Error: --timeout-seconds must be a positive number of seconds."}`, false},
		{"apply-default-record-rights", map[string]any{"environment-name": "t19", "entity-schema-name": "A", "timeout-seconds": "abc"},
			"invalid-parameter-type: argument 'timeout-seconds' for MCP tool 'apply-default-record-rights' must be a number. Received an incompatible JSON value.", true},
		{"get-entity-connections", map[string]any{"environment-name": "nope", "package-name": "Custom"},
			`{"success":false,"error":"entity-schema-name is required."}`, false},
		{"update-entity-connections", map[string]any{"environment-name": "t19", "entity-schema-name": "A", "package-name": "Custom"},
			`{"success":false,"error":"connections is required: pass the full desired ordered list of column names (an empty list removes every connection)."}`, false},
		{"update-entity-connections", map[string]any{"environment-name": "t19", "entity-schema-name": "A", "package-name": "Custom", "connections": []any{"Owner", nil}},
			`{"success":false,"error":"connections must not contain an empty column name."}`, false},
		{"update-entity-connections", map[string]any{"environment-name": "t19", "entity-schema-name": "A", "package-name": "Custom", "connections": []any{"Owner", 5}},
			"invalid-parameter-type: argument 'connections' for MCP tool 'update-entity-connections' contains a value that does not match the documented shape. Received an incompatible JSON value.", true},
	} {
		text, isError := t19Call(t, session, test.tool, test.args)
		// A binding failure is raised with clio's text as it is.
		if text != test.want || isError != test.isError {
			t.Errorf("%s %v:\n got  %s\n want %s", test.tool, test.args, text, test.want)
		}
	}
	text, _ := t19Call(t, session, "get-object-rights", map[string]any{"environment-name": "nope", "entity-schema-name": "A"})
	if !strings.HasPrefix(text, `{"success":false,"error":"[EnvironmentResolutionException] Environment with key 'nope' not found.`) {
		t.Errorf("unknown environment: %s", text)
	}
	text, isError := t19Call(t, session, "get-entity-connections", map[string]any{"environment-name": "safe", "entity-schema-name": "A", "package-name": "P"})
	if !isError || !strings.HasPrefix(text, "Error: tool 'get-entity-connections' failed: Safe environment confirmation required") {
		t.Errorf("safe environment: %s", text)
	}
}
