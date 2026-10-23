package creatio

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
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

// userTasksBundledVersion is the CrtProcessBuilder clio 8.1.0.142 carries.
const userTasksBundledVersion = "1.6.6.104"

// userTasksBundle points the convergence check at a clio archive carrying CrtProcessBuilder 1.6.6.104, so a
// test does not depend on the clio installed on the machine.
func userTasksBundle(t *testing.T) {
	t.Helper()
	processWriteArchive(t, map[string]string{"descriptor.json": `{"Descriptor":{"PackageVersion":"` + userTasksBundledVersion + `"}}`})
}

func TestListUserTasksListsPaletteAfterPackageGate(t *testing.T) {
	userTasksBundle(t)
	server := userTasksServer(t, `{"success":true,"rows":[{"Name":"CrtBase","Version":"8.1.0"},{"Name":"crtprocessbuilder","Version":"1.6.6.104"}]}`,
		`{"ListUserTasksResult":{"success":true,"userTasks":[{"name":"ReadDataUserTask","uid":"u-1"},{"name":"ApprovalUserTask","uid":"u-2"}]}}`)
	defer server.Close()
	result := newFormsTestClient(t, server.URL).ListUserTasks(context.Background(), "dev")
	// clio's log opens with the catalog read line, measured against clio 8.1.0.142 on a fake stand.
	want := []LogMessage{{"Info", "Reading user task catalog from 'dev'..."}, {"Info", "ReadDataUserTask\tu-1"},
		{"Info", "ApprovalUserTask\tu-2"}, {"Info", "Total user tasks: 2"}}
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
	userTasksBundle(t)
	server := userTasksServer(t, `{"success":true,"rows":[{"Name":"CrtBase","Version":"8.1.0"}]}`, `{}`)
	defer server.Close()
	result := newFormsTestClient(t, server.URL).ListUserTasks(context.Background(), "dev")
	if result.ExitCode != 1 || len(result.Messages) != 1 || result.Messages[0].MessageType != "Error" || result.Messages[0].Value != processBuilderMissingMessage {
		t.Fatalf("result = %#v", result)
	}
}

// clio's convergence rule (BundledPackageConvergence): an environment whose CrtProcessBuilder is older than the
// one the installed clio carries is refused before ProcessDesignService is called, naming both versions.
func TestListUserTasksRefusesAProcessBuilderOlderThanTheBundledOne(t *testing.T) {
	userTasksBundle(t)
	server := userTasksServer(t, `{"success":true,"rows":[{"Name":"CrtBase","Version":"8.1.0"},{"Name":"CrtProcessBuilder","Version":"1.6.6.103"}]}`, `{}`)
	defer server.Close()
	result := newFormsTestClient(t, server.URL).ListUserTasks(context.Background(), "dev")
	want := "This clio carries CrtProcessBuilder 1.6.6.104, but the target environment has 1.6.6.103. " +
		"Update the package in the target environment and retry.\n" + processWriteInstallHint
	if result.ExitCode != 1 || len(result.Messages) != 1 || result.Messages[0] != (LogMessage{"Error", want}) {
		t.Fatalf("result = %#v", result)
	}
}

// Without a readable clio archive there is nothing to converge to, so an older package is let through, as clio
// does when its own archive cannot be read.
func TestListUserTasksLetsAnOlderProcessBuilderThroughWithoutABundledArchive(t *testing.T) {
	processWriteNoArchive(t)
	server := userTasksServer(t, `{"success":true,"rows":[{"Name":"CrtProcessBuilder","Version":"1.6.6.103"}]}`,
		`{"ListUserTasksResult":{"success":true,"userTasks":[]}}`)
	defer server.Close()
	result := newFormsTestClient(t, server.URL).ListUserTasks(context.Background(), "dev")
	if result.ExitCode != 0 || len(result.Messages) != 2 || result.Messages[1] != (LogMessage{"Info", "Total user tasks: 0"}) {
		t.Fatalf("result = %#v", result)
	}
}

func TestListUserTasksReportsServiceFailure(t *testing.T) {
	userTasksBundle(t)
	server := userTasksServer(t, `{"success":true,"rows":[{"Name":"CrtProcessBuilder","Version":"1.6.6.104"}]}`,
		`{"ListUserTasksResult":{"success":false,"errorMessage":"Palette unavailable"}}`)
	defer server.Close()
	result := newFormsTestClient(t, server.URL).ListUserTasks(context.Background(), "dev")
	want := []LogMessage{{"Info", "Reading user task catalog from 'dev'..."}, {"Error", "Palette unavailable"}}
	if result.ExitCode != 1 || len(result.Messages) != len(want) || result.Messages[0] != want[0] || result.Messages[1] != want[1] {
		t.Fatalf("result = %#v", result)
	}
}

func TestListUserTasksReportsAnUnresolvableHostAsClioExceptionChain(t *testing.T) {
	client := newFormsTestClient(t, "http://nonexistent.invalid")
	client.http.Transport = dnsFailingTransport()
	result := client.ListUserTasks(context.Background(), "dev")
	sentence := connectSentence(&net.DNSError{IsNotFound: true}, runtime.GOOS)
	want := "[WebException] " + sentence + " (nonexistent.invalid:80) → [HttpRequestException] " + sentence +
		" (nonexistent.invalid:80) → [SocketException] " + sentence
	if result.ExitCode != -1 || len(result.Messages) != 1 || result.Messages[0] != (LogMessage{"Error", want}) {
		t.Fatalf("result = %#v", result)
	}
}
