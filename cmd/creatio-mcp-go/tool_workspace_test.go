package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/workspace"
)

// offlineWorkspaceNuGet keeps create-workspace's CreatioSDK lookup off api.nuget.org.
func offlineWorkspaceNuGet(t *testing.T) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"items":[{"items":[{"catalogEntry":{"version":"8.1.3.3000"}}]}]}`))
	}))
	previous := workspace.NuGetBaseURL
	workspace.NuGetBaseURL = server.URL
	t.Cleanup(func() { workspace.NuGetBaseURL = previous; server.Close() })
}

func TestWorkspaceToolsThroughClioRun(t *testing.T) {
	offlineWorkspaceNuGet(t)
	fake := newPkgWriteFake(t, map[string]func(string) (int, string){
		"/DataService/json/SyncReply/QuerySysSettings": func(string) (int, string) {
			return 200, `{"success":true,"values":{"SchemaNamePrefix":{"value":"Abc"}}}`
		},
		"/DataService/json/SyncReply/SelectQuery": func(string) (int, string) { return 200, `{"success":true,"rows":[]}` },
	})
	session := pkgWriteSession(t, fake, map[string]any{"Maintainer": "Acme"})
	base := t.TempDir()
	root := filepath.Join(base, "ws")

	var answer creatio.CommandResult
	pkgWriteCall(t, session, "create-workspace", map[string]any{"workspaceName": "ws", "directory": base}, &answer)
	if answer.ExitCode != 0 || answer.Messages[0].Value != "Workspace created at: "+root {
		t.Fatalf("create-workspace = %+v", answer)
	}
	pkgWriteAssertGated(t, session, "create-workspace", map[string]any{"workspaceName": "w2", "directory": base})

	pkgWriteCall(t, session, "add-package", map[string]any{"name": "App1", "as-app": true, "environment-name": "parity-env", "workspace-path": root}, &answer)
	if answer.ExitCode != 0 || !strings.Contains(answer.Messages[1].Value, "Using schema-name prefix 'Abc'") {
		t.Fatalf("add-package = %+v", answer)
	}
	descriptor, err := os.ReadFile(filepath.Join(root, "packages", "App1", "descriptor.json"))
	if err != nil || !strings.Contains(string(descriptor), `"Maintainer": "Acme"`) {
		t.Fatalf("descriptor.json = %s %v", descriptor, err)
	}
	pkgWriteCall(t, session, "add-package", map[string]any{"name": "App2", "environment-name": "missing-env", "workspace-path": root}, &answer)
	if answer.ExitCode != 1 || !strings.HasPrefix(answer.Messages[0].Value, "[EnvironmentResolutionException] Environment with key 'missing-env' not found.") {
		t.Fatalf("unknown environment = %+v", answer)
	}

	pkgWriteCall(t, session, "new-test-project", map[string]any{"package-name": "App1", "workspace-path": root, "environment-name": "parity-env"}, &answer)
	if answer.ExitCode != 0 {
		t.Fatalf("new-test-project = %+v", answer)
	}
	pkgWriteCall(t, session, "new-integration-test-project", map[string]any{"package-name": "App1", "workspace-path": root}, &answer)
	if answer.ExitCode != 0 || !strings.HasSuffix(answer.Messages[len(answer.Messages)-1].Value, "App1.IntegrationTests.csproj") {
		t.Fatalf("new-integration-test-project = %+v", answer)
	}

	for args, want := range map[string]string{
		"":         "workspaceDirectory is required and must be an absolute path to an existing clio workspace.",
		"relative": "workspaceDirectory must be a fully-qualified absolute path.",
		base:       "workspaceDirectory is not a clio workspace (missing '.clio/workspaceSettings.json'): '" + base + "'.",
		root:       "packageName must be a simple identifier matching '^[A-Za-z0-9_]+$'.",
	} {
		pkgWriteCall(t, session, "new-ui-project", map[string]any{"workspaceDirectory": args, "projectName": "p", "packageName": "../x", "vendorPrefix": "usr"}, &answer)
		if answer.ExitCode != 1 || !strings.HasPrefix(answer.Messages[0].Value, want) {
			t.Fatalf("new-ui-project %q = %+v", args, answer)
		}
	}
	pkgWriteCall(t, session, "new-ui-project", map[string]any{"workspaceDirectory": root, "projectName": "my_module", "packageName": "App1", "vendorPrefix": "usr"}, &answer)
	if answer.ExitCode != 0 || !workspaceTestDirectoryExists(filepath.Join(root, "projects", "my_module")) {
		t.Fatalf("new-ui-project = %+v", answer)
	}
}

func workspaceTestDirectoryExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
