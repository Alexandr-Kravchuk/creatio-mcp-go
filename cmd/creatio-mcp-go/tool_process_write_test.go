package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// processWriteTestServer is a Creatio with CrtProcessBuilder at version (or without it when "") whose
// ProcessDesignService answers come from respond; it records every service request body by route.
func processWriteTestServer(t *testing.T, version string, respond func(path, body string) string) (*mcp.ClientSession, func() map[string]string) {
	t.Helper()
	var mu sync.Mutex
	seen := map[string]string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		seen[r.URL.Path] = string(raw)
		mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/Login"):
			_, _ = w.Write([]byte(`{"Code":0}`))
		case strings.HasSuffix(r.URL.Path, "/SelectQuery") && strings.Contains(string(raw), `"SysPackage"`):
			rows := `{"Name":"Base","Version":"8.1.0"}`
			if version != "" {
				rows += `,{"Name":"CrtProcessBuilder","Version":"` + version + `"}`
			}
			_, _ = w.Write([]byte(`{"success":true,"rows":[` + rows + `]}`))
		default:
			_, _ = w.Write([]byte(respond(r.URL.Path, string(raw))))
		}
	}))
	t.Cleanup(server.Close)
	client, err := creatio.NewClient(creatio.Config{BaseURL: server.URL, Login: "example", Password: "replace-me"})
	if err != nil {
		t.Fatal(err)
	}
	session := connectTestClient(t, newMCPServer(client), mcp.NewClient(&mcp.Implementation{Name: "process-write-probe", Version: "test"}, nil))
	return session, func() map[string]string {
		mu.Lock()
		defer mu.Unlock()
		copied := map[string]string{}
		for key, value := range seen {
			copied[key] = value
		}
		return copied
	}
}

// processWriteCall calls a tool through every path callers use and returns clio-run's answer, after checking
// that all paths agree; a raw call to a write-capable tool must be refused.
func processWriteCall(t *testing.T, session *mcp.ClientSession, tool string, args map[string]any) (map[string]any, *mcp.CallToolResult) {
	t.Helper()
	var first map[string]any
	var firstResult *mcp.CallToolResult
	for _, executor := range []string{"clio-run", "clio-run-destructive"} {
		result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: executor, Arguments: map[string]any{"command": tool, "args": args}})
		if err != nil {
			t.Fatalf("%s %s: %v", executor, tool, err)
		}
		var answer map[string]any
		_ = json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &answer)
		if first == nil {
			first, firstResult = answer, result
		}
	}
	if requiresConfirmation(tool) {
		raw, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
		if err != nil || !raw.IsError || raw.StructuredContent.(map[string]any)["code"] != "confirmation-required" {
			t.Fatalf("raw %s = %#v %v", tool, raw, err)
		}
	}
	return first, firstResult
}

func processWriteEnvelope(t *testing.T, answer map[string]any) (int, []string, string) {
	t.Helper()
	code, _ := answer["exit-code"].(float64)
	messages := []string{}
	items, _ := answer["execution-log-messages"].([]any)
	for _, item := range items {
		message := item.(map[string]any)
		messages = append(messages, message["message-type"].(string)+": "+message["value"].(string))
	}
	note, _ := answer["note"].(string)
	return int(code), messages, note
}

