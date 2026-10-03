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

// appWriteFake is a Creatio stand for the application write tools: it answers by route and, for DataService
// queries, by root schema, and records every request in order.
type appWriteFake struct {
	t       *testing.T
	mu      sync.Mutex
	calls   []appWriteCall
	answers map[string]func(body map[string]any) (int, string)
}

type appWriteCall struct {
	route string
	body  map[string]any
	raw   string
}

func (f *appWriteFake) key(path string, body map[string]any) string {
	route := path[strings.LastIndex(path, "/")+1:]
	if root, ok := body["rootSchemaName"].(string); ok {
		return route + ":" + root
	}
	return route
}

func (f *appWriteFake) serve(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	if strings.HasSuffix(r.URL.Path, "/Login") {
		io.WriteString(w, `{"Code":0}`)
		return
	}
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	key := f.key(r.URL.Path, body)
	f.mu.Lock()
	f.calls = append(f.calls, appWriteCall{route: key, body: body, raw: string(raw)})
	answer, ok := f.answers[key]
	if !ok {
		answer, ok = f.answers[key[:strings.Index(key+":", ":")]]
	}
	f.mu.Unlock()
	if !ok {
		f.t.Errorf("unexpected request %s %s", key, raw)
		http.NotFound(w, r)
		return
	}
	status, text := answer(body)
	w.WriteHeader(status)
	io.WriteString(w, text)
}

func (f *appWriteFake) routes() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	routes := make([]string, len(f.calls))
	for i, call := range f.calls {
		routes[i] = call.route
	}
	return routes
}

func (f *appWriteFake) find(route string) []appWriteCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var found []appWriteCall
	for _, call := range f.calls {
		if call.route == route {
			found = append(found, call)
		}
	}
	return found
}

func ok(text string) func(map[string]any) (int, string) {
	return func(map[string]any) (int, string) { return http.StatusOK, text }
}

// newAppWriteFake starts the fake with the reads every application tool makes: profile culture en-US,
// SchemaNamePrefix "Usr", one icon, an application with a primary package and no entities or pages, and a
// navigation reset that succeeds.
func newAppWriteFake(t *testing.T) (*appWriteFake, *Client) {
	t.Helper()
	schemaWriteCultureCache.Lock()
	schemaWriteCultureCache.entries = map[string]schemaWriteCultureEntry{}
	schemaWriteCultureCache.Unlock()
	fake := &appWriteFake{t: t, answers: map[string]func(map[string]any) (int, string){
		"GetApplicationInfo":             ok(`{"applicationInfo":{"sysValues":{"userCulture":{"displayValue":"en-US"}}}}`),
		"QuerySysSettings":               ok(`{"success":true,"values":{"SchemaNamePrefix":"Usr"}}`),
		"SelectQuery:SysAppIcons":        ok(`{"success":true,"rows":[{"Id":"11111111-1111-1111-1111-111111111111"}]}`),
		"SelectQuery:SysInstalledApp":    ok(`{"success":true,"rows":[{"Id":"aaaaaaaa-0000-0000-0000-000000000001","Code":"UsrTodo","Name":"Todo","Version":"1.0.0"}]}`),
		"GetApplicationPackages":         ok(`{"success":true,"packages":[{"uId":"bbbbbbbb-0000-0000-0000-000000000002","name":"UsrTodo","isApplicationPrimaryPackage":true}]}`),
		"SelectQuery:ApplicationEntity":  ok(`{"success":true,"rows":[]}`),
		"SelectQuery:SysSchema":          ok(`{"success":true,"rows":[]}`),
		"SelectQuery:SysSettingsValue":   ok(`{"success":true,"rows":[{"TextValue":"Usr"}]}`),
		"IsODataBuildRunning":            ok(`{"value":false}`),
		"GetData":                        ok(`{"success":true}`),
		"GetDataForgeStatus":             ok(`<html>no data forge</html>`),
		"GetDataStructureReadinessState": ok(`<html>no data forge</html>`),
	}}
	server := httptest.NewServer(http.HandlerFunc(fake.serve))
	t.Cleanup(server.Close)
	waits, sectionWaits := appWriteCreateWaits, appWriteSectionWaits
	appWriteCreateWaits = appWriteCreateTimings{pollDelay: time.Millisecond, gateInterval: time.Millisecond, createTimeout: 5 * time.Second}
	appWriteSectionWaits = appWriteSectionTimings{insert: 5 * time.Second, readback: 5 * time.Second, verify: 5 * time.Second,
		pollDelay: time.Millisecond, recoveryInitial: time.Millisecond, recoveryMax: time.Millisecond, recoveryBudget: time.Second, serializationCap: time.Second}
	t.Cleanup(func() { appWriteCreateWaits, appWriteSectionWaits = waits, sectionWaits })
	return fake, newFormsTestClient(t, server.URL)
}

