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

func TestGetRecordRightsPostsTableAndRendersGrants(t *testing.T) {
	var request map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ServiceModel/AuthService.svc/Login":
			_, _ = w.Write([]byte(`{"Code":0}`))
		case "/0/rest/RightsService/GetRecordRights":
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &request)
			_, _ = w.Write([]byte(`{"GetRecordRightsResult":[` +
				`{"Id":"r1","Operation":0,"RightLevel":2,"SysAdminUnit":{"value":"u1","displayValue":"All employees"}},` +
				`{"Id":"r2","Operation":5,"RightLevel":1,"SysAdminUnit":null}]}`))
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	result := newFormsTestClient(t, server.URL).GetRecordRights(context.Background(), "Contact", "id-1")
	if request["tableName"] != "SysContactRight" || request["recordId"] != "id-1" {
		t.Fatalf("request = %#v", request)
	}
	want := "Record rights for Contact 'id-1':\n  read / delegated -> All employees (u1) [id: r1]\n  5 / granted -> (unknown) ((unknown)) [id: r2]"
	if !result.Success || result.Output != want {
		t.Fatalf("result = %#v", result)
	}
	if recordRightsTableName("SysSchemaAdminUnit") != "SysSchemaAdminUnitRight" {
		t.Fatal("Sys-prefixed table name")
	}
}

func TestGetRecordRightsReportsServiceFailureInEnvelope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/Login") {
			_, _ = w.Write([]byte(`{"Code":0}`))
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`<html>Request Error</html>`))
	}))
	defer server.Close()
	result := newFormsTestClient(t, server.URL).GetRecordRights(context.Background(), "ZzNoEntity", "id-1")
	if result.Success || !strings.HasPrefix(result.Error, "Error: ") || result.Output != "" {
		t.Fatalf("result = %#v", result)
	}
}
