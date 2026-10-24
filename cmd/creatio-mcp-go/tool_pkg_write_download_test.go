package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
)

func TestDownloadConfigurationRefusals(t *testing.T) {
	fake := newPkgWriteFake(t, nil)
	session := pkgWriteSession(t, fake, nil)
	workspace := t.TempDir()
	missing := filepath.Join(workspace, "missing")
	for _, item := range []struct {
		tool string
		args map[string]any
		want string
	}{
		{"download-configuration-by-build", map[string]any{"build-path": "relative.zip", "workspace-path": workspace}, "Build path must be absolute: relative.zip"},
		{"download-configuration-by-build", map[string]any{"workspace-path": workspace}, "Build path must be absolute: "},
		{"download-configuration-by-build", map[string]any{"build-path": missing}, "Workspace path is required."},
		{"download-configuration-by-build", map[string]any{"build-path": missing, "workspace-path": "ws"}, "Workspace path must be absolute: ws"},
		{"download-configuration-by-build", map[string]any{"build-path": missing, "workspace-path": missing}, "Workspace path not found: " + missing},
		{"download-configuration-by-build", map[string]any{"build-path": missing, "workspace-path": workspace}, "Directory not found: " + missing},
		{"download-configuration-by-environment", map[string]any{"environment-name": "parity-env"}, "Workspace path is required."},
		{"download-configuration-by-environment", map[string]any{"environment-name": "parity-env", "workspace-path": "ws"}, "Workspace path must be absolute: ws"},
		{"download-configuration-by-environment", map[string]any{"workspace-path": workspace}, "[EnvironmentResolutionException] " + missingTargetMessage},
		{"download-configuration-by-environment", map[string]any{"environment-name": "missing-env", "workspace-path": workspace},
			"[EnvironmentResolutionException] Environment with key 'missing-env' not found."},
	} {
		var answer creatio.CommandResult
		pkgWriteCall(t, session, item.tool, item.args, &answer)
		if answer.ExitCode != 1 || !strings.HasPrefix(answer.Messages[0].Value, item.want) {
			t.Fatalf("%s %v = %+v", item.tool, item.args, answer)
		}
	}
	pkgWriteAssertGated(t, session, "download-configuration-by-build", map[string]any{"build-path": missing, "workspace-path": workspace})
	typed := pkgWriteCall(t, session, "download-configuration-by-environment", map[string]any{"environment-name": "parity-env", "workspace-path": 7}, nil)
	if !typed.IsError || !strings.Contains(pkgWriteText(typed), "argument 'workspace-path' for MCP tool 'download-configuration-by-environment' must be a string") {
		t.Fatalf("typed = %s", pkgWriteText(typed))
	}
	if calls := fake.recorded(); len(calls) != 0 {
		t.Fatalf("refusals must not reach Creatio: %q", calls)
	}
}

func TestDownloadConfigurationByEnvironmentFromEnvironmentPath(t *testing.T) {
	build := t.TempDir()
	if err := os.MkdirAll(filepath.Join(build, "Terrasoft.Configuration", "Lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(build, "Terrasoft.Core.dll"), []byte("core"), 0o644); err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, ".clio"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".clio", "workspaceSettings.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	fake := newPkgWriteFake(t, nil)
	session := pkgWriteSession(t, fake, map[string]any{"EnvironmentPath": build})
	var answer creatio.CommandResult
	pkgWriteCall(t, session, "download-configuration-by-environment", map[string]any{"environment-name": "parity-env", "workspace-path": workspace}, &answer)
	if answer.ExitCode != 0 || answer.Messages[len(answer.Messages)-1].Value != "Download from "+build+" path completed successfully" {
		t.Fatalf("download = %+v", answer)
	}
	if _, err := os.Stat(filepath.Join(workspace, ".application", "net-core", "core-bin", "Terrasoft.Core.dll")); err != nil {
		t.Fatal(err)
	}
	if calls := fake.recorded(); len(calls) != 0 {
		t.Fatalf("a local EnvironmentPath is read without Creatio: %q", calls)
	}
}
