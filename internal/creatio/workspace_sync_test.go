package creatio

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// workspaceSyncTestLog is a CommandLog that keeps "<type>: <value>" lines.
type workspaceSyncTestLog struct {
	mu    sync.Mutex
	lines []string
}

func (l *workspaceSyncTestLog) add(kind, value string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, kind+": "+value)
}
func (l *workspaceSyncTestLog) Line(value string)    { l.add("None", value) }
func (l *workspaceSyncTestLog) Info(value string)    { l.add("Info", value) }
func (l *workspaceSyncTestLog) Warning(value string) { l.add("Warning", value) }
func (l *workspaceSyncTestLog) Error(value string)   { l.add("Error", value) }

// workspaceSyncStand is a fake Creatio site that records "<method> <path> <body>" for every request but the login.
type workspaceSyncStand struct {
	mu       sync.Mutex
	requests []string
	server   *httptest.Server
}

func newWorkspaceSyncStand(t *testing.T, handler func(w http.ResponseWriter, r *http.Request, body string)) *workspaceSyncStand {
	t.Helper()
	stand := &workspaceSyncStand{}
	stand.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/AuthService.svc/Login") {
			http.SetCookie(w, &http.Cookie{Name: "BPMCSRF", Value: "token", Path: "/"})
			_, _ = io.WriteString(w, `{"Code":0}`)
			return
		}
		data, _ := io.ReadAll(r.Body)
		body := string(data)
		if strings.HasSuffix(r.URL.Path, "/UploadPackage") {
			body = "<archive>"
		}
		stand.mu.Lock()
		stand.requests = append(stand.requests, r.Method+" "+r.URL.Path+" "+body)
		stand.mu.Unlock()
		handler(w, r, body)
	}))
	t.Cleanup(stand.server.Close)
	return stand
}

func (s *workspaceSyncStand) client(t *testing.T) *Client {
	t.Helper()
	client, err := NewClient(Config{BaseURL: s.server.URL, Login: "example-user", Password: "replace-me", IsNetCore: true})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func (s *workspaceSyncStand) recorded() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var lines []string
	for _, line := range s.requests {
		// The log listener's polls race the install request; count each run of them once.
		if strings.HasSuffix(line, "/GetLogFile ") && len(lines) > 0 && lines[len(lines)-1] == line {
			continue
		}
		lines = append(lines, line)
	}
	return lines
}

func workspaceSyncArchive(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "CreatioPackages.zip")
	if err := os.WriteFile(path, []byte("PK archive"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestInstallWorkspaceArchiveSendsClioRequestsInOrder(t *testing.T) {
	saved := appWriteInstallTimings
	appWriteInstallTimings.logInterval = time.Millisecond
	t.Cleanup(func() { appWriteInstallTimings = saved })
	var installed atomic.Bool
	stand := newWorkspaceSyncStand(t, func(w http.ResponseWriter, r *http.Request, body string) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/GetLogFile") && !installed.Load():
			_, _ = io.WriteString(w, "start\n")
		case strings.HasSuffix(r.URL.Path, "/GetLogFile"):
			_, _ = io.WriteString(w, "start\nPackage installation finished\n")
		case strings.HasSuffix(r.URL.Path, "/InstallAppFromFile"):
			installed.Store(true)
			_, _ = io.WriteString(w, `{"success":true}`)
		default:
			_, _ = io.WriteString(w, `{"success":true}`)
		}
	})
	log := &workspaceSyncTestLog{}
	success, err := stand.client(t).InstallWorkspaceArchive(context.Background(), WorkspaceInstallRequest{
		ArchivePath: workspaceSyncArchive(t), CreateBackup: false, EnvironmentURI: "http://example"}, log)
	if err != nil || !success {
		t.Fatalf("install %v %v %v", success, err, log.lines)
	}
	want := []string{
		"POST /ServiceModel/UserInfoService.svc/GetCurrentUserInfo {}",
		"POST /ServiceModel/PackageInstallerService.svc/UploadPackage <archive>",
		"GET /ServiceModel/PackageInstallerService.svc/GetLogFile ",
		`POST /ServiceModel/AppInstallerService.svc/InstallAppFromFile {"Name":"CreatioPackages","Code":"CreatioPackages","ZipPackageName":"CreatioPackages.zip","LastUpdateString":0}`,
		"GET /ServiceModel/PackageInstallerService.svc/GetLogFile ",
		"POST /ServiceModel/AppInstallerService.svc/RestartApp {}",
	}
	if got := stand.recorded(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("requests\n%s", strings.Join(got, "\n"))
	}
	text := strings.Join(log.lines, "\n")
	for _, expected := range []string{"None: Uploading...", "Info: http://example", "Info: Package backup skipped.",
		"None: Install CreatioPackages.zip ...", "None: Package installation finished\n", "None: Restart application..."} {
		if !strings.Contains(text, expected) {
			t.Fatalf("missing %q in\n%s", expected, text)
		}
	}
}