func TestAppWriteValidateCreate(t *testing.T) {
	template := " appfreedomui "
	cases := []struct {
		request AppCreateRequest
		want    string
	}{
		{AppCreateRequest{Code: "X"}, "name is required."},
		{AppCreateRequest{Name: "X"}, "code is required."},
		{AppCreateRequest{Name: "X", Code: "X", IconBackground: "#a6de01"}, "is not a Freedom UI palette color"},
		{AppCreateRequest{Name: "X", Code: "X", LocalizationMaps: true}, "create-app is scalar-only"},
		{AppCreateRequest{Name: "X", Code: "X", OptionalTemplateDataJSON: `{"useAIContentGeneration":true}`}, "useAiContentGeneration=true is not supported"},
		{AppCreateRequest{Name: "X", Code: "X", OptionalTemplateDataJSON: `{"useExistingEntitySchema":true}`}, "entitySchemaName is required when useExistingEntitySchema=true."},
		{AppCreateRequest{Name: "X", Code: "X", OptionalTemplateDataJSON: `{bad`}, "Invalid optional-template-data-json format: "},
	}
	for _, tc := range cases {
		if _, err := ValidateAppCreate(tc.request); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%+v: %v", tc.request, err)
		}
	}
	data, err := ValidateAppCreate(AppCreateRequest{Name: "X", Code: "X", TemplateCode: &template, IconBackground: "#a6de00",
		OptionalTemplateDataJSON: `{"UseExistingEntitySchema":true,"entityschemaname":"Contact"}`})
	if err != nil || data == nil || *data.EntitySchemaName != "Contact" {
		t.Fatalf("valid request refused: %v %+v", err, data)
	}
}

func TestAppWriteSanitizeCode(t *testing.T) {
	for _, tc := range []struct{ code, prefix, want string }{
		{"todo list", "Usr", "UsrTodoList"}, {"UsrTodoList", "Usr", "UsrTodoList"}, {"usrtodo", "Usr", "UsrTodo"},
		{"2Go", "Usr", "Usr_2Go"}, {"my-app", "", "MyApp"},
	} {
		if got, err := appWriteSanitizeCode(tc.code, tc.prefix); err != nil || got != tc.want {
			t.Errorf("%q/%q = %q, %v", tc.code, tc.prefix, got, err)
		}
	}
	if _, err := appWriteSanitizeCode("9lives", ""); err == nil || !strings.Contains(err.Error(), "starts with a digit") {
		t.Fatalf("digit without prefix: %v", err)
	}
}

