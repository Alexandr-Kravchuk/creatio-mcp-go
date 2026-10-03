package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const groupJUserRow = `{"Id":"7f3b869f-34f3-4f20-ab4d-7480a5fdf647","Name":"Supervisor","SysAdminUnitTypeValue":4,"ParentRole":"","Active":true,` +
	`"Contact":"","ConnectionType":0,"SynchronizeWithLDAP":false,"ForceChangePassword":false}`

func TestGroupJToolsAnswerByRawNameAndThroughClioRun(t *testing.T) {
	creatioServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ServiceModel/AuthService.svc/Login":
			_, _ = w.Write([]byte(`{"Code":0}`))
		case "/0/DataService/json/SyncReply/SelectQuery":
			_, _ = w.Write([]byte(`{"success":true,"rows":[` + groupJUserRow + `]}`))
		case "/0/rest/RightsService/GetCanExecuteOperation":
			_, _ = w.Write([]byte(`{"GetCanExecuteOperationResult":false}`))
		case "/0/ServiceModel/LicenseService.svc/GetLicOperationStatuses":
			_, _ = w.Write([]byte(`{"GetLicOperationStatusesResult":{"success":true,"licOperationStatuses":[]}}`))
		case "/0/ServiceModel/ThemeService.svc/GetAvailableThemes":
			_, _ = w.Write([]byte(`{"success":true,"values":[]}`))
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
	rowsInfo := `{"exit-code":0,"execution-log-messages":[{"message-type":"Info","value":` + groupEJSON(t, "["+groupJUserRow+"]") + `}]}`
	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{"inspect-user", map[string]any{"action": "list", "user-login": "Supervisor", "limit": "1", "foo": 1}, rowsInfo},
		{"inspect-role", map[string]any{"action": "members", "id": "7f3b869f-34f3-4f20-ab4d-7480a5fdf647"},
			`{"exit-code":1,"execution-log-messages":[{"message-type":"Error","value":"The ID must identify a role."}]}`},
		{"inspect-license", map[string]any{"action": "role-assign"},
			`{"exit-code":1,"execution-log-messages":[{"message-type":"Error","value":"This inspection tool accepts only: user-list, role-list."}]}`},
		{"inspect-access", map[string]any{"action": "operations", "environment-name": "other"},
			`{"exit-code":1,"execution-log-messages":[{"message-type":"Error","value":` +
				groupEJSON(t, "[EnvironmentResolutionException] "+environmentNotFoundMessage("other", creatio.ClioSettings{})) + `}]}`},
		{"check-theming-access", map[string]any{},
			`{"success":true,"canManageThemes":false,"canCustomizeBranding":false,"themeServiceMinVersion":"10.0.0"}`},
		{"get-theme", map[string]any{"id": "00000000-0000-0000-0000-000000000001"},
			`{"success":false,"error":` + groupEJSON(t, "Theme '00000000-0000-0000-0000-000000000001' was not found and no custom themes are listed on this environment. "+
				"This can also mean the CanCustomizeBranding license is missing (list-themes returns an empty catalog in that case) — verify access with check-theming-access.") + `}`},
		{"get-theme", map[string]any{"id": "x", "outputFile": "a.css"}, `{"success":false,"error":` + groupEJSON(t, "Rename: 'outputFile' -> 'output-file'.") + `}`},
	}
	for _, c := range cases {
		for _, call := range []*mcp.CallToolParams{
			{Name: c.name, Arguments: c.args},
			{Name: "clio-run", Arguments: map[string]any{"command": c.name, "args": c.args}},
		} {
			result, err := session.CallTool(context.Background(), call)
			if err != nil || result.IsError {
				t.Fatalf("call %q via %q = %#v, err = %v", c.name, call.Name, result, err)
			}
			assertOneJSONTextContent(t, result)
			if text := result.Content[0].(*mcp.TextContent).Text; text != c.want {
				t.Errorf("%s via %s = %s\nwant %s", c.name, call.Name, text, c.want)
			}
		}
	}
	contract, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "get-tool-contract", Arguments: map[string]any{"name": "inspect-access"}})
	if err != nil || contract.IsError {
		t.Fatalf("inspect-access contract = %#v, err = %v", contract, err)
	}
}

func TestGroupJToolsReportArgumentTypeErrorsAsToolErrors(t *testing.T) {
	session := connectTestClient(t, newMCPServer(&creatio.Client{}), mcp.NewClient(&mcp.Implementation{Name: "probe-client", Version: "test"}, nil))
	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{"inspect-user", map[string]any{"action": "lock-status", "id": "{7f3b869f-34f3-4f20-ab4d-7480a5fdf647}"},
			"invalid-parameter-type: argument 'id' for MCP tool 'inspect-user' must be an object. Received an incompatible JSON value."},
		{"inspect-user", map[string]any{"action": "list", "limit": 1.5},
			"invalid-parameter-type: argument 'limit' for MCP tool 'inspect-user' must be a number. Received an incompatible JSON value."},
		{"inspect-role", map[string]any{"action": "list", "effective": nil},
			"invalid-parameter-type: argument 'effective' for MCP tool 'inspect-role' must be a boolean. Received an incompatible JSON value."},
		{"inspect-access", map[string]any{"action": 5},
			"invalid-parameter-type: argument 'action' for MCP tool 'inspect-access' must be a string. Received an incompatible JSON value."},
		{"get-theme", map[string]any{"id": 5},
			"invalid-parameter-type: argument 'id' for MCP tool 'get-theme' must be a string. Received an incompatible JSON value."},
	}
	for _, c := range cases {
		for _, call := range []*mcp.CallToolParams{
			{Name: c.name, Arguments: c.args},
			{Name: "clio-run", Arguments: map[string]any{"command": c.name, "args": c.args}},
		} {
			result, err := session.CallTool(context.Background(), call)
			if err != nil || !result.IsError {
				t.Fatalf("call %q via %q = %#v, err = %v", c.name, call.Name, result, err)
			}
			if text := result.Content[0].(*mcp.TextContent).Text; text != c.want {
				t.Errorf("%s via %s = %s\nwant %s", c.name, call.Name, text, c.want)
			}
		}
	}
}
