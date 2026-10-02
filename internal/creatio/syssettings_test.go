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

// groupCServer answers the login route and hands every other request to handle with its decoded JSON body.
func groupCServer(t *testing.T, handle func(path string, body map[string]any) (int, string)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ServiceModel/AuthService.svc/Login" {
			http.SetCookie(w, &http.Cookie{Name: "BPMCSRF", Value: "csrf-token", Path: "/"})
			_, _ = w.Write([]byte(`{"Code":0}`))
			return
		}
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		status, response := handle(r.URL.Path, body)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(response))
	}))
}

func TestGetSchemaNamePrefixTrimsQuotedValue(t *testing.T) {
	var requested any
	server := groupCServer(t, func(path string, body map[string]any) (int, string) {
		if path != "/0/DataService/json/SyncReply/QuerySysSettings" {
			t.Errorf("unexpected route %s", path)
			return http.StatusNotFound, ""
		}
		requested = body["sysSettingsNameCollection"]
		return http.StatusOK, `{"success":true,"values":{"SchemaNamePrefix":"\" Usr \""}}`
	})
	defer server.Close()
	result := newFormsTestClient(t, server.URL).GetSchemaNamePrefix(context.Background())
	if !result.Success || result.SchemaNamePrefix != "Usr" {
		t.Fatalf("result = %#v", result)
	}
	if list, ok := requested.([]any); !ok || len(list) != 1 || list[0] != "SchemaNamePrefix" {
		t.Fatalf("requested codes = %#v", requested)
	}
}

func TestGetSchemaNamePrefixFallsBackToSettingValueRow(t *testing.T) {
	server := groupCServer(t, func(path string, body map[string]any) (int, string) {
		switch path {
		case "/0/DataService/json/SyncReply/QuerySysSettings":
			return http.StatusOK, `{"success":true,"values":{}}`
		case "/0/DataService/json/SyncReply/SelectQuery":
			if body["rootSchemaName"] != "SysSettingsValue" {
				t.Errorf("fallback root = %v", body["rootSchemaName"])
			}
			return http.StatusOK, `{"success":true,"rows":[{"TextValue":"Abc","GuidValue":null,"ValueTypeName":"Text"}]}`
		}
		return http.StatusNotFound, ""
	})
	defer server.Close()
	result := newFormsTestClient(t, server.URL).GetSchemaNamePrefix(context.Background())
	if !result.Success || result.SchemaNamePrefix != "Abc" {
		t.Fatalf("result = %#v", result)
	}
}

func TestGetSchemaNamePrefixReportsGenericFailure(t *testing.T) {
	server := groupCServer(t, func(path string, body map[string]any) (int, string) {
		return http.StatusInternalServerError, `{}`
	})
	defer server.Close()
	result := newFormsTestClient(t, server.URL).GetSchemaNamePrefix(context.Background())
	if result.Success || result.Error != genericSchemaNamePrefixRead {
		t.Fatalf("result = %#v", result)
	}
	if !strings.Contains(mustJSON(t, result), `"schema-name-prefix":""`) {
		t.Fatalf("failure must keep an empty prefix: %s", mustJSON(t, result))
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}
