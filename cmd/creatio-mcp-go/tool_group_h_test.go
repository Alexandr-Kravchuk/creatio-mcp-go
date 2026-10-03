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

func TestGroupHToolsAnswerByRawNameAndThroughClioRun(t *testing.T) {
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
		"get-schema":             {map[string]any{"schema-name": "ZzNope"}, `{"success":false,"bodyLength":0,"error":"Schema 'ZzNope' not found (ManagerName='SourceCodeSchemaManager')"}`},
		"get-client-unit-schema": {map[string]any{}, `{"success":false,"bodyLength":0,"fullHierarchy":false,"localizableStringCount":0,"error":"schema-name or schema-uid is required"}`},
		"get-page-hierarchy":     {map[string]any{"schema-name": "ZzNope"}, `{"success":false,"totalCount":0,"offset":0,"returnedCount":0,"hasMore":false,"bodiesOmittedForSize":false,"error":"Schema 'ZzNope' not found"}`},
		"get-classic-list-columns": {map[string]any{"schema-name": "1bad"}, `{"success":false,"sectionSchema":"1bad","columns":[],"notes":[],` +
			`"error":"schema-name must start with a letter and contain only letters, digits, or underscores"}`},
		"get-classic-page-sources": {map[string]any{"schema-name": "ZzNope"}, `{"success":false,"layerCount":0,"seedCount":0,"resourceCount":0,` +
			`"columnCount":0,"detailCount":0,"sectionLayerCount":0,"childPageCount":0,"enumVocabularyCount":0,"error":"Schema 'ZzNope' not found (ManagerName='ClientUnitSchemaManager')"}`},
		"validate-page": {map[string]any{}, `{"valid":false,"validation":{"markers-ok":false,"js-syntax-ok":false,"content-ok":false,` +
			`"errors":["Either 'body' or 'body-file' must provide page body content."]}}`},
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

func TestGroupHToolsRefuseWrongTypesAndForeignEnvironments(t *testing.T) {
	session := connectTestClient(t, newMCPServer(&creatio.Client{}), mcp.NewClient(&mcp.Implementation{Name: "probe-client", Version: "test"}, nil))
	for _, call := range []struct {
		params *mcp.CallToolParams
		want   string
	}{
		{&mcp.CallToolParams{Name: "get-page-hierarchy", Arguments: map[string]any{"schema-name": "X", "offset": 1.5}},
			"invalid-parameter-type: argument 'offset' for MCP tool 'get-page-hierarchy' must be a number. Received an incompatible JSON value."},
		{&mcp.CallToolParams{Name: "get-client-unit-schema", Arguments: map[string]any{"full-hierarchy": "yes"}},
			"invalid-parameter-type: argument 'full-hierarchy' for MCP tool 'get-client-unit-schema' must be a boolean. Received an incompatible JSON value."},
	} {
		result, err := session.CallTool(context.Background(), call.params)
		if err != nil || !result.IsError || result.Content[0].(*mcp.TextContent).Text != call.want {
			t.Fatalf("%s = %#v, err = %v", call.params.Name, result, err)
		}
	}
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "get-schema", Arguments: map[string]any{"environment-name": "other"}})
	if err != nil || result.IsError || !strings.Contains(result.Content[0].(*mcp.TextContent).Text, "environment-name is not accepted") {
		t.Fatalf("get-schema environment = %#v, err = %v", result, err)
	}
}
