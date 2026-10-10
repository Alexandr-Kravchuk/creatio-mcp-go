package creatio

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// pkgWriteSyncServer answers the FSM probe and the two load routes with the given bodies.
func pkgWriteSyncServer(t *testing.T, fsm string, load func() (int, string)) (*Client, *[]string) {
	t.Helper()
	calls := &[]string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/Login") {
			_, _ = io.WriteString(w, `{"Code":0}`)
			return
		}
		body, _ := io.ReadAll(r.Body)
		*calls = append(*calls, r.Method+" "+r.URL.Path+" "+string(body))
		if strings.HasSuffix(r.URL.Path, "GetIsFileDesignMode") {
			_, _ = io.WriteString(w, fsm)
			return
		}
		status, text := load()
		w.WriteHeader(status)
		_, _ = io.WriteString(w, text)
	}))
	t.Cleanup(server.Close)
	return newFormsTestClient(t, server.URL), calls
}

func pkgWriteValues(result CommandResult) []string {
	values := make([]string, len(result.Messages))
	for i, message := range result.Messages {
		values[i] = message.MessageType + ":" + message.Value
	}
	return values
}

func TestPkgWriteSyncRequestsAndOutcomes(t *testing.T) {
	ok := func() (int, string) { return 200, `{"success":true}` }
	client, calls := pkgWriteSyncServer(t, `{"success":true,"value":true}`, ok)
	if result := client.SyncPackages(context.Background(), true); result.ExitCode != 0 ||
		strings.Join(pkgWriteValues(result), "|") != "None:Start load packages to database on a web application|Info:Load packages to database on a web application completed|None:" {
		t.Fatalf("to db = %q", pkgWriteValues(result))
	}
	if result := client.SyncPackages(context.Background(), false); result.ExitCode != 0 {
		t.Fatalf("to fs = %+v", result)
	}
	if len(*calls) != 4 || (*calls)[0] != "POST /0/ServiceModel/WorkspaceExplorerService.svc/GetIsFileDesignMode " ||
		(*calls)[1] != "POST /0/ServiceModel/AppInstallerService.svc/LoadPackagesToDB " ||
		(*calls)[3] != "POST /0/ServiceModel/AppInstallerService.svc/LoadPackagesToFileSystem " {
		t.Fatalf("calls = %q", *calls)
	}

	disabled, calls := pkgWriteSyncServer(t, `{"success":true,"value":false}`, ok)
	if result := disabled.SyncPackages(context.Background(), true); result.ExitCode != 1 ||
		strings.Join(pkgWriteValues(result), "|") != "None:|Error:Load packages to database on a web application ended with error: disabled file design mode" || len(*calls) != 1 {
		t.Fatalf("disabled = %q calls %q", pkgWriteValues(result), *calls)
	}

	unknown, _ := pkgWriteSyncServer(t, `{"success":false,"errorInfo":{"message":"denied","errorCode":"E1"}}`, ok)
	if result := unknown.SyncPackages(context.Background(), false); result.ExitCode != 1 ||
		strings.Join(pkgWriteValues(result), "|") != "Error:Get file design mode ended with error: denied (error code: E1)|Error:Load packages to file system on a web application ended with error: file design mode state is unknown|None:" {
		t.Fatalf("unknown = %q", pkgWriteValues(result))
	}

	rejected, _ := pkgWriteSyncServer(t, `{"success":true,"value":true}`, func() (int, string) {
		return 200, `{"success":false,"errorInfo":{"message":"sync failed","errorCode":"S"},"errors":[{"workspaceItem":{"name":"UsrSchema"},"errorInfo":{"message":"bad","errorCode":"X"}},{"workspaceItem":null,"errorInfo":null}]}`
	})
	if result := rejected.SyncPackages(context.Background(), true); result.ExitCode != 1 || strings.Join(pkgWriteValues(result), "|") !=
		"None:Start load packages to database on a web application|"+
			"Error:Load packages to database on a web application ended with error: sync failed (error code: S)|"+
			"Error:Load packages to database on a web application ended with error: Synchronization rejected item 'UsrSchema': bad (error code: X)|"+
			"Error:Load packages to database on a web application ended with error: Synchronization rejected item 'unknown': unknown error|None:" {
		t.Fatalf("rejected = %q", pkgWriteValues(result))
	}

	failed, _ := pkgWriteSyncServer(t, `{"success":true,"value":true}`, func() (int, string) { return 200, `{"success":false}` })
	if result := failed.SyncPackages(context.Background(), true); result.ExitCode != 1 ||
		result.Messages[1].Value != "Load packages to database on a web application ended with error: unknown error" {
		t.Fatalf("failed = %q", pkgWriteValues(result))
	}
}

func TestPkgWriteSyncRetriesThenReportsTheFailure(t *testing.T) {
	previousAttempts, previousDelay := pkgWriteSyncAttempts, pkgWriteSyncDelay
	pkgWriteSyncAttempts, pkgWriteSyncDelay = 3, time.Millisecond
	t.Cleanup(func() { pkgWriteSyncAttempts, pkgWriteSyncDelay = previousAttempts, previousDelay })
	client, calls := pkgWriteSyncServer(t, `{"success":true,"value":true}`, func() (int, string) { return 500, `{}` })
	result := client.SyncPackages(context.Background(), true)
	if result.ExitCode != 1 || len(*calls) != 4 || len(result.Messages) != 2 || result.Messages[1].MessageType != "Error" ||
		!strings.Contains(result.Messages[1].Value, "HTTP 500") {
		t.Fatalf("retry = %q calls %d", pkgWriteValues(result), len(*calls))
	}
}
