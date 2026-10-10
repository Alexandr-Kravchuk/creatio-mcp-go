package creatio

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// adminWriteFakeSetting is one SysSettings row of the fake stand with its All employees value.
type adminWriteFakeSetting struct {
	id, valueType, referenceUID string
	value                       map[string]any // SysSettingsValue columns; nil when the setting has no value row
}

// adminWriteFakeStand answers the DataService routes the sys-setting writes use and records every request.
type adminWriteFakeStand struct {
	t        *testing.T
	mu       sync.Mutex
	settings map[string]*adminWriteFakeSetting
	schemas  map[string]string // SysSchema name -> UId
	packages []map[string]any
	requests []adminWriteFakeRequest
	// insertAnswer / saveAnswer replace the default answers when set.
	insertAnswer, saveAnswer, queryAnswer string
}

type adminWriteFakeRequest struct {
	route string
	body  string
}

func newAdminWriteFakeStand(t *testing.T) (*adminWriteFakeStand, *Client) {
	t.Helper()
	stand := &adminWriteFakeStand{t: t, settings: map[string]*adminWriteFakeSetting{}, schemas: map[string]string{}}
	server := httptest.NewServer(http.HandlerFunc(stand.serve))
	t.Cleanup(server.Close)
	return stand, newFormsTestClient(t, server.URL)
}

func (s *adminWriteFakeStand) routes() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var routes []string
	for _, request := range s.requests {
		routes = append(routes, request.route)
	}
	return routes
}

func (s *adminWriteFakeStand) body(route string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, request := range s.requests {
		if request.route == route {
			return request.body
		}
	}
	return ""
}

func (s *adminWriteFakeStand) serve(w http.ResponseWriter, r *http.Request) {
	if strings.HasSuffix(r.URL.Path, "/Login") {
		_, _ = w.Write([]byte(`{"Code":0}`))
		return
	}
	raw, _ := io.ReadAll(r.Body)
	route := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	s.mu.Lock()
	defer s.mu.Unlock()
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	if route == "SelectQuery" {
		route = "SelectQuery:" + body["rootSchemaName"].(string)
	}
	s.requests = append(s.requests, adminWriteFakeRequest{route: route, body: string(raw)})
	answer := func(value any) {
		encoded, _ := json.Marshal(value)
		_, _ = w.Write(encoded)
	}
	switch route {
	case "SelectQuery:SysSettings":
		code := adminWriteFilterValue(body, "Code")
		setting := s.settings[code]
		if setting == nil {
			answer(map[string]any{"success": true, "rows": []any{}})
			return
		}
		answer(map[string]any{"success": true, "rows": []any{map[string]any{"Id": setting.id, "ValueTypeName": setting.valueType,
			"ReferenceSchemaUId": setting.referenceUID}}})
	case "SelectQuery:SysSettingsValue":
		id := adminWriteFilterValue(body, "SysSettings")
		for _, setting := range s.settings {
			if setting.id == id && setting.value != nil {
				answer(map[string]any{"success": true, "rows": []any{setting.value}})
				return
			}
		}
		answer(map[string]any{"success": true, "rows": []any{}})
	case "SelectQuery:SysSchema":
		name := adminWriteFilterValue(body, "Name")
		if uid, ok := s.schemas[name]; ok {
			answer(map[string]any{"success": true, "rows": []any{map[string]any{"UId": uid}}})
			return
		}
		answer(map[string]any{"success": true, "rows": []any{}})
	case "SelectQuery:SysPackage":
		answer(map[string]any{"success": true, "rows": s.packages})
	case "InsertSysSettingRequest":
		if s.insertAnswer != "" {
			_, _ = w.Write([]byte(s.insertAnswer))
			return
		}
		code := body["code"].(string)
		s.settings[code] = &adminWriteFakeSetting{id: "3c50b1a3-7aae-49f6-8921-92fb3f5adc40", valueType: body["valueTypeName"].(string)}
		answer(map[string]any{"success": true, "id": "3c50b1a3-7aae-49f6-8921-92fb3f5adc40", "rowsAffected": 1})
	case "PostSysSettingsValues":
		if s.saveAnswer != "" {
			_, _ = w.Write([]byte(s.saveAnswer))
			return
		}
		saved := map[string]bool{}
		for code, value := range body["sysSettingsValues"].(map[string]any) {
			setting := s.settings[code]
			if setting == nil {
				saved[code] = false
				continue
			}
			column := "TextValue"
			switch value.(type) {
			case bool:
				column = "BooleanValue"
			case float64:
				column = "IntegerValue"
				if setting.valueType != "Integer" {
					column = "FloatValue"
				}
			}
			setting.value = map[string]any{column: value}
			saved[code] = true
		}
		answer(map[string]any{"success": true, "saveResult": saved, "rowsAffected": 1})
	case "QuerySysSettings":
		_, _ = w.Write([]byte(s.queryAnswer))
	default:
		s.t.Errorf("unexpected route %s", r.URL.Path)
		http.NotFound(w, r)
	}
}

