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

// sysSettingToolServer answers SysSettings and SysSettingsValue SelectQueries and records each request body.
func sysSettingToolServer(t *testing.T, settings, values string, requests *[]map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/Login") {
			_, _ = w.Write([]byte(`{"Code":0}`))
			return
		}
		if r.URL.Path != "/0/DataService/json/SyncReply/SelectQuery" {
			t.Errorf("unexpected route %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var query map[string]any
		if err := json.Unmarshal(body, &query); err != nil {
			t.Errorf("decode SelectQuery: %v", err)
		}
		*requests = append(*requests, query)
		if query["rootSchemaName"] == "SysSettings" {
			_, _ = w.Write([]byte(settings))
			return
		}
		_, _ = w.Write([]byte(values))
	}))
}

func sysSettingToolFilterValue(t *testing.T, query map[string]any, name string) any {
	t.Helper()
	items := query["filters"].(map[string]any)["items"].(map[string]any)
	filter, ok := items[name].(map[string]any)
	if !ok {
		t.Fatalf("filter %q missing in %#v", name, items)
	}
	return filter["rightExpression"].(map[string]any)["parameter"].(map[string]any)["value"]
}

func TestGetSysSettingReadsAllUsersValueByCode(t *testing.T) {
	var requests []map[string]any
	server := sysSettingToolServer(t,
		`{"success":true,"rows":[{"Id":"setting-1","ValueTypeName":"DateTime"}]}`,
		`{"success":true,"rows":[{"TextValue":"","IntegerValue":0,"FloatValue":0.00,"BooleanValue":false,"DateTimeValue":"2013-12-15T16:42:51.940","GuidValue":""}]}`,
		&requests)
	defer server.Close()
	result := newFormsTestClient(t, server.URL).GetSysSetting(context.Background(), "LastSync")
	if !result.Success || result.Code != "LastSync" || result.Value != "2013-12-15T16:42:51.9400000" {
		t.Fatalf("result = %#v", result)
	}
	if len(requests) != 2 || sysSettingToolFilterValue(t, requests[0], "Code") != "LastSync" {
		t.Fatalf("requests = %#v", requests)
	}
	if sysSettingToolFilterValue(t, requests[1], "SysSettings") != "setting-1" || sysSettingToolFilterValue(t, requests[1], "SysAdminUnit") != sysSettingToolAllEmployeesID {
		t.Fatalf("value query = %#v", requests[1])
	}
}

func TestGetSysSettingUnknownCodeIsEmptySuccessAndSecureTextIsMasked(t *testing.T) {
	var requests []map[string]any
	server := sysSettingToolServer(t, `{"success":true,"rows":[]}`, `{"success":true,"rows":[]}`, &requests)
	defer server.Close()
	client := newFormsTestClient(t, server.URL)
	if result := client.GetSysSetting(context.Background(), "ZzNoSuch"); !result.Success || result.Value != "" || len(requests) != 1 {
		t.Fatalf("unknown code = %#v after %d requests", result, len(requests))
	}

	secret := sysSettingToolServer(t, `{"success":true,"rows":[{"Id":"s","ValueTypeName":"SecureText"}]}`,
		`{"success":true,"rows":[{"TextValue":"cipher"}]}`, &requests)
	defer secret.Close()
	if result := newFormsTestClient(t, secret.URL).GetSysSetting(context.Background(), "Secret"); result.Value != "***" {
		t.Fatalf("SecureText = %#v", result)
	}
}

func TestGetSysSettingRefusesBlankCodeWithoutCallingCreatio(t *testing.T) {
	client, _ := NewClient(Config{BaseURL: "http://127.0.0.1:1", Login: "u", Password: "p"})
	result := client.GetSysSetting(context.Background(), " ")
	if result.Success || result.Error != "code is required." || result.ErrorCategory != "Validation" || result.RecoveryAction == "" {
		t.Fatalf("result = %#v", result)
	}
}

