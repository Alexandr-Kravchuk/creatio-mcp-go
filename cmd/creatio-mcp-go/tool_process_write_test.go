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

func TestProcessWriteAnnotationsMatchClio(t *testing.T) {
	expected := map[string]toolAnnotations{
		"set-active-business-process-version": {Destructive: true, Idempotent: true},
	}
	for name, want := range expected {
		got := annotationsOf(name)
		if got.ReadOnly != want.ReadOnly || got.Destructive != want.Destructive || got.Idempotent != want.Idempotent || got.OpenWorld {
			t.Errorf("%s annotations %#v", name, got)
		}
	}
}
