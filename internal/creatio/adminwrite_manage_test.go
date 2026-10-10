package creatio

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// adminWriteManageStand answers by route ("SelectQuery:<schema>" for SelectQuery) with canned bodies and keeps
// every request body in order.
type adminWriteManageStand struct {
	mu       sync.Mutex
	answers  map[string][]string // route -> answers, the last one repeats
	requests []adminWriteFakeRequest
}

func newAdminWriteManageStand(t *testing.T, answers map[string][]string) (*adminWriteManageStand, *Client) {
	t.Helper()
	stand := &adminWriteManageStand{answers: answers}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/Login") {
			_, _ = w.Write([]byte(`{"Code":0}`))
			return
		}
		raw, _ := io.ReadAll(r.Body)
		route := strings.TrimPrefix(r.URL.Path, "/0/")
		if strings.HasSuffix(route, "/SelectQuery") {
			var body map[string]any
			_ = json.Unmarshal(raw, &body)
			route = "SelectQuery:" + body["rootSchemaName"].(string)
		}
		stand.mu.Lock()
		defer stand.mu.Unlock()
		stand.requests = append(stand.requests, adminWriteFakeRequest{route: route, body: string(raw)})
		queue := stand.answers[route]
		if len(queue) == 0 {
			t.Errorf("unexpected route %s", route)
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(queue[0]))
		if len(queue) > 1 {
			stand.answers[route] = queue[1:]
		}
	}))
	t.Cleanup(server.Close)
	return stand, newFormsTestClient(t, server.URL)
}

func (s *adminWriteManageStand) routes() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var routes []string
	for _, request := range s.requests {
		routes = append(routes, request.route)
	}
	return strings.Join(routes, ",")
}

func (s *adminWriteManageStand) body(route string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, request := range s.requests {
		if request.route == route {
			return request.body
		}
	}
	return ""
}

const (
	adminWriteTestRole    = "11111111-1111-4111-8111-111111111111"
	adminWriteTestUser    = "22222222-2222-4222-8222-222222222222"
	adminWriteTestContact = "33333333-3333-4333-8333-333333333333"
)

func adminWriteUnitRow(id, name string, unitType int, parent string) string {
	parentJSON := `""`
	if parent != "" {
		parentJSON = `{"value":"` + parent + `","displayValue":"Parent","primaryImageValue":"","primaryColorValue":""}`
	}
	contact := `""`
	if unitType == 4 {
		contact = `{"value":"` + adminWriteTestContact + `","displayValue":"Contact","primaryImageValue":"","primaryColorValue":""}`
	}
	encoded, _ := json.Marshal(name)
	return `{"Id":"` + id + `","Name":` + string(encoded) + `,"SysAdminUnitTypeValue":` + itoaTest(unitType) + `,"ParentRole":` + parentJSON +
		`,"Active":true,"Contact":` + contact + `,"ConnectionType":0,"SynchronizeWithLDAP":false,"ForceChangePassword":false}`
}

// adminWriteQuote is how System.Text.Json writes a quote inside a string.
const adminWriteQuote = "\\u0022"

func itoaTest(value int) string { return string(rune('0' + value)) }

func adminWriteRows(rows ...string) string {
	return `{"success":true,"rows":[` + strings.Join(rows, ",") + `]}`
}

func TestManageRoleCreateSavesThroughAdministrationServiceAndVerifies(t *testing.T) {
	created := adminWriteUnitRow(adminWriteTestRole, "UsrParityRole", 6, adminWriteAllEmployees)
	stand, client := newAdminWriteManageStand(t, map[string][]string{
		"SelectQuery:SysAdminUnit": {
			adminWriteRows(), // the id is free
			adminWriteRows(adminWriteUnitRow(adminWriteAllEmployees, "All employees", 0, "")), // parent
			adminWriteRows(created), // readback
		},
		"SelectQuery:SysAdminUnitType":        {adminWriteRows(`{"Id":"B759DAF9-AE07-4E9A-9B1E-5E1A2F0C58D6","Value":6}`)},
		"rest/AdministrationService/SaveRole": {`{"SaveRoleResult":"{\"success\":true}"}`},
	})
	name := "UsrParityRole"
	roleType := 6
	parent := adminWriteAllEmployees
	result := client.ManageAdministration(context.Background(), AdminWriteManageRole, AdminWriteManageRequest{
		InspectRequest: InspectRequest{Action: "create", ID: adminWriteTestRole, Name: &name, Type: &roleType, Limit: 100}, ParentID: &parent})
	if result.ExitCode != 0 || len(result.Messages) != 1 || result.Messages[0].Value != created {
		t.Fatalf("create = %#v", result)
	}
	if got := stand.routes(); got != "SelectQuery:SysAdminUnit,SelectQuery:SysAdminUnit,SelectQuery:SysAdminUnitType,rest/AdministrationService/SaveRole,SelectQuery:SysAdminUnit" {
		t.Errorf("routes = %s", got)
	}
	want := strings.ReplaceAll(`{"jsonObject":"{'Id':'`+adminWriteTestRole+`','Name':'UsrParityRole','ParentRole':'`+
		adminWriteAllEmployees+`','SysAdminUnitType':'B759DAF9-AE07-4E9A-9B1E-5E1A2F0C58D6','ConnectionType':0,'Active':true}"}`, "'", adminWriteQuote)
	if got := stand.body("rest/AdministrationService/SaveRole"); got != want {
		t.Errorf("SaveRole body = %s\nwant %s", got, want)
	}
}