func adminWriteFilterValue(query map[string]any, column string) string {
	items, _ := query["filters"].(map[string]any)["items"].(map[string]any)
	for _, raw := range items {
		filter := raw.(map[string]any)
		left, _ := filter["leftExpression"].(map[string]any)
		if left["columnPath"] == column {
			value, _ := filter["rightExpression"].(map[string]any)["parameter"].(map[string]any)["value"].(string)
			return value
		}
	}
	return ""
}

func adminWriteString(value string) *string { return &value }
func adminWriteFlag(value bool) *bool       { return &value }

func TestCreateSysSettingInsertsThenAssignsTheValue(t *testing.T) {
	stand, client := newAdminWriteFakeStand(t)
	result := client.CreateSysSetting(context.Background(), AdminWriteCreateSetting{
		Code: adminWriteString("UsrParityExample"), Name: adminWriteString("Example"), ValueTypeName: adminWriteString("ShortText"),
		Description: adminWriteString("own setting"), Value: adminWriteString("first")})
	if !result.Success || result.Value == nil || *result.Value != "first" || result.Error != "" || result.Warning != "" {
		t.Fatalf("create = %#v", result)
	}
	// clio's CreatioSysSetting serialization: subclass property first, nulls left out.
	if got, want := stand.body("InsertSysSettingRequest"),
		`{"valueTypeName":"ShortText","code":"UsrParityExample","description":"own setting","isCacheable":true,"isPersonal":false,"name":"Example"}`; got != want {
		t.Errorf("insert body = %s\nwant %s", got, want)
	}
	if got, want := stand.body("PostSysSettingsValues"), `{"isPersonal":false,"sysSettingsValues":{"UsrParityExample":"first"}}`; got != want {
		t.Errorf("value body = %s\nwant %s", got, want)
	}
	routes := strings.Join(stand.routes(), ",")
	if routes != "InsertSysSettingRequest,SelectQuery:SysSettings,PostSysSettingsValues,SelectQuery:SysSettings,SelectQuery:SysSettingsValue" {
		t.Errorf("routes = %s", routes)
	}
}

func TestCreateSysSettingAliasesAndLookupReference(t *testing.T) {
	stand, client := newAdminWriteFakeStand(t)
	stand.schemas["Contact"] = "16BE3651-8FE2-4159-8DD0-A803D4683DD3"
	result := client.CreateSysSetting(context.Background(), AdminWriteCreateSetting{Code: adminWriteString("UsrParityLookup"),
		Name: adminWriteString("Lookup"), ValueTypeName: adminWriteString("Lookup"), ReferenceSchemaName: adminWriteString("Contact"),
		IsCacheable: adminWriteFlag(false), IsPersonal: adminWriteFlag(true)})
	if !result.Success || result.Value != nil || result.ValueTypeName != "Lookup" {
		t.Fatalf("create = %#v", result)
	}
	if got := stand.body("InsertSysSettingRequest"); !strings.HasSuffix(got, `"isCacheable":false,"isPersonal":true,"name":"Lookup","referenceSchemaUId":"16be3651-8fe2-4159-8dd0-a803d4683dd3"}`) ||
		!strings.Contains(got, `"description":""`) {
		t.Errorf("insert body = %s", got)
	}
	stand, client = newAdminWriteFakeStand(t)
	result = client.CreateSysSetting(context.Background(), AdminWriteCreateSetting{Code: adminWriteString("UsrParityMoney"),
		Name: adminWriteString("Money"), ValueTypeName: adminWriteString("Currency")})
	if !result.Success || result.ValueTypeName != "Currency" || !strings.HasPrefix(stand.body("InsertSysSettingRequest"), `{"valueTypeName":"Money"`) {
		t.Fatalf("currency create = %#v, body %s", result, stand.body("InsertSysSettingRequest"))
	}
}

