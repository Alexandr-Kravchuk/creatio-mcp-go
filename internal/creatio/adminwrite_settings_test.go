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

func TestAdminWriteCreateSysSettingUsesNativeWriteAndReadback(t *testing.T) {
	var routes []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/Login") {
			_, _ = w.Write([]byte(`{"Code":0}`))
			return
		}
		routes = append(routes, r.URL.Path)
		body, _ := io.ReadAll(r.Body)
		var request map[string]any
		if err := json.Unmarshal(body, &request); err != nil {
			t.Errorf("invalid JSON: %v", err)
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/InsertSysSettingRequest"):
			for key, expected := range map[string]any{"code": "UsrParityExample", "name": "Example", "valueTypeName": "ShortText", "description": "own setting", "isCacheable": true, "isPersonal": false} {
				if request[key] != expected {
					t.Errorf("insert %s = %v, want %v", key, request[key], expected)
				}
			}
			if _, ok := request["value"]; ok {
				t.Error("insert must not set value before PostSysSettingsValues")
			}
			_, _ = w.Write([]byte(`{"success":true,"id":"3c50b1a3-7aae-49f6-8921-92fb3f5adc40"}`))
		case strings.HasSuffix(r.URL.Path, "/PostSysSettingsValues"):
			if request["isPersonal"] != false || request["sysSettingsValues"].(map[string]any)["UsrParityExample"] != "first" {
				t.Errorf("post body = %s", body)
			}
			_, _ = w.Write([]byte(`{"saveResult":{"UsrParityExample":true}}`))
		case strings.HasSuffix(r.URL.Path, "/SelectQuery"):
			if request["rootSchemaName"] == "SysSettings" {
				_, _ = w.Write([]byte(`{"success":true,"rows":[{"Id":"3c50b1a3-7aae-49f6-8921-92fb3f5adc40","ValueTypeName":"ShortText"}]}`))
			} else if request["rootSchemaName"] == "SysSettingsValue" {
				_, _ = w.Write([]byte(`{"success":true,"rows":[{"TextValue":"first"}]}`))
			} else {
				t.Errorf("unexpected query: %s", body)
			}
		default:
			t.Errorf("unexpected route: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	client := newFormsTestClient(t, server.URL)
	value := "first"
	result := client.AdminWriteCreateSysSetting(context.Background(), AdminWriteCreateSetting{Code: "UsrParityExample", Name: "Example", ValueTypeName: "ShortText", Description: "own setting", Value: &value})
	if !result.Success || result.Value == nil || *result.Value != value {
		t.Fatalf("create = %#v", result)
	}
	if len(routes) != 5 || !strings.HasSuffix(routes[0], "/InsertSysSettingRequest") || !strings.HasSuffix(routes[2], "/PostSysSettingsValues") {
		t.Fatalf("routes = %#v", routes)
	}
}

func TestAdminWriteSettingsRefuseInvalidInputBeforeWrite(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++; http.Error(w, "unexpected", 500) }))
	defer server.Close()
	client, _ := NewClient(Config{BaseURL: server.URL, Login: "u", Password: "p"})
	create := client.AdminWriteCreateSysSetting(context.Background(), AdminWriteCreateSetting{Code: "Example", Name: "Example", ValueTypeName: "Wrong"})
	if create.Success || create.ErrorCategory != "Validation" {
		t.Fatalf("create = %#v", create)
	}
	update := client.AdminWriteUpdateSysSetting(context.Background(), AdminWriteUpdateSetting{Code: "Example"})
	if update.Success || update.ErrorCategory != "Validation" {
		t.Fatalf("update = %#v", update)
	}
	if requests != 0 {
		t.Fatalf("made %d remote requests", requests)
	}
}

func TestAdminWriteCreateSysSettingProviderFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/Login") {
			_, _ = w.Write([]byte(`{"Code":0}`))
			return
		}
		_, _ = w.Write([]byte(`{"success":false,"responseStatus":{"Message":"rejected"}}`))
	}))
	defer server.Close()
	result := newFormsTestClient(t, server.URL).AdminWriteCreateSysSetting(context.Background(), AdminWriteCreateSetting{Code: "UsrParityExample", Name: "Example", ValueTypeName: "ShortText"})
	if result.Success || result.Error == "" {
		t.Fatalf("create = %#v", result)
	}
}
