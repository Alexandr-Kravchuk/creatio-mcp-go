package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// callPaths are the ways a test reaches a hidden tool: always through clio-run, and directly by raw name
// when the tool is read-only (a write-capable one answers confirmation-required there, as in clio).
func callPaths(name string, args map[string]any) []*mcp.CallToolParams {
	calls := []*mcp.CallToolParams{{Name: "clio-run", Arguments: map[string]any{"command": name, "args": args}}}
	if !requiresConfirmation(name) {
		calls = append(calls, &mcp.CallToolParams{Name: name, Arguments: args})
	}
	return calls
}

// registerTestTool registers a tool for one test and removes it afterwards.
func registerTestTool(t *testing.T, name string, invoke func(context.Context, *environments, map[string]any) (*mcp.CallToolResult, error), options ...toolOption) {
	t.Helper()
	registerTool(map[string]any{"name": name, "description": "test tool",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}}}, invoke, options...)
	t.Cleanup(func() { delete(registeredTools, name) })
}

func TestWriteToolIsRefusedDirectlyAndRunsThroughBothExecutors(t *testing.T) {
	var runs int
	var mu sync.Mutex
	registerTestTool(t, "zz-delete-thing", func(context.Context, *environments, map[string]any) (*mcp.CallToolResult, error) {
		mu.Lock()
		runs++
		mu.Unlock()
		return structuredToolResult(map[string]any{"success": true}), nil
	}, withAnnotations(toolAnnotations{Destructive: true}))
	session := connectTestClient(t, newMCPServer(&creatio.Client{}), mcp.NewClient(&mcp.Implementation{Name: "probe-client", Version: "test"}, nil))

	direct, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "zz-delete-thing", Arguments: map[string]any{"password": "pw", "id": "1"}})
	if err != nil || !direct.IsError || runs != 0 {
		t.Fatalf("direct call must be refused without running: %#v, err = %v, runs = %d", direct, err, runs)
	}
	text := direct.Content[0].(*mcp.TextContent).Text
	if !strings.HasPrefix(text, "Tool 'zz-delete-thing' can overwrite or delete existing state and was NOT executed") {
		t.Fatalf("refusal text = %q", text)
	}
	payload := direct.StructuredContent.(map[string]any)
	if payload["code"] != "confirmation-required" || payload["destructive"] != true || payload["write-capable"] != true ||
		strings.Join(stringsOf(payload["argument-names"]), ",") != "id,password" {
		t.Fatalf("refusal payload = %#v", payload)
	}
	if encoded, _ := json.Marshal(payload); strings.Contains(string(encoded), `"pw"`) {
		t.Fatalf("the refusal must not echo argument values: %s", encoded)
	}
	retry := payload["retry"].(map[string]any)
	if retry["tool"] != "clio-run" || retry["arguments"].(map[string]any)["command"] != "zz-delete-thing" {
		t.Fatalf("retry = %#v", retry)
	}

	for _, executor := range []string{"clio-run", "clio-run-destructive"} {
		result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: executor,
			Arguments: map[string]any{"command": "zz-delete-thing", "args": map[string]any{}}})
		if err != nil || result.IsError {
			t.Fatalf("%s = %#v, err = %v", executor, result, err)
		}
		audit, _ := result.Meta["clio-run"].(map[string]any)
		if audit["dispatchedTool"] != "zz-delete-thing" || audit["destructive"] != true {
			t.Fatalf("%s _meta = %#v", executor, result.Meta)
		}
	}
	if runs != 2 {
		t.Fatalf("runs = %d, want 2", runs)
	}
}

func TestReadOnlyHiddenToolRunsDirectly(t *testing.T) {
	registerTestTool(t, "zz-read-thing", func(context.Context, *environments, map[string]any) (*mcp.CallToolResult, error) {
		return structuredToolResult(map[string]any{"success": true}), nil
	})
	session := connectTestClient(t, newMCPServer(&creatio.Client{}), mcp.NewClient(&mcp.Implementation{Name: "probe-client", Version: "test"}, nil))
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "zz-read-thing"})
	if err != nil || result.IsError || result.Meta["clio-run"] != nil {
		t.Fatalf("direct read = %#v, err = %v", result, err)
	}
}

