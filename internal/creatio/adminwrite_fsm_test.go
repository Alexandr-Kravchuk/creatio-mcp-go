package creatio

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

const adminWriteFsmOff = `{"applicationInfo":{"sysValues":{},"useStaticFileContent":true,"staticFileContent":{"schemasRuntimePath":"conf/content"}}}`

func TestSetFsmModeRejectsAnUnknownMode(t *testing.T) {
	stand, client := newAdminWriteManageStand(t, map[string][]string{})
	result := client.SetFsmMode(context.Background(), AdminWriteFsmTarget{Name: "dev"}, "maybe")
	if result.ExitCode != 1 || !reflect.DeepEqual(result.Messages, []LogMessage{{"Error", "Invalid value for IsFsm. Expected: 'on' or 'off'."}}) {
		t.Fatalf("result = %#v", result)
	}
	if stand.routes() != "" {
		t.Errorf("requests = %s", stand.routes())
	}
}

func TestSetFsmModeOnTogglesRemotelyThenExports(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the remote toggle is the macOS/Linux path")
	}
	stand, client := newAdminWriteManageStand(t, map[string][]string{
		applicationInfoRoute: {adminWriteFsmOff},
		"rest/CreatioApiGateway/SetFileDesignMode": {
			`{"success":true,"previousFileDesignMode":"false","newFileDesignMode":"true","webConfigPath":"C:\\site\\Web.config"}`},
		"ServiceModel/WorkspaceExplorerService.svc/GetIsFileDesignMode": {`{"success":true,"value":true}`},
		"ServiceModel/AppInstallerService.svc/LoadPackagesToFileSystem": {`{"success":true,"errors":[]}`},
	})
	result := client.SetFsmMode(context.Background(), AdminWriteFsmTarget{Name: "dev"}, " ON ")
	want := []LogMessage{
		{"None", "FSM toggled remotely via cliogate on environment 'dev': false → true. IIS will auto-recycle the AppPool to pick up the new Web.config."},
		{"None", "Start load packages to file system on a web application"},
		{"Info", "Load packages to file system on a web application completed"},
		{"None", ""}, // LoadPackagesToFileSystemCommand.Load's WriteLine (seen live in T23)
	}
	if result.ExitCode != 0 || !reflect.DeepEqual(result.Messages, want) {
		t.Fatalf("result = %#v", result)
	}
	if got := stand.body("rest/CreatioApiGateway/SetFileDesignMode"); got != `{"isFileDesignMode":true}` {
		t.Errorf("toggle body = %s", got)
	}
	if got := stand.routes(); got != applicationInfoRoute+",rest/CreatioApiGateway/SetFileDesignMode,"+
		"ServiceModel/WorkspaceExplorerService.svc/GetIsFileDesignMode,ServiceModel/AppInstallerService.svc/LoadPackagesToFileSystem" {
		t.Errorf("routes = %s", got)
	}
}

func TestSetFsmModeOffImportsFirstAndStopsWhenTheImportFails(t *testing.T) {
	stand, client := newAdminWriteManageStand(t, map[string][]string{
		"ServiceModel/WorkspaceExplorerService.svc/GetIsFileDesignMode": {`{"success":true,"value":true}`},
		"ServiceModel/AppInstallerService.svc/LoadPackagesToDB": {
			`{"success":false,"errors":[{"workspaceItem":{"name":"UsrPkg"},"errorInfo":{"message":"locked","errorCode":"E1"}}]}`},
	})
	result := client.SetFsmMode(context.Background(), AdminWriteFsmTarget{Name: "dev"}, "off")
	if result.ExitCode != 1 || len(result.Messages) != 4 ||
		result.Messages[1].Value != "Load packages to database on a web application ended with error: Synchronization rejected item 'UsrPkg': locked (error code: E1)" ||
		result.Messages[2] != (LogMessage{"None", ""}) ||
		!strings.HasPrefix(result.Messages[3].Value, "Packages were not imported into the database, so the file system mode configuration was left unchanged.") {
		t.Fatalf("result = %#v", result)
	}
	if strings.Contains(stand.routes(), "SetFileDesignMode") || strings.Contains(stand.routes(), applicationInfoRoute) {
		t.Errorf("the configuration must not change: %s", stand.routes())
	}
}

