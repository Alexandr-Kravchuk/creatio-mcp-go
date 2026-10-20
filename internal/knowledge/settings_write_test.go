package knowledge

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const settingsSample = `{
  "creatio-products": "C:\\CreatioProductBuild",
  "Environments": {
    "dev": {
      "Uri": "http://localhost:80",
      "Login": "Supervisor",
      "Password": "secret",
      "Safe": false,
      "custom-number": 1.50,
      "unicode": "Привіт <&>"
    }
  },
  "knowledge-root-path": "LEGACY",
  "features": {
    "Ring": true
  },
  "unknown-future-section": [1, {"a": null}, []]
}`

// TestSettingsWritePreservesEverythingElse edits one section and checks that the rest of the file is
// clio's own content, in clio's order, with numbers and escapes as they were.
func TestSettingsWritePreservesEverythingElse(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "appsettings.json")
	legacy := filepath.Join(dir, "legacy-root")
	encodedLegacy, _ := json.Marshal(legacy)
	sample := strings.Replace(settingsSample, `"LEGACY"`, string(encodedLegacy), 1)
	if err := os.WriteFile(path, []byte(sample), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := SetFeature(path, "ring", false); err != nil {
		t.Fatal(err)
	}
	if err := SetFeature(path, "new-flag", true); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	text := string(data)
	for _, expected := range []string{`"Ring": false`, `"new-flag": true`, `"custom-number": 1.50`, `"unicode": "Привіт <&>"`,
		`"unknown-future-section": [`, `"knowledge-root-path": ` + string(encodedLegacy), `"creatio-products": "C:\\CreatioProductBuild"`} {
		if !strings.Contains(text, expected) {
			t.Errorf("missing %s in\n%s", expected, text)
		}
	}
	if strings.Index(text, `"creatio-products"`) > strings.Index(text, `"Environments"`) {
		t.Error("key order changed")
	}
	if info, _ := os.Stat(path); runtime.GOOS != "windows" && info.Mode().Perm() != 0o640 {
		t.Errorf("permissions changed to %v", info.Mode().Perm())
	}
	if strings.HasSuffix(text, "\n") {
		t.Error("Newtonsoft writes no trailing newline")
	}
	// A knowledge write migrates the legacy root and drops the legacy key, as clio's knowledge writes do.
	root, err := EnsureRootPath(path, filepath.Join(dir, "unused-default"))
	if err != nil || root != legacy {
		t.Fatalf("root %q %v", root, err)
	}
	data, _ = os.ReadFile(path)
	if strings.Contains(string(data), "knowledge-root-path") || !strings.Contains(string(data), `"root-path": `+string(encodedLegacy)) {
		t.Fatalf("legacy root not migrated:\n%s", data)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".tmp") {
			t.Fatalf("temporary file left: %s", entry.Name())
		}
	}
}

// TestSettingsWriteRetriesWhenAnEditorChangesTheFile simulates an editor that does not take clio's lock:
// the first save sees the change, re-reads and re-applies the mutation, so neither edit is lost.
func TestSettingsWriteRetriesWhenAnEditorChangesTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "appsettings.json")
	if err := os.WriteFile(path, []byte(`{"features": {}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	attempts := 0
	err := UpdateSettings(path, func(root *JSONObject) (bool, error) {
		attempts++
		if attempts == 1 {
			// The editor saves between this process's read and its publish.
			if err := os.WriteFile(path, []byte(`{"features": {"editor": true}}`), 0o600); err != nil {
				return false, err
			}
		}
		root.EnsureObject("features").Set("tool", true)
		return true, nil
	})
	if err != nil || attempts != 2 {
		t.Fatalf("attempts %d: %v", attempts, err)
	}
	settings, _ := ReadSettings(path)
	if !settings.Features["editor"] || !settings.Features["tool"] {
		t.Fatalf("an edit was lost: %v", settings.Features)
	}
}

func TestSettingsWriteCreatesAMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "appsettings.json")
	keyDir, _ := filepath.EvalSymlinks(t.TempDir()) // macOS temp dirs sit behind /var, a link clio refuses
	keyPath := filepath.Join(keyDir, "k.pem")
	encodedKey, _ := json.Marshal(keyPath)
	source, err := ValidateSourceInput(SourceInput{LibraryID: "com.example.kb", Type: SourceNuGet, Location: "https://feed.example/v3/index.json",
		TrustedKeyID: ptr("k"), TrustedPublicKeyPath: ptr(keyPath), PackageID: ptr("Example.KB"), Enabled: true,
		Participation: ParticipationSupplement})
	if err != nil {
		t.Fatal(err)
	}
	if added, err := TryAddSource(path, "kb", source); err != nil || !added {
		t.Fatalf("add: %v %v", added, err)
	}
	data, _ := os.ReadFile(path)
	expected := `"kb": {
        "library-id": "com.example.kb",
        "type": "nuget",
        "location": "https://feed.example/v3/index.json",
        "trusted-key-id": "k",
        "trusted-public-key-path": ` + string(encodedKey) + `,
        "package-id": "Example.KB",
        "enabled": true,
        "priority": 0,
        "participation": "supplement"
      }`
	if !strings.Contains(strings.ReplaceAll(string(data), "\r\n", "\n"), expected) {
		t.Fatalf("source not written as clio writes it:\n%s", data)
	}
	if info, _ := os.Stat(path); runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("a new settings file must be owner-only, got %v", info.Mode().Perm())
	}
}

// TestSettingsLockHelperProcess holds appsettings.json.lock for TestSettingsWriteWaitsForAnotherProcess.
func TestSettingsLockHelperProcess(t *testing.T) {
	path := os.Getenv("SETTINGS_LOCK_HELPER")
	if path == "" {
		t.Skip("helper process only")
	}
	lock, err := acquireFileLock(path+".lock", time.Second, 10*time.Millisecond)
	if err != nil {
		os.Exit(2)
	}
	fmt.Println("locked")
	time.Sleep(time.Second)
	// While holding the lock this process rewrites the file, as clio does inside its lock.
	_ = os.WriteFile(path, []byte(`{"features": {"other-process": true}}`), 0o600)
	lock.Unlock()
	os.Exit(0)
}

func TestSettingsWriteWaitsForAnotherProcess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "appsettings.json")
	if err := os.WriteFile(path, []byte(`{"features": {}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	helper := exec.Command(os.Args[0], "-test.run=^TestSettingsLockHelperProcess$")
	helper.Env = append(os.Environ(), "SETTINGS_LOCK_HELPER="+path)
	output, _ := helper.StdoutPipe()
	if err := helper.Start(); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 32)
	if n, _ := output.Read(buffer); !strings.Contains(string(buffer[:n]), "locked") {
		t.Fatalf("helper: %q", buffer[:n])
	}
	if err := SetFeature(path, "this-process", true); err != nil {
		t.Fatal(err)
	}
	_ = helper.Wait()
	settings, _ := ReadSettings(path)
	if !settings.Features["other-process"] || !settings.Features["this-process"] {
		t.Fatalf("the write did not wait for the other process: %v", settings.Features)
	}
}
