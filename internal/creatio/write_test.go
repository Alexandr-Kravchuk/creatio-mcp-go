package creatio

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestInsertTreatsSuccessFalseAsLoudRefusal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ServiceModel/AuthService.svc/Login":
			_, _ = io.WriteString(w, `{"Code":0}`)
		case "/0/DataService/json/SyncReply/InsertQuery":
			_, _ = io.WriteString(w, `{"success":false,"errorInfo":{"message":"permission denied"}}`)
		default:
			t.Fatalf("unexpected route %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client, err := NewClient(Config{BaseURL: server.URL, Login: "user", Password: "pass"})
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := client.Insert(context.Background(), "SysSchema", map[string]any{"Name": "probe"})
	if err == nil {
		t.Fatal("Insert succeeded for success:false response")
	}
	if outcome.Succeeded || outcome.FailureClass != "server-refused" || outcome.FailureDetail != "permission denied" {
		t.Fatalf("outcome = %#v", outcome)
	}
}
