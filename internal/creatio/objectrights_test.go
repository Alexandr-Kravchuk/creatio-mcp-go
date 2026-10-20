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
	"time"
)

const (
	testSales   = "11111111-1111-1111-1111-111111111111"
	testSupport = "22222222-2222-2222-2222-222222222222"
)

// rightsStand is a canned RightManagementService: objects by schema UId, the SysSchema rows that name them, the
// SysAdminUnit names, and every save it received (applied, so a read-back sees it).
type rightsStand struct {
	t       *testing.T
	mu      sync.Mutex
	nodes   map[string]map[string]any
	schemas map[string][]string
	units   map[string]string
	saves   []map[string]any
	routes  []string
	runs    []map[string]any
	status  string
}

func newRightsStand(t *testing.T) (*rightsStand, *Client) {
	stand := &rightsStand{t: t, nodes: map[string]map[string]any{}, schemas: map[string][]string{},
		units: map[string]string{allEmployeesID: "All employees", testSales: "Sales managers", testSupport: "Support"}, status: processStatusCompleted}
	server := httptest.NewServer(http.HandlerFunc(stand.serve))
	t.Cleanup(server.Close)
	return stand, newFormsTestClient(t, server.URL)
}

func (s *rightsStand) object(name string, uids []string, node map[string]any) {
	s.schemas[name] = uids
	s.nodes[uids[len(uids)-1]] = node
}

func (s *rightsStand) serve(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	route := strings.TrimPrefix(r.URL.Path, "/0/")
	s.mu.Lock()
	defer s.mu.Unlock()
	s.routes = append(s.routes, route)
	reply := func(value any) { encoded, _ := json.Marshal(value); _, _ = w.Write(encoded) }
	switch {
	case strings.HasSuffix(route, "AuthService.svc/Login"):
		_, _ = w.Write([]byte(`{"Code":0}`))
	case strings.HasSuffix(route, "/GetAdministratedObject"):
		node, ok := s.nodes[body["schemaUId"].(string)]
		if !ok {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte("<html>Request Error</html>"))
			return
		}
		reply(map[string]any{"success": true, "administratedObject": node})
	case strings.HasSuffix(route, "/SaveAdministratedObject"):
		payload := body["administratedObject"].(map[string]any)
		s.saves = append(s.saves, payload)
		node := s.nodes[payload["uId"].(string)]
		node["administratedByOperations"] = payload["administratedByOperations"]
		node["administratedByRecords"] = payload["administratedByRecords"]
		if rows, ok := payload["entitySchemaOperationsRights"].([]any); ok {
			for _, row := range rows {
				unit := row.(map[string]any)["sysAdminUnit"].(map[string]any)
				unit["name"] = s.units[unit["id"].(string)]
			}
			node["entitySchemaOperationsRights"] = rows
		}
		if rules, ok := payload["entitySchemaRecordDefRights"].([]any); ok {
			node["entitySchemaRecordDefRights"] = rules
		}
		reply(map[string]any{"success": true})
	case strings.HasSuffix(route, "/SelectQuery"):
		reply(s.selectAnswer(body))
	case strings.HasSuffix(route, "/RunProcess"):
		s.runs = append(s.runs, body)
		reply(map[string]any{"processId": "77777777-7777-7777-7777-777777777777", "processStatus": 1, "success": true})
	default:
		s.t.Errorf("unexpected route %s", route)
		http.NotFound(w, r)
	}
}

func (s *rightsStand) selectAnswer(body map[string]any) map[string]any {
	values := map[string]any{}
	if filters, ok := body["filters"].(map[string]any); ok {
		for _, item := range filters["items"].(map[string]any) {
			filter := item.(map[string]any)
			column := filter["leftExpression"].(map[string]any)["columnPath"].(string)
			values[column] = filter["rightExpression"].(map[string]any)["parameter"].(map[string]any)["value"]
		}
	}
	columns := body["columns"].(map[string]any)["items"].(map[string]any)
	rows := []any{}
	switch root := body["rootSchemaName"].(string); {
	case root == "SysSchema" && columns["ExtendParent"] != nil:
		for _, uid := range s.schemas[values["Name"].(string)] {
			rows = append(rows, map[string]any{"UId": uid})
		}
	case root == "SysSchema":
		for name, uids := range s.schemas {
			if node := s.nodes[uids[len(uids)-1]]; node["caption"] == values["Caption"] {
				rows = append(rows, map[string]any{"Name": name, "Caption": node["caption"]})
			}
		}
	case root == "SysAdminUnit":
		if name, ok := s.units[values["Id"].(string)]; ok {
			rows = append(rows, map[string]any{"Name": name})
		}
	case root == "SysProcessLog" && columns["Status"] != nil:
		rows = append(rows, map[string]any{"Status": map[string]any{"value": s.status, "displayValue": "Some status"}})
	case root == "SysProcessLog":
	default:
		rows = append(rows, map[string]any{"Count": 7})
	}
	return map[string]any{"success": true, "rows": rows}
}

