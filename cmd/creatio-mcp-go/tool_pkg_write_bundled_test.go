package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
)

// pkgWriteFakeClioTool lays out a clio .NET tool store under a temporary home with the given archives
// (folder/file -> content) and points the locator at it.
func pkgWriteFakeClioTool(t *testing.T, version string, archives map[string]string) {
	t.Helper()
	home := t.TempDir()
	root := filepath.Join(home, ".dotnet", "tools", ".store", "clio", version, "clio", version, "tools", "net10.0", "any")
	for path, content := range archives {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	previous := pkgWriteClioHome
	pkgWriteClioHome = home
	t.Cleanup(func() { pkgWriteClioHome = previous })
}

func TestBundledArchiveLocatorPicksTheNewestClio(t *testing.T) {
	pkgWriteFakeClioTool(t, "8.1.0.9", map[string]string{"cliogate/cliogate.gz": "old"})
	newer := filepath.Join(pkgWriteClioHome, ".dotnet", "tools", ".store", "clio", "8.1.0.10", "clio", "8.1.0.10", "tools", "net8.0", "any", "cliogate")
	if err := os.MkdirAll(newer, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(newer, "cliogate.gz"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if found := pkgWriteBundledArchive("cliogate", "cliogate.gz"); !strings.Contains(found, "8.1.0.10") {
		t.Fatalf("found %s", found)
	}
	if found := pkgWriteBundledArchive("cliogate", "cliogate_netcore.gz"); found != "" {
		t.Fatalf("absent archive found as %s", found)
	}
}

func TestInstallGateAndDashboardsMigratorRefusals(t *testing.T) {
	pkgWriteFakeClioTool(t, "8.1.0.134", nil)
	fake := newPkgWriteFake(t, nil)
	session := pkgWriteSession(t, fake, nil)
	for _, tool := range []string{"install-gate", "install-dashboards-migrator"} {
		var answer creatio.CommandResult
		pkgWriteCall(t, session, tool, map[string]any{"environment-name": "parity-env"}, &answer)
		if answer.ExitCode != 1 || !strings.HasPrefix(answer.Messages[0].Value, "The bundled ") ||
			!strings.Contains(answer.Messages[0].Value, "no clio .NET tool installation was found") {
			t.Fatalf("%s without clio = %+v", tool, answer)
		}
		pkgWriteCall(t, session, tool, map[string]any{"environment-name": "missing-env"}, &answer)
		if answer.ExitCode != 1 || !strings.HasPrefix(answer.Messages[0].Value, "[EnvironmentResolutionException] Environment with key 'missing-env' not found.") {
			t.Fatalf("%s unknown = %+v", tool, answer)
		}
		pkgWriteCall(t, session, tool, map[string]any{}, &answer)
		if answer.ExitCode != 1 || answer.Messages[0].Value != "environment-name is required and cannot be empty." {
			t.Fatalf("%s blank = %+v", tool, answer)
		}
		typed := pkgWriteCall(t, session, tool, map[string]any{"environment-name": true}, nil)
		if !typed.IsError || !strings.Contains(pkgWriteText(typed), "must be a string") {
			t.Fatalf("%s typed = %s", tool, pkgWriteText(typed))
		}
		pkgWriteAssertGated(t, session, tool, map[string]any{"environment-name": "parity-env"})
	}
	if calls := fake.recorded(); len(calls) != 0 {
		t.Fatalf("refusals must not reach Creatio: %q", calls)
	}
}

func TestInstallGateThroughClioRun(t *testing.T) {
	pkgWriteFakeClioTool(t, "8.1.0.134", map[string]string{"cliogate/cliogate.gz": "not a gzip archive, uploaded as is"})
	fake := newPkgWriteFake(t, map[string]func(string) (int, string){
		"/UploadPackage":   func(string) (int, string) { return 200, `{"success":true}` },
		"/CreateBackup":    func(string) (int, string) { return 200, `{"success":true}` },
		"/GetLogFile":      func(string) (int, string) { return 200, "" },
		"/InstallPackage":  func(string) (int, string) { return 200, `{"success":true}` },
		"/UnloadAppDomain": func(string) (int, string) { return 200, `` },
	})
	session := pkgWriteSession(t, fake, nil)
	var answer creatio.CommandResult
	pkgWriteCall(t, session, "install-gate", map[string]any{"environment-name": "parity-env"}, &answer)
	if answer.ExitCode != 0 || answer.Messages[len(answer.Messages)-2].Value != "Done" || answer.Messages[len(answer.Messages)-1].Value != "Restart application..." {
		t.Fatalf("install-gate = %+v", answer)
	}
	if !pkgWriteHasCall(fake.recorded(), `POST /0/ServiceModel/PackageInstallerService.svc/InstallPackage "cliogate.gz"`) {
		t.Fatalf("calls = %q", fake.recorded())
	}
}
