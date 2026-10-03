package creatio

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDataWriteRunProcessShapeAndResult(t *testing.T) {
	launches := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ServiceModel/AuthService.svc/Login":
			io.WriteString(w, `{"Code":0}`)
		case "/0/DataService/json/SyncReply/SelectQuery":
			io.WriteString(w, `{"success":true,"rows":[{"Id":"aaaaaaaa-0000-0000-0000-000000000001","Name":"UsrParityProcess","Caption":"Own process","VersionParentUId":"aaaaaaaa-0000-0000-0000-000000000001","IsActiveVersion":true}]}`)
		case "/0/DataService/json/SyncReply/ProcessSchemaRequest":
			metadata, _ := json.Marshal(`{"metaData":{"schema":{"parameters":[{"name":"Input","dataValueType":"325a73b8-0f47-44a0-8412-7606f78003ac","direction":0},{"name":"Output","dataValueType":"325a73b8-0f47-44a0-8412-7606f78003ac","direction":1}]}}}`)
			io.WriteString(w, `{"success":true,"schema":{"metaData":`+string(metadata)+`}}`)
		case "/0/ServiceModel/ProcessEngineService.svc/RunProcess":
			launches++
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			if body["schemaName"] != "UsrParityProcess" || body["parameterValues"].([]any)[0].(map[string]any)["value"] != "hello" || body["resultParameterNames"].([]any)[0] != "Output" {
				t.Errorf("request %#v", body)
			}
			io.WriteString(w, `{"success":true,"processId":"bbbbbbbb-0000-0000-0000-000000000001","processStatus":2,"resultParameterValues":{"Output":"done"}}`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	client, _ := NewClient(Config{BaseURL: server.URL, Login: "example-user", Password: "replace-me"})
	result := client.DataWriteRunProcess(context.Background(), "UsrParityProcess", map[string]json.RawMessage{"Input": json.RawMessage(`"hello"`)}, []string{"Output"}, 10)
	if result.Status != "completed" || result.Error != "" || launches != 1 {
		t.Fatalf("result=%#v launches=%d", result, launches)
	}
}

func TestDataWriteRunProcessRefusesBeforeLaunch(t *testing.T) {
	client, _ := NewClient(Config{BaseURL: "http://localhost", Login: "example-user", Password: "replace-me"})
	result := client.DataWriteRunProcess(context.Background(), "", nil, nil, 0)
	if result.Error != "process-name is required" {
		t.Fatalf("result=%#v", result)
	}
}
