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

func TestGroupDToolsAnswerByRawNameAndThroughClioRun(t *testing.T) {
	creatioServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ServiceModel/AuthService.svc/Login":
			_, _ = w.Write([]byte(`{"Code":0}`))
		case "/0/DataService/json/SyncReply/SelectQuery":
			_, _ = w.Write([]byte(`{"success":true,"rows":[]}`))
		case "/0/rest/RightsService/GetRecordRights":
			_, _ = w.Write([]byte(`{"GetRecordRightsResult":[]}`))
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
		"read-entity-business-rules": {map[string]any{"package-name": "Nope", "entity-schema-name": "Account"}, `{"count":0,"rules":[],"error":"Package 'Nope' was not found."}`},
		"read-page-business-rules":   {map[string]any{"package-name": "Nope"}, `{"count":0,"rules":[],"error":"page-schema-name is required."}`},
		"get-record-rights":          {map[string]any{"entity": "Contact", "record-id": "x"}, `{"success":true,"output":"No record rights found for Contact 'x'."}`},
		"get-process-signature":      {map[string]any{"process-name": "P", "bogus": 1}, `{"success":false,"processResolutionFailed":false,"parameters":[],"error":"Unknown args: 'bogus'. Valid: culture, process-name."}`},
	}
	for name, c := range cases {
		for _, call := range []*mcp.CallToolParams{
			{Name: name, Arguments: c.args},
			{Name: "clio-run", Arguments: map[string]any{"command": name, "args": c.args}},
		} {
			result, err := session.CallTool(context.Background(), call)
			if err != nil {
				t.Fatalf("%s via %s: %v", name, call.Name, err)
			}
			if encoded := canonicalJSON(t, result.StructuredContent); encoded != canonicalJSON(t, json.RawMessage(c.want)) {
				t.Errorf("%s via %s = %s, want %s", name, call.Name, encoded, c.want)
			}
		}
	}
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "get-process-signature", Arguments: map[string]any{"process-name": "P", "uri": "http://example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"success":false,"processResolutionFailed":false,"parameters":[],"error":"` + directConnectionRefusal + `"}`
	if encoded := canonicalJSON(t, result.StructuredContent); encoded != canonicalJSON(t, json.RawMessage(want)) {
		t.Fatalf("uri refusal = %s", encoded)
	}
}

// canonicalJSON re-encodes a value through map[string]any so key order does not matter.
func canonicalJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var decoded any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	canonical, _ := json.Marshal(decoded)
	return string(canonical)
}