func testRow(unit, name string, position int, ops ...bool) map[string]any {
	return map[string]any{"sysAdminUnit": map[string]any{"id": unit, "name": name}, "position": position,
		"canRead": ops[0], "canAppend": ops[1], "canEdit": ops[2], "canDelete": ops[3], "extra": "kept"}
}

func testObject(uid, name, caption string, operations, records bool, rows []any, rules []any) map[string]any {
	return map[string]any{"uId": uid, "name": name, "caption": caption, "administratedByOperations": operations,
		"administratedByRecords": records, "entitySchemaOperationsRights": rows, "entitySchemaRecordDefRights": rules,
		"entitySchemaColumnsRights": []any{}, "entityOperationGrantees": []any{}, "unknownField": 42}
}

func TestGetObjectRightsReadsTheBaseLayerAndReportsBothLayers(t *testing.T) {
	stand, client := newRightsStand(t)
	stand.object("UsrOrder", []string{"bbbbbbbb-0000-0000-0000-000000000001", "aaaaaaaa-0000-0000-0000-000000000001"},
		testObject("aaaaaaaa-0000-0000-0000-000000000001", "UsrOrder", "Orders", true, true,
			[]any{testRow(testSupport, "Support", 0, true, false, false, false), testRow(allEmployeesID, "All employees", 1, true, true, true, true)},
			[]any{map[string]any{"authorSysAdminUnit": map[string]any{"id": allEmployeesID, "name": "All employees"},
				"granteeSysAdminUnit": map[string]any{"id": testSales, "name": "Sales managers"}, "readRightLevel": 1, "editRightLevel": 2}}))
	result := client.GetObjectRights(context.Background(), ObjectRightsGetRequest{EntitySchemaName: " UsrOrder ", Grantee: strings.ToUpper(allEmployeesID)})
	want := strings.Join([]string{
		"Object permissions for 'Orders' (UsrOrder) (grantee " + allEmployeesID + "):",
		"  " + objectRightsPriorityRule,
		"  " + objectRightsGuidancePointer,
		"  'Orders' (UsrOrder): administered by operation permissions. Rows in priority order:",
		"    [1] All employees (" + allEmployeesID + "): read/create/edit/delete",
		"    Rows above it, which decide first for a user who is also in those roles:",
		"      [0] Support (" + testSupport + "): read",
		"    Record permissions: ON. Default record rules (records created by author → rights of grantee; rules add up, their order has no meaning):",
		"      no rule matches the filter (1 rule(s) in all).",
		"  UsrOrder: 7 existing record(s), counted under the calling account.",
	}, "\n")
	if !result.Success || result.Output != want {
		t.Fatalf("result = %#v\nwant %s", result, want)
	}
}

func TestGetObjectRightsResolvesATitleAndRefusesAnAmbiguousOne(t *testing.T) {
	stand, client := newRightsStand(t)
	stand.object("Feature", []string{"aaaaaaaa-0000-0000-0000-000000000005"},
		testObject("aaaaaaaa-0000-0000-0000-000000000005", "Feature", "Creatio functionality", false, false, []any{}, []any{}))
	result := client.GetObjectRights(context.Background(), ObjectRightsGetRequest{EntitySchemaName: "Creatio functionality"})
	if !result.Success || !strings.Contains(result.Output, "  'Creatio functionality' is not an object code: it is the title of Feature, which is read.\n") ||
		!strings.Contains(result.Output, "It has no 'All employees' row") || strings.Contains(result.Output, "existing record") {
		t.Fatalf("result = %#v", result)
	}
	stand.object("UsrOther", []string{"aaaaaaaa-0000-0000-0000-000000000006"},
		testObject("aaaaaaaa-0000-0000-0000-000000000006", "UsrOther", "Creatio functionality", false, false, []any{}, []any{}))
	result = client.GetObjectRights(context.Background(), ObjectRightsGetRequest{EntitySchemaName: "Creatio functionality"})
	if result.Success || !strings.HasPrefix(result.Error, "Error: 'Creatio functionality' is not an object code: it is the title of 2 objects: ") ||
		!strings.HasSuffix(result.Error, ". Re-run with the exact code.") {
		t.Fatalf("result = %#v", result)
	}
}

