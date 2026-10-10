package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// dataWriteTestSession serves handler as the Creatio environment "parity-stand" (forms login answered here)
// and connects an MCP client to a server that resolves it by name.
func dataWriteTestSession(t *testing.T, handler http.HandlerFunc) *mcp.ClientSession {
	t.Helper()
	clearCreatioVariables(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/AuthService.svc/Login") {
			_, _ = io.WriteString(w, `{"Code":0}`)
			return
		}
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	path := filepath.Join(t.TempDir(), "appsettings.json")
	writeSettings(t, path, "parity-stand", map[string]map[string]any{
		"parity-stand": {"Uri": server.URL, "Login": "example-user", "Password": "replace-me", "IsNetCore": false},
	})
	return connectTestClient(t, newMCPServerWithHiddenTools(newEnvironments(path), defaultHiddenToolServices()),
		mcp.NewClient(&mcp.Implementation{Name: "data-write-probe", Version: "test"}, nil))
}

// dataWriteTestCall calls tool through executor ("" calls it by its raw name) and returns the decoded text.
func dataWriteTestCall(t *testing.T, session *mcp.ClientSession, executor, tool string, args map[string]any) (*mcp.CallToolResult, map[string]any) {
	t.Helper()
	params := &mcp.CallToolParams{Name: tool, Arguments: args}
	if executor != "" {
		params = &mcp.CallToolParams{Name: executor, Arguments: map[string]any{"command": tool, "args": args}}
	}
	result, err := session.CallTool(context.Background(), params)
	if err != nil {
		t.Fatalf("%s %s: %v", executor, tool, err)
	}
	var answer map[string]any
	_ = json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &answer)
	return result, answer
}

func dataWriteTestText(result *mcp.CallToolResult) string {
	return result.Content[0].(*mcp.TextContent).Text
}

