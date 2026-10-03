package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestGetComponentInfoAnswersDirectlyAndThroughClioRun(t *testing.T) {
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/mcp/latest/ComponentRegistry.json" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"components":[{"componentType":"crt.Button","description":"Clickable."},{"componentType":"crt.Input"}]}`))
	}))
	defer cdn.Close()
	t.Setenv("CLIO_HOME", t.TempDir())
	t.Setenv("CLIO_COMPONENT_REGISTRY_CDN_BASE_URL", cdn.URL+"/api/mcp/")
	t.Setenv("CLIO_COMPONENT_REGISTRY_LOCAL_FILE", "")
	if !isClioResident("get-component-info") || requiresConfirmation("get-component-info") {
		t.Fatal("get-component-info is a resident read-only tool in clio")
	}
	client, _ := creatio.NewClient(creatio.Config{BaseURL: "http://127.0.0.1:1", Login: "example", Password: "replace-me"})
	session := connectTestClient(t, newMCPServer(client), mcp.NewClient(&mcp.Implementation{Name: "component-info-probe", Version: "test"}, nil))

	calls := callPaths("get-component-info", map[string]any{"component-type": "crt.button"})
	calls = append(calls, &mcp.CallToolParams{Name: "get-component-info", Arguments: map[string]any{"args": map[string]any{"component-type": "crt.button"}}})
	if len(calls) != 3 {
		t.Fatalf("calls = %d", len(calls))
	}
	for _, call := range calls {
		result, err := session.CallTool(context.Background(), call)
		if err != nil || result.IsError {
			t.Fatalf("%s: %#v %v", call.Name, result, err)
		}
		var answer map[string]any
		if err := json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &answer); err != nil {
			t.Fatal(err)
		}
		if answer["success"] != true || answer["mode"] != "detail" || answer["componentType"] != "crt.Button" || answer["resolvedFrom"] != "latest-fallback" {
			t.Fatalf("%s: %v", call.Name, answer)
		}
	}

	alias, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "get-component-info",
		Arguments: map[string]any{"componentType": "crt.Input", "zzz": 1}})
	if err != nil || alias.IsError {
		t.Fatalf("alias: %#v %v", alias, err)
	}
	want := `{"success":false,"mode":"list","count":0,"error":"Rename: 'componentType' -\u003e 'component-type'. Unknown args: 'zzz'. ` +
		`Valid: component-type, composite, search, schema-type, environment-name, version, uri, login, password.","items":[]}`
	if text := alias.Content[0].(*mcp.TextContent).Text; text != want {
		t.Fatalf("alias answer %s", text)
	}

	typed, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "get-component-info", Arguments: map[string]any{"search": 5}})
	if err != nil || !typed.IsError || typed.Content[0].(*mcp.TextContent).Text !=
		"invalid-parameter-type: argument 'search' for MCP tool 'get-component-info' must be a string. Received an incompatible JSON value." {
		t.Fatalf("typed: %#v %v", typed, err)
	}

	exclusive, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "get-component-info",
		Arguments: map[string]any{"version": "8.3.3", "environment-name": "dev"}})
	if err != nil || !strings.Contains(exclusive.Content[0].(*mcp.TextContent).Text,
		`"error":"'version' and 'environment-name'/'uri' are mutually exclusive. Pass one or neither."`) {
		t.Fatalf("exclusive: %#v %v", exclusive, err)
	}
}
