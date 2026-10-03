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

func TestGroupFToolsAnswerByRawNameAndThroughClioRun(t *testing.T) {
	creatioServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ServiceModel/AuthService.svc/Login":
			_, _ = w.Write([]byte(`{"Code":0}`))
		case "/0/api/ConfigurationStatus/GetLastCompilationResult":
			_, _ = w.Write([]byte(`{"errors":[],"buildResult":0,"success":true}`))
		case "/0/ServiceModel/ApplicationInfoService.svc/GetApplicationInfo":
			_, _ = w.Write([]byte(`{"applicationInfo":{"useStaticFileContent":false,"staticFileContent":null}}`))
		case "/0/DataService/json/SyncReply/SelectQuery":
			_, _ = w.Write([]byte(`{"success":true,"rows":[{"Id":"7f3b869f-34f3-4f20-ab4d-7480a5fdf647","Name":"Supervisor"}]}`))
		default:
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
		"compile-status": {map[string]any{"operation-id": "abc", "ignored": 1},
			`{"success":true,"status":"not-found","note":"` + runtimeCompileNotFoundNote + `"}`},
		"restart-status":       {map[string]any{}, `{"success":true,"status":"not-found","note":"` + runtimeRestartNotFoundNote + `"}`},
		"last-compilation-log": {map[string]any{}, `{"success":true,"compilation-succeeded":true,"build-result":0,"diagnostics":[]}`},
		"get-fsm-mode":         {map[string]any{"ignored": 1}, `{"mode":"on","useStaticFileContent":false,"staticFileContent":null}`},
		"resolve-oauth-system-user": {map[string]any{"name": "Supervisor"},
			`{"success":true,"user":{"systemUserId":"7f3b869f-34f3-4f20-ab4d-7480a5fdf647","name":"Supervisor","found":true}}`},
		"verify-oauth-app": {map[string]any{}, `{"success":false,"error":"OAuth verification failed. Check the IdentityService URL and CRM connectivity; configure OAuth credentials or supply both --client-id and --client-secret."}`},
	}
	for name, c := range cases {
		for _, call := range callPaths(name, c.args) {
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
}

func TestGroupFToolsRefuseEnvironmentSelectorsAndWrongTypes(t *testing.T) {
	session := connectTestClient(t, newMCPServer(&creatio.Client{}), mcp.NewClient(&mcp.Implementation{Name: "probe-client", Version: "test"}, nil))
	cases := []struct {
		call    *mcp.CallToolParams
		isError bool
		want    string
	}{
		{&mcp.CallToolParams{Name: "compile-status", Arguments: map[string]any{"environment-name": "Other"}}, false, `"status":"not-found","environment-name":"Other","note":`},
		{&mcp.CallToolParams{Name: "restart-status", Arguments: map[string]any{"environmentName": "other"}}, false, `{"success":true,"status":"not-found","note":`},
		{&mcp.CallToolParams{Name: "compile-status", Arguments: map[string]any{"operation-id": 5}}, true, "invalid-parameter-type: argument 'operation-id' for MCP tool 'compile-status' must be a string."},
		{&mcp.CallToolParams{Name: "last-compilation-log", Arguments: map[string]any{"foo": 1}}, false, `"error":"Unknown args: 'foo'.`},
		{&mcp.CallToolParams{Name: "last-compilation-log", Arguments: map[string]any{"uri": "elsewhere"}}, false, `"error":"Unknown args: 'uri'. Valid: environment-name."`},
		{&mcp.CallToolParams{Name: "last-compilation-log", Arguments: map[string]any{"environment-name": "other"}}, false, `"diagnostics":[],"error":"Environment with key 'other' not found.`},
		{&mcp.CallToolParams{Name: "get-fsm-mode", Arguments: map[string]any{"environmentName": "other"}}, true,
			"MCP tool 'get-fsm-mode' failed: Environment with key 'other' not found. Check your clio configuration."},
		{&mcp.CallToolParams{Name: "clio-run", Arguments: map[string]any{"command": "get-fsm-mode", "args": map[string]any{"environment-name": "other"}}}, true,
			"Error: tool 'get-fsm-mode' failed: Environment with key 'other' not found. Check your clio configuration."},
		{&mcp.CallToolParams{Name: "resolve-oauth-system-user", Arguments: map[string]any{"environment-name": "other"}}, false, `{"success":false,"error":"Environment with key 'other' not found.`},
		{&mcp.CallToolParams{Name: "resolve-oauth-system-user", Arguments: map[string]any{"id": 5}}, true, "argument 'id' for MCP tool 'resolve-oauth-system-user' must be a string."},
		{&mcp.CallToolParams{Name: "verify-oauth-app", Arguments: map[string]any{"environment-name": "other"}}, false, `"error":"OAuth verification failed.`},
		{&mcp.CallToolParams{Name: "verify-oauth-app", Arguments: map[string]any{"client-secret": 5}}, true, "argument 'client-secret' for MCP tool 'verify-oauth-app' must be a string."},
	}
	for _, c := range cases {
		result, err := session.CallTool(context.Background(), c.call)
		if err != nil || result.IsError != c.isError {
			t.Fatalf("%s %v = %#v, err = %v", c.call.Name, c.call.Arguments, result, err)
		}
		if text := result.Content[0].(*mcp.TextContent).Text; !strings.Contains(text, c.want) {
			t.Errorf("%s %v = %s, want it to contain %s", c.call.Name, c.call.Arguments, text, c.want)
		}
	}
}