func TestGetObjectRightsRefusesBadFiltersBeforeAnyRequest(t *testing.T) {
	stand, client := newRightsStand(t)
	for _, test := range []struct {
		input ObjectRightsGetRequest
		want  string
	}{
		{ObjectRightsGetRequest{EntitySchemaName: " "}, "Error: --entity-schema-name is required."},
		{ObjectRightsGetRequest{EntitySchemaName: "Account", Grantee: emptyGUID}, "Error: --grantee must be a SysAdminUnit id (GUID)."},
		{ObjectRightsGetRequest{EntitySchemaName: "Account", Author: "x"}, "Error: --author must be a SysAdminUnit id (GUID)."},
	} {
		if result := client.GetObjectRights(context.Background(), test.input); result.Success || result.Error != test.want {
			t.Fatalf("%#v: %#v", test.input, result)
		}
	}
	if len(stand.routes) != 0 {
		t.Fatalf("requests sent: %v", stand.routes)
	}
}

func TestSetObjectRightsWritesOnlyTheChangedRowAndVerifiesIt(t *testing.T) {
	stand, client := newRightsStand(t)
	stand.object("UsrOrder", []string{"aaaaaaaa-0000-0000-0000-000000000001"},
		testObject("aaaaaaaa-0000-0000-0000-000000000001", "UsrOrder", "Orders", false, false, []any{}, []any{}))
	grantee, operations := testSales, "edit, READ"
	result := client.SetObjectRights(context.Background(), ObjectRightsSetRequest{EntitySchemaName: "UsrOrder", Grantee: &grantee,
		Operations: &operations, Enable: true})
	want := strings.Join([]string{
		"'Orders' (UsrOrder): granted [read/edit] for grantee 'Sales managers' (" + testSales + ").",
		"  Operation permissions on 'Orders' (UsrOrder) are turned ON: from then on its rows decide, in priority order, who can reach it.",
		"  An 'All employees' row with read/create/edit/delete is added at position 0, below the existing rows.",
		"  A row for the grantee is added at position 1, the lowest priority, with read/edit.",
		"  Rows above the grantee's row, which decide first for a user who is also in those roles: [0] All employees: read/create/edit/delete.",
		"  Rows now: [0] All employees: read/create/edit/delete; [1] Sales managers: read/edit.",
	}, "\n")
	if !result.Success || result.Output != want {
		t.Fatalf("result = %#v\nwant %s", result, want)
	}
	if len(stand.saves) != 1 {
		t.Fatalf("saves = %d", len(stand.saves))
	}
	saved := stand.saves[0]
	if saved["administratedByOperations"] != true || saved["entitySchemaRecordDefRights"] != nil || saved["entitySchemaColumnsRights"] != nil ||
		saved["entityOperationGrantees"] != nil || saved["unknownField"] != float64(42) {
		t.Fatalf("save payload = %#v", saved)
	}
	rows := saved["entitySchemaOperationsRights"].([]any)
	if len(rows) != 2 || rows[1].(map[string]any)["canEdit"] != true || rows[1].(map[string]any)["canAppend"] != false {
		t.Fatalf("rows = %#v", rows)
	}
}

