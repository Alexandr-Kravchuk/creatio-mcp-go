package toolkit

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// stubCLI is a fake agent CLI that records its arguments in $HOME/<name>.log and answers like the real one:
// removing a marketplace that is not registered fails with "not found", adding one twice fails as
// "already registered", and STUB_FAIL=<name> makes `plugin install` fail.
const stubCLI = `#!/bin/sh
name=$(basename "$0")
echo "$*" >> "$HOME/$name.log"
state="$HOME/.$name-marketplace"
case "$1 $2 $3" in
"plugin marketplace remove")
  if [ -f "$state" ]; then rm "$state"; exit 0; fi
  echo "Marketplace 'creatio' not found" >&2; exit 1;;
"plugin marketplace add")
  if [ -f "$state" ]; then echo 'Error: marketplace "creatio" already registered' >&2; exit 1; fi
  echo "$4" > "$state"; exit 0;;
esac
if [ "$STUB_FAIL" = "$name" ] && [ "$1 $2" = "plugin install" ]; then echo "network unreachable" >&2; exit 3; fi
exit 0
`

func testEnvironment(t *testing.T, agents ...string) Environment {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the stub agent CLIs are POSIX shell scripts")
	}
	home := t.TempDir()
	bin := t.TempDir()
	for _, name := range []string{"claude", "codex", "copilot"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(stubCLI), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, agent := range agents {
		if err := os.MkdirAll(filepath.Join(home, "."+agent), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home) // the stubs log under $HOME; never the real home
	return Environment{Home: home, ClioHome: filepath.Join(home, "clio-home"), Path: bin + string(os.PathListSeparator) + "/usr/bin:/bin"}
}

func readText(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestRunValidatesLikeClio(t *testing.T) {
	env := testEnvironment(t)
	cases := map[string]Result{
		"bogus": {ExitCode: 1, Summary: "Unknown target 'bogus'. Valid agents: claude, codex, copilot, cursor."},
	}
	for target, expected := range cases {
		if got := Run(env, Install, target, ""); got.ExitCode != expected.ExitCode || got.Summary != expected.Summary || len(got.Outcomes) != 0 {
			t.Errorf("%s: %+v", target, got)
		}
	}
	for repository, message := range map[string]string{
		"-x":      "Invalid --repo value '-x': must not start with '-' (it would be parsed as a command-line option).",
		"ext::sh": "Invalid --repo value 'ext::sh': git transport helpers (e.g. 'ext::', 'fd::') are not allowed.",
		"ftp://x": "Invalid --repo value 'ftp://x': unsupported URL scheme 'ftp'. Allowed: https, http, ssh, git, file (or a local path).",
	} {
		if got := Run(env, Install, "", repository); got.ExitCode != 1 || got.Summary != message {
			t.Errorf("%s: %+v", repository, got)
		}
	}
	if got := Run(env, Install, "", ""); got.ExitCode != 0 || got.Summary != "No agents were installed." || len(got.Outcomes) != 4 ||
		got.Outcomes[0].Message != "Claude Code is not installed; nothing to do." {
		t.Errorf("nothing detected: %+v", got)
	}
}

func TestMarketplaceAgentsUseTheirCLIs(t *testing.T) {
	env := testEnvironment(t, "claude", "codex", "copilot")
	settings := filepath.Join(env.Home, ".claude", "settings.json")
	if err := os.WriteFile(settings, []byte(`{"theme": "dark", "note": "<&>", "extraKnownMarketplaces": {"creatio": {"source": {"source": "directory"}}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	codexConfig := filepath.Join(env.Home, ".codex", "config.toml")
	if err := os.WriteFile(codexConfig, []byte("model = \"o3\"\n\n[marketplaces.creatio]\nurl = \"x\"\n\n[profiles.keep]\nmodel = \"y\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result := Run(env, Install, "", "")
	if result.ExitCode != 0 || result.Summary != "Installed creatio-ai-app-development-toolkit for: claude, codex, copilot." {
		t.Fatalf("install: %+v", result)
	}
	if log := readText(t, filepath.Join(env.Home, "claude.log")); log != "plugin marketplace remove creatio\nplugin marketplace add "+MarketplaceGitURL+"\nplugin install "+PluginSource+"\n" {
		t.Fatalf("claude calls:\n%s", log)
	}
	expectedSettings := "{\n  \"theme\": \"dark\",\n  \"note\": \"\\u003C\\u0026\\u003E\",\n  \"extraKnownMarketplaces\": {\n    \"creatio\": {\n      \"autoUpdate\": true\n    }\n  }\n}\n"
	if got := readText(t, settings); got != expectedSettings {
		t.Fatalf("claude settings:\n%s", got)
	}
	toml := readText(t, codexConfig)
	if strings.Contains(toml, "[marketplaces.creatio]") || !strings.Contains(toml, "[profiles.keep]") ||
		!strings.HasSuffix(toml, "\n# Added by clio installer.\n[mcp_servers.clio]\ncommand = \"clio\"\nargs = [\"mcp-server\"]\nenabled = true\n") {
		t.Fatalf("codex config:\n%s", toml)
	}
	manifest := readText(t, filepath.Join(env.Home, ".clio", "managed-skills.json"))
	for _, agent := range []string{`"claude"`, `"codex"`, `"copilot"`, `"Source": "` + MarketplaceGitURL + `"`} {
		if !strings.Contains(manifest, agent) {
			t.Fatalf("manifest lacks %s:\n%s", agent, manifest)
		}
	}
	// copilot does not pre-remove: a second install meets "already registered" and recovers.
	if result := Run(env, Update, "copilot", ""); result.ExitCode != 0 {
		t.Fatalf("copilot update: %+v", result)
	}
	if log := readText(t, filepath.Join(env.Home, "copilot.log")); !strings.Contains(log, "plugin marketplace remove creatio --force\n") {
		t.Fatalf("copilot did not recover from a registered marketplace:\n%s", log)
	}
	t.Setenv("STUB_FAIL", "claude")
	failed := Run(env, Install, "claude", "")
	if failed.ExitCode != 1 || failed.Outcomes[0].Status != Failed || failed.Summary != "No agents were installed." ||
		failed.Outcomes[0].Message != "claude plugin install creatio-ai-app-development-toolkit@creatio failed: network unreachable\n" {
		t.Fatalf("claude failure: %+v", failed)
	}
	t.Setenv("STUB_FAIL", "")
	removed := Run(env, Delete, "", "")
	if removed.ExitCode != 0 || removed.Summary != "Removed creatio-ai-app-development-toolkit for: claude, codex, copilot." {
		t.Fatalf("delete: %+v", removed)
	}
	if strings.Contains(readText(t, settings), "creatio") {
		t.Fatal("delete kept the marketplace in claude settings")
	}
	if _, err := os.Stat(filepath.Join(env.Home, ".clio", "managed-skills.json")); err == nil {
		t.Fatal("an empty manifest must be deleted")
	}
	// An agent whose CLI is missing is skipped, not failed.
	env.Path = "/usr/bin:/bin"
	if result := Run(env, Install, "claude", ""); result.Outcomes[0].Status != Skipped ||
		result.Outcomes[0].Message != "Claude Code is detected but 'claude' is not on PATH; skipped." {
		t.Fatalf("missing CLI: %+v", result)
	}
}

func TestCursorInstallsFromALocalCheckout(t *testing.T) {
	env := testEnvironment(t, "cursor")
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	repository := t.TempDir()
	for path, text := range map[string]string{".release-manifest.json": `{"plugin_runtime": ["AGENTS.md", "context", "missing.txt"]}`,
		"AGENTS.md": "agents\n", "context/INDEX.md": "index\n", "context/__pycache__/a.pyc": "c"} {
		full := filepath.Join(repository, filepath.FromSlash(path))
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		_ = os.WriteFile(full, []byte(text), 0o644)
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}, {"-c", "user.email=t@example.com", "-c", "user.name=t", "commit", "-qm", "init"}} {
		command := exec.Command("git", args...)
		command.Dir = repository
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, output)
		}
	}
	result := Run(env, Install, "cursor", repository)
	if result.ExitCode != 0 || result.Outcomes[0].Message != "Installed creatio-ai-app-development-toolkit for Cursor." {
		t.Fatalf("install: %+v", result)
	}
	plugin := filepath.Join(env.Home, ".cursor", "plugins", "local", PluginName)
	if readText(t, filepath.Join(plugin, "AGENTS.md")) != "agents\n" {
		t.Fatal("AGENTS.md not copied")
	}
	// clio 8.1.0.142 copies a runtime directory through an inverted filter: only __pycache__ survives.
	if _, err := os.Stat(filepath.Join(plugin, "context", "INDEX.md")); err == nil {
		t.Fatal("expected clio's directory copy, which skips regular files")
	}
	if _, err := os.Stat(filepath.Join(plugin, "context", "__pycache__", "a.pyc")); err != nil {
		t.Fatal("expected clio's directory copy, which keeps __pycache__")
	}
	if mcp := readText(t, filepath.Join(env.Home, ".cursor", "mcp.json")); mcp != "{\n  \"mcpServers\": {\n    \"clio\": {\n      \"command\": \"clio\",\n      \"args\": [\n        \"mcp-server\"\n      ]\n    }\n  }\n}\n" {
		t.Fatalf("mcp.json:\n%s", mcp)
	}
	rule := readText(t, filepath.Join(env.Home, ".cursor", "rules", SkillName+".mdc"))
	if !strings.Contains(rule, "Toolkit repository is installed at: `"+plugin+"`") {
		t.Fatalf("rule:\n%s", rule)
	}
	missing := Run(env, Install, "cursor", filepath.Join(env.Home, "no-such-repo.git"))
	if missing.ExitCode != 1 || !strings.HasPrefix(missing.Outcomes[0].Message, "Cursor: Unable to clone skills repository '") {
		t.Fatalf("missing repository: %+v", missing)
	}
	if removed := Run(env, Delete, "cursor", ""); removed.ExitCode != 0 {
		t.Fatalf("delete: %+v", removed)
	}
	if _, err := os.Stat(plugin); err == nil {
		t.Fatal("delete kept the plugin directory")
	}
}