func TestExecutorTargetShapes(t *testing.T) {
	session := connectTestClient(t, newMCPServer(&creatio.Client{}), mcp.NewClient(&mcp.Implementation{Name: "probe-client", Version: "test"}, nil))
	cases := []struct {
		args map[string]any
		want string
	}{
		{map[string]any{}, "Error: 'command' is required"},
		{map[string]any{"command": "CLIO-RUN-destructive"}, "Error: 'CLIO-RUN-destructive' cannot be a clio-run target"},
		{map[string]any{"args": map[string]any{"command": "compile-status", "environment-name": 5}},
			"invalid-parameter-type: argument 'environment-name' for MCP tool 'compile-status' must be a string."},
		{map[string]any{"args": map[string]any{"command": "compile-status", "args": map[string]any{"operation-id": true}}},
			"invalid-parameter-type: argument 'operation-id' for MCP tool 'compile-status' must be a string."},
	}
	for _, c := range cases {
		result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "clio-run-destructive", Arguments: c.args})
		if err != nil || !result.IsError || !strings.HasPrefix(result.Content[0].(*mcp.TextContent).Text, c.want) {
			t.Fatalf("clio-run-destructive %v = %#v, err = %v", c.args, result, err)
		}
	}
}

func TestAnnotationsComeFromClio(t *testing.T) {
	session := connectTestClient(t, newMCPServer(&creatio.Client{}), mcp.NewClient(&mcp.Implementation{Name: "probe-client", Version: "test"}, nil))
	listed, err := session.ListTools(context.Background(), &mcp.ListToolsParams{})
	if err != nil {
		t.Fatal(err)
	}
	executors := 0
	for _, tool := range listed.Tools {
		a, want := tool.Annotations, annotationsOf(tool.Name)
		if a == nil || a.DestructiveHint == nil || *a.DestructiveHint != want.Destructive || a.ReadOnlyHint != want.ReadOnly {
			t.Fatalf("%s: tools/list annotations %#v, gate reads %+v", tool.Name, a, want)
		}
		if tool.Name == "clio-run" || tool.Name == "clio-run-destructive" {
			executors++
			if !want.Destructive || want.ReadOnly || !want.OpenWorld {
				t.Fatalf("%s annotations = %+v", tool.Name, want)
			}
		}
	}
	if executors != 2 {
		t.Fatalf("tools/list carries %d executors, want clio-run and clio-run-destructive", executors)
	}
	// A hidden tool's destructive flag is clio's index entry; its read-only flag is the registration.
	for _, name := range servedToolNames() {
		if destructive, ok := clioFacts().destructive[name]; ok && annotationsOf(name).Destructive != destructive {
			t.Fatalf("%s destructive = %v, clio's index says %v", name, annotationsOf(name).Destructive, destructive)
		}
		if annotationsOf(name).Destructive && annotationsOf(name).ReadOnly {
			t.Fatalf("%s is registered read-only but clio marks it destructive", name)
		}
	}
	for name, readOnly := range map[string]bool{"get-sql-schema": false, "get-schema": false, "get-theme": false, "odata-read": true, "get-page": false} {
		if annotationsOf(name).ReadOnly != readOnly {
			t.Fatalf("%s read-only = %v", name, !readOnly)
		}
	}
	if requiresConfirmation("get-page") || !requiresConfirmation("get-schema") || requiresConfirmation("odata-read") {
		t.Fatal("gate: get-page is resident in clio, get-schema is a hidden local write, odata-read is a read")
	}
}

