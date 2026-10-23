package main

import (
	"archive/zip"
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/workspace"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// workspaceSyncTestSession serves the tools against a fake Creatio site registered as environment "fake";
// requests lists every request but the login, uploads holds the uploaded bytes.
func workspaceSyncTestSession(t *testing.T, handler func(w http.ResponseWriter, r *http.Request)) (*mcp.ClientSession, *[]string, *bytes.Buffer) {
	t.Helper()
	clearCreatioVariables(t)
	temp := t.TempDir()
	for _, name := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(name, temp)
	}
	t.Setenv("CLIO_WORKING_DIRECTORY", "")
	var mu sync.Mutex
	var requests []string
	var uploads bytes.Buffer
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/AuthService.svc/Login") {
			_, _ = io.WriteString(w, `{"Code":0}`)
			return
		}
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		if strings.HasSuffix(r.URL.Path, "/UploadPackage") {
			uploads.Write(body)
			body = []byte("<archive>")
		}
		line := r.Method + " " + r.URL.Path + " " + string(body)
		if !(strings.HasSuffix(r.URL.Path, "/GetLogFile") && len(requests) > 0 && requests[len(requests)-1] == line) {
			requests = append(requests, line)
		}
		mu.Unlock()
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	path := filepath.Join(t.TempDir(), "appsettings.json")
	writeSettings(t, path, "fake", map[string]map[string]any{
		"fake": {"Uri": server.URL, "Login": "example-user", "Password": "replace-me", "IsNetCore": true},
	})
	session := connectTestClient(t, newMCPServerWithHiddenTools(newEnvironments(path), defaultHiddenToolServices()),
		mcp.NewClient(&mcp.Implementation{Name: "workspace-sync-probe", Version: "test"}, nil))
	return session, &requests, &uploads
}

func workspaceSyncTestWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func workspaceSyncTestMessages(answer map[string]any) string {
	var lines []string
	messages, _ := answer["execution-log-messages"].([]any)
	for _, message := range messages {
		entry := message.(map[string]any)
		lines = append(lines, entry["message-type"].(string)+": "+entry["value"].(string))
	}
	return strings.Join(lines, "\n")
}

