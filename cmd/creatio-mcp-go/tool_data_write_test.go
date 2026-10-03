package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestODataWritesThroughBothExecutors(t *testing.T) {
	writes := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ServiceModel/AuthService.svc/Login":
			w.Write([]byte(`{"Code":0}`))
		case "/0/odata/UsrParityMock":
			writes++
			w.Write([]byte(`{"Id":"00000000-0000-0000-0000-000000000001"}`))
		case "/0/odata/UsrParityMock(00000000-0000-0000-0000-000000000001)":
			writes++
			w.WriteHeader(204)
		case "/0/odata/$metadata":
			w.Write([]byte(`<Schema><EntityType Name="UsrParityMock"><Property Name="Id" Type="Edm.Guid"/><Property Name="Name" Type="Edm.String"/></EntityType></Schema>`))
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
		}
	}))
	defer server.Close()
	client, _ := creatio.NewClient(creatio.Config{BaseURL: server.URL, Login: "example-user", Password: "replace-me"})
	session := connectTestClient(t, newMCPServer(client), mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil))
	for _, executor := range []string{"clio-run", "clio-run-destructive"} {
		for _, tool := range []string{"odata-create", "odata-update", "odata-delete"} {
			args := map[string]any{"entity": "UsrParityMock"}
			if tool == "odata-create" {
				args["rows"] = []any{map[string]any{"Name": "test"}}
			} else {
				args["id"] = "00000000-0000-0000-0000-000000000001"
				args["confirm"] = true
				if tool == "odata-update" {
					args["data"] = map[string]any{"Name": "test"}
				}
			}
			result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: executor, Arguments: map[string]any{"command": tool, "args": args}})
			if err != nil || result.IsError {
				t.Fatalf("%s %s result=%#v err=%v", executor, tool, result, err)
			}
			var response map[string]any
			if err := json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &response); err != nil {
				t.Fatal(err)
			}
			if tool == "odata-create" {
				if response["created"] != float64(1) {
					t.Fatalf("response %#v", response)
				}
			} else if response["success"] != true {
				t.Fatalf("response %#v", response)
			}
			before := writes
			result, err = session.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
			if err != nil || !result.IsError || writes != before {
				t.Fatalf("raw %s escaped write gate", tool)
			}
		}
	}
	if writes != 6 {
		t.Fatalf("writes=%d", writes)
	}
}