func TestFailedHiddenResultsAreRedactedSuccessesAreNot(t *testing.T) {
	registerTestTool(t, "zz-envelope", func(_ context.Context, _ *environments, args map[string]any) (*mcp.CallToolResult, error) {
		switch args["shape"] {
		case "failure":
			return structuredToolResult(map[string]any{"success": false, "error": "POST https://h.example.com/x failed",
				"uri": "https://h.example.com/y", "details": map[string]any{"message": "see /Users/someone/a.log"}}), nil
		case "success":
			return structuredToolResult(map[string]any{"success": true, "uri": "https://h.example.com/y"}), nil
		case "command":
			return structuredToolResult(creatio.NewCommandResult(1, "Error", "see https://h.example.com/y")), nil
		default:
			return nil, errors.New("dial tcp 10.1.2.3:443: refused")
		}
	})
	session := connectTestClient(t, newMCPServer(&creatio.Client{}), mcp.NewClient(&mcp.Implementation{Name: "probe-client", Version: "test"}, nil))
	call := func(via, shape string) *mcp.CallToolResult {
		params := &mcp.CallToolParams{Name: "zz-envelope", Arguments: map[string]any{"shape": shape}}
		if via == "clio-run" {
			params = &mcp.CallToolParams{Name: "clio-run", Arguments: map[string]any{"command": "zz-envelope", "args": map[string]any{"shape": shape}}}
		}
		result, err := session.CallTool(context.Background(), params)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	for _, via := range []string{"direct", "clio-run"} {
		failure := call(via, "failure")
		text := failure.Content[0].(*mcp.TextContent).Text
		structured, _ := json.Marshal(failure.StructuredContent)
		for _, leak := range []string{"h.example.com", "/Users/someone"} {
			if strings.Contains(text, leak) || strings.Contains(string(structured), leak) {
				t.Fatalf("%s failure leaks %q: text %s structured %s", via, leak, text, structured)
			}
		}
		// clio redacts the whole failure text, so a non-error field is redacted too, in text and structure alike.
		if !strings.Contains(string(structured), `"uri":"[redacted-uri]"`) {
			t.Fatalf("%s structured = %s", via, structured)
		}
		if success := call(via, "success").Content[0].(*mcp.TextContent).Text; !strings.Contains(success, "h.example.com") {
			t.Fatalf("%s success must stay untouched: %s", via, success)
		}
		// The command envelope signals no failure the way clio's backstop reads it, so clio leaves it as is.
		if command := call(via, "command").Content[0].(*mcp.TextContent).Text; !strings.Contains(command, "h.example.com") {
			t.Fatalf("%s command envelope = %s", via, command)
		}
		raised := call(via, "raised")
		if text := raised.Content[0].(*mcp.TextContent).Text; !raised.IsError || !strings.HasSuffix(text, "failed: dial tcp [redacted-uri]: refused") {
			t.Fatalf("%s raised = %q", via, text)
		}
	}
}

func TestOperationRegistryFeedsStatusTools(t *testing.T) {
	envs := staticEnvironments(nil)
	tenant := envs.tenantKey("dev")
	op := compileOperations.begin(tenant, "dev", operationDetails{PackageName: "UsrPkg"})
	other := restartOperations.begin("someone-else", "other", operationDetails{})
	session := connectTestClient(t, newMCPServerWithHiddenTools(envs, defaultHiddenToolServices()), mcp.NewClient(&mcp.Implementation{Name: "probe-client", Version: "test"}, nil))
	status := func(tool string, args map[string]any) map[string]any {
		result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
		if err != nil || result.IsError {
			t.Fatalf("%s = %#v, err = %v", tool, result, err)
		}
		return result.StructuredContent.(map[string]any)
	}
	running := status("compile-status", map[string]any{"environment-name": "dev"})
	if running["status"] != "running" || running["operation-id"] != op.ID || running["package-name"] != "UsrPkg" ||
		running["finished-utc"] != nil || len(running["message-tail"].([]any)) != 0 {
		t.Fatalf("running = %#v", running)
	}
	messages := make([]creatio.LogMessage, 60)
	for i := range messages {
		messages[i] = creatio.LogMessage{MessageType: "Info", Value: "line"}
	}
	messages[59].Value = "built /Users/someone/bin"
	compileOperations.finish(op.ID, 1, messages)
	failed := status("compile-status", map[string]any{"environment-name": "dev", "operation-id": " " + op.ID + " "})
	tail := failed["message-tail"].([]any)
	if failed["status"] != "failed" || failed["exit-code"] != float64(1) || len(tail) != 50 || tail[49] != "built [redacted-path]" {
		t.Fatalf("failed = %#v", failed)
	}
	if started, _ := failed["started-utc"].(string); !strings.HasSuffix(started, "Z") || strings.Count(started, ":") != 2 {
		t.Fatalf("started-utc = %q", started)
	}
	// Another environment's operation id is not readable.
	if foreign := status("restart-status", map[string]any{"environment-name": "dev", "operation-id": other.ID}); foreign["status"] != "not-found" {
		t.Fatalf("foreign = %#v", foreign)
	}
	restartOperations.finish(other.ID, 0, nil)
	if record, _ := restartOperations.lookup("someone-else", ""); record.Status != "ready" || record.MessageTail != nil {
		t.Fatalf("restart record = %#v", record)
	}
}

func TestOperationStoreEvictsFinishedNeverRunning(t *testing.T) {
	store := newOperationStore("succeeded", "failed", true)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	running := store.begin("t", "e", operationDetails{})
	var finished []string
	for i := 0; i < maxOperationRecords+5; i++ {
		record := store.begin("t"+string(rune('a'+i%26)), "e", operationDetails{})
		store.finish(record.ID, 0, nil)
		finished = append(finished, record.ID)
		now = now.Add(time.Second)
	}
	if len(store.byID) != maxOperationRecords {
		t.Fatalf("records = %d", len(store.byID))
	}
	if _, ok := store.byID[running.ID]; !ok {
		t.Fatal("a running operation was evicted")
	}
	if _, ok := store.byID[finished[0]]; ok {
		t.Fatal("the oldest finished record survived the cap")
	}
	now = now.Add(operationIdleTTL + time.Minute)
	store.begin("x", "e", operationDetails{})
	if len(store.byID) != 2 {
		t.Fatalf("after the idle TTL only the running and the new record remain, got %d", len(store.byID))
	}
}

func TestRunLongOperationProgressDeadlineAndCancellation(t *testing.T) {
	var mu sync.Mutex
	var messages []string
	var sequence []float64
	ctx := withProgress(context.Background(), func(progress, _ float64, message string) error {
		mu.Lock()
		defer mu.Unlock()
		messages, sequence = append(messages, message), append(sequence, progress)
		return nil
	})
	result, finished, err := runLongOperation(ctx, "op", time.Second, func(_ context.Context, stage func(string)) int {
		stage("one")
		stage("two")
		return 7
	})
	if err != nil || !finished || result != 7 || strings.Join(messages, ",") != "one,two" || sequence[1] != 2 {
		t.Fatalf("result %d finished %v err %v messages %v sequence %v", result, finished, err, messages, sequence)
	}

	release := make(chan struct{})
	background := make(chan error, 1)
	_, finished, err = runLongOperation(context.Background(), "slow", 20*time.Millisecond, func(ctx context.Context, _ func(string)) int {
		<-release
		background <- ctx.Err()
		return 0
	})
	if err != nil || finished {
		t.Fatalf("past the deadline: finished %v err %v", finished, err)
	}
	close(release)
	if workErr := <-background; workErr != nil {
		t.Fatalf("work past the deadline must keep a live context, got %v", workErr)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	workSaw := make(chan error, 1)
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	_, finished, err = runLongOperation(cancelled, "cancel", time.Minute, func(ctx context.Context, _ func(string)) int {
		<-ctx.Done()
		workSaw <- ctx.Err()
		return 0
	})
	if !errors.Is(err, context.Canceled) || finished || !errors.Is(<-workSaw, context.Canceled) {
		t.Fatalf("cancellation: finished %v err %v", finished, err)
	}
}

func stringsOf(value any) []string {
	items, _ := value.([]any)
	out := make([]string, 0, len(items))
	for _, item := range items {
		text, _ := item.(string)
		out = append(out, text)
	}
	return out
}
