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

func TestAppWriteToolsAreGatedAndRunThroughBothExecutors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/Login"):
			w.Write([]byte(`{"Code":0}`))
		case strings.HasSuffix(r.URL.Path, "/GetApplicationInfo"):
			w.Write([]byte(`{"applicationInfo":{"sysValues":{"userCulture":{"displayValue":"en-US"}}}}`))
		case strings.HasSuffix(r.URL.Path, "/QuerySysSettings"):
			w.Write([]byte(`{"success":true,"values":{"SchemaNamePrefix":"Usr"}}`))
		default:
			w.Write([]byte(`{"success":true,"rows":[]}`))
		}
	}))
	defer server.Close()
	client, _ := creatio.NewClient(creatio.Config{BaseURL: server.URL, Login: "example", Password: "replace-me"})
	session := connectTestClient(t, newMCPServer(client), mcp.NewClient(&mcp.Implementation{Name: "app-write-probe", Version: "test"}, nil))
	cases := []struct {
		tool       string
		invalid    map[string]any
		refusal    string
		missingApp map[string]any
	}{
		{"create-app", map[string]any{"code": "UsrTodo"}, "name is required.", nil},
		{"create-app-section", map[string]any{"application-code": "UsrTodo", "caption": "Orders", "icon-background": "beige"},
			"icon-background 'beige' is not a recognised color.", map[string]any{"application-code": "UsrNone", "caption": "Orders"}},
		{"update-app-section", map[string]any{"application-code": "UsrTodo", "section-code": "UsrOrders"},
			"Provide at least one mutable field: caption, description, icon-id, or icon-background.",
			map[string]any{"application-code": "UsrNone", "section-code": "UsrOrders", "caption": "Orders"}},
		{"delete-app-section", map[string]any{"application-code": "UsrTodo"}, "section-code is required.",
			map[string]any{"application-code": "UsrNone", "section-code": "UsrOrders"}},
	}
	for _, tc := range cases {
		t.Run(tc.tool, func(t *testing.T) {
			raw, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: tc.tool, Arguments: tc.invalid})
			if err != nil || !raw.IsError || !schemaWriteTestContainsCode(raw, "confirmation-required") {
				t.Fatalf("raw gate %#v %v", raw, err)
			}
			for _, executor := range []string{"clio-run", "clio-run-destructive"} {
				result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: executor, Arguments: map[string]any{"command": tc.tool, "args": tc.invalid}})
				if err != nil || result.IsError {
					t.Fatalf("%s %#v %v", executor, result, err)
				}
				var answer map[string]any
				_ = json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &answer)
				if answer["success"] != false || !strings.HasPrefix(answer["error"].(string), tc.refusal) {
					t.Fatalf("%s validation %v", executor, answer)
				}
				audit := result.Meta["clio-run"].(map[string]any)
				if audit["dispatchedTool"] != tc.tool || audit["destructive"] != true {
					t.Fatalf("audit %#v", audit)
				}
			}
			if tc.missingApp == nil {
				return
			}
			result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "clio-run", Arguments: map[string]any{"command": tc.tool, "args": tc.missingApp}})
			if err != nil || result.IsError || !strings.Contains(result.Content[0].(*mcp.TextContent).Text, `"error":"Application 'UsrNone' not found."`) {
				t.Fatalf("missing app %#v %v", result, err)
			}
		})
	}
	bad, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "clio-run", Arguments: map[string]any{"command": "create-app", "args": map[string]any{"name": "X", "code": "X", "with-mobile-pages": "no"}}})
	if err != nil || !bad.IsError || !strings.Contains(bad.Content[0].(*mcp.TextContent).Text, "argument 'with-mobile-pages' for MCP tool 'create-app' must be a boolean") {
		t.Fatalf("binding refusal %#v %v", bad, err)
	}
}
