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

func TestGroupIToolsAnswerByRawNameAndThroughClioRun(t *testing.T) {
	creatioServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ServiceModel/AuthService.svc/Login":
			_, _ = w.Write([]byte(`{"Code":0}`))
		case "/0/DataService/json/SyncReply/SelectQuery":
			_, _ = w.Write([]byte(`{"success":true,"rows":[]}`))
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
		"describe-business-process": {map[string]any{"process-name": "P", "bogus": 1},
			`{"exit-code":1,"execution-log-messages":[{"message-type":"Error","value":"Unknown args: 'bogus'. Valid: environment-name, process-name, process-uid, process-caption, culture."}]}`},
		"get-process-page-facts": {map[string]any{"pageName": "P", "zz": 1},
			`{"success":false,"error":"Rename: 'pageName' -> 'schema-name'. Unknown args: 'zz'. Valid: schema-name, culture, environment-name, uri, login, password."}`},
		"read-data-binding-db": {map[string]any{"package-name": "Nope", "binding-name": "B"},
			`{"exit-code":1,"execution-log-messages":[{"message-type":"Error","value":"Package 'Nope' was not found in the environment. Check the name against list-packages."}]}`},
		"get-email-template":     {map[string]any{"email-id": "x"}, `{"success":false,"error":"email-id must be a GUID.","variants":[]}`},
		"get-related-page-addon": {map[string]any{"entity-schema-name": "Account"}, `{"success":false,"pageCount":0,"error":"package-name is required."}`},
		"get-sequence-context": {map[string]any{"bogus": 1},
			`{"success":false,"availability":"absent","sections":{"schema-presence":{"state":"complete","data":[]}},"limitations":["Metadata presence does not prove permission to activate, enroll or send email.","Required fields describe schema metadata, not dynamic rules or defaults.","Choices are capped at 100 rows; use targeted execute-esq reads for truncated sections.","Use native sequence enrollment; do not create Active participants or their activities manually."]}`},
	}
	for name, c := range cases {
		for _, call := range callPaths(name, c.args) {
			result, err := session.CallTool(context.Background(), call)
			if err != nil {
				t.Fatalf("%s via %s: %v", name, call.Name, err)
			}
			if encoded := canonicalJSON(t, result.StructuredContent); encoded != canonicalJSON(t, json.RawMessage(c.want)) {
				t.Errorf("%s via %s = %s, want %s", name, call.Name, encoded, c.want)
			}
		}
	}
}

func TestGroupIToolsRefuseLikeClio(t *testing.T) {
	client, err := creatio.NewClient(creatio.Config{BaseURL: "http://127.0.0.1:1", Login: "example-user", Password: "replace-me"})
	if err != nil {
		t.Fatal(err)
	}
	session := connectTestClient(t, newMCPServer(client), mcp.NewClient(&mcp.Implementation{Name: "probe-client", Version: "test"}, nil))
	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{"get-sequence-context", map[string]any{"sequence-id": "{55555555-5555-5555-5555-555555555555}"},
			"invalid-parameter-type: argument 'sequence-id' for MCP tool 'get-sequence-context' must be an object. Received an incompatible JSON value."},
		{"get-sequence-context", map[string]any{"sequence-id": "00000000-0000-0000-0000-000000000000"},
			"MCP tool 'get-sequence-context' failed: sequence-id must be a non-empty UUID."},
		{"get-email-template", map[string]any{},
			"invalid-parameter-type: argument 'args' for MCP tool 'get-email-template' must be an object. Received an incompatible JSON value."},
		{"describe-business-process", map[string]any{"process-name": 5},
			"invalid-parameter-type: argument 'process-name' for MCP tool 'describe-business-process' must be a string. Received an incompatible JSON value."},
		{"get-related-page-addon", map[string]any{"entity-schema-name": "A", "package-name": "P", "environment-name": "other"},
			`"error":"Environment with key 'other' not found. No environments are registered.`},
	}
	for _, c := range cases {
		result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: c.name, Arguments: c.args})
		if err != nil {
			t.Fatal(err)
		}
		text := result.Content[0].(*mcp.TextContent).Text
		if !strings.Contains(text, c.want) {
			t.Errorf("%s %v = %s, want %s", c.name, c.args, text, c.want)
		}
	}
	// clio redacts the password placeholder of the suggested reg-web-app call in this tool's answer.
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "get-related-page-addon",
		Arguments: map[string]any{"entity-schema-name": "A", "package-name": "P", "environment-name": "other"}})
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &envelope); err != nil ||
		!strings.Contains(envelope.Error, `"password":"[redacted]"`) || strings.Contains(envelope.Error, "<password>") {
		t.Fatalf("get-related-page-addon error = %q, err = %v", envelope.Error, err)
	}
}
