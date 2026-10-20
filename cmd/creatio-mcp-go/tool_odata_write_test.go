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

// The answers below are clio 8.1.0.142's, measured with its MCP server on the same calls (T20).
func TestODataWriteArgumentsOfClio142(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(500)
	}))
	defer server.Close()
	client, _ := creatio.NewClient(creatio.Config{BaseURL: server.URL, Login: "example-user", Password: "replace-me"})
	session := connectTestClient(t, newMCPServer(client), mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil))
	const id = "11111111-1111-1111-1111-111111111111"
	cases := []struct {
		tool, label string
		args        map[string]any
		error       string // the answer's error, or the raised failure
		warning     string
		raised      bool
	}{
		{"odata-update", "rows-file without data reaches the confirmation gate", map[string]any{"entity": "Contact", "id": id, "rows-file": "/nonexistent/x.json"},
			"Refusing to update Contact(" + id + ") without confirmation. This is a destructive operation; re-call odata-update with \"confirm\": true to authorize this change.", "", false},
		{"odata-update", "both payloads", map[string]any{"entity": "Contact", "id": id, "data": map[string]any{"Name": "x"}, "rows-file": "/x.json"},
			"Provide either data or rows-file, not both.", "", false},
		{"odata-update", "snake_case rows_file", map[string]any{"entity": "Contact", "id": id, "data": map[string]any{"Name": "x"}, "rows_file": "/x.json"},
			"Rename: 'rows_file' -> 'rows-file'.", "", false},
		{"odata-update", "unknown key", map[string]any{"entity": "Contact", "id": id, "data": map[string]any{"Name": "x"}, "bogus": 1},
			"Unknown args: 'bogus'. Valid: entity, id, environment-name, data, rows-file, confirm.", "", false},
		{"odata-update", "synonym of data", map[string]any{"entity": "Contact", "id": id, "Values": map[string]any{"Name": "x"}},
			"Refusing to update Contact(" + id + ") without confirmation. This is a destructive operation; re-call odata-update with \"confirm\": true to authorize this change.",
			"Argument 'Values' was read as 'data', but the call was refused (see error); use 'data' in future calls.", false},
		{"odata-update", "synonym beside data", map[string]any{"entity": "Contact", "id": id, "values": map[string]any{"Name": "x"}, "data": map[string]any{"a": 1}},
			"Ambiguous payload: 'data' and 'values' were all supplied, and they name the same thing. Send the payload once, as 'data'. Nothing was written.", "", false},
		{"odata-update", "id still required", map[string]any{"entity": "Contact", "rows-file": "/x.json"},
			"invalid-parameter-type: argument 'args' for MCP tool 'odata-update' must be an object. Received an incompatible JSON value.", "", true},
		{"odata-update", "confirm must be a boolean", map[string]any{"entity": "Contact", "id": id, "data": map[string]any{"a": 1}, "confirm": nil},
			"invalid-parameter-type: argument 'confirm' for MCP tool 'odata-update' must be a boolean. Received an incompatible JSON value.", "", true},
		{"odata-create", "rows is optional now", map[string]any{"entity": "Contact"},
			"rows is required and must be a non-empty array of field/value objects.", "", false},
		{"odata-create", "synonym of rows", map[string]any{"entity": "Contact", "body": "x"},
			"rows is required and must be a non-empty array of field/value objects.",
			"Argument 'body' was read as 'rows', but the call was refused (see error); use 'rows' in future calls.", false},
		{"odata-create", "snake_case and unknown", map[string]any{"entity": "Contact", "rows": []any{map[string]any{"a": 1}}, "bogus": 1, "stop_on_error": true},
			"Rename: 'stop_on_error' -> 'stop-on-error'. Unknown args: 'bogus'. Valid: entity, environment-name, rows, rows-file, stop-on-error.", "", false},
		{"odata-delete", "ids without confirm", map[string]any{"entity": "Contact", "ids": []any{id, " " + id}},
			"Refusing to delete Contact(" + id + ") without confirmation. This is a destructive operation; re-call odata-delete with \"confirm\": true to authorize this deletion.", "", false},
		{"odata-delete", "id and ids", map[string]any{"entity": "Contact", "id": id, "ids": []any{id}}, "Pass id or ids, not both.", "", false},
		{"odata-delete", "unknown keys are ignored", map[string]any{"entity": "Contact", "id": id, "bogus": true},
			"Refusing to delete Contact(" + id + ") without confirmation. This is a destructive operation; re-call odata-delete with \"confirm\": true to authorize this deletion.", "", false},
		{"odata-delete", "cascade on Contact", map[string]any{"entity": "contact", "id": id, "cascade": true, "confirm": true},
			"cascade is refused for contact: rows that require a contact only refer to it, they are not owned by it. Delete without cascade, and read explanation for what blocks it.", "", false},
		{"odata-delete", "ids element of another kind", map[string]any{"entity": "Contact", "ids": []any{1}},
			"invalid-parameter-type: argument 'ids' for MCP tool 'odata-delete' contains a value that does not match the documented shape. Received an incompatible JSON value.", "", true},
		{"odata-delete", "dry-run must be a boolean", map[string]any{"entity": "Contact", "id": id, "dry-run": "true"},
			"invalid-parameter-type: argument 'dry-run' for MCP tool 'odata-delete' must be a boolean. Received an incompatible JSON value.", "", true},
	}
	for _, c := range cases {
		for _, call := range callPaths(c.tool, c.args) {
			result, err := session.CallTool(context.Background(), call)
			if err != nil {
				t.Fatalf("%s: %v", c.label, err)
			}
			text := result.Content[0].(*mcp.TextContent).Text
			if c.raised {
				if !result.IsError || !strings.Contains(text, c.error) {
					t.Fatalf("%s: %s", c.label, text)
				}
				continue
			}
			var answer struct {
				Error    string   `json:"error"`
				Warnings []string `json:"warnings"`
			}
			if err := json.Unmarshal([]byte(text), &answer); err != nil || answer.Error != c.error {
				t.Fatalf("%s: %s", c.label, text)
			}
			if (c.warning == "") != (len(answer.Warnings) == 0) || (c.warning != "" && answer.Warnings[0] != c.warning) {
				t.Fatalf("%s warnings: %s", c.label, text)
			}
		}
	}
	if requests != 0 {
		t.Fatalf("%d requests reached Creatio", requests)
	}
}