func TestSetObjectRightsRefusalsWriteNothing(t *testing.T) {
	stand, client := newRightsStand(t)
	stand.object("UsrOrder", []string{"aaaaaaaa-0000-0000-0000-000000000001"},
		testObject("aaaaaaaa-0000-0000-0000-000000000001", "UsrOrder", "UsrOrder", false, false, []any{}, []any{}))
	stand.object("UsrDup", []string{"aaaaaaaa-0000-0000-0000-000000000002"},
		testObject("aaaaaaaa-0000-0000-0000-000000000002", "UsrDup", "", true, false,
			[]any{testRow(testSales, "Sales managers", 0, true, false, false, false), testRow(testSales, "Sales managers", 4, false, false, false, false)}, []any{}))
	grantee, operations := testSales, "read"
	unknown := "33333333-3333-3333-3333-333333333333"
	for _, test := range []struct {
		input ObjectRightsSetRequest
		want  string
	}{
		{ObjectRightsSetRequest{EntitySchemaName: "UsrOrder", Grantee: &grantee, Operations: &operations},
			"Error: 'UsrOrder' does not use operation permissions yet. Granting would turn them ON, and from then on its rows decide who can reach " +
				"it; an 'All employees' row with read/create/edit/delete would be added below them. " +
				"Re-run with --enable-operation-permissions if that is intended. Nothing was changed."},
		{ObjectRightsSetRequest{EntitySchemaName: "UsrDup", Grantee: &grantee, Operations: &operations},
			"Error: grantee 'Sales managers' (" + testSales + ") has 2 rows on 'UsrDup' (positions 0, 4). Which of them decides depends on the other " +
				"rows, so the tool changes none of them. Remove the duplicates in the Object permissions designer, then re-run. Nothing was changed."},
		{ObjectRightsSetRequest{EntitySchemaName: "UsrOrder", Grantee: &unknown, Operations: &operations},
			"Error: grantee " + unknown + " was not found in SysAdminUnit. Nothing was changed — pass the id of an existing role or user."},
		{ObjectRightsSetRequest{EntitySchemaName: "UsrOrder", Disable: true}, "'UsrOrder': operation permissions are already OFF — the object is available to all internal users (no change)."},
	} {
		result := client.SetObjectRights(context.Background(), test.input)
		text := result.Error + result.Output
		if text != test.want {
			t.Fatalf("got  %s\nwant %s", text, test.want)
		}
	}
	if len(stand.saves) != 0 {
		t.Fatalf("saves = %#v", stand.saves)
	}
}

func TestSetObjectRightsRefusesASaveTheDeadlineCannotCover(t *testing.T) {
	stand, client := newRightsStand(t)
	stand.object("UsrOrder", []string{"aaaaaaaa-0000-0000-0000-000000000001"},
		testObject("aaaaaaaa-0000-0000-0000-000000000001", "UsrOrder", "UsrOrder", true, false,
			[]any{testRow(allEmployeesID, "All employees", 0, true, true, true, true)}, []any{}))
	start := time.Now()
	calls := 0
	clock := rightsClock{now: func() time.Time {
		calls++
		if calls > 2 {
			return start.Add(61 * time.Second) // the reads took 61 of the 100 s
		}
		return start
	}, sleep: func(context.Context, time.Duration) {}}
	grantee, operations := testSales, "read"
	result := client.SetObjectRights(context.Background(), ObjectRightsSetRequest{EntitySchemaName: "UsrOrder", Grantee: &grantee,
		Operations: &operations, clock: &clock})
	want := "Error: 'UsrOrder': the reads before the save took most of the call's time limit of 100 s: 39 s are left, and the save and the " +
		"read-back need up to 50 s. The save was not sent — nothing was changed. Re-run the call."
	if result.Error != want || len(stand.saves) != 0 {
		t.Fatalf("result = %#v saves = %d", result, len(stand.saves))
	}
}

func TestSetDefaultRecordRightsReplacesTheWholeRuleListAndReportsTheCount(t *testing.T) {
	stand, client := newRightsStand(t)
	keep := map[string]any{"authorSysAdminUnit": map[string]any{"id": testSales, "name": "Sales managers"},
		"granteeSysAdminUnit": map[string]any{"id": testSupport, "name": "Support"}, "readRightLevel": 1, "custom": "kept"}
	stand.object("UsrOrder", []string{"aaaaaaaa-0000-0000-0000-000000000001"},
		testObject("aaaaaaaa-0000-0000-0000-000000000001", "UsrOrder", "UsrOrder", false, false, []any{}, []any{keep}))
	author, grantee, operations, level := allEmployeesID, testSales, "delete", "delegated"
	result := client.SetDefaultRecordRights(context.Background(), RecordRightsSetRequest{EntitySchemaName: "UsrOrder", Author: &author,
		Grantee: &grantee, Operations: &operations, Level: &level, Enable: true})
	if !result.Success || !strings.HasPrefix(result.Output, "Done. On 'UsrOrder': turn record permissions ON; grant [delete] delegated by rule 'All employees'") ||
		!strings.Contains(result.Output, "'UsrOrder' has 7 existing record(s), counted under the calling account.") {
		t.Fatalf("result = %#v", result)
	}
	rules := stand.saves[0]["entitySchemaRecordDefRights"].([]any)
	if len(rules) != 2 || rules[0].(map[string]any)["custom"] != "kept" || rules[1].(map[string]any)["deleteRightLevel"] != float64(2) ||
		stand.saves[0]["entitySchemaOperationsRights"] != nil {
		t.Fatalf("save = %#v", stand.saves[0])
	}
}

