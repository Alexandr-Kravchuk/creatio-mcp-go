package creatio

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestDataWriteSQLGatewayJSONExport(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ServiceModel/AuthService.svc/Login" {
			io.WriteString(w, `{"Code":0}`)
			return
		}
		requests++
		if r.Method != "POST" || r.URL.Path != "/0/rest/CreatioApiGateway/ExecuteSqlScript" {
			t.Errorf("request %s %s", r.Method, r.URL.Path)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["script"] != "SELECT Id FROM Contact WHERE Name = 'UsrParityOwn'" {
			t.Errorf("body %#v", body)
		}
		io.WriteString(w, `[{"Id":"own"}]`)
	}))
	defer server.Close()
	client, _ := NewClient(Config{BaseURL: server.URL, Login: "example-user", Password: "replace-me"})
	path := filepath.Join(t.TempDir(), "rows.json")
	got := client.DataWriteSQL(context.Background(), "SELECT Id FROM Contact WHERE Name = 'UsrParityOwn'", "", "json", path, true)
	if got.ExitCode != 0 || requests != 1 || len(got.Messages) != 2 {
		t.Fatalf("got=%#v requests=%d", got, requests)
	}
}

func TestDataWriteSQLGatewayError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ServiceModel/AuthService.svc/Login" {
			io.WriteString(w, `{"Code":0}`)
			return
		}
		io.WriteString(w, `"ExecuteSQL ERROR: denied"`)
	}))
	defer server.Close()
	client, _ := NewClient(Config{BaseURL: server.URL, Login: "example-user", Password: "replace-me"})
	got := client.DataWriteSQL(context.Background(), "SELECT 1", "", "json", "", false)
	if got.ExitCode != 1 || got.Messages[0].Value != "denied" {
		t.Fatalf("got=%#v", got)
	}
}