func TestDataServiceBatchToolThroughExecutorsAndGate(t *testing.T) {
	posts := 0
	session := dataWriteTestSession(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/0/DataService/json/SyncReply/BatchQuery" {
			t.Errorf("unexpected route %s", r.URL.Path)
			return
		}
		posts++
		var body struct {
			Items []map[string]any `json:"items"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = io.WriteString(w, `{"queryResults":[{"queryId":"`+body.Items[0]["queryId"].(string)+`","success":false,"responseStatus":{"Message":"Column Name is required"}}]}`)
	})
	args := map[string]any{"environment-name": "parity-stand", "operations": []any{map[string]any{
		"operation": "update", "schema-name": "Contact", "record-id": "00000000-0000-0000-0000-000000000001",
		"values": map[string]any{"Name": map[string]any{"data-value-type": 1, "value": "x"}}}}}

	raw, _ := dataWriteTestCall(t, session, "", "execute-dataservice-batch", args)
	if !raw.IsError || !strings.Contains(dataWriteTestText(raw), "was NOT executed") || posts != 0 {
		t.Fatalf("raw call escaped the write gate: %s", dataWriteTestText(raw))
	}
	for _, executor := range []string{"clio-run", "clio-run-destructive"} {
		result, answer := dataWriteTestCall(t, session, executor, "execute-dataservice-batch", args)
		if result.IsError || answer["success"] != false || answer["failed-count"] != float64(1) {
			t.Fatalf("%s answer %s", executor, dataWriteTestText(result))
		}
		item := answer["items"].([]any)[0].(map[string]any)
		if item["state"] != "failed" || item["error"] != "[untrusted-source-text begin] Column Name is required [untrusted-source-text end]" {
			t.Fatalf("%s item %v", executor, item)
		}
	}
	if posts != 2 {
		t.Fatalf("posts=%d", posts)
	}

	// Validation is raised before any request, as clio's service throws.
	result, _ := dataWriteTestCall(t, session, "clio-run", "execute-dataservice-batch", map[string]any{"environment-name": "parity-stand", "operations": []any{}})
	if !result.IsError || dataWriteTestText(result) != "Error: tool 'execute-dataservice-batch' failed: Provide 1–100 explicit operations." {
		t.Fatalf("empty operations %s", dataWriteTestText(result))
	}
	result, _ = dataWriteTestCall(t, session, "clio-run", "execute-dataservice-batch", map[string]any{"environment-name": "parity-stand", "operations": "x"})
	if !result.IsError || !strings.Contains(dataWriteTestText(result), "argument 'operations' for MCP tool 'execute-dataservice-batch' must be an array") {
		t.Fatalf("non-array operations %s", dataWriteTestText(result))
	}
	if posts != 2 {
		t.Fatalf("validation sent a request: posts=%d", posts)
	}
}

func TestDataBindingDbToolsThroughExecutorsAndGate(t *testing.T) {
	writes := 0
	session := dataWriteTestSession(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/SelectQuery") {
			_, _ = io.WriteString(w, `{"success":true,"rows":[{"Name":"UsrParityPkg","UId":"66666666-6666-6666-6666-666666666666","InstallType":0}]}`)
			return
		}
		writes++
		_, _ = io.WriteString(w, `{"success":true}`)
	})
	cases := map[string]map[string]any{
		"create-data-binding-db":     {"package-name": "UsrParityNone", "schema-name": "ContactType"},
		"upsert-data-binding-row-db": {"package-name": "UsrParityNone", "binding-name": "B", "values": "{}"},
		"remove-data-binding-row-db": {"package-name": "UsrParityNone", "binding-name": "B", "key-value": "1"},
	}
	for tool, args := range cases {
		args["environment-name"] = "parity-stand"
		raw, _ := dataWriteTestCall(t, session, "", tool, args)
		if !raw.IsError || !strings.Contains(dataWriteTestText(raw), "was NOT executed") {
			t.Fatalf("%s raw call escaped the write gate: %s", tool, dataWriteTestText(raw))
		}
		for _, executor := range []string{"clio-run", "clio-run-destructive"} {
			result, answer := dataWriteTestCall(t, session, executor, tool, args)
			messages, _ := answer["execution-log-messages"].([]any)
			if result.IsError || answer["exit-code"] != float64(1) || len(messages) != 1 ||
				messages[0].(map[string]any)["value"] != "Package 'UsrParityNone' was not found in the environment. Check the name against list-packages." {
				t.Fatalf("%s %s answer %s", executor, tool, dataWriteTestText(result))
			}
		}
	}
	result, _ := dataWriteTestCall(t, session, "clio-run", "create-data-binding-db",
		map[string]any{"environment-name": "parity-stand", "package-name": "P", "schema-name": "S", "rows": []any{}})
	if !result.IsError || !strings.Contains(dataWriteTestText(result), "argument 'rows' for MCP tool 'create-data-binding-db' must be a string") {
		t.Fatalf("rows binding %s", dataWriteTestText(result))
	}
	if writes != 0 {
		t.Fatalf("writes=%d", writes)
	}
}

func TestExecuteSQLScriptToolValidatesGatesAndRuns(t *testing.T) {
	version, scripts := "2.0.0.40", 0
	session := dataWriteTestSession(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/SelectQuery") {
			_, _ = io.WriteString(w, `{"success":true,"rows":[{"Name":"cliogate","Version":"`+version+`"}]}`)
			return
		}
		scripts++
		_, _ = io.WriteString(w, `"5"`)
	})
	args := map[string]any{"environment-name": "parity-stand", "script": "UPDATE UsrParityTable SET Name = Name"}
	raw, _ := dataWriteTestCall(t, session, "", "execute-sql-script", args)
	if !raw.IsError || !strings.Contains(dataWriteTestText(raw), "was NOT executed") {
		t.Fatalf("raw call escaped the write gate: %s", dataWriteTestText(raw))
	}
	check := func(executor string, args map[string]any, exitCode float64, value string) {
		t.Helper()
		result, answer := dataWriteTestCall(t, session, executor, "execute-sql-script", args)
		messages, _ := answer["execution-log-messages"].([]any)
		if result.IsError || answer["exit-code"] != exitCode || len(messages) == 0 || messages[0].(map[string]any)["value"] != value {
			t.Fatalf("%s %v answer %s", executor, args, dataWriteTestText(result))
		}
	}
	check("clio-run", map[string]any{"environment-name": "parity-stand"}, 1, "Provide exactly one of script or file.")
	check("clio-run", map[string]any{"environment-name": "parity-stand", "script": "x", "view": "XML"}, 1, "view must be table, json, csv, or xlsx.")
	check("clio-run", args, 1, "To use this command, you need to install the cliogate package version 2.0.0.41 or higher. Install or update the package in the target environment and retry.\n"+
		"Run 'clio install-gate -e <environment>' (or call the install-gate MCP tool) to install/update cliogate.")
	if scripts != 0 {
		t.Fatalf("a refused call ran the script")
	}
	version = "2.0.0.41"
	for _, executor := range []string{"clio-run", "clio-run-destructive"} {
		check(executor, args, 0, "(5 rows affected)")
	}
	if scripts != 2 {
		t.Fatalf("scripts=%d", scripts)
	}
}

func TestRunProcessToolAnswersByTheResponseDeadline(t *testing.T) {
	t.Setenv("CLIO_MCP_RESPONSE_DEADLINE_SECONDS", "1")
	var launches atomic.Int32
	var slowSignature, slowRun atomic.Bool
	session := dataWriteTestSession(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/SelectQuery"):
			if slowSignature.Load() {
				time.Sleep(1500 * time.Millisecond)
			}
			_, _ = io.WriteString(w, `{"success":true,"rows":[{"Id":"aaaaaaaa-0000-0000-0000-000000000001","Name":"UsrParityProcess","Caption":"Own","VersionParentUId":"aaaaaaaa-0000-0000-0000-000000000001","IsActiveVersion":true}]}`)
		case strings.HasSuffix(r.URL.Path, "/ProcessSchemaRequest"):
			_, _ = io.WriteString(w, `{"success":true,"schema":{"metaData":"{\"metaData\":{\"schema\":{\"parameters\":[]}}}"}}`)
		case strings.HasSuffix(r.URL.Path, "/RunProcess"):
			launches.Add(1)
			if slowRun.Load() {
				time.Sleep(1500 * time.Millisecond)
			}
			_, _ = io.WriteString(w, `{"success":true,"processId":"bbbbbbbb-0000-0000-0000-000000000001","processStatus":2}`)
		}
	})
	args := map[string]any{"environment-name": "parity-stand", "process-name": "UsrParityProcess"}
	raw, _ := dataWriteTestCall(t, session, "", "run-process", args)
	if !raw.IsError || !strings.Contains(dataWriteTestText(raw), "was NOT executed") || launches.Load() != 0 {
		t.Fatalf("raw call escaped the write gate: %s", dataWriteTestText(raw))
	}
	for _, executor := range []string{"clio-run", "clio-run-destructive"} {
		if _, answer := dataWriteTestCall(t, session, executor, "run-process", args); answer["status"] != "completed" {
			t.Fatalf("%s answer %v", executor, answer)
		}
	}
	slowRun.Store(true)
	if _, answer := dataWriteTestCall(t, session, "clio-run", "run-process", args); answer["status"] != "still-running" ||
		!strings.HasPrefix(answer["warnings"].([]any)[0].(string), "The launch request for 'UsrParityProcess' was sent at ") {
		t.Fatalf("still-running answer %v", answer)
	}
	slowRun.Store(false)
	slowSignature.Store(true)
	before := launches.Load()
	if _, answer := dataWriteTestCall(t, session, "clio-run", "run-process", args); answer["status"] != "not-started" ||
		!strings.HasPrefix(answer["error"].(string), "'UsrParityProcess' was not launched: preparing the launch") {
		t.Fatalf("not-started answer %v", answer)
	}
	time.Sleep(2 * time.Second)
	if launches.Load() != before {
		t.Fatalf("a withdrawn launch was sent afterwards")
	}
	_, answer := dataWriteTestCall(t, session, "clio-run", "run-process", map[string]any{"environment-name": "parity-stand", "process-name": "X", "processName": "typo"})
	if !strings.Contains(answer["error"].(string), "processName") {
		t.Fatalf("unknown key answer %v", answer)
	}
}

func TestGetRequestInfoIsReadOnlyAndReachableBothWays(t *testing.T) {
	dir := t.TempDir()
	registry := filepath.Join(dir, "RequestRegistry.json")
	if err := os.WriteFile(registry, []byte(`{"requests":[{"requestType":"crt.ClosePageRequest","description":"Closes","parameters":{}}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLIO_REQUEST_REGISTRY_LOCAL_FILE", registry)
	session := dataWriteTestSession(t, func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	for _, params := range callPaths("get-request-info", map[string]any{"request-type": "crt.ClosePageRequest"}) {
		result, err := session.CallTool(context.Background(), params)
		if err != nil || result.IsError || !strings.Contains(dataWriteTestText(result), `"mode":"detail"`) {
			t.Fatalf("%s: %v %v", params.Name, result, err)
		}
	}
	if len(callPaths("get-request-info", nil)) != 2 {
		t.Fatal("get-request-info must be callable by its raw name")
	}
}