func TestManageRoleRefusalsAndGenericFailure(t *testing.T) {
	_, client := newAdminWriteManageStand(t, map[string][]string{
		"SelectQuery:SysAdminUnit": {adminWriteRows(adminWriteUnitRow(adminWriteAllEmployees, "All employees", 0, ""))},
	})
	result := client.ManageAdministration(context.Background(), AdminWriteManageRole, AdminWriteManageRequest{
		InspectRequest: InspectRequest{Action: "delete", ID: adminWriteAllEmployees}})
	if result.ExitCode != 1 || result.Messages[0].Value != "Built-in root and system-administrator roles cannot be deleted by this command." {
		t.Errorf("delete root = %#v", result)
	}
	result = client.ManageAdministration(context.Background(), AdminWriteManageRole, AdminWriteManageRequest{InspectRequest: InspectRequest{Action: "nope"}})
	if result.ExitCode != 1 || result.Messages[0].Value != "Unknown role action. See manage-role help for supported actions." {
		t.Errorf("unknown = %#v", result)
	}

	// A rejected native call never shows the server's answer: the command's fixed message replaces it.
	child := adminWriteUnitRow(adminWriteTestRole, "UsrParityRole", 6, adminWriteAllEmployees)
	_, client = newAdminWriteManageStand(t, map[string][]string{
		"SelectQuery:SysAdminUnit":                            {adminWriteRows(child), adminWriteRows(), adminWriteRows(child)},
		"SelectQuery:SysUserInRole":                           {adminWriteRows()},
		"rest/GridUtilitiesService/DeleteRecords":             {`{"DeleteRecordsResult":"{\"success\":false,\"errorInfo\":{\"message\":\"password=secret\"}}"}`},
		"rest/AdministrationService/ActualizeAdminUnitInRole": {`{"ActualizeAdminUnitInRoleResult":true}`},
	})
	result = client.ManageAdministration(context.Background(), AdminWriteManageRole, AdminWriteManageRequest{
		InspectRequest: InspectRequest{Action: "delete", ID: adminWriteTestRole}})
	if result.ExitCode != 1 || result.Messages[0].Value != InspectRoleTool.failure {
		t.Errorf("rejected delete = %#v", result)
	}
}

func TestManageRoleRemoveFunctionalGateAndIncompleteReceipt(t *testing.T) {
	org := adminWriteUnitRow(adminWriteTestRole, "UsrParityDiv", 1, adminWriteAllEmployees)
	functional := adminWriteUnitRow(adminWriteTestUser, "UsrParityRole", 6, adminWriteAllEmployees)
	association := `{"Id":"44444444-4444-4444-8444-444444444444","OrgRole":"x","FuncRole":"y"}`
	_, client := newAdminWriteManageStand(t, map[string][]string{
		"SelectQuery:SysPackage": {adminWriteRows(`{"Name":"cliogate","UId":"","Maintainer":"","Version":"2.0.0.49"}`)},
	})
	request := AdminWriteManageRequest{InspectRequest: InspectRequest{Action: "remove-functional", ID: adminWriteTestRole}, FunctionalID: adminWriteTestUser}
	result := client.ManageAdministration(context.Background(), AdminWriteManageRole, request)
	if result.ExitCode != 1 || result.Messages[0].Value != "To use this command, you need to install the cliogate package version 2.0.0.50 or higher. "+
		"Install or update the package in the target environment and retry.\nRun clio install-gate for this environment." {
		t.Fatalf("old gate = %#v", result)
	}

	stand, client := newAdminWriteManageStand(t, map[string][]string{
		"SelectQuery:SysPackage":           {adminWriteRows(`{"Name":"cliogate","UId":"","Maintainer":"","Version":"2.0.0.53"}`)},
		"SelectQuery:SysAdminUnit":         {adminWriteRows(org), adminWriteRows(functional)},
		"SelectQuery:SysFuncRoleInOrgRole": {adminWriteRows(association), adminWriteRows()},
		"rest/CreatioApiGateway/RemoveFunctionalRoleAssociation": {
			`{"RemoveFunctionalRoleAssociationResult":"{\"success\":true,\"completed\":false,\"licenseReconciliationRequired\":true}"}`},
	})
	result = client.ManageAdministration(context.Background(), AdminWriteManageRole, request)
	if result.ExitCode != 1 || len(result.Messages) != 2 || result.Messages[1].MessageType != "Error" ||
		result.Messages[0].Value != `{"associations":[],"redistributionReceipt":{"success":true,"completed":false,"licenseReconciliationRequired":true},`+
			`"completed":false,"licenseReconciliationRequired":true,"userAssignmentsVerified":false}` {
		t.Fatalf("incomplete receipt = %#v", result)
	}
	if got := stand.body("rest/CreatioApiGateway/RemoveFunctionalRoleAssociation"); got != `{"associationId":"44444444-4444-4444-8444-444444444444"}` {
		t.Errorf("remove body = %s", got)
	}
	if strings.Contains(stand.routes(), "Actualize") {
		t.Error("a receipt replaces the actualization")
	}
}