func TestCreateSysSettingRefusalsAndPartialSuccess(t *testing.T) {
	stand, client := newAdminWriteFakeStand(t)
	stand.insertAnswer = `{"success":false,"id":"00000000-0000-0000-0000-000000000000","responseStatus":{"ErrorCode":"x","Message":"Code UsrParityTaken already exists"}}`
	result := client.CreateSysSetting(context.Background(), AdminWriteCreateSetting{Code: adminWriteString("UsrParityTaken"),
		Name: adminWriteString("Taken"), ValueTypeName: adminWriteString("Text")})
	wantCause := "[untrusted-source-text begin] Code UsrParityTaken already exists [untrusted-source-text end]"
	if result.Success || result.ErrorCategory != "ProviderFailure" || result.Cause != wantCause || result.Error != "Failed creating sys-setting: "+wantCause {
		t.Fatalf("refused create = %#v", result)
	}

	stand, client = newAdminWriteFakeStand(t)
	result = client.CreateSysSetting(context.Background(), AdminWriteCreateSetting{Code: adminWriteString("UsrParityInt"),
		Name: adminWriteString("Int"), ValueTypeName: adminWriteString("Integer"), Value: adminWriteString("forty")})
	if !result.Success || result.Warning != "Sys-setting was created, but the initial value could not be applied." || result.Value != nil {
		t.Fatalf("partial create = %#v", result)
	}
	if strings.Contains(strings.Join(stand.routes(), ","), "PostSysSettingsValues") {
		t.Error("a value clio refuses locally must not be sent")
	}

	for _, c := range []struct {
		input AdminWriteCreateSetting
		want  string
	}{
		{AdminWriteCreateSetting{Name: adminWriteString("x"), ValueTypeName: adminWriteString("Text")}, "code is required."},
		{AdminWriteCreateSetting{Code: adminWriteString("x"), ValueTypeName: adminWriteString("Text")}, "name is required."},
		{AdminWriteCreateSetting{Code: adminWriteString("x"), Name: adminWriteString(" ")}, "name is required."},
		{AdminWriteCreateSetting{Code: adminWriteString("x"), Name: adminWriteString("y")}, "value-type-name is required."},
		{AdminWriteCreateSetting{Code: adminWriteString("x"), Name: adminWriteString("y"), ValueTypeName: adminWriteString("text")},
			"Unsupported value-type-name 'text'. Allowed values: Text, ShortText, MediumText, LongText, SecureText, MaxSizeText, Boolean, DateTime, Date, Time, Integer, Money, Float, Lookup, Currency, Decimal, Binary."},
		{AdminWriteCreateSetting{Code: adminWriteString("x"), Name: adminWriteString("y"), ValueTypeName: adminWriteString("Lookup")},
			"reference-schema-name is required when value-type-name is 'Lookup'."},
	} {
		stand, client = newAdminWriteFakeStand(t)
		result = client.CreateSysSetting(context.Background(), c.input)
		if result.Success || result.Error != c.want || result.Cause != c.want || result.ErrorCategory != "Validation" {
			t.Errorf("create %#v = %#v", c.input, result)
		}
		if len(stand.routes()) != 0 {
			t.Errorf("validation sent %v", stand.routes())
		}
	}
}

func TestUpdateSysSettingConvertsValuesByTheExistingType(t *testing.T) {
	stand, client := newAdminWriteFakeStand(t)
	stand.settings["UsrFlag"] = &adminWriteFakeSetting{id: "11111111-1111-1111-1111-111111111111", valueType: "Boolean"}
	stand.settings["UsrAmount"] = &adminWriteFakeSetting{id: "22222222-2222-2222-2222-222222222222", valueType: "Money"}
	stand.settings["UsrWhen"] = &adminWriteFakeSetting{id: "33333333-3333-3333-3333-333333333333", valueType: "Date"}
	for _, c := range []struct {
		code, value, body string
	}{
		{"UsrFlag", " TRUE ", `{"isPersonal":false,"sysSettingsValues":{"UsrFlag":true}}`},
		{"UsrAmount", "1,234.50", `{"isPersonal":false,"sysSettingsValues":{"UsrAmount":1234.50}}`},
		{"UsrWhen", "2026-10-10", `{"isPersonal":false,"sysSettingsValues":{"UsrWhen":"2026-10-10T00:00:00.000"}}`},
	} {
		stand.requests = nil
		result := client.UpdateSysSetting(context.Background(), AdminWriteUpdateSetting{Code: &c.code, Value: &c.value})
		if !result.Success || result.Value == nil {
			t.Fatalf("update %s = %#v", c.code, result)
		}
		if got := stand.body("PostSysSettingsValues"); got != c.body {
			t.Errorf("update %s body = %s\nwant %s", c.code, got, c.body)
		}
	}
}