func TestInstallWorkspaceArchiveReportsFailureWithoutRestart(t *testing.T) {
	stand := newWorkspaceSyncStand(t, func(w http.ResponseWriter, r *http.Request, body string) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/InstallAppFromFile"):
			_, _ = io.WriteString(w, `{"success":false,"errorInfo":{"errorCode":"E1","message":"broken"}}`)
		case strings.HasSuffix(r.URL.Path, "/GetLogFile"):
			_, _ = io.WriteString(w, "log\n")
		case strings.HasSuffix(r.URL.Path, "/GetCurrentUserInfo"):
			w.WriteHeader(http.StatusForbidden)
		default:
			_, _ = io.WriteString(w, `{"success":true}`)
		}
	})
	log := &workspaceSyncTestLog{}
	success, err := stand.client(t).InstallWorkspaceArchive(context.Background(), WorkspaceInstallRequest{
		ArchivePath: workspaceSyncArchive(t), CreateBackup: true}, log)
	if err != nil || success {
		t.Fatalf("install %v %v", success, err)
	}
	text := strings.Join(log.lines, "\n")
	if !strings.Contains(text, "Error: Package installation failed: E1: broken") || strings.Contains(text, "Restart application") ||
		!strings.Contains(text, "None: Backup completed") ||
		!strings.Contains(text, "Warning: The request that starts the installation session answered HTTP 403.") {
		t.Fatalf("log\n%s", text)
	}
	for _, request := range stand.recorded() {
		if strings.Contains(request, "RestartApp") {
			t.Fatal("restarted after a failed installation")
		}
	}
	if !strings.Contains(strings.Join(stand.recorded(), "\n"), `CreatePackageBackup {"Name":"CreatioPackages","Code":"CreatioPackages","ZipPackageName":"CreatioPackages.zip","LastUpdate":0}`) {
		t.Fatalf("no backup request: %v", stand.recorded())
	}
}

func TestInstallWorkspaceArchiveRefusesWebFarmBeforeUpload(t *testing.T) {
	stand := newWorkspaceSyncStand(t, func(w http.ResponseWriter, r *http.Request, body string) {
		if strings.HasSuffix(r.URL.Path, "/GetCurrentUserInfo") {
			http.SetCookie(w, &http.Cookie{Name: "StickySession", Value: "design", Path: "/"})
		}
		_, _ = io.WriteString(w, `{"success":true}`)
	})
	_, err := stand.client(t).InstallWorkspaceArchive(context.Background(), WorkspaceInstallRequest{ArchivePath: workspaceSyncArchive(t)}, &workspaceSyncTestLog{})
	if err == nil || !strings.Contains(err.Error(), "Nothing was uploaded") {
		t.Fatalf("web farm %v", err)
	}
	for _, request := range stand.recorded() {
		if strings.Contains(request, "UploadPackage") {
			t.Fatal("uploaded to a web farm")
		}
	}
}

func TestInstallWorkspaceArchiveInvalidArchiveIsRaised(t *testing.T) {
	stand := newWorkspaceSyncStand(t, func(w http.ResponseWriter, r *http.Request, body string) {
		if strings.HasSuffix(r.URL.Path, "/InstallAppFromFile") {
			_, _ = io.WriteString(w, `{"success":false,"errorInfo":{"errorCode":"InvalidGZipArchiveException","message":"bad gzip"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"success":true}`)
	})
	_, err := stand.client(t).InstallWorkspaceArchive(context.Background(), WorkspaceInstallRequest{ArchivePath: workspaceSyncArchive(t)}, &workspaceSyncTestLog{})
	var invalid WorkspaceInvalidArchive
	if err == nil || err.Error() != "bad gzip" || !errorsAs(err, &invalid) {
		t.Fatalf("invalid archive %v", err)
	}
	if _, err := workspaceSyncParseResponse([]byte("<html>")); err == nil ||
		err.Error() != "Unexpected character encountered while parsing value: <. Path '', line 0, position 0." {
		t.Fatalf("html answer %v", err)
	}
}

func errorsAs(err error, target *WorkspaceInvalidArchive) bool {
	value, ok := err.(WorkspaceInvalidArchive)
	if ok {
		*target = value
	}
	return ok
}

func TestWorkspaceSyncDownloadsAndPing(t *testing.T) {
	stand := newWorkspaceSyncStand(t, func(w http.ResponseWriter, r *http.Request, body string) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/GetZipPackages") && body == `["UsrPkg"]`:
			_, _ = io.WriteString(w, "zip-bytes")
		case strings.HasSuffix(r.URL.Path, "/GetAutogeneratedFolder"):
			w.WriteHeader(http.StatusNotFound)
		default:
			_, _ = io.WriteString(w, "ok")
		}
	})
	client := stand.client(t)
	if !client.PingApplication(context.Background()) {
		t.Fatal("ping failed")
	}
	target := filepath.Join(t.TempDir(), "UsrPkg.zip")
	if err := client.DownloadPackageArchive(context.Background(), "Usr Pkg", target); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(target); string(data) != "zip-bytes" {
		t.Fatalf("download %q", data)
	}
	err := client.DownloadAutogeneratedFolder(context.Background(), "UsrPkg", filepath.Join(t.TempDir(), "a.gz"))
	if err == nil || err.Error() != "The remote server returned an error: (404) Not Found." {
		t.Fatalf("autogenerated %v", err)
	}
	if got := stand.recorded(); got[0] != "GET / " || got[1] != `POST /ServiceModel/PackageInstallerService.svc/GetZipPackages ["UsrPkg"]` ||
		got[2] != `POST /rest/CreatioApiGateway/GetAutogeneratedFolder "UsrPkg"` {
		t.Fatalf("requests %v", got)
	}
	if got := workspaceSyncSafeName("A, B"); got != `A","B` {
		t.Fatalf("safe name %q", got)
	}
	refused, err := NewClient(Config{BaseURL: "http://127.0.0.1:1", Login: "example-user", Password: "replace-me", IsNetCore: true})
	if err != nil {
		t.Fatal(err)
	}
	if refused.PingApplication(context.Background()) {
		t.Fatal("a ping whose login gets no answer must fail")
	}
	if err := refused.ResetSchemaChangeState(context.Background(), "UsrPkg"); err == nil || !strings.Contains(err.Error(), "Connection refused (127.0.0.1:1)") {
		t.Fatalf("transport text %v", err)
	}
}