func TestManageUserCreateReadsThePasswordReferenceAndGatesTheVersion(t *testing.T) {
	login := "UsrParityUser"
	contact := adminWriteTestContact
	reference := "CLIO_ADMIN_PASSWORD_T12TEST"
	request := AdminWriteManageRequest{InspectRequest: InspectRequest{Action: "create", ID: adminWriteTestUser, UserLogin: &login},
		ContactID: &contact, PasswordEnv: &reference, ForceChangePassword: true}

	_, client := newAdminWriteManageStand(t, map[string][]string{
		applicationInfoRoute: {`{"applicationInfo":{"sysValues":{"coreVersion":"8.1.0.0"}}}`},
	})
	result := client.ManageAdministration(context.Background(), AdminWriteManageUser, request)
	if result.ExitCode != 78 || !strings.HasPrefix(result.Messages[0].Value, "This command requires Creatio 10.1.585.0 or later. The target environment runs 8.1.0.0.") ||
		!strings.HasSuffix(result.Messages[0].Value, "older implementations may log plaintext passwords. [version-too-old]") {
		t.Fatalf("old version = %#v", result)
	}

	created := adminWriteUnitRow(adminWriteTestUser, login, 4, "")
	stand, client := newAdminWriteManageStand(t, map[string][]string{
		applicationInfoRoute:                            {`{"applicationInfo":{"sysValues":{"coreVersion":"10.2.363.0"}}}`},
		"SelectQuery:Contact":                           {adminWriteRows(`{"Id":"` + adminWriteTestContact + `"}`)},
		"SelectQuery:SysAdminUnit":                      {adminWriteRows(), adminWriteRows(), adminWriteRows(created)},
		"rest/AdministrationService/UpdateOrCreateUser": {`{"UpdateOrCreateUserResult":""}`},
	})
	result = client.ManageAdministration(context.Background(), AdminWriteManageUser, request)
	if result.ExitCode != 1 || result.Messages[0].Value != "Supply a populated password reference named CLIO_ADMIN_PASSWORD_<SUFFIX> using uppercase letters, digits or underscores." {
		t.Fatalf("unset reference = %#v", result)
	}
	t.Setenv(reference, `Pa"ss+1`)
	result = client.ManageAdministration(context.Background(), AdminWriteManageUser, request)
	if result.ExitCode != 0 || result.Messages[0].Value != created {
		t.Fatalf("create = %#v", result)
	}
	body := stand.body("rest/AdministrationService/UpdateOrCreateUser")
	if !strings.Contains(body, strings.ReplaceAll(`'UserPassword':'Pa\\u0022ss\\u002B1'`, "'", adminWriteQuote)) || !strings.HasSuffix(body, `,"roleId":""}`) ||
		!strings.Contains(body, strings.ReplaceAll(`'SysAdminUnitType':'472e97c7-6bd7-df11-9b2a-001d60e938c6'`, "'", adminWriteQuote)) {
		t.Errorf("user body = %s", body)
	}
}

