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

func TestODataCreateNeverReplaysRejectedWrite(t *testing.T) {
	writes := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ServiceModel/AuthService.svc/Login":
			io.WriteString(w, `{"Code":0}`)
		case "/0/odata/UsrParityMock":
			writes++
			if r.Method != "POST" {
				t.Errorf("method %s", r.Method)
			}
			var row map[string]any
			json.NewDecoder(r.Body).Decode(&row)
			if row["Name"] != "test" {
				t.Errorf("row %#v", row)
			}
			w.WriteHeader(401)
			io.WriteString(w, `{"error":{"message":"expired"}}`)
		default:
			t.Errorf("unexpected %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client, _ := NewClient(Config{BaseURL: server.URL, Login: "example-user", Password: "replace-me"})
	result := ODataCreate(context.Background(), client, nil, ODataCreateRequest{Entity: "UsrParityMock", Rows: json.RawMessage(`[{"Name":"test"}]`)})
	if writes != 1 || result.Created != 0 || result.Unverified != 1 || result.Results[0].RecordCreated != nil {
		t.Fatalf("writes=%d result=%#v", writes, result)
	}
	if result.Results[0].Diagnostic.SideEffect != "unknown" {
		t.Fatalf("diagnostic %#v", result.Results[0].Diagnostic)
	}
}

func TestODataCreateRowsAndStopOnError(t *testing.T) {
	writes := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/Login") {
			io.WriteString(w, `{"Code":0}`)
			return
		}
		writes++
		io.WriteString(w, `{"Id":42}`)
	}))
	defer server.Close()
	client, _ := NewClient(Config{BaseURL: server.URL, Login: "example-user", Password: "replace-me"})
	result := ODataCreate(context.Background(), client, nil, ODataCreateRequest{Entity: "UsrParityMock", Rows: json.RawMessage(`[{"Name":"test"},{}, {"Name":"unused"}]`), StopOnError: true})
	if writes != 1 || result.Created != 1 || result.Failed != 1 || len(result.Results) != 2 || *result.Results[0].ID != "42" || *result.Results[1].RecordCreated {
		t.Fatalf("writes=%d result=%#v", writes, result)
	}
}

func TestODataUpdateMetadataGuardAndDeleteConfirmation(t *testing.T) {
	writes := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/Login"):
			io.WriteString(w, `{"Code":0}`)
		case strings.HasSuffix(r.URL.Path, "/$metadata"):
			io.WriteString(w, `<Schema><EntityType Name="UsrParityMock"><Property Name="Id" Type="Edm.Guid"/><Property Name="Name" Type="Edm.String"/></EntityType></Schema>`)
		default:
			writes++
			if r.Method != "PATCH" && r.Method != "DELETE" {
				t.Errorf("method %s", r.Method)
			}
			w.WriteHeader(204)
		}
	}))
	defer server.Close()
	client, _ := NewClient(Config{BaseURL: server.URL, Login: "example-user", Password: "replace-me"})
	request := ODataKeyedRequest{Entity: "UsrParityMock", ID: "00000000-0000-0000-0000-000000000001", Data: json.RawMessage(`{"Typo":"bad"}`), Confirm: true}
	refusal := ODataUpdate(context.Background(), client, nil, request)
	if refusal.Success || writes != 0 || !strings.Contains(*refusal.Error, "Typo") {
		t.Fatalf("refusal=%#v error=%s writes=%d", refusal, *refusal.Error, writes)
	}
	request.Data = json.RawMessage(`{"Name":"good"}`)
	result := ODataUpdate(context.Background(), client, nil, request)
	if !result.Success || writes != 1 {
		t.Fatalf("result=%#v writes=%d", result, writes)
	}
	request.Confirm = false
	refusal = ODataDelete(context.Background(), client, nil, request)
	if refusal.Success || writes != 1 {
		t.Fatalf("refusal=%#v error=%s writes=%d", refusal, *refusal.Error, writes)
	}
	request.Confirm = true
	result = ODataDelete(context.Background(), client, nil, request)
	if !result.Success || writes != 2 {
		t.Fatalf("result=%#v writes=%d", result, writes)
	}
}

func TestODataCreateNeverFollowsWriteRedirect(t *testing.T) {
	writes := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/Login") {
			io.WriteString(w, `{"Code":0}`)
			return
		}
		writes++
		w.Header().Set("Location", "/0/odata/UsrParitySecond")
		w.WriteHeader(http.StatusTemporaryRedirect)
		io.WriteString(w, `{"error":{"message":"redirected"}}`)
	}))
	defer server.Close()
	client, _ := NewClient(Config{BaseURL: server.URL, Login: "example-user", Password: "replace-me"})
	result := ODataCreate(context.Background(), client, nil, ODataCreateRequest{Entity: "UsrParityMock", Rows: json.RawMessage(`[{"Name":"test"}]`)})
	if writes != 1 || result.Unverified != 1 {
		t.Fatalf("writes=%d result=%#v", writes, result)
	}
}
