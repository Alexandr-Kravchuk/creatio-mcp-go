package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestPageAndRightsWritesReachHandlersThroughExecutors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Errorf("local refusal contacted %s", r.URL.Path) }))
	defer server.Close()
	client, _ := creatio.NewClient(creatio.Config{BaseURL: server.URL, Login: "example-user", Password: "replace-me"})
	session := connectTestClient(t, newMCPServer(client), mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil))
	for _, executor := range []string{"clio-run", "clio-run-destructive"} {
		for _, test := range []struct {
			name string
			args map[string]any
		}{
			{"create-client-unit-schema", map[string]any{"schema-name": "bad/name", "package-name": "none"}},
			{"update-client-unit-schema", map[string]any{"schema-name": "UsrParityMock", "body": ""}},
			{"set-record-rights", map[string]any{"entity": "UsrParityMock", "record-id": "record", "grantee": "bad", "operation": "read"}},
		} {
			result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: executor, Arguments: map[string]any{"command": test.name, "args": test.args}})
			if err != nil || result.IsError {
				t.Fatalf("%s via %s result %#v err %v", test.name, executor, result, err)
			}
			assertOneJSONTextContent(t, result)
			result, err = session.CallTool(context.Background(), &mcp.CallToolParams{Name: test.name, Arguments: test.args})
			if err != nil || !result.IsError {
				t.Fatalf("%s raw gate result %#v err %v", test.name, result, err)
			}
		}
	}
}
