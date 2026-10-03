package creatio

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func userTasksServer(t *testing.T, packages string, listUserTasks string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ServiceModel/AuthService.svc/Login":
			_, _ = w.Write([]byte(`{"Code":0}`))
		case "/0/DataService/json/SyncReply/SelectQuery":
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), `"rootSchemaName":"SysPackage"`) {
				t.Errorf("package query = %s", body)
			}
			_, _ = w.Write([]byte(packages))
		case "/0/rest/ProcessDesignService/ListUserTasks":
			body, _ := io.ReadAll(r.Body)
			if r.Method != http.MethodPost || string(body) != "{}" {
				t.Errorf("ListUserTasks request = %s %q", r.Method, body)
			}
			_, _ = w.Write([]byte(listUserTasks))
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
}

func TestListUserTasksListsPaletteAfterPackageGate(t *testing.T) {
	server := userTasksServer(t, `{"success":true,"rows":[{"Name":"CrtBase"},{"Name":"crtprocessbuilder"}]}`,
		`{"ListUserTasksResult":{"success":true,"userTasks":[{"name":"ReadDataUserTask","uid":"u-1"},{"name":"ApprovalUserTask","uid":"u-2"}]}}`)
	defer server.Close()
	result := newFormsTestClient(t, server.URL).ListUserTasks(context.Background())
	want := []LogMessage{{"Info", "ReadDataUserTask\tu-1"}, {"Info", "ApprovalUserTask\tu-2"}, {"Info", "Total user tasks: 2"}}
	if result.ExitCode != 0 || len(result.Messages) != len(want) {
		t.Fatalf("result = %#v", result)
	}
	for index, message := range want {
		if result.Messages[index] != message {
			t.Fatalf("message %d = %#v, want %#v", index, result.Messages[index], message)
		}
	}
}

func TestListUserTasksRefusesWithoutProcessBuilder(t *testing.T) {
	server := userTasksServer(t, `{"success":true,"rows":[{"Name":"CrtBase"}]}`, `{}`)
	defer server.Close()
	result := newFormsTestClient(t, server.URL).ListUserTasks(context.Background())
	if result.ExitCode != 1 || len(result.Messages) != 1 || result.Messages[0].MessageType != "Error" || result.Messages[0].Value != processBuilderMissingMessage {
		t.Fatalf("result = %#v", result)
	}
}

func TestListUserTasksReportsServiceFailure(t *testing.T) {
	server := userTasksServer(t, `{"success":true,"rows":[{"Name":"CrtProcessBuilder"}]}`,
		`{"ListUserTasksResult":{"success":false,"errorMessage":"Palette unavailable"}}`)
	defer server.Close()
	result := newFormsTestClient(t, server.URL).ListUserTasks(context.Background())
	if result.ExitCode != 1 || len(result.Messages) != 1 || result.Messages[0].Value != "Palette unavailable" {
		t.Fatalf("result = %#v", result)
	}
}
