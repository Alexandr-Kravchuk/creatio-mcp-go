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

func groupGSession(t *testing.T) *mcp.ClientSession {
	t.Helper()
	creatioServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ServiceModel/AuthService.svc/Login":
			_, _ = w.Write([]byte(`{"Code":0}`))
		case "/0/ServiceModel/ApplicationInfoService.svc/GetApplicationInfo":
			_, _ = w.Write([]byte(`{"applicationInfo":{"sysValues":{"coreVersion":"10.2.363.0"}}}`))
		case "/0/rest/DataForgeMaintenanceService/GetServiceStatus":
			_, _ = w.Write([]byte(`{"GetServiceStatusResult":{"IsOnline":true,"Readiness":{"HttpStatusCode":200},"DataStructureReadiness":"ok","LookupsReadinessInfo":"ok"}}`))
		case "/0/rest/DataForgeSchemaReadService/GetSimilarTableNames":
			_, _ = w.Write([]byte(`{"GetSimilarTableNamesResult":{"success":true,"Data":[{"Name":"Contact","Caption":"Contact"}]}}`))
		case "/0/rest/DataForgeSchemaReadService/GetLookupValues":
			_, _ = w.Write([]byte(`{"GetLookupValuesResult":{"success":true,"data":[{"valueId":"v1","referenceSchemaName":"ContactType","valueName":"Customer","vectorSimilarityScore":0.5}]}}`))
		case "/0/rest/DataForgeSchemaReadService/GetTableRelationships":
			_, _ = w.Write([]byte(`{"GetTableRelationshipsResult":{"success":true,"paths":["Contact.Account"]}}`))
		case "/0/DataService/json/SyncReply/RuntimeEntitySchemaRequest":
			_, _ = w.Write([]byte(`{"success":true,"schema":{"name":"Contact","columns":{"items":{"a":{"name":"Name","caption":"Name","dataValueType":28,"isRequired":true}}}}}`))
		default:
			t.Errorf("unexpected Creatio route %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(creatioServer.Close)
	client, err := creatio.NewClient(creatio.Config{BaseURL: creatioServer.URL, Login: "example-user", Password: "replace-me"})
	if err != nil {
		t.Fatal(err)
	}
	return connectTestClient(t, newMCPServer(client), mcp.NewClient(&mcp.Implementation{Name: "probe-client", Version: "test"}, nil))
}

func TestGroupGToolsAnswerByRawNameAndThroughClioRun(t *testing.T) {
	session := groupGSession(t)
	const head = `{"success":true,"source":"clio+dataforge-service","correlation-id":"","warnings":[],`
	health := `"health":{"liveness":true,"readiness":true,"data-structure-readiness":true,"lookups-readiness":true,"correlation-id":""},"status":{"success":true,"status":"Ready"}`
	columns := `[{"name":"Name","caption":"Name","data-type":"MediumText","required":true}]`
	cases := map[string]struct {
		args map[string]any
		want string
	}{
		"dataforge-status":            {map[string]any{"foo": 1}, head + health + `}`},
		"dataforge-find-tables":       {map[string]any{"query": "person", "limit": "3"}, head + `"similar-tables":[{"name":"Contact","caption":"Contact"}]}`},
		"dataforge-find-lookups":      {map[string]any{"query": "customer", "schema-name": "ContactType"}, head + `"similar-lookups":[{"lookup-id":"v1","schema-name":"ContactType","value":"Customer","score":0.5}]}`},
		"dataforge-get-relations":     {map[string]any{"source-table": "Contact", "target-table": "Account", "limit": 2}, head + `"relations":["Contact.Account"]}`},
		"dataforge-get-table-columns": {map[string]any{"table-name": "Contact"}, head + `"columns":` + columns + `}`},
		"dataforge-context": {map[string]any{"candidate-terms": []any{"person"}}, head + health +
			`,"similar-tables":[{"name":"Contact","caption":"Contact"}],"similar-lookups":[],"relations":{},"columns":{"Contact":` + columns + `},` +
			`"coverage":{"health":true,"tables":true,"lookups":true,"relations":true,"table-columns":true}}`},
	}
	for name, c := range cases {
		for _, call := range callPaths(name, c.args) {
			result, err := session.CallTool(context.Background(), call)
			if err != nil || result.IsError {
				t.Fatalf("call %q via %q = %#v, err = %v", name, call.Name, result, err)
			}
			assertOneJSONTextContent(t, result)
			if text := result.Content[0].(*mcp.TextContent).Text; text != c.want {
				t.Errorf("%s via %s = %s\nwant %s", name, call.Name, text, c.want)
			}
		}
	}
	contract, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "get-tool-contract", Arguments: map[string]any{"name": "dataforge-context"}})
	if err != nil || contract.IsError {
		t.Fatalf("dataforge-context contract = %#v, err = %v", contract, err)
	}
}

func TestGroupGToolsRefuseWrongTypesAndForeignEnvironments(t *testing.T) {
	session := groupGSession(t)
	typeErrors := map[string]struct {
		args map[string]any
		want string
	}{
		"dataforge-find-tables":       {map[string]any{"query": "x", "limit": 1.5}, "invalid-parameter-type: argument 'limit' for MCP tool 'dataforge-find-tables' must be a number. Received an incompatible JSON value."},
		"dataforge-find-lookups":      {map[string]any{"query": "x", "schema-name": 5}, "invalid-parameter-type: argument 'schema-name' for MCP tool 'dataforge-find-lookups' must be a string. Received an incompatible JSON value."},
		"dataforge-get-table-columns": {map[string]any{"table-name": true}, "invalid-parameter-type: argument 'table-name' for MCP tool 'dataforge-get-table-columns' must be a string. Received an incompatible JSON value."},
		"dataforge-context":           {map[string]any{"relation-pairs": []any{"Contact"}}, "invalid-parameter-type: argument 'relation-pairs' for MCP tool 'dataforge-context' contains a value that does not match the documented shape. Received an incompatible JSON value."},
	}
	for name, c := range typeErrors {
		result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: c.args})
		if err != nil || !result.IsError {
			t.Fatalf("%s = %#v, err = %v", name, result, err)
		}
		if text := result.Content[0].(*mcp.TextContent).Text; text != c.want {
			t.Errorf("%s = %s\nwant %s", name, text, c.want)
		}
	}
	for name, code := range map[string]string{"dataforge-status": "status_error", "dataforge-get-relations": "relations_error", "dataforge-context": "context_error"} {
		result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: map[string]any{"environment-name": "other"}})
		if err != nil || result.IsError {
			t.Fatalf("%s = %#v, err = %v", name, result, err)
		}
		var envelope struct {
			Success bool `json:"success"`
			Error   struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &envelope); err != nil || envelope.Success ||
			envelope.Error.Code != code || envelope.Error.Message != redacted(&environmentError{message: environmentNotFoundMessage("other", creatio.ClioSettings{})}) {
			t.Errorf("%s envelope = %#v, err = %v", name, envelope, err)
		}
	}
}