func TestSetActiveBusinessProcessVersionMCP(t *testing.T) {
	session, seen := processWriteTestServer(t, "99.0.0.0", func(path, body string) string {
		return `{"SetActiveProcessVersionResult":{"success":true,"activeVersionName":"UsrP_v2","activeVersionSchemaUId":"aaaaaaaa-0000-0000-0000-000000000002"}}`
	})
	answer, _ := processWriteCall(t, session, "set-active-business-process-version", map[string]any{"version-name": "UsrP_v2"})
	code, messages, note := processWriteEnvelope(t, answer)
	if code != 0 || note != "compile-creatio not required" || len(messages) != 3 || messages[1] != "Info: Version 'UsrP_v2' is now the actual one (UId: aaaaaaaa-0000-0000-0000-000000000002)." {
		t.Fatalf("%d %q %q", code, note, messages)
	}
	if body := seen()["/0/rest/ProcessDesignService/SetActiveProcessVersion"]; body != `{"request":{"name":"UsrP_v2"}}` {
		t.Fatalf("body = %s", body)
	}
	refusals := []struct {
		args map[string]any
		code int
		want string
	}{
		{map[string]any{}, -1, "one of version-name or version-uid is required."},
		{map[string]any{"version-name": "a", "version-uid": "b"}, -1, "Provide only one of version-name or version-uid, not both."},
		{map[string]any{"version-name": "a", "environment-name": " "}, -1, "environment-name is required and cannot be empty."},
		{map[string]any{"version-name": "a", "process-name": "b", "environmentName": "c"}, 1, "Rename: 'environmentName' -> 'environment-name'. Unknown args: 'process-name'. Valid: environment-name, version-name, version-uid."},
	}
	for _, item := range refusals {
		answer, _ := processWriteCall(t, session, "set-active-business-process-version", item.args)
		code, messages, note := processWriteEnvelope(t, answer)
		if code != item.code || len(messages) != 1 || messages[0] != "Error: "+item.want || note != "" {
			t.Errorf("%v: %d %q %q", item.args, code, messages, note)
		}
	}
	_, result := processWriteCall(t, session, "set-active-business-process-version", map[string]any{"version-name": 5})
	if !result.IsError || !strings.Contains(result.Content[0].(*mcp.TextContent).Text, "argument 'version-name' for MCP tool 'set-active-business-process-version' must be a string") {
		t.Fatalf("binding %#v", result)
	}
}

func TestProcessDesignWritesMCP(t *testing.T) {
	session, seen := processWriteTestServer(t, "99.0.0.0", func(path, body string) string {
		switch {
		case strings.HasSuffix(path, "/BuildProcess"):
			return `{"BuildProcessResult":{"success":true,"schemaName":"UsrP","schemaUId":"u1"}}`
		case strings.HasSuffix(path, "/ModifyProcess"):
			return `{"ModifyProcessResult":{"success":true,"schemaName":"UsrP","schemaUId":"u1","appliedOperations":1}}`
		case strings.HasSuffix(path, "/ModifyProcessAsNewVersion"):
			return `{"ModifyProcessAsNewVersionResult":{"success":true,"versionName":"UsrP_v2","version":2,"isActiveVersion":false}}`
		}
		return `{}`
	})
	successes := []struct {
		tool  string
		args  map[string]any
		route string
		body  string
		last  string
	}{
		{"create-business-process", map[string]any{"descriptor": map[string]any{"name": "UsrP"}, "package-name": "UsrPkg"}, "BuildProcess",
			`{"request":{"name":"UsrP","packageName":"UsrPkg"}}`, "Info: Process 'UsrP' created (UId: u1)."},
		{"create-business-process", map[string]any{"descriptor": `{"name":"UsrP"}`}, "BuildProcess", `{"request":{"name":"UsrP"}}`, "Info: Process 'UsrP' created (UId: u1)."},
		{"modify-business-process", map[string]any{"process-name": "UsrP", "operations": []any{map[string]any{"op": "x"}}, "confirm-layout-change": true}, "ModifyProcess",
			`{"request":{"name":"UsrP","operations":[{"op":"x"}],"confirmLayoutChange":true}}`, "Info: Process 'UsrP' edited (1 operation(s) applied; UId: u1)."},
		{"modify-business-process-as-new-version", map[string]any{"process-uid": "u1", "operations": " "}, "ModifyProcessAsNewVersion",
			`{"request":{"uid":"u1","operations":[]}}`, "Info: The source version is still the actual one and keeps running. ASK THE USER to open this version and say whether to make it actual; call set-active-business-process-version only once they have. Do not chain the two - a version is created inactive precisely so they get to look first."},
	}
	for _, item := range successes {
		answer, _ := processWriteCall(t, session, item.tool, item.args)
		code, messages, note := processWriteEnvelope(t, answer)
		if code != 0 || note != "compile-creatio not required" || messages[len(messages)-1] != item.last {
			t.Errorf("%s: %d %q %q", item.tool, code, note, messages)
		}
		if body := seen()["/0/rest/ProcessDesignService/"+item.route]; body != item.body {
			t.Errorf("%s body = %s", item.tool, body)
		}
	}
	refusals := []struct {
		tool string
		args map[string]any
		code int
		want string
	}{
		{"create-business-process", map[string]any{}, 1, "descriptor is required and cannot be empty."},
		{"create-business-process", map[string]any{"descriptor": "  "}, 1, "descriptor is required and cannot be empty."},
		{"create-business-process", map[string]any{"descriptor": []any{}}, 1, "descriptor must be a JSON object, or a string holding one. Received a JSON array."},
		{"create-business-process", map[string]any{"descriptor": true, "x": 1}, 1, "Unknown args: 'x'. Valid: environment-name, package-name, descriptor."},
		{"create-business-process", map[string]any{"descriptor": "{}", "environment-name": ""}, -1, "environment-name is required and cannot be empty."},
		{"modify-business-process", map[string]any{"operations": "[]"}, -1, "one of process-name or process-uid is required."},
		{"modify-business-process", map[string]any{"process-name": "a"}, 1, "operations is required and cannot be empty."},
		{"modify-business-process", map[string]any{"process-name": "a", "operations": 3}, 1, "operations must be a JSON array, or a string holding one. Received a JSON number."},
		{"modify-business-process", map[string]any{"process-name": "a", "process-uid": "b", "operations": "[]"}, -1, "Provide only one of process-name or process-uid, not both."},
		{"modify-business-process-as-new-version", map[string]any{"process-name": "a", "operations": false}, 1, "operations must be a JSON array, or a string holding one. Received a JSON boolean."},
		{"modify-business-process-as-new-version", map[string]any{"process-name": "a", "confirm-layout-change": true}, 1, "Unknown args: 'confirm-layout-change'. Valid: environment-name, process-name, process-uid, package-name, operations."},
	}
	for _, item := range refusals {
		answer, _ := processWriteCall(t, session, item.tool, item.args)
		code, messages, note := processWriteEnvelope(t, answer)
		if code != item.code || len(messages) != 1 || messages[0] != "Error: "+item.want || note != "" {
			t.Errorf("%s %v: %d %q", item.tool, item.args, code, messages)
		}
	}
	_, result := processWriteCall(t, session, "modify-business-process", map[string]any{"process-name": "a", "operations": "[]", "confirm-layout-change": "yes"})
	if !result.IsError || !strings.Contains(result.Content[0].(*mcp.TextContent).Text, "argument 'confirm-layout-change' for MCP tool 'modify-business-process' must be a boolean") {
		t.Fatalf("binding %#v", result)
	}
}

