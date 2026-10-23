package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// newCodegenTestSession serves a fake Creatio as environment "cg-env" (answer: route suffix -> body) and
// connects a client; it returns the session and the requests the site received.
func newCodegenTestSession(t *testing.T, answer func(route, body string) string) (*mcp.ClientSession, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ServiceModel/AuthService.svc/Login" {
			_, _ = w.Write([]byte(`{"Code":0}`))
			return
		}
		raw, _ := io.ReadAll(r.Body)
		route := strings.TrimPrefix(r.URL.Path, "/0/")
		mu.Lock()
		requests = append(requests, route+" "+string(raw))
		mu.Unlock()
		_, _ = w.Write([]byte(answer(route, string(raw))))
	}))
	t.Cleanup(server.Close)
	clearCreatioVariables(t)
	path := filepath.Join(t.TempDir(), "appsettings.json")
	writeSettings(t, path, "cg-env", map[string]map[string]any{
		"cg-env": {"Uri": server.URL, "Login": "example-user", "Password": "replace-me", "IsNetCore": false},
	})
	session := connectTestClient(t, newMCPServerWithHiddenTools(newEnvironments(path), defaultHiddenToolServices()),
		mcp.NewClient(&mcp.Implementation{Name: "probe-client", Version: "test"}, nil))
	return session, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), requests...)
	}
}

func codegenCall(t *testing.T, session *mcp.ClientSession, tool string, args map[string]any) (string, bool) {
	t.Helper()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "clio-run", Arguments: map[string]any{"command": tool, "args": args}})
	if err != nil {
		t.Fatal(err)
	}
	return result.Content[0].(*mcp.TextContent).Text, result.IsError
}

func TestGenerateSourceCodeArgumentsThroughClioRun(t *testing.T) {
	session, requests := newCodegenTestSession(t, func(string, string) string { return `{"success":true}` })
	const done = `{"message-type":"Info","value":"Done generate-source-code"}]}`
	refusal := func(text string) string {
		return `{"exit-code":1,"execution-log-messages":[{"message-type":"Error","value":"` + text + `"}]}`
	}
	cases := []struct {
		args    map[string]any
		want    string
		isError bool
	}{
		{map[string]any{"environment-name": "cg-env", "modified": nil, "background": nil}, done, false},
		{map[string]any{"Environment-Name": "cg-env", "TIMEOUT": "120000"}, "more than 2 minutes", false},
		{map[string]any{"environment-name": "cg-env", "timeOut": 180000}, "more than 3 minutes", false},
		{map[string]any{"environment-name": "cg-env", "timeout": 0}, refusal("generate-source-code 'timeout' must be between 1 and 10800000 milliseconds (180 minutes)."), false},
		{map[string]any{"environment-name": "cg-env", "timeout": 10800001}, refusal("generate-source-code 'timeout' must be between 1 and 10800000 milliseconds (180 minutes)."), false},
		{map[string]any{"environment": "cg-env", "timeout": 0}, refusal("generate-source-code arguments are invalid: Rename: 'environment' -> 'environment-name'. Nothing was generated."), false},
		{map[string]any{"environment-name": "cg-env", "Extra": 1, "timeout-ms": 5}, refusal("generate-source-code arguments are invalid: Rename: 'timeout-ms' -> 'timeout'. " +
			"Unknown args: 'Extra'. Valid fields: environment-name, modified, required, background, timeout. Nothing was generated."), false},
		{map[string]any{"environment-name": " "}, refusal("[EnvironmentResolutionException] " + missingTargetMessage), false},
		{map[string]any{}, refusal("[EnvironmentResolutionException] " + missingTargetMessage), false},
		{map[string]any{"environment-name": "cg-env", "modified": "true"}, "argument 'modified' for MCP tool 'generate-source-code' must be a boolean", true},
		{map[string]any{"environment-name": "cg-env", "timeout": 1.5}, "argument 'timeout' for MCP tool 'generate-source-code' must be a number", true},
	}
	for _, test := range cases {
		text, isError := codegenCall(t, session, "generate-source-code", test.args)
		if isError != test.isError || !strings.Contains(text, test.want) {
			t.Fatalf("%v: isError=%v %s", test.args, isError, text)
		}
	}
	if got := requests(); len(got) != 3 {
		t.Fatalf("requests = %q", got)
	}
	adminWriteRawNameIsGated(t, session, "generate-source-code", map[string]any{"environment-name": "cg-env"})
}