func TestSetFsmModeOffEditsTheLocalConfigOfANetCoreSite(t *testing.T) {
	directory := t.TempDir()
	config := filepath.Join(directory, "Terrasoft.WebHost.dll.config")
	original := "<configuration>\n  <terrasoft>\n    <fileDesignMode enabled=\"true\" />\n  </terrasoft>\n" +
		"  <appSettings>\n    <add key=\"UseStaticFileContent\" value=\"false\" />\n  </appSettings>\n</configuration>\n"
	if err := os.WriteFile(config, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	_, client := newAdminWriteManageStand(t, map[string][]string{
		"ServiceModel/WorkspaceExplorerService.svc/GetIsFileDesignMode": {`{"success":true,"value":false}`},
	})
	result := client.SetFsmMode(context.Background(), AdminWriteFsmTarget{Name: "dev", IsNetCore: true, EnvironmentPath: directory}, "off")
	if result.ExitCode != 0 || len(result.Messages) != 3 || result.Messages[0] != (LogMessage{"None", ""}) ||
		result.Messages[1].MessageType != "Warning" ||
		!strings.Contains(result.Messages[2].Value, "fileDesignMode           true          false") ||
		!strings.Contains(result.Messages[2].Value, "UseStaticFileContent     false         true") {
		t.Fatalf("result = %#v", result)
	}
	data, _ := os.ReadFile(config)
	if !strings.Contains(string(data), `<fileDesignMode enabled="false" />`) || !strings.Contains(string(data), `value="true"`) {
		t.Errorf("config = %s", data)
	}
	missing := client.SetFsmMode(context.Background(), AdminWriteFsmTarget{Name: "dev", IsNetCore: true}, "off")
	if missing.ExitCode != -1 || missing.Messages[len(missing.Messages)-1].Value != "[Exception] Could not find path to environment: 'dev'" {
		t.Errorf("no path = %#v", missing)
	}
}

// The probe before the remote toggle waits for a slow GetApplicationInfo the way clio's FsmModeStatusService does
// (Timeout.Infinite) instead of failing at the client's default timeout: the site may still be restarting after
// the previous toggle (live run T24).
func TestSetFsmModeProbeOutlastsTheClientTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the remote toggle is the macOS/Linux path")
	}
	const delay = 300 * time.Millisecond
	var routes []string
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/Login") {
			_, _ = w.Write([]byte(`{"Code":0}`))
			return
		}
		route := strings.TrimPrefix(r.URL.Path, "/0/")
		mu.Lock()
		routes = append(routes, route)
		mu.Unlock()
		switch route {
		case "ServiceModel/WorkspaceExplorerService.svc/GetIsFileDesignMode":
			_, _ = w.Write([]byte(`{"success":true,"value":false}`))
		case applicationInfoRoute:
			time.Sleep(delay)
			_, _ = w.Write([]byte(adminWriteFsmOff))
		default:
			t.Errorf("unexpected route %s", route)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	client := newFormsTestClient(t, server.URL)
	client.http.Timeout = delay / 3

	result := client.SetFsmMode(context.Background(), AdminWriteFsmTarget{Name: "dev"}, "off")
	want := []LogMessage{
		{"None", ""},
		{"Warning", "The environment already has file design mode disabled, so there was nothing to import. " +
			"Applying the file system mode configuration."},
		{"None", "FSM already off on environment 'dev'; no config edit needed."},
	}
	if result.ExitCode != 0 || !reflect.DeepEqual(result.Messages, want) {
		t.Fatalf("result = %#v", result)
	}
	mu.Lock()
	got := strings.Join(routes, ",")
	mu.Unlock()
	if got != "ServiceModel/WorkspaceExplorerService.svc/GetIsFileDesignMode,"+applicationInfoRoute {
		t.Errorf("routes = %s", got)
	}
	// get-fsm-mode waits the same way (live run T24: the first read after a restart took 60.9-70.4 s on clio).
	if status, err := client.GetFsmMode(context.Background()); err != nil || status.Mode != "off" {
		t.Errorf("get-fsm-mode = %#v, %v", status, err)
	}
}

// adminWriteFsmFlakyStand answers the login, then aborts the first drops[route] requests of a route without an
// answer (the connection is closed, as by a site that is restarting) before answering with answers[route]. It
// counts the requests per route, the login included.
func adminWriteFsmFlakyStand(t *testing.T, drops map[string]int, answers map[string]func(http.ResponseWriter)) (*Client, func(string) int) {
	t.Helper()
	previousDelay := pkgWriteSyncDelay
	pkgWriteSyncDelay = time.Millisecond
	t.Cleanup(func() { pkgWriteSyncDelay = previousDelay })
	var mu sync.Mutex
	counts := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		route := strings.TrimPrefix(r.URL.Path, "/0/")
		mu.Lock()
		counts[route]++
		count := counts[route]
		mu.Unlock()
		if count <= drops[route] {
			panic(http.ErrAbortHandler)
		}
		if route == "/ServiceModel/AuthService.svc/Login" {
			_, _ = w.Write([]byte(`{"Code":0}`))
			return
		}
		answer, ok := answers[route]
		if !ok {
			t.Errorf("unexpected route %s", route)
			http.NotFound(w, r)
			return
		}
		answer(w)
	}))
	t.Cleanup(server.Close)
	return newFormsTestClient(t, server.URL), func(route string) int {
		mu.Lock()
		defer mu.Unlock()
		return counts[route]
	}
}

