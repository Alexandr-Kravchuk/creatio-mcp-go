package creatio

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const inspectTestUserID = "7f3b869f-34f3-4f20-ab4d-7480a5fdf647"

// inspectTestUnitRow is a SysAdminUnit row in the key order Creatio returns for clio's column order.
func inspectTestUnitRow(id string, unitType int) string {
	return `{"Id":"` + id + `","Name":"Supervisor","SysAdminUnitTypeValue":` + string(rune('0'+unitType)) +
		`,"ParentRole":"","Active":true,"Contact":{"value":"c1","displayValue":"Süpervisor <x>"},"ConnectionType":0,` +
		`"SynchronizeWithLDAP":false,"ForceChangePassword":false}`
}

func TestInspectUserListSendsClioSelectQueryAndPrintsRowsAsSystemTextJSON(t *testing.T) {
	var query string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ServiceModel/AuthService.svc/Login":
			_, _ = w.Write([]byte(`{"Code":0}`))
		case "/0/DataService/json/SyncReply/SelectQuery":
			body, _ := io.ReadAll(r.Body)
			query = string(body)
			_, _ = w.Write([]byte(`{"success":true,"rows":[` + inspectTestUnitRow(inspectTestUserID, 4) + `],"rowsAffected":1}`))
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	login := "Supervisor"
	result := newFormsTestClient(t, server.URL).Inspect(context.Background(), InspectUserTool,
		InspectRequest{Action: "list", ID: emptyGUID, UserLogin: &login, Offset: 2, Limit: 5})
	if result.ExitCode != 0 || len(result.Messages) != 1 || result.Messages[0].MessageType != "Info" {
		t.Fatalf("result = %#v", result)
	}
	// The row keeps Creatio's key order; non-ASCII and HTML-sensitive characters are escaped as STJ does.
	want := `[{"Id":"` + inspectTestUserID + `","Name":"Supervisor","SysAdminUnitTypeValue":4,"ParentRole":"","Active":true,` +
		`"Contact":{"value":"c1","displayValue":"S` + "\\u00FC" + `pervisor ` + "\\u003Cx\\u003E" + `"},"ConnectionType":0,"SynchronizeWithLDAP":false,"ForceChangePassword":false}]`
	if got := result.Messages[0].Value; got != want {
		t.Fatalf("value = %s\nwant %s", got, want)
	}
	wantQuery := `{"rootSchemaName":"SysAdminUnit","operationType":0,"rowCount":5,"rowsOffset":2,"isPageable":true,"columns":{"items":{` +
		`"Id":{"expression":{"expressionType":0,"columnPath":"Id"},"orderDirection":1,"orderPosition":0},` +
		`"Name":{"expression":{"expressionType":0,"columnPath":"Name"},"orderDirection":0,"orderPosition":-1},`
	if !strings.HasPrefix(query, wantQuery) {
		t.Fatalf("query = %s", query)
	}
	wantFilters := `"filters":{"filterType":6,"logicalOperation":0,"isEnabled":true,"items":{` +
		`"Name":{"filterType":1,"comparisonType":3,"isEnabled":true,"leftExpression":{"expressionType":0,"columnPath":"Name"},` +
		`"rightExpression":{"expressionType":2,"parameter":{"dataValueType":1,"value":"Supervisor"}}},` +
		`"SysAdminUnitTypeValue":{"filterType":4,"comparisonType":3,"isEnabled":true,"leftExpression":{"expressionType":0,"columnPath":"SysAdminUnitTypeValue"},` +
		`"rightExpressions":[{"expressionType":2,"parameter":{"dataValueType":4,"value":4}},{"expressionType":2,"parameter":{"dataValueType":4,"value":5}},` +
		`{"expressionType":2,"parameter":{"dataValueType":4,"value":7}}]}}}}`
	if !strings.HasSuffix(query, wantFilters) {
		t.Fatalf("query filters = %s", query)
	}
}

func TestInspectLockStatusChecksTheUserThenAsksAdministrationService(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ServiceModel/AuthService.svc/Login":
			_, _ = w.Write([]byte(`{"Code":0}`))
		case "/0/DataService/json/SyncReply/SelectQuery":
			_, _ = w.Write([]byte(`{"success":true,"rows":[` + inspectTestUnitRow(inspectTestUserID, 4) + `]}`))
		case "/0/rest/AdministrationService/GetIsUserBlocked":
			body, _ := io.ReadAll(r.Body)
			if string(body) != `{"userId":"`+inspectTestUserID+`"}` {
				t.Errorf("body = %s", body)
			}
			_, _ = w.Write([]byte(`{"GetIsUserBlockedResult":true}`))
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
		}
	}))
	defer server.Close()
	result := newFormsTestClient(t, server.URL).Inspect(context.Background(), InspectUserTool,
		InspectRequest{Action: "lock-status", ID: inspectTestUserID, Limit: 100})
	if result.ExitCode != 0 || result.Messages[0].Value != `{"id":"`+inspectTestUserID+`","blocked":true}` {
		t.Fatalf("result = %#v", result)
	}
}

