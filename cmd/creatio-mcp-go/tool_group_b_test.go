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

func TestGroupBToolsAnswerByRawNameAndThroughClioRun(t *testing.T) {
	creatioServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ServiceModel/AuthService.svc/Login":
			_, _ = w.Write([]byte(`{"Code":0}`))
		case "/0/DataService/json/SyncReply/SelectQuery":
			_, _ = w.Write([]byte(`{"success":true,"rows":[]}`))
		case "/0/ServiceModel/ApplicationInfoService.svc/GetApplicationInfo":
			_, _ = w.Write([]byte(`{"applicationInfo":{"sysValues":{"coreVersion":"8.1","userCulture":{"displayValue":"en-US"}}}}`))
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
		"get-sys-setting":   {map[string]any{"code": "ZzNoSuch", "ignored": 1}, `{"success":true,"code":"ZzNoSuch","value":""}`},
		"list-sys-settings": {map[string]any{}, `{"success":true,"settings":[]}`},
		"get-user-culture":  {map[string]any{}, `{"success":true,"culture":"en-US","resolvedFrom":"environment"}`},
		"describe-environment": {map[string]any{"timeout": 5000}, `{"exit-code":0,"execution-log-messages":[` +
			`{"message-type":"Warning","value":"cliogate 2.0.0.32+ is not installed - ProductName and LicenseInfo are unavailable. All other fields (incl. DbEngineType and framework when CanManageSolution is granted) are reported."},` +
			`{"message-type":"None","value":"{\n  \"coreVersion\": \"8.1\",\n  \"userCulture\": {\n    \"displayValue\": \"en-US\"\n  }\n}"}]}`},
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
}

func TestGroupBToolsRefuseForeignEnvironmentSelectorsInsideTheEnvelope(t *testing.T) {
	session := connectTestClient(t, newMCPServer(&creatio.Client{}), mcp.NewClient(&mcp.Implementation{Name: "probe-client", Version: "test"}, nil))
	cases := []struct {
		call *mcp.CallToolParams
		want string
	}{
		{&mcp.CallToolParams{Name: "get-sys-setting", Arguments: map[string]any{"code": "X", "environment-name": "other"}}, `"error-category":"Validation"`},
		{&mcp.CallToolParams{Name: "list-sys-settings", Arguments: map[string]any{"uri": "elsewhere"}}, `"settings":[]`},
		{&mcp.CallToolParams{Name: "get-user-culture", Arguments: map[string]any{"foo": 1}}, `"reason":"Unknown args: 'foo'.`},
		{&mcp.CallToolParams{Name: "describe-environment", Arguments: map[string]any{"client-id": "x"}}, `"exit-code":1`},
		{&mcp.CallToolParams{Name: "describe-environment", Arguments: map[string]any{"environmentName": "x"}}, "environment-name is not accepted"},
	}
	for _, c := range cases {
		result, err := session.CallTool(context.Background(), c.call)
		if err != nil || result.IsError {
			t.Fatalf("%s = %#v, err = %v", c.call.Name, result, err)
		}
		if text := result.Content[0].(*mcp.TextContent).Text; !strings.Contains(text, c.want) {
			t.Errorf("%s %v = %s, want it to contain %s", c.call.Name, c.call.Arguments, text, c.want)
		}
	}
	for _, call := range []*mcp.CallToolParams{
		{Name: "get-sys-setting", Arguments: map[string]any{"code": 5}},
		{Name: "describe-environment", Arguments: map[string]any{"timeout": "x"}},
	} {
		result, err := session.CallTool(context.Background(), call)
		if err != nil || !result.IsError {
			t.Errorf("%s with a wrong-typed argument must be a tool error: %#v, err = %v", call.Name, result, err)
		}
	}
}