func TestApplyDefaultRecordRightsWaitsForTheRun(t *testing.T) {
	stand, client := newRightsStand(t)
	stand.object("UsrOrder", []string{"aaaaaaaa-0000-0000-0000-000000000001"},
		testObject("aaaaaaaa-0000-0000-0000-000000000001", "UsrOrder", "UsrOrder", false, true, []any{}, []any{}))
	result := client.ApplyDefaultRecordRights(context.Background(), RecordRightsApplyRequest{EntitySchemaName: "UsrOrder"})
	if !result.Success || !strings.HasSuffix(result.Output, "the record-rights update completed (process 77777777-7777-7777-7777-777777777777). "+
		"Existing records now have the rights of the current default rules; rights granted by hand were kept.") {
		t.Fatalf("result = %#v", result)
	}
	run := stand.runs[0]
	if run["schemaName"] != actualizationProcessName || run["parameterValues"].([]any)[0].(map[string]any)["value"] != "aaaaaaaa-0000-0000-0000-000000000001" {
		t.Fatalf("run = %#v", run)
	}
	// A run that does not end in the wait is reported as still running, never as a failure.
	stand.status = processStatusRunning
	now := time.Now()
	clock := rightsClock{now: func() time.Time { return now }, sleep: func(_ context.Context, d time.Duration) { now = now.Add(d) }}
	timeout := 5
	result = client.ApplyDefaultRecordRights(context.Background(), RecordRightsApplyRequest{EntitySchemaName: "UsrOrder", TimeoutSeconds: &timeout, clock: &clock})
	if !result.Success || !strings.Contains(result.Output, "the record-rights update is still running (process ") {
		t.Fatalf("result = %#v", result)
	}
	zero := 0
	if result := client.ApplyDefaultRecordRights(context.Background(), RecordRightsApplyRequest{EntitySchemaName: "UsrOrder", TimeoutSeconds: &zero}); result.Error != "Error: --timeout-seconds must be a positive number of seconds." {
		t.Fatalf("result = %#v", result)
	}
}

func TestObjectRightsPlannerTransitions(t *testing.T) {
	support := roleRow{granteeID: testSupport, granteeName: "Support", position: 0, ops: [4]bool{true}}
	all := roleRow{granteeID: allEmployeesID, granteeName: "All employees", position: 2, ops: [4]bool{true, true, true, true}}
	administered := objectRightsState{administered: true, roles: []roleRow{all, support}}
	// A revoke that would leave no granting row is refused; one that leaves a row granting something is planned.
	plan := planObjectRights(objectRightsState{administered: true, roles: []roleRow{support}},
		objectRightsRequest{grantee: testSupport, operations: []objectOperation{opRead}, revoke: true})
	if plan.refusal != refusalLeavesNoGrantingRow {
		t.Fatalf("plan = %#v", plan)
	}
	plan = planObjectRights(administered, objectRightsRequest{grantee: allEmployeesID, operations: []objectOperation{opDelete}, revoke: true})
	if plan.refused() || plan.after.roles[1].ops != [4]bool{true, true, true, false} || len(plan.rowsAboveGrantee) != 1 {
		t.Fatalf("plan = %#v", plan)
	}
	// A grant to All employees on an object being enabled makes its own row, with exactly the named operations.
	plan = planObjectRights(objectRightsState{}, objectRightsRequest{grantee: allEmployeesID, granteeName: "All employees",
		operations: []objectOperation{opRead}, enable: true})
	if plan.refused() || plan.addsAllEmployeesRow || len(plan.after.roles) != 1 || plan.after.roles[0].ops != [4]bool{true} {
		t.Fatalf("plan = %#v", plan)
	}
	// Enabling over rows that grant nothing is refused, as no internal user could reach the object.
	plan = planObjectRights(objectRightsState{roles: []roleRow{{granteeID: allEmployeesID, position: 0}}}, objectRightsRequest{grantee: emptyGUID, enable: true})
	if plan.refusal != refusalLeavesNoGrantingRow {
		t.Fatalf("plan = %#v", plan)
	}
	// A disable keeps every row; the synthesized All employees row read back afterwards is not a difference.
	plan = planObjectRights(objectRightsState{administered: true}, objectRightsRequest{grantee: emptyGUID, disable: true})
	critical, differences := compareReadBack(plan, emptyGUID, objectRightsState{roles: []roleRow{{granteeID: allEmployeesID, position: 0, ops: [4]bool{true, true, true, true}}}})
	if !plan.disables || len(critical) != 0 || len(differences) != 0 {
		t.Fatalf("plan = %#v critical %v differences %v", plan, critical, differences)
	}
}