func TestAddItemModelThroughClioRun(t *testing.T) {
	session, requests := newCodegenTestSession(t, func(route, body string) string {
		if strings.HasSuffix(route, "EntitySchemaManagerRequest") {
			return `{"collection":[{"name":"Thing"}]}`
		}
		return `{"schema":{"columns":{"Items":{"ae0e45ca-c495-4fe7-a39d-3ab7278e1617":{"name":"Id","dataValueType":0,"isRequired":true},` +
			`"be0e45ca-c495-4fe7-a39d-3ab7278e1617":{"name":"Title","dataValueType":28,"isRequired":true,"description":{"en-US":"Title"}}}}}}`
	})
	folder := filepath.Join(t.TempDir(), "out")
	text, isError := codegenCall(t, session, "add-item-model", map[string]any{"namespace": "Acme", "folder": folder, "environment-name": "cg-env", "culture": "x"})
	want := `{"exit-code":0,"execution-log-messages":[{"message-type":"Info","value":"Generating models..."},` +
		`{"message-type":"None","value":"Models will be generated in directory: ` + strings.ReplaceAll(folder, `\`, `\\`) + `"},` +
		`{"message-type":"Info","value":"Generated 1 models; requested filter: none."}]}`
	if isError || text != want {
		t.Fatalf("answer = %s", text)
	}
	class, err := os.ReadFile(filepath.Join(folder, "Thing.cs"))
	if err != nil || !strings.Contains(string(class), "[Schema(\"Thing\")]") || !strings.Contains(string(class), "public string Title { get; set; }") {
		t.Fatalf("Thing.cs = %s (%v)", class, err)
	}
	if got := requests(); len(got) != 2 || got[1] != `DataService/json/SyncReply/RuntimeEntitySchemaRequest {"Name" : "Thing"}` {
		t.Fatalf("requests = %q", got)
	}

	text, _ = codegenCall(t, session, "add-item-model", map[string]any{"namespace": "Acme", "folder": "relative", "environment-name": "cg-env"})
	if text != `{"exit-code":1,"execution-log-messages":[{"message-type":"Error","value":"Folder path must be absolute: relative"}]}` {
		t.Fatalf("relative folder = %s", text)
	}
	unknownFolder := filepath.Join(t.TempDir(), "made-anyway")
	text, isError = codegenCall(t, session, "add-item-model", map[string]any{"namespace": "Acme", "folder": unknownFolder, "environment-name": "nope"})
	if !isError || !strings.HasPrefix(text, "Error: tool 'add-item-model' failed: Environment with key 'nope' not found.") {
		t.Fatalf("unknown environment = %s", text)
	}
	if info, err := os.Stat(unknownFolder); err != nil || !info.IsDir() {
		t.Fatalf("the folder is created before the environment is resolved: %v", err)
	}
	text, _ = codegenCall(t, session, "add-item-model", map[string]any{"folder": folder, "environment-name": "cg-env"})
	if text != `{"exit-code":1,"execution-log-messages":[{"message-type":"Error","value":"Namespace is required for model generation."}]}` {
		t.Fatalf("no namespace = %s", text)
	}
	adminWriteRawNameIsGated(t, session, "add-item-model", map[string]any{"namespace": "Acme", "folder": folder, "environment-name": "cg-env"})
}

func TestCodegenToolAnnotations(t *testing.T) {
	for name, want := range map[string]toolAnnotations{
		"generate-source-code": {ReadOnly: false, Destructive: false, Idempotent: true, OpenWorld: false},
		"add-item-model":       {ReadOnly: false, Destructive: true, Idempotent: false, OpenWorld: false},
	} {
		if _, ok := registeredTools[name]; !ok {
			t.Fatalf("%s is not registered", name)
		}
		if got := annotationsOf(name); got != want {
			t.Fatalf("%s annotations = %#v, want %#v", name, got, want)
		}
	}
}