func TestUpdateSysSettingRefusalsAndFailures(t *testing.T) {
	stand, client := newAdminWriteFakeStand(t)
	stand.settings["UsrCount"] = &adminWriteFakeSetting{id: "11111111-1111-1111-1111-111111111111", valueType: "Integer"}
	result := client.UpdateSysSetting(context.Background(), AdminWriteUpdateSetting{Code: adminWriteString("UsrCount"), Value: adminWriteString("1.5")})
	if result.Success || result.Error != adminWriteRefusedUpdateError || result.Cause != adminWriteRefusedUpdateCause || result.ErrorCategory != "ProviderFailure" {
		t.Fatalf("refused update = %#v", result)
	}

	stand.saveAnswer = `<html><body>gateway</body></html>`
	result = client.UpdateSysSetting(context.Background(), AdminWriteUpdateSetting{Code: adminWriteString("UsrCount"), Value: adminWriteString("2")})
	if result.ErrorCategory != "Network" || result.Error != "Creatio returned a non-JSON response updating sys-setting." {
		t.Fatalf("non-JSON update = %#v", result)
	}
	stand.saveAnswer = `[1]`
	result = client.UpdateSysSetting(context.Background(), AdminWriteUpdateSetting{Code: adminWriteString("UsrCount"), Value: adminWriteString("2")})
	if result.ErrorCategory != "Network" || result.Error != "Creatio returned a response of an unexpected shape updating sys-setting." {
		t.Fatalf("wrong-shape update = %#v", result)
	}

	stand.requests = nil
	for _, c := range []struct {
		input AdminWriteUpdateSetting
		want  string
	}{
		{AdminWriteUpdateSetting{Value: adminWriteString("x")}, "code is required."},
		{AdminWriteUpdateSetting{Code: adminWriteString("UsrCount")}, "value is required (supply 'value' or 'value-file-path')."},
		{AdminWriteUpdateSetting{Code: adminWriteString("UsrCount"), Value: adminWriteString("x"), ValueFilePath: adminWriteString("/tmp/x")},
			"Provide either 'value' or 'value-file-path', not both."},
	} {
		result = client.UpdateSysSetting(context.Background(), c.input)
		if result.Success || result.Error != c.want || result.ErrorCategory != "Validation" {
			t.Errorf("update %#v = %#v", c.input, result)
		}
	}
	if len(stand.routes()) != 0 {
		t.Errorf("validation sent %v", stand.routes())
	}
}

func TestUpdateSysSettingUploadsAFileUnderTheFilePolicy(t *testing.T) {
	directory := t.TempDir()
	png := filepath.Join(directory, "logo.png")
	if err := os.WriteFile(png, []byte{1, 2, 3}, 0o600); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(directory, "tool.exe")
	if err := os.WriteFile(exe, []byte{4}, 0o600); err != nil {
		t.Fatal(err)
	}
	stand, client := newAdminWriteFakeStand(t)
	stand.settings["UsrLogo"] = &adminWriteFakeSetting{id: "11111111-1111-1111-1111-111111111111", valueType: "Binary"}
	stand.settings["UsrText"] = &adminWriteFakeSetting{id: "22222222-2222-2222-2222-222222222222", valueType: "Text"}
	stand.settings["FileSecurityMode"] = &adminWriteFakeSetting{id: "44444444-4444-4444-4444-444444444444", valueType: "Lookup",
		value: map[string]any{"GuidValue": "60849C6E-24B4-45DF-9AAD-2F69D419823C"}}
	stand.settings["FileExtensionsDenyList"] = &adminWriteFakeSetting{id: "55555555-5555-5555-5555-555555555555", valueType: "MaxSizeText",
		value: map[string]any{"TextValue": ".exe, .bat"}}

	result := client.UpdateSysSetting(context.Background(), AdminWriteUpdateSetting{Code: adminWriteString("UsrLogo"), ValueFilePath: &png})
	if !result.Success {
		t.Fatalf("upload = %#v", result)
	}
	if got := stand.body("PostSysSettingsValues"); got != `{"isPersonal":false,"sysSettingsValues":{"UsrLogo":"AQID"}}` {
		t.Errorf("upload body = %s", got)
	}
	result = client.UpdateSysSetting(context.Background(), AdminWriteUpdateSetting{Code: adminWriteString("UsrLogo"), ValueFilePath: &exe})
	if result.Success || result.Error != "Cannot upload 'tool.exe': files with extension '.exe' are not allowed in this environment (DenyList file-security policy)." {
		t.Errorf("denied upload = %#v", result)
	}
	result = client.UpdateSysSetting(context.Background(), AdminWriteUpdateSetting{Code: adminWriteString("UsrLogo"), Value: adminWriteString("AQID")})
	if result.Success || !strings.HasPrefix(result.Error, "Sys-setting 'UsrLogo' is Binary and this environment has an active file-security policy.") {
		t.Errorf("inline binary = %#v", result)
	}
	result = client.UpdateSysSetting(context.Background(), AdminWriteUpdateSetting{Code: adminWriteString("UsrText"), ValueFilePath: &png})
	if result.Success || result.Error != "Cannot upload a file to sys-setting 'UsrText': it is type 'Text', not Binary. A file value can only be written to a Binary setting." {
		t.Errorf("file on text = %#v", result)
	}
	result = client.UpdateSysSetting(context.Background(), AdminWriteUpdateSetting{Code: adminWriteString("UsrMissing"), ValueFilePath: &png})
	if result.Success || !strings.HasPrefix(result.Error, "Sys-setting 'UsrMissing' was not found or is not readable by the current user.") {
		t.Errorf("file on missing = %#v", result)
	}
}

