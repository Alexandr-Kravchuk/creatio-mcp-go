package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// t6WriteSession isolates every location the T6 write tools touch: clio's home and settings, the telemetry
// root (uploading off), the user's home (coding agents) and the component registry (a local file).
func t6WriteSession(t *testing.T) (*mcp.ClientSession, string) {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLIO_HOME", filepath.Join(home, "clio"))
	t.Setenv("HOME", filepath.Join(home, "user"))
	t.Setenv("USERPROFILE", filepath.Join(home, "user"))
	t.Setenv("CLIO_TELEMETRY_HOME", filepath.Join(home, "telemetry"))
	t.Setenv("CLIO_TELEMETRY_ENABLED", "false")
	t.Setenv("CLIO_TELEMETRY_ENDPOINT", "http://127.0.0.1:9/v1/logs")
	t.Setenv("CLIO_COMPONENT_REGISTRY_CDN_BASE_URL", "http://127.0.0.1:9/")
	registry := filepath.Join(home, "registry.json")
	if err := os.WriteFile(registry, []byte(`{"components":[{"componentType":"crt.Button","inputs":{"a":{},"b":{}}},{"componentType":"crt.Label","properties":{"c":{}}}],"composites":[{}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLIO_COMPONENT_REGISTRY_LOCAL_FILE", registry)
	session := connectTestClient(t, newMCPServerWithHiddenTools(nil, defaultHiddenToolServices()),
		mcp.NewClient(&mcp.Implementation{Name: "probe-client", Version: "test"}, nil))
	return session, home
}

// t6Run calls a tool through clio-run and returns its decoded answer.
func t6Run(t *testing.T, session *mcp.ClientSession, tool string, args map[string]any) map[string]any {
	t.Helper()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "clio-run", Arguments: map[string]any{"command": tool, "args": args}})
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	text := result.Content[0].(*mcp.TextContent).Text
	var answer map[string]any
	if err := json.Unmarshal([]byte(text), &answer); err != nil {
		t.Fatalf("%s answered %q", tool, text)
	}
	return answer
}

func TestT6WriteToolsAreGatedAndReachableThroughClioRun(t *testing.T) {
	session, _ := t6WriteSession(t)
	for _, tool := range []string{"install-knowledge", "update-knowledge", "delete-knowledge", "add-knowledge-source", "remove-knowledge-source",
		"enable-knowledge-source", "disable-knowledge-source", "configure-knowledge-feedback-policy", "install-toolkit", "update-toolkit",
		"delete-toolkit", "experimental", "export-component-registry", "send-telemetry", "withdraw-telemetry-consent"} {
		if !requiresConfirmation(tool) {
			t.Errorf("%s is write-capable in clio and must be gated", tool)
		}
		direct, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: map[string]any{}})
		if err != nil || !direct.IsError {
			t.Errorf("%s ran by its raw name: %v", tool, err)
		}
	}
}

func TestKnowledgeSourceToolsWriteClioSettings(t *testing.T) {
	session, home := t6WriteSession(t)
	add := map[string]any{"alias": "kb", "libraryId": "com.example.kb", "type": "github-release", "location": "https://api.github.com",
		"repositoryOwner": "o", "repositoryName": "r", "assetName": "kb.zip"}
	if answer := t6Run(t, session, "add-knowledge-source", add); answer["success"] != false ||
		answer["message"] != "Adding a trusted knowledge source requires explicit confirmation." || answer["sourceAlias"] != "kb" {
		t.Fatalf("unconfirmed add: %v", answer)
	}
	add["confirmed"] = true
	if answer := t6Run(t, session, "add-knowledge-source", add); answer["success"] != true ||
		answer["message"] != "Knowledge source 'kb' was added. Run install-knowledge --source kb to install it." {
		t.Fatalf("add: %v", answer)
	}
	list := t6Run(t, session, "list-knowledge-sources", map[string]any{})
	sources := list["sources"].([]any)
	if len(sources) != 1 || sources[0].(map[string]any)["location"] != "https://api.github.com/" {
		t.Fatalf("list after add: %v", list)
	}
	if answer := t6Run(t, session, "disable-knowledge-source", map[string]any{"alias": "kb"}); answer["message"] != "Knowledge source 'kb' was disabled; its cache was retained." {
		t.Fatalf("disable: %v", answer)
	}
	if answer := t6Run(t, session, "install-knowledge", map[string]any{"source": "kb"}); answer["message"] != "Knowledge source 'kb' is disabled." {
		t.Fatalf("install disabled: %v", answer)
	}
	if answer := t6Run(t, session, "enable-knowledge-source", map[string]any{"alias": "kb"}); answer["success"] != true {
		t.Fatalf("enable: %v", answer)
	}
	if answer := t6Run(t, session, "delete-knowledge", map[string]any{"source": "kb", "confirmed": true}); answer["success"] != false ||
		answer["sources"].([]any)[0].(map[string]any)["status"] != "notinstalled" {
		t.Fatalf("delete before install: %v", answer)
	}
	if answer := t6Run(t, session, "remove-knowledge-source", map[string]any{"alias": "kb", "confirmed": true}); answer["message"] != "Knowledge source 'kb' was removed." {
		t.Fatalf("remove: %v", answer)
	}
	settings, err := os.ReadFile(filepath.Join(home, "clio", "appsettings.json"))
	if err != nil || strings.Contains(string(settings), `"kb"`) || !strings.Contains(string(settings), `"root-path"`) {
		t.Fatalf("settings after remove:\n%s", settings)
	}
	off := t6Run(t, session, "configure-knowledge-feedback-policy", map[string]any{"mode": "off"})
	if off["success"] != true || off["policy"].(map[string]any)["approvalState"] != "disabled" {
		t.Fatalf("feedback off: %v", off)
	}
	auto := t6Run(t, session, "configure-knowledge-feedback-policy", map[string]any{"mode": "auto"})
	if auto["success"] != false || !strings.HasPrefix(auto["error"].(string), "Selecting auto or changing its destination/reporting scope requires confirmed=true") {
		t.Fatalf("feedback auto: %v", auto)
	}
}

func TestExperimentalListsAndSetsFeatures(t *testing.T) {
	session, home := t6WriteSession(t)
	set := t6Run(t, session, "experimental", map[string]any{"name": "my-flag", "enable": true})
	messages := set["execution-log-messages"].([]any)
	if set["exit-code"] != float64(0) || messages[0].(map[string]any)["value"] != "Feature 'my-flag' is now ENABLED." ||
		messages[1].(map[string]any)["value"] != "No command or MCP tool currently references the feature key 'my-flag'." {
		t.Fatalf("set: %v", set)
	}
	list := t6Run(t, session, "experimental", map[string]any{})
	table := list["execution-log-messages"].([]any)[0].(map[string]any)
	value := strings.ReplaceAll(table["value"].(string), "\r\n", "\n")
	if table["message-type"] != "Table" || !strings.Contains(value, "| my-flag                     | ENABLED (orphan - no command uses this |\n") ||
		!strings.HasSuffix(value, "\n\n Count: 6") {
		t.Fatalf("list:\n%s", value)
	}
	if both := t6Run(t, session, "experimental", map[string]any{"name": "x"}); both["exit-code"] != float64(1) {
		t.Fatalf("toggle without a direction: %v", both)
	}
	settings, _ := os.ReadFile(filepath.Join(home, "clio", "appsettings.json"))
	if !strings.Contains(string(settings), `"my-flag": true`) {
		t.Fatalf("settings:\n%s", settings)
	}
}

func TestTelemetryToolsKeepClioConsent(t *testing.T) {
	session, home := t6WriteSession(t)
	if answer := t6Run(t, session, "send-telemetry", map[string]any{"session_id": "s1", "event_name": "workflow_started"}); answer["status"] != "rejected" ||
		answer["error"].(map[string]any)["code"] != "telemetry-consent-required" {
		t.Fatalf("no consent: %v", answer)
	}
	if answer := t6Run(t, session, "send-telemetry", map[string]any{"session_id": "s1", "event_name": "workflow_started", "telemetry_consent": "granted", "unexpected": 1}); answer["error"].(map[string]any)["code"] != "unsupported-fields" {
		t.Fatalf("extra field: %v", answer)
	}
	recorded := t6Run(t, session, "send-telemetry", map[string]any{"session_id": "s1", "event_name": "workflow_started", "telemetry_consent": "granted"})
	// The recorded event starts a background flush that reads the telemetry environment; it must finish
	// while this test's isolated environment is still in place.
	telemetryFlusher().Drain()
	if recorded["status"] != "recorded" || len(recorded["event_id"].(string)) != 32 {
		t.Fatalf("recorded: %v", recorded)
	}
	if consent := t6Run(t, session, "get-telemetry-consent", map[string]any{}); consent["telemetry_consent"] != "granted" {
		t.Fatalf("consent: %v", consent)
	}
	withdrawn := t6Run(t, session, "withdraw-telemetry-consent", map[string]any{})
	if withdrawn["status"] != "withdrawn" || withdrawn["events_purged"] != float64(1) {
		t.Fatalf("withdraw: %v", withdrawn)
	}
	if files, _ := filepath.Glob(filepath.Join(home, "telemetry", "events", "*")); len(files) != 0 {
		t.Fatalf("outbox not purged: %v", files)
	}
}

func TestExportComponentRegistryWritesTheRegistryVerbatim(t *testing.T) {
	session, home := t6WriteSession(t)
	output := filepath.Join(os.TempDir(), "t6-export-"+filepath.Base(home)+".json")
	t.Cleanup(func() { _ = os.Remove(output) })
	answer := t6Run(t, session, "export-component-registry", map[string]any{"version": "8.3.4", "output-file": output})
	if answer["success"] != true || answer["resolvedFrom"] != "environment" || answer["componentCount"] != float64(2) ||
		answer["compositeCount"] != float64(1) || answer["inputCount"] != float64(3) {
		t.Fatalf("export: %v", answer)
	}
	written, _ := os.ReadFile(output)
	original, _ := os.ReadFile(os.Getenv("CLIO_COMPONENT_REGISTRY_LOCAL_FILE"))
	if string(written) != string(original) {
		t.Fatal("the export must be byte-faithful")
	}
	again := t6Run(t, session, "export-component-registry", map[string]any{"version": "8.3.4", "output-file": output})
	if again["success"] != false || !strings.Contains(again["error"].(string), "already exists; refusing to overwrite it") {
		t.Fatalf("repeat: %v", again)
	}
	if alias := t6Run(t, session, "export-component-registry", map[string]any{"schemaType": "mobile"}); alias["error"] !=
		"Rename: 'schemaType' -> 'schema-type'." {
		t.Fatalf("alias: %v", alias)
	}
}

func TestToolkitToolsAnswerWithClioEnvelope(t *testing.T) {
	session, _ := t6WriteSession(t)
	unknown := t6Run(t, session, "install-toolkit", map[string]any{"target": "bogus"})
	messages := unknown["execution-log-messages"].([]any)
	if unknown["exit-code"] != float64(1) || len(messages) != 1 || messages[0].(map[string]any)["message-type"] != "Error" ||
		messages[0].(map[string]any)["value"] != "Unknown target 'bogus'. Valid agents: claude, codex, copilot, cursor." {
		t.Fatalf("unknown target: %v", unknown)
	}
	none := t6Run(t, session, "delete-toolkit", map[string]any{})
	messages = none["execution-log-messages"].([]any)
	if none["exit-code"] != float64(0) || len(messages) != 5 || messages[4].(map[string]any)["value"] != "No agents were removed." {
		t.Fatalf("no agents: %v", none)
	}
}
