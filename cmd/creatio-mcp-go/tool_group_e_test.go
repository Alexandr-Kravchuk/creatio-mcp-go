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

func TestGroupEToolsAnswerByRawNameAndThroughClioRun(t *testing.T) {
	creatioServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ServiceModel/AuthService.svc/Login":
			_, _ = w.Write([]byte(`{"Code":0}`))
		case "/0/ServiceModel/ThemeService.svc/GetAvailableThemes":
			_, _ = w.Write([]byte(`{"success":true,"values":[]}`))
		case "/0/DataService/json/SyncReply/SelectQuery":
			_, _ = w.Write([]byte(`{"success":true,"rows":[]}`))
		case "/0/rest/schema.template.api/templates":
			_, _ = w.Write([]byte(`{"success":true,"items":[]}`))
		default:
			t.Errorf("unexpected Creatio route %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer creatioServer.Close()
	client, err := creatio.NewClient(creatio.Config{BaseURL: creatioServer.URL, Login: "example-user", Password: "replace-me"})
	if err != nil {
		t.Fatal(err)
	}
	session := connectTestClient(t, newMCPServer(client), mcp.NewClient(&mcp.Implementation{Name: "probe-client", Version: "test"}, nil))
	cases := map[string]struct {
		args map[string]any
		want string
	}{
		"list-themes":     {map[string]any{}, `{"success":true,"themes":[]}`},
		"list-printables": {map[string]any{"entity-name": "Contact"}, `{"success":true,"count":0,"printables":[]}`},
		"list-user-tasks": {map[string]any{}, `{"exit-code":1,"execution-log-messages":[{"message-type":"Error","value":` + groupEJSON(t, "To use this command, you need to install the CrtProcessBuilder package. Install the package in the target environment and retry.\nRun 'clio install-process-builder -e <environment>' (or call the install-process-builder MCP tool) to install or update CrtProcessBuilder.") + `}]}`},
		"list-page-templates": {map[string]any{"schema-type": "web"}, `{"success":true,"count":2,"items":[` +
			`{"uId":"eb4d4a67-25d8-fcfa-7851-c4c91efb7b9c","name":"BaseDashboardTemplate","title":"Dashboard","groupName":"DashboardPage","schemaType":9},` +
			`{"uId":"fbc98c89-0691-479c-bc25-59c11ac2365f","name":"CentralAreaDesktopTemplate","title":"Desktop","groupName":"Desktop","schemaType":9}]}`},
	}
	for name, c := range cases {
		for _, call := range []*mcp.CallToolParams{
			{Name: name, Arguments: c.args},
			{Name: "clio-run", Arguments: map[string]any{"command": name, "args": c.args}},
		} {
			result, err := session.CallTool(context.Background(), call)
			if err != nil || result.IsError {
				t.Fatalf("call %q via %q = %#v, err = %v", name, call.Name, result, err)
			}
			assertOneJSONTextContent(t, result)
			if text := result.Content[0].(*mcp.TextContent).Text; text != c.want {
				t.Errorf("%s via %s = %s, want %s", name, call.Name, text, c.want)
			}
		}
	}
	contract, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "get-tool-contract", Arguments: map[string]any{"name": "list-printables"}})
	if err != nil || contract.IsError {
		t.Fatalf("list-printables contract = %#v, err = %v", contract, err)
	}
}

func TestGroupEToolsRefuseForeignEnvironmentSelectors(t *testing.T) {
	session := connectTestClient(t, newMCPServer(&creatio.Client{}), mcp.NewClient(&mcp.Implementation{Name: "probe-client", Version: "test"}, nil))
	for _, call := range []*mcp.CallToolParams{
		// A uri that is not an absolute http(s) address cannot be a direct connection.
		{Name: "list-printables", Arguments: map[string]any{"uri": "elsewhere"}},
		{Name: "list-page-templates", Arguments: map[string]any{"environment-name": "other"}},
		{Name: "list-themes", Arguments: map[string]any{"foo": 1}},
	} {
		result, err := session.CallTool(context.Background(), call)
		if err != nil || result.IsError {
			t.Fatalf("%s = %#v, err = %v", call.Name, result, err)
		}
		var envelope struct {
			Success bool   `json:"success"`
			Error   string `json:"error"`
		}
		if err := json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &envelope); err != nil || envelope.Success || envelope.Error == "" {
			t.Fatalf("%s envelope = %#v, err = %v", call.Name, envelope, err)
		}
	}
}

func groupEJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}