func TestWorkspaceSyncToolsRefuseBeforeReachingCreatio(t *testing.T) {
	session, requests, _ := workspaceSyncTestSession(t, func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	folder := t.TempDir()
	raw, _ := dataWriteTestCall(t, session, "", "push-workspace", map[string]any{"environment-name": "fake", "workspace-path": folder})
	if !raw.IsError || !strings.Contains(dataWriteTestText(raw), "was NOT executed") {
		t.Fatalf("raw call escaped the write gate: %s", dataWriteTestText(raw))
	}
	for _, tool := range []string{"push-workspace", "restore-workspace"} {
		for _, test := range []struct {
			args map[string]any
			want string
		}{
			{map[string]any{"environment-name": "fake"}, "Error: Workspace path is required."},
			{map[string]any{"environment-name": "fake", "workspace-path": "//server/share"}, "Error: Workspace path must be a local absolute path: //server/share"},
			{map[string]any{"environment-name": "fake", "workspace-path": "relative/ws"}, "Error: Workspace path must be absolute: relative/ws"},
			{map[string]any{"environment-name": "fake", "workspace-path": filepath.Join(folder, "missing")}, "Error: Workspace path not found: " + filepath.Join(folder, "missing")},
			{map[string]any{"workspace-path": folder}, "Error: [EnvironmentResolutionException] Either a configured environment name or an explicit URI is required"},
			{map[string]any{"environment-name": "nope", "workspace-path": folder}, "Error: [EnvironmentResolutionException] Environment with key 'nope' not found."},
			{map[string]any{"Environment-Name": "fake", "WORKSPACE-PATH": folder}, "Error: Json file not found by path: '" + filepath.Join(folder, ".clio", "workspaceSettings.json") + "'"},
		} {
			for _, executor := range []string{"clio-run", "clio-run-destructive"} {
				result, answer := dataWriteTestCall(t, session, executor, tool, test.args)
				if result.IsError || answer["exit-code"] != float64(1) || !strings.Contains(workspaceSyncTestMessages(answer), test.want) {
					t.Fatalf("%s %s %v: %s", executor, tool, test.args, dataWriteTestText(result))
				}
			}
		}
	}
	bad, _ := dataWriteTestCall(t, session, "clio-run", "push-workspace", map[string]any{"environment-name": "fake", "workspace-path": "", "skip-backup": "yes"})
	if !bad.IsError || !strings.Contains(dataWriteTestText(bad), "invalid-parameter-type: argument 'skip-backup' for MCP tool 'push-workspace' must be a boolean.") {
		t.Fatalf("binding %s", dataWriteTestText(bad))
	}
	result, _ := dataWriteTestCall(t, session, "clio-run", "push-workspace", map[string]any{"environment-name": "nope", "workspace-path": folder})
	if text := dataWriteTestText(result); !strings.Contains(text, "<url>") || strings.Contains(text, `\u003c`) {
		t.Fatalf("the envelope escapes '<': %s", text)
	}
	if len(*requests) != 0 {
		t.Fatalf("a refused call reached Creatio: %v", *requests)
	}
}

func TestPushWorkspaceInstallsThePackedWorkspace(t *testing.T) {
	var installed sync.Map
	session, requests, uploads := workspaceSyncTestSession(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/InstallAppFromFile"):
			installed.Store("done", true)
			_, _ = io.WriteString(w, `{"success":true}`)
		case strings.HasSuffix(r.URL.Path, "/GetLogFile"):
			if _, done := installed.Load("done"); done {
				_, _ = io.WriteString(w, "start\nPackage installation finished\n")
				return
			}
			_, _ = io.WriteString(w, "start\n")
		default:
			_, _ = io.WriteString(w, `{"success":true}`)
		}
	})
	root := t.TempDir()
	workspaceSyncTestWrite(t, filepath.Join(root, ".clio", "workspaceSettings.json"), `{"Packages":["UsrPkg","UsrIgnored"],"IgnorePackages":["UsrIgnored"]}`)
	workspaceSyncTestWrite(t, filepath.Join(root, "packages", "UsrPkg", "descriptor.json"), `{"Descriptor":{"Name":"UsrPkg"}}`)
	workspaceSyncTestWrite(t, filepath.Join(root, "packages", "UsrPkg", "Schemas", "UsrA", "descriptor.json"), "{}")
	workspaceSyncTestWrite(t, filepath.Join(root, "packages", "UsrPkg", "Schemas", "UsrA", "UsrA.js"),
		`define("UsrA", [], function() { return { viewConfigDiff: /**SCHEMA_VIEW_CONFIG_DIFF*/[{"name": "L", "values": {"caption": "Text"}}]/**SCHEMA_VIEW_CONFIG_DIFF*/ }; });`)
	result, answer := dataWriteTestCall(t, session, "clio-run", "push-workspace", map[string]any{"environment-name": "fake", "workspace-path": root, "skip-backup": true})
	text := workspaceSyncTestMessages(answer)
	if result.IsError || answer["exit-code"] != float64(0) {
		t.Fatalf("push %s", dataWriteTestText(result))
	}
	for _, want := range []string{"Info: Push workspace...", "Info: Ignored 1 package(s): UsrIgnored",
		"Warning: Page schema 'UsrA' (package 'UsrPkg') sets user-visible text as inline literals: L.caption.",
		"Info: Installing workspace packages using ApplicationInstaller...", "Info: Package backup skipped.",
		"None: Restart application...", "Info: Installation completed successfully.", "Info: Done"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in\n%s", want, text)
		}
	}
	want := strings.Join([]string{
		`POST /rest/CreatioApiGateway/ResetSchemaChangeState {"packageName":"UsrPkg"}`,
		"POST /ServiceModel/UserInfoService.svc/GetCurrentUserInfo {}",
		"POST /ServiceModel/PackageInstallerService.svc/UploadPackage <archive>",
		"GET /ServiceModel/PackageInstallerService.svc/GetLogFile ",
		`POST /ServiceModel/AppInstallerService.svc/InstallAppFromFile {"Name":"CreatioPackages","Code":"CreatioPackages","ZipPackageName":"CreatioPackages.zip","LastUpdateString":0}`,
		"GET /ServiceModel/PackageInstallerService.svc/GetLogFile ",
		"POST /ServiceModel/AppInstallerService.svc/RestartApp {}",
	}, "\n")
	if got := strings.Join(*requests, "\n"); got != want {
		t.Fatalf("requests\n%s", got)
	}
	archive, err := zip.NewReader(bytes.NewReader(uploads.Bytes()), int64(uploads.Len()))
	if err != nil || len(archive.File) != 1 || archive.File[0].Name != "UsrPkg.gz" {
		t.Fatalf("uploaded archive %v %v", archive, err)
	}
	entries, _ := os.ReadDir(os.Getenv("TMPDIR") + string(os.PathSeparator) + "clio")
	if len(entries) != 0 {
		t.Fatalf("temporary folders left behind: %v", entries)
	}
}

func TestRestoreWorkspaceWithoutEligiblePackagesStillPreparesTheWorkspace(t *testing.T) {
	nuget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"items":[{"items":[{"catalogEntry":{"version":"8.1.3.6802"}}]}]}`)
	}))
	t.Cleanup(nuget.Close)
	saved := workspace.NuGetBaseURL
	workspace.NuGetBaseURL = nuget.URL
	t.Cleanup(func() { workspace.NuGetBaseURL = saved })
	session, requests, _ := workspaceSyncTestSession(t, func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	t.Setenv("PATH", t.TempDir()) // no dotnet: the restore step reports that it could not start
	root := t.TempDir()
	workspaceSyncTestWrite(t, filepath.Join(root, ".clio", "workspaceSettings.json"), `{"Packages":[],"ApplicationVersion":"8.1.3"}`)
	workspaceSyncTestWrite(t, filepath.Join(root, "packages", "placeholder.txt"), "")
	if err := os.MkdirAll(filepath.Join(root, ".solution"), 0o755); err != nil {
		t.Fatal(err)
	}
	result, answer := dataWriteTestCall(t, session, "clio-run", "restore-workspace", map[string]any{"environment-name": "fake", "workspace-path": root})
	text := workspaceSyncTestMessages(answer)
	if result.IsError || answer["exit-code"] != float64(0) ||
		!strings.HasPrefix(text, "Warning: No packages are eligible for restore from .clio/workspaceSettings.json; package download was skipped.") ||
		!strings.Contains(text, "None: An error occurred trying to start process 'dotnet' with working directory '") ||
		!strings.HasSuffix(text, "Info: Done") {
		t.Fatalf("restore\n%s", text)
	}
	if _, err := os.Stat(filepath.Join(root, ".solution", "set-netcore-environment.cmd")); err != nil {
		t.Fatal(err)
	}
	if len(*requests) != 0 {
		t.Fatalf("a restore without packages reached Creatio: %v", *requests)
	}
}