func TestAppWriteCreateAppSuccess(t *testing.T) {
	fake, client := newAppWriteFake(t)
	fake.answers["CreateApp"] = ok(`{"success":true,"value":"aaaaaaaa-0000-0000-0000-000000000001"}`)
	description := " A list "
	var stages []string
	result := client.CreateApp(context.Background(), AppCreateRequest{Name: "Todo", Code: "todo", Description: &description,
		IconBackground: "#247EE5", WithMobilePages: false}, nil, func(stage string) { stages = append(stages, stage) })
	if !result.Success || result.ApplicationCode != "UsrTodo" || result.PackageName != "UsrTodo" || result.PackageUID == "" ||
		result.SchemaNamePrefix == nil || *result.SchemaNamePrefix != "Usr" || result.NextStep == "" || result.Warnings != nil {
		t.Fatalf("result %+v", result)
	}
	if result.DataForge == nil || !result.DataForge.Used || len(result.DataForge.Warnings) != 1 || !strings.HasPrefix(result.DataForge.Warnings[0], "dataforge:") {
		t.Fatalf("dataforge %+v", result.DataForge)
	}
	creates := fake.find("CreateApp")
	if len(creates) != 1 {
		t.Fatalf("CreateApp calls %d", len(creates))
	}
	body := creates[0].body
	if body["code"] != "UsrTodo" || body["name"] != "Todo" || body["description"] != "A list" || body["templateCode"] != "AppFreedomUI" ||
		body["iconBackground"] != "#247EE5" || body["iconId"] != "11111111-1111-1111-1111-111111111111" ||
		body["clientTypeId"] != appWriteWebClientTypeID {
		t.Fatalf("CreateApp body %s", creates[0].raw)
	}
	if optional, ok := body["optionalTemplateData"].(map[string]any); !ok || len(optional) != 0 {
		t.Fatalf("optionalTemplateData %s", creates[0].raw)
	}
	if got := strings.Join(stages, "|"); got != "enriching application model|creating application package|loading application metadata" {
		t.Fatalf("stages %s", got)
	}
	routes := strings.Join(fake.routes(), ",")
	if strings.Index(routes, "IsODataBuildRunning") > strings.Index(routes, "CreateApp") || !strings.HasSuffix(routes, "GetData") {
		t.Fatalf("order %s", routes)
	}
}

func TestAppWriteCreateAppFailureAndTimeoutPolling(t *testing.T) {
	fake, client := newAppWriteFake(t)
	fake.answers["CreateApp"] = ok(`{"success":false,"errorInfo":{"message":"Code is taken."},"dependenciesErrors":[{"source":"A","package":"P"},{}]}`)
	result := client.CreateApp(context.Background(), AppCreateRequest{Name: "Todo", Code: "UsrTodo", WithMobilePages: true}, nil, nil)
	if result.Success || result.Error != "Code is taken. Dependencies: source=A, package=P; unknown dependency error" || result.DataForge != nil {
		t.Fatalf("failure %+v", result)
	}
	if body := fake.find("CreateApp")[0].body; body["clientTypeId"] != nil || body["description"] != nil {
		t.Fatalf("body %v", body)
	}

	fake.answers["CreateApp"] = ok(`{"success":false,"errorInfo":{"message":"App Installer CreateApp request failed: timeout of 90000ms exceeded"}}`)
	result = client.CreateApp(context.Background(), AppCreateRequest{Name: "Todo", Code: "UsrTodo", WithMobilePages: true}, nil, nil)
	if !result.Success || result.ApplicationID != "aaaaaaaa-0000-0000-0000-000000000001" {
		t.Fatalf("timeout polling %+v", result)
	}

	fake.answers["SelectQuery:SysInstalledApp"] = ok(`{"success":true,"rows":[]}`)
	fake.answers["CreateApp"] = ok(`{"success":true,"value":"aaaaaaaa-0000-0000-0000-000000000001"}`)
	result = client.CreateApp(context.Background(), AppCreateRequest{Name: "Todo", Code: "UsrTodo"}, nil, nil)
	if result.Success || result.Error != "Application 'UsrTodo' was created but its metadata could not be loaded after 15 attempts. Last error: Application 'aaaaaaaa-0000-0000-0000-000000000001' not found." {
		t.Fatalf("readback failure %+v", result)
	}
}

func appWriteSectionRows(rows ...string) func(map[string]any) (int, string) {
	return ok(`{"success":true,"rows":[` + strings.Join(rows, ",") + `]}`)
}

const appWriteOrdersRow = `{"Id":"%s","ApplicationId":"aaaaaaaa-0000-0000-0000-000000000001","Caption":"Orders","Code":"UsrOrders","PackageId":"bbbbbbbb-0000-0000-0000-000000000002","LogoId":"11111111-1111-1111-1111-111111111111","IconBackground":"#FF8800","SectionSchemaUId":"cccccccc-0000-0000-0000-000000000003"}`

