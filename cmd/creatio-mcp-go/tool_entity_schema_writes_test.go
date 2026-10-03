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

// The entity-schema write tools are not read-only: a raw call answers confirmation-required, and both
// executors run them. Each case refuses in the tool's own validation, so the fake stand only answers reads.
func TestEntitySchemaWritesAreGatedAndReachableThroughExecutors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/Login") {
			w.Write([]byte(`{"Code":0}`))
			return
		}
		if strings.Contains(r.URL.Path, "EntitySchemaDesignerService") || strings.Contains(r.URL.Path, "SchemaDesignerRequest") {
			t.Errorf("a refused call reached the designer: %s", r.URL.Path)
		}
		w.Write([]byte(`{"success":true,"rows":[]}`))
	}))
	defer server.Close()
	client, _ := creatio.NewClient(creatio.Config{BaseURL: server.URL, Login: "example", Password: "replace-me"})
	session := connectTestClient(t, newMCPServer(client), mcp.NewClient(&mcp.Implementation{Name: "entity-schema-probe", Version: "test"}, nil))
	cases := []struct {
		tool      string
		args      map[string]any
		want      string
		dataForge bool
	}{
		{"create-entity-schema", map[string]any{"package-name": "P", "schema-name": "UsrA"},
			"Schema 'UsrA' requires 'title-localizations' with a non-empty 'en-US' value.", true},
		{"create-lookup", map[string]any{"package-name": "P", "schema-name": "UsrA", "title-localizations": map[string]any{"en-US": "A"},
			"columns": []any{map[string]any{"name": "Name", "type": "Text"}}}, "create-lookup inherits BaseLookup columns. Do not add inherited columns: Name.", true},
		{"update-entity-schema", map[string]any{"package-name": "P", "schema-name": "UsrA", "operations": []any{}},
			creatio.UpdateEntitySchemaMissingOperations, false},
		{"modify-entity-schema-column", map[string]any{"package-name": "P", "schema-name": "UsrA", "action": "rename", "column-name": "UsrB", "type": "Text"},
			"action must be one of: add, modify, remove.", false},
		{"set-entity-schema-properties", map[string]any{"package-name": "P", "schema-name": "UsrA"},
			"At least one schema property to set is required (for example --primary-display-column, --is-db-view, --title or --title-localizations). (Parameter 'options')", false},
	}
	for _, item := range cases {
		t.Run(item.tool, func(t *testing.T) {
			raw, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: item.tool, Arguments: item.args})
			if err != nil || !raw.IsError || !schemaWriteTestContainsCode(raw, "confirmation-required") {
				t.Fatalf("raw gate %#v %v", raw, err)
			}
			for _, executor := range []string{"clio-run", "clio-run-destructive"} {
				result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: executor, Arguments: map[string]any{"command": item.tool, "args": item.args}})
				if err != nil || result.IsError {
					t.Fatalf("%s %#v %v", executor, result, err)
				}
				var envelope struct {
					ExitCode  int                  `json:"exit-code"`
					Messages  []creatio.LogMessage `json:"execution-log-messages"`
					DataForge json.RawMessage      `json:"dataforge"`
				}
				text := result.Content[0].(*mcp.TextContent).Text
				if err := json.Unmarshal([]byte(text), &envelope); err != nil {
					t.Fatal(err)
				}
				if envelope.ExitCode != 1 || envelope.Messages[len(envelope.Messages)-1].Value != item.want || (len(envelope.DataForge) > 0) != item.dataForge {
					t.Fatalf("%s: %s", executor, text)
				}
			}
		})
	}
	t.Run("sync-schemas", func(t *testing.T) {
		raw, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "sync-schemas", Arguments: map[string]any{"package-name": "P"}})
		if err != nil || !raw.IsError || !schemaWriteTestContainsCode(raw, "confirmation-required") {
			t.Fatalf("raw gate %#v %v", raw, err)
		}
		result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "clio-run", Arguments: map[string]any{"command": "sync-schemas",
			"args": map[string]any{"package-name": "P", "operations": []any{map[string]any{"type": "seed-data", "schema-name": "UsrA"}}}}})
		if err != nil {
			t.Fatal(err)
		}
		text := result.Content[0].(*mcp.TextContent).Text
		if !strings.Contains(text, `"error":"sync-schemas operations[0] is invalid: a seed-data operation requires a non-empty 'seed-rows' array."`) ||
			!strings.Contains(text, `"dataforge"`) || !strings.Contains(text, `"resume-plan"`) {
			t.Fatal(text)
		}
	})
	t.Run("binding", func(t *testing.T) {
		result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "clio-run", Arguments: map[string]any{"command": "create-entity-schema",
			"args": map[string]any{"package-name": "P", "schema-name": "UsrA", "title-localizations": "A"}}})
		if err != nil || !result.IsError || !strings.Contains(result.Content[0].(*mcp.TextContent).Text, "invalid-parameter-type") {
			t.Fatalf("%#v %v", result, err)
		}
	})
}
