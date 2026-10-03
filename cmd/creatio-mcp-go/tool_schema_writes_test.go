package main

import (
	"context"
	"encoding/json"
	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSchemaAndAppWritesAreGatedAndExecutorReturnsValidation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/Login") {
			w.Write([]byte(`{"Code":0}`))
		} else {
			w.Write([]byte(`{"success":true,"rows":[]}`))
		}
	}))
	defer server.Close()
	client, _ := creatio.NewClient(creatio.Config{BaseURL: server.URL, Login: "example", Password: "replace-me"})
	session := connectTestClient(t, newMCPServer(client), mcp.NewClient(&mcp.Implementation{Name: "schema-probe", Version: "test"}, nil))
	for _, name := range []string{"create-schema", "update-schema", "create-sql-schema", "update-sql-schema", "install-sql-schema", "delete-schema", "export-schema", "import-schema", "delete-app"} {
		t.Run(name, func(t *testing.T) {
			raw, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: map[string]any{}})
			if err != nil || !raw.IsError || !schemaWriteTestContainsCode(raw, "confirmation-required") {
				t.Fatalf("raw gate %#v %v", raw, err)
			}
			for _, executor := range []string{"clio-run", "clio-run-destructive"} {
				result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: executor, Arguments: map[string]any{"command": name, "args": map[string]any{}}})
				if err != nil || result.IsError {
					t.Fatalf("%s %#v %v", executor, result, err)
				}
				assertOneJSONTextContent(t, result)
				text := result.Content[0].(*mcp.TextContent).Text
				if strings.Contains(text, "confirmation-required") || (!strings.Contains(text, "required") && !strings.Contains(text, "cannot be empty") && !strings.Contains(text, "need to install")) {
					t.Fatalf("%s validation %s", executor, text)
				}
			}
		})
	}
}

func schemaWriteTestContainsCode(result *mcp.CallToolResult, code string) bool {
	raw, _ := json.Marshal(result.StructuredContent)
	return strings.Contains(string(raw), code)
}