func TestProcessDesignWritesRefuseWithoutCrtProcessBuilder(t *testing.T) {
	session, seen := processWriteTestServer(t, "", func(path, body string) string { return `{}` })
	for tool, args := range map[string]map[string]any{
		"create-business-process":                map[string]any{"descriptor": "{}"},
		"modify-business-process":                map[string]any{"process-name": "a", "operations": "[]"},
		"modify-business-process-as-new-version": map[string]any{"process-name": "a"},
	} {
		answer, _ := processWriteCall(t, session, tool, args)
		code, messages, _ := processWriteEnvelope(t, answer)
		if code != 1 || len(messages) != 1 || !strings.HasPrefix(messages[0], "Error: To use this command, you need to install the CrtProcessBuilder package version 1.6.6.77 or higher.") {
			t.Errorf("%s: %d %q", tool, code, messages)
		}
	}
	for path := range seen() {
		if strings.Contains(path, "ProcessDesignService") {
			t.Fatalf("the service was called: %s", path)
		}
	}
}

func TestProcessWriteAnnotationsMatchClio(t *testing.T) {
	expected := map[string]toolAnnotations{
		"set-active-business-process-version":    {Destructive: true, Idempotent: true},
		"create-business-process":                {Destructive: true},
		"modify-business-process":                {Destructive: true},
		"modify-business-process-as-new-version": {},
	}
	for name, want := range expected {
		got := annotationsOf(name)
		if got.ReadOnly != want.ReadOnly || got.Destructive != want.Destructive || got.Idempotent != want.Idempotent || got.OpenWorld {
			t.Errorf("%s annotations %#v", name, got)
		}
	}
}