func TestAppWriteCreateSectionSuccessAfterContention(t *testing.T) {
	fake, client := newAppWriteFake(t)
	inserts := 0
	var sectionID string
	fake.answers["InsertQuery:ApplicationSection"] = func(body map[string]any) (int, string) {
		inserts++
		items := body["columnValues"].(map[string]any)["items"].(map[string]any)
		sectionID = items["Id"].(map[string]any)["parameter"].(map[string]any)["value"].(string)
		if inserts == 1 {
			return http.StatusOK, `{"success":false,"errorInfo":{"message":"InsertQuery failed."}}`
		}
		return http.StatusOK, `{"success":true}`
	}
	fake.answers["SelectQuery:ApplicationSection"] = func(map[string]any) (int, string) {
		if inserts < 2 {
			return http.StatusOK, `{"success":true,"rows":[]}`
		}
		return http.StatusOK, `{"success":true,"rows":[` + strings.Replace(appWriteOrdersRow, "%s", sectionID, 1) + `]}`
	}
	fake.answers["UpdateQuery:ApplicationSection"] = ok(`{"success":true}`)
	color := "#FF8800"
	result := client.CreateAppSection(context.Background(), AppSectionCreateRequest{ApplicationCode: "UsrTodo", Caption: "Orders",
		IconBackground: &color, WithMobilePages: true}, nil)
	if !result.Success || result.Section == nil || result.Section.Code != "UsrOrders" || *result.Section.IconBackground != "#FF8800" ||
		result.Pages == nil || result.NextStep == "" || inserts != 2 {
		t.Fatalf("result %+v inserts %d", result, inserts)
	}
	insert := fake.find("InsertQuery:ApplicationSection")[0].body["columnValues"].(map[string]any)["items"].(map[string]any)
	for column, want := range map[string]any{"Code": "UsrOrders", "Caption": "Orders", "Type": float64(0), "IconBackground": "#FF8800",
		"PackageId": "bbbbbbbb-0000-0000-0000-000000000002", "LogoId": "11111111-1111-1111-1111-111111111111"} {
		if got := insert[column].(map[string]any)["parameter"].(map[string]any)["value"]; got != want {
			t.Errorf("%s = %v", column, got)
		}
	}
	if insert["ClientTypeId"] != nil || insert["EntitySchemaName"] != nil || insert["Description"] != nil {
		t.Fatalf("insert %v", insert)
	}
	if update := fake.find("UpdateQuery:ApplicationSection"); len(update) != 1 {
		t.Fatalf("icon background update %d", len(update))
	}
}

func TestAppWriteCreateSectionFailures(t *testing.T) {
	fake, client := newAppWriteFake(t)
	fake.answers["InsertQuery:ApplicationSection"] = ok(`{"success":false,"errorInfo":{"message":"Duplicate code"}}`)
	result := client.CreateAppSection(context.Background(), AppSectionCreateRequest{ApplicationCode: "UsrTodo", Caption: "Orders", Code: "Orders"}, nil)
	if result.Success || result.ErrorClass != "server-error" || result.SectionCreated != "false" ||
		!strings.HasPrefix(result.Error, "Failed to create section 'Orders' (code 'UsrOrders') in application 'UsrTodo'. Server error: Duplicate code. A section with code") {
		t.Fatalf("server error %+v", result)
	}
	fake.answers["SelectQuery:SysSchema"] = ok(`{"success":true,"rows":[{"Name":"UsrOrders"}]}`)
	result = client.CreateAppSection(context.Background(), AppSectionCreateRequest{ApplicationCode: "UsrTodo", Caption: "Orders"}, nil)
	if result.ErrorClass != "" || result.Error != "Entity schema 'UsrOrders' already exists. To create section 'Orders' reusing the existing entity, add: --entity-schema-name UsrOrders" {
		t.Fatalf("existing entity %+v", result)
	}
	if in := AppSectionInProgress("Orders", " UsrOrders "); in.SectionCreated != "in-progress" || in.ErrorClass != "creatio-timeout" ||
		!strings.Contains(in.Error, "(code 'UsrOrders', pages 'UsrOrders_ListPage' / 'UsrOrders_FormPage')") {
		t.Fatalf("in-progress %+v", in)
	}
}

