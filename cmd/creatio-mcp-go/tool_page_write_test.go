package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestPageWriteToolsAreGatedAndAnswerThroughTheExecutors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/Login") {
			w.Write([]byte(`{"Code":0}`))
			return
		}
		t.Errorf("unexpected Creatio call %s", r.URL.Path)
	}))
	defer server.Close()
	client, _ := creatio.NewClient(creatio.Config{BaseURL: server.URL, Login: "example", Password: "replace-me"})
	session := connectTestClient(t, newMCPServer(client), mcp.NewClient(&mcp.Implementation{Name: "page-write-probe", Version: "test"}, nil))
	cases := map[string]struct {
		args map[string]any
		want string
	}{
		"create-page": {map[string]any{"schema-name": "1bad", "template": "t", "package-name": "p"},
			`"error":"schema-name must start with a letter and contain only letters, digits, or underscores"`},
		"update-page": {map[string]any{"schema-name": "UsrP", "body": "", "dry-run": true},
			`"dryRun":true,"error":"Either 'body' or 'body-file' must provide page body content."`},
		"create-related-page-addon": {map[string]any{"package-name": "p", "pages": []any{}},
			`"error":"entity-schema-name is required."`},
		"sync-pages": {map[string]any{"pages": []any{map[string]any{"schema-name": "UsrP", "body": "define("}}},
			`"error":"JavaScript syntax error at line 1, column 8: Unexpected end of input. The body was NOT sent to Creatio."`},
	}
	for name, item := range cases {
		t.Run(name, func(t *testing.T) {
			raw, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: item.args})
			if err != nil || !raw.IsError || !schemaWriteTestContainsCode(raw, "confirmation-required") {
				t.Fatalf("raw gate %#v %v", raw, err)
			}
			for _, executor := range []string{"clio-run", "clio-run-destructive"} {
				result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: executor,
					Arguments: map[string]any{"command": name, "args": item.args}})
				if err != nil {
					t.Fatal(err)
				}
				assertOneJSONTextContent(t, result)
				if text := result.Content[0].(*mcp.TextContent).Text; !strings.Contains(text, item.want) {
					t.Fatalf("%s: %s", executor, text)
				}
			}
		})
	}
}
