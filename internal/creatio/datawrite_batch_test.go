package creatio

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDataWriteBatchNativeShapeAndOutcomes(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ServiceModel/AuthService.svc/Login" {
			io.WriteString(w, `{"Code":0}`)
			return
		}
		calls++
		if r.URL.Path != "/0/DataService/json/SyncReply/BatchQuery" || r.Method != "POST" {
			t.Errorf("request %s %s", r.Method, r.URL.Path)
		}
		var body struct {
			Items    []map[string]any `json:"items"`
			Continue bool             `json:"continueIfError"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if !body.Continue || len(body.Items) != 1 || body.Items[0]["rootSchemaName"] != "Contact" || body.Items[0]["operationType"] != float64(1) {
			t.Errorf("body %#v", body)
		}
		io.WriteString(w, `{"queryResults":[{"queryId":"`+body.Items[0]["queryId"].(string)+`","success":true,"rowsAffected":1}]}`)
	}))
	defer server.Close()
	client, _ := NewClient(Config{BaseURL: server.URL, Login: "example-user", Password: "replace-me"})
	rows, err := DataWriteBatchDecode(json.RawMessage(`[{"operation":"insert","schema-name":"Contact","record-id":"00000000-0000-0000-0000-000000000001","values":{"Name":{"data-value-type":1,"value":"batch"}}}]`))
	if err != nil {
		t.Fatal(err)
	}
	got, err := client.ExecuteDataServiceBatch(context.Background(), rows)
	if err != nil || !got.Success || got.CompletedCount != 1 || calls != 1 {
		t.Fatalf("got=%#v err=%v calls=%d", got, err, calls)
	}
}

func TestDataWriteBatchValidationBeforeRemoteCall(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Errorf("unexpected request %s", r.URL.Path) }))
	defer server.Close()
	client, _ := NewClient(Config{BaseURL: server.URL, Login: "example-user", Password: "replace-me"})
	_, err := client.ExecuteDataServiceBatch(context.Background(), nil)
	if err == nil || err.Error() != "Provide 1–100 explicit operations." {
		t.Fatalf("err=%v", err)
	}
}

func TestDataWriteBatchCorrelatesEveryOutcome(t *testing.T) {
	var body struct {
		Items []map[string]any `json:"items"`
	}
	var answer func() string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ServiceModel/AuthService.svc/Login" {
			io.WriteString(w, `{"Code":0}`)
			return
		}
		body.Items = nil
		_ = json.NewDecoder(r.Body).Decode(&body)
		io.WriteString(w, answer())
	}))
	defer server.Close()
	client, _ := NewClient(Config{BaseURL: server.URL, Login: "example-user", Password: "replace-me"})
	rows, err := DataWriteBatchDecode(json.RawMessage(`[
		{"operation":"insert","schema-name":"Contact","record-id":"00000000-0000-0000-0000-00000000000A","values":{"Name":{"data-value-type":1,"value":"a"}}},
		{"operation":"update","schema-name":"Contact","record-id":"00000000-0000-0000-0000-00000000000b","values":{"Name":{"data-value-type":1,"value":null}}},
		{"operation":"delete","schema-name":"Contact","record-id":"00000000-0000-0000-0000-00000000000c"}]`))
	if err != nil {
		t.Fatal(err)
	}
	answer = func() string {
		id := func(i int) string { return body.Items[i]["queryId"].(string) }
		if body.Items[0]["columnValues"].(map[string]any)["items"].(map[string]any)["Id"] == nil || body.Items[1]["filters"] == nil || body.Items[2]["columnValues"] != nil {
			t.Errorf("query shapes %v", body.Items)
		}
		return `{"queryResults":[{"queryId":"` + id(0) + `","success":true,"rowsAffected":1},{"queryId":"` + id(1) + `","success":false,"errorInfo":{"message":"denied"}}]}`
	}
	got, err := client.ExecuteDataServiceBatch(context.Background(), rows)
	if err != nil || got.Success || got.CompletedCount != 1 || got.FailedCount != 1 || got.UnknownCount != 1 {
		t.Fatalf("got=%#v err=%v", got, err)
	}
	if got.Items[0].RecordID != "00000000-0000-0000-0000-00000000000a" || *got.Items[0].RowsAffected != 1 || got.Items[0].Error != nil {
		t.Fatalf("completed item %#v", got.Items[0])
	}
	if *got.Items[1].Error != "[untrusted-source-text begin] denied [untrusted-source-text end]" || got.Items[2].State != "unknown" {
		t.Fatalf("items %#v %#v", got.Items[1], got.Items[2])
	}
	if got.Items[2].Diagnostic == nil {
		t.Fatalf("diagnostic missing")
	}

	// An uncorrelated result makes every item unknown, and nothing is resubmitted.
	answer = func() string {
		return `{"queryResults":[{"queryId":"11111111-1111-1111-1111-111111111111","success":true}]}`
	}
	got, err = client.ExecuteDataServiceBatch(context.Background(), rows)
	if err != nil || got.UnknownCount != 3 || *got.Items[0].Error != "[untrusted-source-text begin] Batch response contained an uncorrelated query result. [untrusted-source-text end]" {
		t.Fatalf("uncorrelated got=%#v err=%v", got, err)
	}
}

func TestDataWriteBatchRefusesInvalidItems(t *testing.T) {
	client, _ := NewClient(Config{BaseURL: "http://127.0.0.1:1", Login: "example-user", Password: "replace-me"})
	shape := "Each operation needs insert/update/delete, a schema-name and a non-empty record-id."
	values := "Values require column identifiers other than Id and compatible scalar DataService types (0–12)."
	for raw, want := range map[string]string{
		`[{"operation":"replace","schema-name":"Contact","record-id":"00000000-0000-0000-0000-000000000001"}]`:                                                     shape,
		`[{"operation":"delete","schema-name":"Contact","record-id":"00000000-0000-0000-0000-000000000000"}]`:                                                      shape,
		`[{"operation":"insert","schema-name":"Bad-Name","record-id":"00000000-0000-0000-0000-000000000001","values":{"Name":{"data-value-type":1,"value":"x"}}}]`: shape,
		`[{"operation":"delete","schema-name":"Contact","record-id":"00000000-0000-0000-0000-000000000001","values":{"Name":{"data-value-type":1,"value":"x"}}}]`:  "Insert/update require 1–50 values; delete accepts no values.",
		`[{"operation":"insert","schema-name":"Contact","record-id":"00000000-0000-0000-0000-000000000001","values":{"Id":{"data-value-type":0,"value":null}}}]`:   values,
		`[{"operation":"insert","schema-name":"Contact","record-id":"00000000-0000-0000-0000-000000000001","values":{"Age":{"data-value-type":4,"value":"7"}}}]`:   values,
	} {
		rows, err := DataWriteBatchDecode(json.RawMessage(raw))
		if err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		if _, err := client.ExecuteDataServiceBatch(context.Background(), rows); err == nil || err.Error() != want {
			t.Errorf("%s: err=%v", raw, err)
		}
	}
	if _, err := DataWriteBatchDecode(json.RawMessage(`[{"operation":"insert","record-id":"nope"}]`)); err != ErrDataWriteBatchBinding {
		t.Fatalf("binding err=%v", err)
	}
}
