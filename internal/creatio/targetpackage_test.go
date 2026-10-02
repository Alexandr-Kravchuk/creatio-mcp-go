package creatio

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

const targetPackageRows = `{"success":true,"rows":[
	{"Name":"Custom","UId":"a00051f4-cde3-4f3f-b08e-c5ad1a5c735a","InstallType":0},
	{"Name":"CrtBase","UId":"b00051f4-cde3-4f3f-b08e-c5ad1a5c735a","InstallType":1}]}`

func TestGetTargetPackageMatchesNamedPackageCaseInsensitively(t *testing.T) {
	server := groupCServer(t, func(path string, body map[string]any) (int, string) {
		if path != "/0/DataService/json/SyncReply/SelectQuery" || body["rootSchemaName"] != "SysPackage" || body["rowCount"] != float64(10000) {
			t.Errorf("unexpected request %s %#v", path, body)
		}
		return http.StatusOK, targetPackageRows
	})
	defer server.Close()
	client := newFormsTestClient(t, server.URL)
	result := client.GetTargetPackage(context.Background(), " custom ")
	if !result.Success || result.PackageName != "Custom" || result.ResolutionFailed != nil {
		t.Fatalf("named result = %#v", result)
	}
	locked := client.GetTargetPackage(context.Background(), "CrtBase")
	if locked.Success || locked.ResolutionFailed == nil || !*locked.ResolutionFailed || !strings.Contains(locked.Error, "is locked") {
		t.Fatalf("locked result = %#v", locked)
	}
	missing := client.GetTargetPackage(context.Background(), "ZzNope")
	if missing.Success || !*missing.ResolutionFailed || missing.Error != "Package 'ZzNope' was not found in the environment. Check the name against list-packages." {
		t.Fatalf("missing result = %#v", missing)
	}
}

func TestGetTargetPackageResolvesCurrentPackageSetting(t *testing.T) {
	var filtered bool
	server := groupCServer(t, func(path string, body map[string]any) (int, string) {
		switch path {
		case "/0/DataService/json/SyncReply/QuerySysSettings":
			return http.StatusOK, `{"success":true,"values":{"CurrentPackageId":{"value":"a00051f4-cde3-4f3f-b08e-c5ad1a5c735a","displayValue":"Custom"}}}`
		case "/0/DataService/json/SyncReply/SelectQuery":
			items := body["filters"].(map[string]any)["items"].(map[string]any)
			filtered = items["Id"] != nil
			return http.StatusOK, `{"success":true,"rows":[{"Name":"Custom","UId":"a00051f4-cde3-4f3f-b08e-c5ad1a5c735a","InstallType":0}]}`
		}
		return http.StatusNotFound, ""
	})
	defer server.Close()
	result := newFormsTestClient(t, server.URL).GetTargetPackage(context.Background(), "")
	if !result.Success || result.PackageName != "Custom" || !filtered {
		t.Fatalf("result = %#v, filtered by Id = %v", result, filtered)
	}
}

func TestGetTargetPackageReportsUnreachableEnvironmentAsNotDefinitive(t *testing.T) {
	server := groupCServer(t, func(path string, body map[string]any) (int, string) {
		return http.StatusInternalServerError, `{}`
	})
	defer server.Close()
	result := newFormsTestClient(t, server.URL).GetTargetPackage(context.Background(), "Custom")
	if result.Success || result.ResolutionFailed == nil || *result.ResolutionFailed || !strings.HasPrefix(result.Error, "The environment could not be asked") {
		t.Fatalf("result = %#v", result)
	}
}

func TestParseGUIDAcceptsDotNetForms(t *testing.T) {
	for _, value := range []string{"A00051F4-CDE3-4F3F-B08E-C5AD1A5C735A", "{a00051f4-cde3-4f3f-b08e-c5ad1a5c735a}", "a00051f4cde34f3fb08ec5ad1a5c735a"} {
		if parsed, ok := parseGUID(value); !ok || parsed != "a00051f4-cde3-4f3f-b08e-c5ad1a5c735a" {
			t.Fatalf("parseGUID(%q) = %q, %v", value, parsed, ok)
		}
	}
	if _, ok := parseGUID("not-a-guid"); ok {
		t.Fatal("parseGUID accepted text")
	}
}
