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

func TestPackageDependencyReadModifySavePreservesExtensionFields(t *testing.T) {
	saved := []map[string]any{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		switch {
		case strings.HasSuffix(r.URL.Path, "/Login"):
			io.WriteString(w, `{"Code":0}`)
		case strings.HasSuffix(r.URL.Path, "/SelectQuery"):
			io.WriteString(w, `{"success":true,"rows":[{"Name":"UsrParity","UId":"11111111-1111-4111-8111-111111111111","Version":"1.0"},{"Name":"Base","UId":"22222222-2222-4222-8222-222222222222","Version":"2.0"}]}`)
		case strings.HasSuffix(r.URL.Path, "/GetPackageProperties"):
			if string(body) != `"11111111-1111-4111-8111-111111111111"` {
				t.Errorf("get body=%s", body)
			}
			io.WriteString(w, `{"success":true,"package":{"uId":"11111111-1111-4111-8111-111111111111","name":"UsrParity","version":"1.0","customProperty":{"keep":true},"dependsOnPackages":[]}}`)
		case strings.HasSuffix(r.URL.Path, "/SavePackageProperties"):
			var dto map[string]any
			if err := json.Unmarshal(body, &dto); err != nil {
				t.Error(err)
			}
			saved = append(saved, dto)
			io.WriteString(w, `{"success":true,"compilationRequired":true}`)
		default:
			t.Errorf("unexpected route=%s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := newFormsTestClient(t, server.URL)
	result := client.AddPackageDependencies(context.Background(), "usrparity", []string{"Base:3.0", "Base"})
	if result.ExitCode != 0 || len(saved) != 1 {
		t.Fatalf("result=%#v saved=%v", result, saved)
	}
	if saved[0]["customProperty"] == nil {
		t.Fatal("extension data lost")
	}
	deps := saved[0]["dependsOnPackages"].([]any)
	if len(deps) != 1 || deps[0].(map[string]any)["version"] != "3.0" {
		t.Fatalf("deps=%v", deps)
	}
	before := len(saved)
	result = client.RemovePackageDependencies(context.Background(), "UsrParity", []string{"Missing"})
	if result.ExitCode != 0 || len(saved) != before {
		t.Fatalf("missing removal wrote: %#v", result)
	}
}

func TestPackageHotfixUsesTargetGUIDAndRejectsMissingName(t *testing.T) {
	route := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/Login"):
			io.WriteString(w, `{"Code":0}`)
		case strings.HasSuffix(r.URL.Path, "/SelectQuery"):
			io.WriteString(w, `{"success":true,"rows":[{"Name":"UsrParity","UId":"11111111-1111-4111-8111-111111111111"}]}`)
		default:
			route = r.URL.Path
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), `"uId": "11111111-1111-4111-8111-111111111111"`) {
				t.Errorf("body=%s", body)
			}
			io.WriteString(w, `{"success":true}`)
		}
	}))
	defer server.Close()
	client := newFormsTestClient(t, server.URL)
	if got := client.SetPackageHotfix(context.Background(), "UsrParity", true); got.ExitCode != 0 || !strings.HasSuffix(route, "/StartPackageHotfix") {
		t.Fatalf("result=%#v route=%s", got, route)
	}
	if got := client.SetPackageHotfix(context.Background(), "", true); got.ExitCode != -1 || !strings.Contains(got.Messages[1].Value, "ArgumentNullException") {
		t.Fatalf("result=%#v", got)
	}
}