func TestInspectRefusalsAndFailuresUseClioMessages(t *testing.T) {
	var rows string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ServiceModel/AuthService.svc/Login":
			_, _ = w.Write([]byte(`{"Code":0}`))
		case "/0/DataService/json/SyncReply/SelectQuery":
			_, _ = w.Write([]byte(rows))
		default:
			http.Error(w, "boom", http.StatusInternalServerError)
		}
	}))
	defer server.Close()
	client := newFormsTestClient(t, server.URL)
	userType := 4
	cases := []struct {
		name    string
		tool    InspectTool
		request InspectRequest
		rows    string
		want    string
	}{
		{"write action", InspectUserTool, InspectRequest{Action: "delete"}, "", "This inspection tool accepts only: list, lock-status."},
		{"empty id", InspectAccessTool, InspectRequest{Action: "ip-list", UnitID: emptyGUID, Limit: 100}, "", "Administration IDs must be nonempty GUIDs."},
		{"limit", InspectUserTool, InspectRequest{Action: "list", ID: emptyGUID, Limit: 201}, "", "Administration reads require columns, a nonnegative offset and a limit from 1 to 200."},
		{"role type", InspectRoleTool, InspectRequest{Action: "list", ID: emptyGUID, Type: &userType, Limit: 100}, "", "Role inspection accepts only role types 0, 1, 2, 3 or 6."},
		{"unknown unit", InspectUserTool, InspectRequest{Action: "lock-status", ID: inspectTestUserID}, `{"success":true,"rows":[]}`, "The administration ID does not identify one existing user or role."},
		{"role as user", InspectRoleTool, InspectRequest{Action: "memberships", UserID: inspectTestUserID, Limit: 100}, `{"success":true,"rows":[` + inspectTestUnitRow(inspectTestUserID, 1) + `]}`, "The member ID must identify a user."},
		{"user as role", InspectLicenseTool, InspectRequest{Action: "role-list", RoleID: inspectTestUserID, Limit: 100}, `{"success":true,"rows":[` + inspectTestUnitRow(inspectTestUserID, 4) + `]}`, "The ID must identify a role."},
		{"functional role", InspectRoleTool, InspectRequest{Action: "functional-roles", ID: inspectTestUserID, Limit: 100}, `{"success":true,"rows":[` + inspectTestUnitRow(inspectTestUserID, 6) + `]}`, "An organizational or manager role is required."},
		{"technical user", InspectLicenseTool, InspectRequest{Action: "user-list", UserID: inspectTestUserID, Limit: 100}, `{"success":true,"rows":[` + inspectTestUnitRow(inspectTestUserID, 7) + `]}`, "Technical accounts do not support personal user licenses."},
		{"rejected select", InspectAccessTool, InspectRequest{Action: "operations", Limit: 100}, `{"success":false,"errorInfo":{"message":"secret"}}`, InspectAccessTool.failure},
		{"missing column", InspectAccessTool, InspectRequest{Action: "operations", Limit: 100}, `{"success":true,"rows":[{"Id":"x"}]}`, InspectAccessTool.failure},
		{"server error", InspectUserTool, InspectRequest{Action: "lock-status", ID: inspectTestUserID}, `{"success":true,"rows":[` + inspectTestUnitRow(inspectTestUserID, 4) + `]}`, InspectUserTool.failure},
	}
	for _, c := range cases {
		rows = c.rows
		result := client.Inspect(context.Background(), c.tool, c.request)
		if result.ExitCode != 1 || len(result.Messages) != 1 || result.Messages[0].MessageType != "Error" || result.Messages[0].Value != c.want {
			encoded, _ := json.Marshal(result)
			t.Errorf("%s: result = %s, want %q", c.name, encoded, c.want)
		}
	}
}

func TestInspectLicenseUserListUnwrapsTheStringEncodedArrayKeepingNumberText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ServiceModel/AuthService.svc/Login":
			_, _ = w.Write([]byte(`{"Code":0}`))
		case "/0/DataService/json/SyncReply/SelectQuery":
			_, _ = w.Write([]byte(`{"success":true,"rows":[` + inspectTestUnitRow(inspectTestUserID, 4) + `]}`))
		case "/0/rest/AdministrationService/GetAvailableLicPackages":
			_, _ = w.Write([]byte(`{"GetAvailableLicPackagesResult":"[{\"Id\":\"p1\",\"Checked\":true,\"Count\":1.50}]"}`))
		}
	}))
	defer server.Close()
	result := newFormsTestClient(t, server.URL).Inspect(context.Background(), InspectLicenseTool,
		InspectRequest{Action: "user-list", UserID: inspectTestUserID, Limit: 100})
	if result.ExitCode != 0 || result.Messages[0].Value != `[{"Id":"p1","Checked":true,"Count":1.50}]` {
		t.Fatalf("result = %#v", result)
	}
}