func TestGetSysSettingClassifiesProviderAndNetworkFailures(t *testing.T) {
	var requests []map[string]any
	server := sysSettingToolServer(t, `{"success":false,"errorInfo":{"message":"Access denied"}}`, `{}`, &requests)
	defer server.Close()
	result := newFormsTestClient(t, server.URL).GetSysSetting(context.Background(), "Code")
	if result.Success || result.ErrorCategory != "ProviderFailure" || result.Error != "Access denied" || result.Cause != "Access denied" {
		t.Fatalf("provider failure = %#v", result)
	}

	unreachable, _ := NewClient(Config{BaseURL: "http://127.0.0.1:1", Login: "u", Password: "p"})
	listed := unreachable.ListSysSettings(context.Background())
	if listed.Success || listed.Settings == nil || len(listed.Settings) != 0 {
		t.Fatalf("list failure must keep settings as [] = %#v", listed)
	}
	if listed.ErrorCategory != "Authentication" && listed.ErrorCategory != "Network" {
		t.Fatalf("unreachable environment category = %q (%s)", listed.ErrorCategory, listed.Error)
	}
}

func TestListSysSettingsFormatsEachTypeLikeClio(t *testing.T) {
	var requests []map[string]any
	server := sysSettingToolServer(t, `{"success":true,"rows":[
		{"Id":"A","Code":"Bool","Name":"n","ValueTypeName":"Boolean","IsCacheable":true,"IsPersonal":false},
		{"Id":"b","Code":"Float","Name":"n","ValueTypeName":"Float","IsCacheable":false,"IsPersonal":true},
		{"Id":"c","Code":"Lookup","Name":"n","ValueTypeName":"Lookup"},
		{"Id":"d","Code":"Logo","Name":"n","ValueTypeName":"Binary"},
		{"Id":"e","Code":"NoValue","Name":"n","ValueTypeName":"Integer"},
		{"Id":"f","Code":"Secret","Name":"n","ValueTypeName":"SecureText"},
		{"Id":"g","Code":"Image","Name":"n","ValueTypeName":"Image"},
		{"Id":"h","Code":"Time","Name":"n","ValueTypeName":"Time"},
		{"Id":"i","Code":"Text","Name":"n","ValueTypeName":"ShortText"}]}`,
		`{"success":true,"rows":[
		{"SettingId":"a","BooleanValue":true},
		{"SettingId":"b","FloatValue":0.40},
		{"SettingId":"c","GuidValue":""},
		{"SettingId":"f","TextValue":""},
		{"SettingId":"g","TextValue":"x"},
		{"SettingId":"h","DateTimeValue":"2000-01-01T04:30:00.000"},
		{"SettingId":"i","TextValue":"#fff"}]}`, &requests)
	defer server.Close()
	result := newFormsTestClient(t, server.URL).ListSysSettings(context.Background())
	if !result.Success {
		t.Fatalf("result = %#v", result)
	}
	want := map[string]string{"Bool": "true", "Float": "0.4", "Lookup": sysSettingToolEmptyGUID, "Logo": "<binary>", "NoValue": "undefined",
		"Secret": "", "Image": "undefined", "Time": "04:30:00", "Text": "#fff"}
	order := []string{"Bool", "Float", "Lookup", "Logo", "NoValue", "Secret", "Image", "Time", "Text"}
	for i, item := range result.Settings {
		if item.Code != order[i] || item.Value != want[item.Code] {
			t.Errorf("setting %d = %#v, want code %s value %q", i, item, order[i], want[order[i]])
		}
	}
	if !result.Settings[0].IsCacheable || !result.Settings[1].IsPersonal {
		t.Fatalf("flags = %#v", result.Settings[:2])
	}
	values := requests[1]
	if values["rootSchemaName"] != "SysSettingsValue" || sysSettingToolFilterValue(t, values, "SysAdminUnit") != sysSettingToolAllEmployeesID {
		t.Fatalf("value query = %#v", values)
	}
}

func TestTrimDecimalKeepsDigitsCreatioSent(t *testing.T) {
	for input, want := range map[string]string{"2.00": "2", "100.00": "100", "0.40": "0.4", "0.00": "0", "12": "12", "1e2": "100"} {
		if got := sysSettingToolDecimal(input); got != want {
			t.Errorf("sysSettingToolDecimal(%q) = %q, want %q", input, got, want)
		}
	}
}