func TestManageAccessWritesTheIPRuleAndOperationGrant(t *testing.T) {
	rule := `{"Id":"55555555-5555-4555-8555-555555555555","SysAdminUnit":{"value":"` + adminWriteTestRole +
		`","displayValue":"Role"},"BeginIP":"10.0.0.1","EndIP":"10.0.0.9"}`
	stand, client := newAdminWriteManageStand(t, map[string][]string{
		"SelectQuery:SysAdminUnit":               {adminWriteRows(adminWriteUnitRow(adminWriteTestRole, "Role", 6, adminWriteAllEmployees))},
		"SelectQuery:SysAdminUnitIPRange":        {adminWriteRows(), adminWriteRows(rule)},
		"DataService/json/SyncReply/InsertQuery": {`{"success":true,"rowsAffected":1}`},
	})
	begin, end := "10.0.0.1", "10.0.0.9"
	result := client.ManageAdministration(context.Background(), AdminWriteManageAccess, AdminWriteManageRequest{
		InspectRequest: InspectRequest{Action: "ip-create", ID: "55555555-5555-4555-8555-555555555555", UnitID: adminWriteTestRole},
		BeginIP:        &begin, EndIP: &end})
	if result.ExitCode != 0 || result.Messages[0].Value != rule {
		t.Fatalf("ip-create = %#v", result)
	}
	want := `{"rootSchemaName":"SysAdminUnitIPRange","operationType":1,"columnValues":{"items":{` +
		`"SysAdminUnit":{"expressionType":2,"parameter":{"dataValueType":0,"value":"` + adminWriteTestRole + `"}},` +
		`"BeginIP":{"expressionType":2,"parameter":{"dataValueType":1,"value":"10.0.0.1"}},` +
		`"EndIP":{"expressionType":2,"parameter":{"dataValueType":1,"value":"10.0.0.9"}},` +
		`"Id":{"expressionType":2,"parameter":{"dataValueType":0,"value":"55555555-5555-4555-8555-555555555555"}}}},` +
		`"filters":{"filterType":6,"logicalOperation":0,"isEnabled":true,"items":{"identity":{"filterType":1,"comparisonType":3,"isEnabled":true,` +
		`"leftExpression":{"expressionType":0,"columnPath":"Id"},"rightExpression":{"expressionType":2,"parameter":{"dataValueType":0,` +
		`"value":"55555555-5555-4555-8555-555555555555"}}}}}}`
	if got := stand.body("DataService/json/SyncReply/InsertQuery"); got != want {
		t.Errorf("insert = %s\nwant %s", got, want)
	}

	grant := `{"Id":"66666666-6666-4666-8666-666666666666","SysAdminOperation":"op","SysAdminUnit":"unit","CanExecute":true,"Position":0}`
	stand, client = newAdminWriteManageStand(t, map[string][]string{
		"SelectQuery:SysAdminOperation":               {adminWriteRows(`{"Id":"77777777-7777-4777-8777-777777777777"}`)},
		"SelectQuery:SysAdminUnit":                    {adminWriteRows(adminWriteUnitRow(adminWriteTestRole, "Role", 6, adminWriteAllEmployees))},
		"SelectQuery:SysAdminOperationGrantee":        {adminWriteRows(), adminWriteRows(grant)},
		"rest/RightsService/SetAdminOperationGrantee": {`{"SetAdminOperationGranteeResult":"{\"success\":true}"}`},
	})
	result = client.ManageAdministration(context.Background(), AdminWriteManageAccess, AdminWriteManageRequest{
		InspectRequest: InspectRequest{Action: "grant-operation", OperationID: "77777777-7777-4777-8777-777777777777", UnitID: adminWriteTestRole}})
	if result.ExitCode != 0 || result.Messages[0].Value != "["+grant+"]" {
		t.Fatalf("grant = %#v", result)
	}
	if got := stand.body("rest/RightsService/SetAdminOperationGrantee"); got != `{"adminOperationId":"77777777-7777-4777-8777-777777777777","adminUnitIds":["`+adminWriteTestRole+`"],"canExecute":true}` {
		t.Errorf("grant body = %s", got)
	}
}

func TestManageLicenseRoleAssignWrapsTheAssociations(t *testing.T) {
	association := `{"Id":"88888888-8888-4888-8888-888888888888","SysRole":"r","SysLicPackage":"p"}`
	stand, client := newAdminWriteManageStand(t, map[string][]string{
		"SelectQuery:SysAdminUnit":               {adminWriteRows(adminWriteUnitRow(adminWriteTestRole, "Role", 6, adminWriteAllEmployees))},
		"SelectQuery:SysLicPackage":              {adminWriteRows(`{"Id":"99999999-9999-4999-8999-999999999999"}`)},
		"SelectQuery:SysLicPackageInRole":        {adminWriteRows(), adminWriteRows(association)},
		"DataService/json/SyncReply/InsertQuery": {`{"success":true,"rowsAffected":1}`},
	})
	result := client.ManageAdministration(context.Background(), AdminWriteManageLicense, AdminWriteManageRequest{
		InspectRequest: InspectRequest{Action: "role-assign", RoleID: adminWriteTestRole}, PackageID: "99999999-9999-4999-8999-999999999999"})
	if result.ExitCode != 0 || result.Messages[0].Value != `{"associations":[`+association+`],"redistributionRequired":true,"userAssignmentsVerified":false}` {
		t.Fatalf("role-assign = %#v", result)
	}
	if body := stand.body("DataService/json/SyncReply/InsertQuery"); !strings.HasPrefix(body, `{"rootSchemaName":"SysLicPackageInRole","operationType":1,`) {
		t.Errorf("insert = %s", body)
	}
}