func TestRecordRightsPlannerRules(t *testing.T) {
	stored := recordRule{authorID: testSales, granteeID: testSupport, levels: [3]int{1, 1, 0}, doNotApplyForManager: true}
	before := recordState{rules: []recordRule{stored}}
	plan := planRecordRights(before, recordRightsRequest{rule: &recordRuleChange{author: allEmployeesID, grantee: testSales,
		operations: []recordOperation{recRead}, level: 1}})
	if plan.refusal != recordRefusalEnableNotRequested || len(plan.storedComingIntoEffect) != 1 {
		t.Fatalf("plan = %#v", plan)
	}
	plan = planRecordRights(before, recordRightsRequest{rule: &recordRuleChange{author: testSales, grantee: testSupport,
		operations: []recordOperation{recRead, recEdit}, revoke: true}})
	if plan.refused() || plan.ruleAfter != nil || len(plan.after.rules) != 0 || !plan.changesRules() {
		t.Fatalf("plan = %#v", plan)
	}
	plan = planRecordRights(recordState{administered: true, rules: []recordRule{stored}}, recordRightsRequest{rule: &recordRuleChange{
		author: testSales, grantee: testSupport, operations: []recordOperation{recDelete}, level: 2}})
	if plan.refused() || plan.ruleAfter.levels != [3]int{1, 1, 2} || !plan.ruleAfter.doNotApplyForManager {
		t.Fatalf("plan = %#v", plan)
	}
	duplicate := recordState{administered: true, rules: []recordRule{stored, stored}}
	plan = planRecordRights(duplicate, recordRightsRequest{rule: &recordRuleChange{author: allEmployeesID, grantee: testSales,
		operations: []recordOperation{recRead}, level: 1}})
	if plan.refusal != recordRefusalDuplicatePairs || len(plan.problemRules) != 2 {
		t.Fatalf("plan = %#v", plan)
	}
	// A call that leaves the rules alone never trips over a bad stored rule: the save sends them as null.
	plan = planRecordRights(recordState{rules: []recordRule{{authorID: testSales, granteeID: testSupport, levels: [3]int{7}}}}, recordRightsRequest{enable: true})
	if plan.refused() || !plan.enables() {
		t.Fatalf("plan = %#v", plan)
	}
}

func TestRightsSanitizeMatchesClio(t *testing.T) {
	for _, test := range []struct{ in, want string }{
		{"a\nb\tc​d", "a b c d"},
		{strings.Repeat("x", 205), strings.Repeat("x", 200) + "..."},
		{strings.Repeat("x", 199) + "😀z", strings.Repeat("x", 199) + "..."},
		{"", ""},
	} {
		if got := rightsSanitize(test.in, 200); got != test.want {
			t.Fatalf("rightsSanitize(%q) = %q, want %q", test.in, got, test.want)
		}
	}
	if got := rightsFormatObject("UsrOrder", "Orders' (UsrX)"); got != "'Orders’ (UsrX)' (UsrOrder)" {
		t.Fatalf("title = %s", got)
	}
	if !rightsIsSecurityObject("SysAdminUnit") || !rightsIsSecurityObject("ContactRight") || rightsIsSecurityObject("Contact") {
		t.Fatal("security object filter")
	}
	if rightsSeconds(1500*time.Millisecond) != "2" || rightsSeconds(2500*time.Millisecond) != "3" {
		t.Fatal("seconds rounding")
	}
}