func adminWriteFsmAnswer(status int, body string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

// clio's FileDesignModePackages sends GetIsFileDesignMode and LoadPackagesTo* with 30 attempts 3 s apart: a
// request that got no answer from a restarting site is sent again (live run T24: a connection reset after 190 s).
func TestSetFsmModeSendsARequestThatGotNoAnswerAgain(t *testing.T) {
	const load = "ServiceModel/AppInstallerService.svc/LoadPackagesToDB"
	client, count := adminWriteFsmFlakyStand(t, map[string]int{pkgWriteFSMRoute: 2, load: 1}, map[string]func(http.ResponseWriter){
		pkgWriteFSMRoute: adminWriteFsmAnswer(http.StatusOK, `{"success":true,"value":true}`),
		load:             adminWriteFsmAnswer(http.StatusOK, `{"success":true,"errors":[]}`),
	})
	log := &adminWriteFsmLog{}
	if result := client.adminWriteLoadPackages(context.Background(), "LoadPackagesToDB", "database", log); result != adminWriteLoadCompleted {
		t.Fatalf("result = %v, log = %#v", result, log.messages)
	}
	want := []LogMessage{
		{"None", "Start load packages to database on a web application"},
		{"Info", "Load packages to database on a web application completed"},
		{"None", ""},
	}
	if !reflect.DeepEqual(log.messages, want) {
		t.Errorf("log = %#v", log.messages)
	}
	if count(pkgWriteFSMRoute) != 3 || count(load) != 2 {
		t.Errorf("requests: GetIsFileDesignMode %d, LoadPackagesToDB %d", count(pkgWriteFSMRoute), count(load))
	}
}

// An answer ends the loop whatever its status (Creatio.Client retries only a request that threw), and the loop
// gives up after the 30th request that got no answer.
func TestSetFsmModeRetriesOnlyUnansweredRequestsAndAtMostThirtyTimes(t *testing.T) {
	client, count := adminWriteFsmFlakyStand(t, nil, map[string]func(http.ResponseWriter){
		pkgWriteFSMRoute: adminWriteFsmAnswer(http.StatusInternalServerError, `{}`),
	})
	log := &adminWriteFsmLog{}
	if result := client.adminWriteLoadPackages(context.Background(), "LoadPackagesToDB", "database", log); result != adminWriteLoadRefused ||
		len(log.messages) != 1 || !strings.Contains(log.messages[0].Value, "returned HTTP 500") {
		t.Fatalf("result = %v, log = %#v", result, log.messages)
	}
	if count(pkgWriteFSMRoute) != 1 {
		t.Errorf("an answered request was sent %d times", count(pkgWriteFSMRoute))
	}

	silent, silentCount := adminWriteFsmFlakyStand(t, map[string]int{pkgWriteFSMRoute: 1000}, nil)
	log = &adminWriteFsmLog{}
	if result := silent.adminWriteLoadPackages(context.Background(), "LoadPackagesToDB", "database", log); result != adminWriteLoadRefused ||
		len(log.messages) != 1 || log.messages[0].MessageType != "Error" || !strings.Contains(log.messages[0].Value, "transport failure") {
		t.Fatalf("result = %v, log = %#v", result, log.messages)
	}
	if silentCount(pkgWriteFSMRoute) != 30 {
		t.Errorf("GetIsFileDesignMode was sent %d times, want 30", silentCount(pkgWriteFSMRoute))
	}
}

// Creatio.Client logs in once before its loop: a login that got no answer is not retried.
func TestSetFsmModeDoesNotRetryTheLogin(t *testing.T) {
	const login = "/ServiceModel/AuthService.svc/Login"
	client, count := adminWriteFsmFlakyStand(t, map[string]int{login: 1000}, nil)
	log := &adminWriteFsmLog{}
	if result := client.adminWriteLoadPackages(context.Background(), "LoadPackagesToDB", "database", log); result != adminWriteLoadRefused ||
		len(log.messages) != 1 || log.messages[0].MessageType != "Error" {
		t.Fatalf("result = %v, log = %#v", result, log.messages)
	}
	if count(login) != 1 || count(pkgWriteFSMRoute) != 0 {
		t.Errorf("requests: login %d, GetIsFileDesignMode %d", count(login), count(pkgWriteFSMRoute))
	}
}