func TestAppWriteUpdateSectionRestoresOtherCultures(t *testing.T) {
	fake, client := newAppWriteFake(t)
	row := strings.Replace(appWriteOrdersRow, "%s", "dddddddd-0000-0000-0000-000000000004", 1)
	fake.answers["SelectQuery:ApplicationSection"] = appWriteSectionRows(row)
	fake.answers["UpdateQuery:ApplicationSection"] = ok(`{"success":true}`)
	fake.answers["SelectLocalizationQuery:SysModule"] = ok(`{"success":true,"rows":[{"CultureName":"en-US","Caption":"Orders"},{"CultureName":"uk-UA","Caption":"Замовлення","ModuleHeader":"Шапка"}]}`)
	fake.answers["UpdateLocalizationQuery:SysModule"] = ok(`{"success":true}`)
	fake.answers["SelectQuery:SysPackageSchemaData"] = ok(`{"success":true,"rows":[]}`)
	caption := "Sales orders"
	result := client.UpdateAppSection(context.Background(), AppSectionUpdateRequest{ApplicationCode: "UsrTodo", SectionCode: "usrorders", Caption: &caption})
	if !result.Success || strings.Join(result.PreservedCultures, ",") != "uk-UA" || len(result.Warnings) != 1 ||
		!strings.HasPrefix(result.Warnings[0], "Package data binding 'SysModule_UsrOrders' was not found") {
		t.Fatalf("restore %+v", result)
	}
	update := fake.find("UpdateQuery:ApplicationSection")[0].body["columnValues"].(map[string]any)["items"].(map[string]any)
	if update["Caption"].(map[string]any)["parameter"].(map[string]any)["value"] != "Sales orders" || update["Description"] != nil {
		t.Fatalf("update %v", update)
	}
	localization := fake.find("UpdateLocalizationQuery:SysModule")[0].body["columnValues"].(map[string]any)["items"].(map[string]any)
	captions := localization["Caption"].(map[string]any)["parameter"].(map[string]any)
	if captions["dataValueType"] != float64(19) || captions["value"] != `{"uk-UA":"Замовлення","en-US":"Sales orders"}` {
		t.Fatalf("restored captions %v", captions)
	}
	if headers := localization["ModuleHeader"].(map[string]any)["parameter"].(map[string]any)["value"]; headers != `{"uk-UA":"Шапка"}` {
		t.Fatalf("restored headers %v", headers)
	}

	fake.answers["SelectLocalizationQuery:SysModule"] = ok(`{"success":true,"rows":[{"CultureName":"en-US","Caption":"Orders"}]}`)
	result = client.UpdateAppSection(context.Background(), AppSectionUpdateRequest{ApplicationCode: "UsrTodo", SectionCode: "UsrOrders", Caption: &caption})
	if !result.Success || result.CaptionCulture != "en-US" || result.PreviousSection.Caption != "Orders" || result.PreservedCultures == nil ||
		len(result.PreservedCultures) != 0 || result.NextStep == "" {
		t.Fatalf("success %+v", result)
	}

	fake.answers["SelectLocalizationQuery:SysModule"] = func(map[string]any) (int, string) {
		if len(fake.find("UpdateLocalizationQuery:SysModule")) > 1 {
			return http.StatusOK, `{"success":true,"rows":[{"CultureName":"en-US","Caption":"Orders"}]}`
		}
		return http.StatusOK, `{"success":true,"rows":[{"CultureName":"en-US","Caption":"Orders"},{"CultureName":"uk-UA","Caption":"Замовлення"}]}`
	}
	lost := client.UpdateAppSection(context.Background(), AppSectionUpdateRequest{ApplicationCode: "UsrTodo", SectionCode: "UsrOrders", Caption: &caption})
	if lost.Error != "The section was saved, but these localized values are not stored as expected: Caption [uk-UA]. Check the culture in the Languages section and the section in Creatio, then retry." {
		t.Fatalf("verification %+v", lost)
	}

	missing := client.UpdateAppSection(context.Background(), AppSectionUpdateRequest{ApplicationCode: "UsrTodo", SectionCode: "UsrNone", Caption: &caption})
	if missing.Error != "Section 'UsrNone' was not found in application 'aaaaaaaa-0000-0000-0000-000000000001'." {
		t.Fatalf("missing %+v", missing)
	}
}

