package creatio

import (
	"os"
	"path/filepath"
	"testing"
)

// The live write run (T23) compared against a clio installed with --tool-path, while ~/.dotnet/tools held an
// older clio: install-process-builder then refused, reading the older clio's CrtProcessBuilder version. The
// override points every bundled-archive lookup at the compared clio.
func TestClioToolsDirectoryOverrideSelectsTheBundledArchive(t *testing.T) {
	tools := t.TempDir()
	archive := filepath.Join(tools, ".store", "clio", "8.1.0.142", "clio", "8.1.0.142", "tools", "net10.0", "any",
		processBuilderPackageName, processBuilderPackageName+".gz")
	if err := os.MkdirAll(filepath.Dir(archive), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(archive, []byte("archive"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(ClioToolsDirectoryVariable, tools)
	if got := ClioToolsDirectory(); got != tools {
		t.Fatalf("ClioToolsDirectory() = %q, want %q", got, tools)
	}
	if got := ProcessWriteArchivePath(); got != archive {
		t.Fatalf("ProcessWriteArchivePath() = %q, want %q", got, archive)
	}
}

func TestClioToolsDirectoryDefaultsToDotnetTools(t *testing.T) {
	t.Setenv(ClioToolsDirectoryVariable, "")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip(err)
	}
	if got, want := ClioToolsDirectory(), filepath.Join(home, ".dotnet", "tools"); got != want {
		t.Fatalf("ClioToolsDirectory() = %q, want %q", got, want)
	}
}