func TestDownloadSysSettingFileSavesDecodedBytesOnce(t *testing.T) {
	stand, client := newAdminWriteFakeStand(t)
	stand.packages = []map[string]any{{"Name": "cliogate", "UId": "", "Maintainer": "", "Version": "2.0.0.53"}}
	stand.settings["UsrLogo"] = &adminWriteFakeSetting{id: "11111111-1111-1111-1111-111111111111", valueType: "Binary"}
	stand.settings["UsrText"] = &adminWriteFakeSetting{id: "22222222-2222-2222-2222-222222222222", valueType: "Text"}
	stand.queryAnswer = `{"success":true,"values":{"UsrLogo":{"value":"AQID","dataValueType":1}}}`
	destination := filepath.Join(t.TempDir(), "logo.png")
	result, err := client.DownloadSysSettingFile(context.Background(), "UsrLogo", destination)
	if err != nil || result.ExitCode != 0 || *result.ByteCount != 3 || *result.FileName != destination {
		t.Fatalf("download = %#v, %v", result, err)
	}
	if data, _ := os.ReadFile(destination); string(data) != "\x01\x02\x03" {
		t.Errorf("file = %v", data)
	}
	if _, err := client.DownloadSysSettingFile(context.Background(), "UsrLogo", destination); err == nil || err.Error() != "Destination already exists; choose a new filename." {
		t.Errorf("second download = %v", err)
	}
	if _, err := client.DownloadSysSettingFile(context.Background(), "UsrText", destination+"2"); err == nil || err.Error() != "Only Binary system settings can be downloaded as files." {
		t.Errorf("text download = %v", err)
	}
	if _, err := client.DownloadSysSettingFile(context.Background(), "UsrNone", destination+"3"); err == nil || err.Error() != "System setting does not exist or is not readable." {
		t.Errorf("missing download = %v", err)
	}
	stand.packages = []map[string]any{}
	_, err = client.DownloadSysSettingFile(context.Background(), "UsrLogo", destination+"4")
	if err == nil || err.Error() != "To use this command, you need to install the cliogate package. Install the package in the target environment and retry.\nRun 'clio install-gate -e <environment>' first." {
		t.Errorf("no cliogate = %v", err)
	}
	entries, _ := os.ReadDir(filepath.Dir(destination))
	if len(entries) != 1 {
		t.Errorf("directory holds %d entries; the temporary file must be removed", len(entries))
	}
}

func TestDotnetNumberParsers(t *testing.T) {
	for text, want := range map[string]string{"1,234.50": "1234.50", " -0.5 ": "-0.5", "007": "7", ".5": "0.5", "5-": "-5", "+3": "3"} {
		if got, ok := dotnetParseDecimal(text); !ok || got != want {
			t.Errorf("decimal %q = %q, %v; want %q", text, got, ok, want)
		}
	}
	for _, text := range []string{"", "abc", "1.2.3", "1e5", "--1"} {
		if got, ok := dotnetParseDecimal(text); ok {
			t.Errorf("decimal %q accepted as %q", text, got)
		}
	}
	if got, ok := dotnetParseInt32(" 42 "); !ok || got != 42 {
		t.Errorf("int = %d, %v", got, ok)
	}
	if _, ok := dotnetParseInt32("4.0"); ok {
		t.Error("int accepted 4.0")
	}
}