func TestAppWriteDeleteSection(t *testing.T) {
	fake, client := newAppWriteFake(t)
	sectionID := "dddddddd-0000-0000-0000-000000000004"
	row := strings.Replace(appWriteOrdersRow, "%s", sectionID, 1)
	fake.answers["SelectQuery:ApplicationSection"] = appWriteSectionRows(row)
	fake.answers["SelectQuery:SysModule"] = ok(`{"success":true,"rows":[{"Id":"` + sectionID + `","SectionSchemaUId":"cccccccc-0000-0000-0000-000000000003","CardSchemaUId":"eeeeeeee-0000-0000-0000-000000000005","SysModuleEntityId":"ffffffff-0000-0000-0000-000000000006"}]}`)
	fake.answers["SelectQuery:SysModuleEdit"] = ok(`{"success":true,"rows":[]}`)
	fake.answers["GetWorkspaceItems"] = ok(`{"success":true,"items":[{"id":"10000000-0000-0000-0000-000000000000","uId":"CCCCCCCC-0000-0000-0000-000000000003","name":"UsrOrders_ListPage","packageUId":"bbbbbbbb-0000-0000-0000-000000000002","packageName":"UsrTodo","type":4,"modifiedOn":"2026-10-03T00:00:00"},{"id":"20000000-0000-0000-0000-000000000000","uId":"eeeeeeee-0000-0000-0000-000000000005","name":"UsrOrders_FormPage","type":4}]}`)
	fake.answers["Delete"] = ok(`{"success":true}`)
	fake.answers["DeleteQuery"] = ok(`{"success":true}`)
	result := client.DeleteAppSection(context.Background(), "UsrTodo", "UsrOrders", false)
	if !result.Success || result.DeletedSection == nil || result.DeletedSection.ID != sectionID || result.ApplicationVersion == nil || result.PackageUID != nil {
		t.Fatalf("result %+v", result)
	}
	var order []string
	for _, call := range fake.calls {
		if strings.HasPrefix(call.route, "DeleteQuery") || call.route == "Delete" {
			order = append(order, call.route)
		}
	}
	if got := strings.Join(order, ","); got != "DeleteQuery:SysModuleInWorkplace,DeleteQuery:SysModuleLcz,Delete,DeleteQuery:ApplicationSection,DeleteQuery:SysModule,DeleteQuery:SysModuleEntity" {
		t.Fatalf("order %s", got)
	}
	var deleted []map[string]any
	for _, call := range fake.calls {
		if call.route == "Delete" {
			_ = json.Unmarshal([]byte(call.raw), &deleted)
		}
	}
	if len(deleted) != 1 || deleted[0]["uId"] != "cccccccc-0000-0000-0000-000000000003" || deleted[0]["name"] != "UsrOrders_ListPage" || deleted[0]["title"] != nil {
		t.Fatalf("deleted schema %v (the form page stays without delete-entity-schema)", deleted)
	}

	fake.answers["DeleteQuery"] = ok(`{"success":false,"errorInfo":{"message":"denied"}}`)
	result = client.DeleteAppSection(context.Background(), "UsrTodo", "UsrOrders", false)
	if result.Success || result.Error != "denied" {
		t.Fatalf("failure %+v", result)
	}
}

func TestAppWritePaletteAndNavigation(t *testing.T) {
	if hex, err := AppWriteResolveIconColor(" червоний "); err != nil || hex != "#FF4013" {
		t.Fatalf("alias %q %v", hex, err)
	}
	if hex, err := AppWriteResolveIconColor("#ff4013"); err != nil || hex != "#FF4013" {
		t.Fatalf("hex %q %v", hex, err)
	}
	if _, err := AppWriteResolveIconColor("beige"); err == nil || !strings.HasPrefix(err.Error(), "icon-background 'beige' is not a recognised color. Allowed values: Lime (#A6DE00)") {
		t.Fatalf("unknown %v", err)
	}
	for payload, want := range map[string]string{`{"success":true}`: "", ``: "empty response", `<html/>`: "the response is not JSON",
		`{"success":false,"errorInfo":{"message":"x"}}`: "x", `[]`: "the service did not report success"} {
		if got := appWriteNavigationFailure([]byte(payload)); got != want {
			t.Errorf("%q = %q", payload, got)
		}
	}
	if got := appWriteLocalizedCaption(appWriteStringPointer(`{"uk-UA":"","de-DE":"Auftrag","en-US":"Order"}`), "x", "uk-UA"); got != "Order" {
		t.Fatalf("localized caption %q", got)
	}
}

func appWriteStringPointer(value string) *string { return &value }
