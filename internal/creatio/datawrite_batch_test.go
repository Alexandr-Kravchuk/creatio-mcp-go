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
