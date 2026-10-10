package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// pkgWriteFake is a fake Creatio for the T15 tools: it answers the forms login and the routes a test gives,
// and records every other request as "METHOD /path body".
type pkgWriteFake struct {
	server *httptest.Server
	mu     sync.Mutex
	calls  []string
	routes map[string]func(body string) (int, string)
}

func newPkgWriteFake(t *testing.T, routes map[string]func(body string) (int, string)) *pkgWriteFake {
	t.Helper()
	fake := &pkgWriteFake{routes: routes}
	fake.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/ServiceModel/AuthService.svc/Login") {
			http.SetCookie(w, &http.Cookie{Name: "BPMCSRF", Value: "token"})
			_, _ = io.WriteString(w, `{"Code":0}`)
			return
		}
		body, _ := io.ReadAll(r.Body)
		fake.mu.Lock()
		fake.calls = append(fake.calls, r.Method+" "+r.URL.Path+" "+string(body))
		fake.mu.Unlock()
		for suffix, answer := range fake.routes {
			if strings.HasSuffix(r.URL.Path, suffix) {
				status, text := answer(string(body))
				w.WriteHeader(status)
				_, _ = io.WriteString(w, text)
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(fake.server.Close)
	return fake
}

func (f *pkgWriteFake) recorded() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// pkgWriteSession serves the MCP server over an appsettings.json naming one environment, "parity-env", at
// the fake.
func pkgWriteSession(t *testing.T, fake *pkgWriteFake, extra map[string]any) *mcp.ClientSession {
	t.Helper()
	clearCreatioVariables(t)
	path := filepath.Join(t.TempDir(), "appsettings.json")
	environment := map[string]any{"Uri": fake.server.URL, "Login": "example-user", "Password": "replace-me", "IsNetCore": false}
	for key, value := range extra {
		environment[key] = value
	}
	writeSettings(t, path, "parity-env", map[string]map[string]any{"parity-env": environment})
	return connectTestClient(t, newMCPServerWithHiddenTools(newEnvironments(path), defaultHiddenToolServices()),
		mcp.NewClient(&mcp.Implementation{Name: "t15-probe", Version: "test"}, nil))
}

// pkgWriteCall runs a tool through clio-run and decodes its JSON answer into out; it returns the raw result.
func pkgWriteCall(t *testing.T, session *mcp.ClientSession, tool string, args map[string]any, out any) *mcp.CallToolResult {
	t.Helper()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "clio-run", Arguments: map[string]any{"command": tool, "args": args}})
	if err != nil {
		t.Fatal(err)
	}
	if out != nil {
		if err := json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), out); err != nil {
			t.Fatalf("%s answer %q: %v", tool, result.Content[0].(*mcp.TextContent).Text, err)
		}
	}
	return result
}

func pkgWriteText(result *mcp.CallToolResult) string {
	return result.Content[0].(*mcp.TextContent).Text
}

// pkgWriteAssertGated checks that a write tool called by its raw name is refused without running.
func pkgWriteAssertGated(t *testing.T, session *mcp.ClientSession, tool string, args map[string]any) {
	t.Helper()
	raw, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil || !raw.IsError || !strings.Contains(pkgWriteText(raw), "was NOT executed") {
		t.Fatalf("%s raw call: %#v %v", tool, raw, err)
	}
}

func TestPkgToDBAndFileSystemThroughClioRun(t *testing.T) {
	fake := newPkgWriteFake(t, map[string]func(string) (int, string){
		"/WorkspaceExplorerService.svc/GetIsFileDesignMode": func(string) (int, string) { return 200, `{"success":true,"value":true}` },
		"/AppInstallerService.svc/LoadPackagesToDB":         func(string) (int, string) { return 200, `{"success":true}` },
		"/AppInstallerService.svc/LoadPackagesToFileSystem": func(string) (int, string) { return 200, `{"success":true}` },
	})
	session := pkgWriteSession(t, fake, nil)
	for _, executor := range []string{"clio-run", "clio-run-destructive"} {
		result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: executor, Arguments: map[string]any{"command": "pkg-to-db", "args": map[string]any{"environmentName": "parity-env"}}})
		if err != nil {
			t.Fatal(err)
		}
		var answer creatio.CommandResult
		_ = json.Unmarshal([]byte(pkgWriteText(result)), &answer)
		if answer.ExitCode != 0 || len(answer.Messages) != 3 || answer.Messages[0].Value != "Start load packages to database on a web application" ||
			answer.Messages[1].Value != "Load packages to database on a web application completed" || answer.Messages[2].MessageType != "None" || answer.Messages[2].Value != "" {
			t.Fatalf("%s pkg-to-db = %s", executor, pkgWriteText(result))
		}
	}
	var answer creatio.CommandResult
	pkgWriteCall(t, session, "pkg-to-file-system", map[string]any{"environmentName": "parity-env", "ignored": 1}, &answer)
	if answer.ExitCode != 0 || answer.Messages[1].Value != "Load packages to file system on a web application completed" {
		t.Fatalf("pkg-to-file-system = %+v", answer)
	}
	calls := fake.recorded()
	if len(calls) != 6 || !strings.HasPrefix(calls[1], "POST /0/ServiceModel/AppInstallerService.svc/LoadPackagesToDB ") ||
		!strings.HasPrefix(calls[5], "POST /0/ServiceModel/AppInstallerService.svc/LoadPackagesToFileSystem ") {
		t.Fatalf("calls = %q", calls)
	}
	pkgWriteAssertGated(t, session, "pkg-to-db", map[string]any{"environmentName": "parity-env"})

	pkgWriteCall(t, session, "pkg-to-db", map[string]any{"environmentName": "missing-env"}, &answer)
	if answer.ExitCode != 1 || !strings.HasPrefix(answer.Messages[0].Value, "[EnvironmentResolutionException] Environment with key 'missing-env' not found.") {
		t.Fatalf("unknown environment = %+v", answer)
	}
	pkgWriteCall(t, session, "pkg-to-db", map[string]any{"environmentName": "  "}, &answer)
	if answer.ExitCode != 1 || answer.Messages[0].Value != "environmentName is required and cannot be empty." {
		t.Fatalf("blank environment = %+v", answer)
	}
	missing := pkgWriteCall(t, session, "pkg-to-db", map[string]any{"environment-name": "parity-env"}, nil)
	if !missing.IsError || !strings.Contains(pkgWriteText(missing), "missing a value for the required parameter 'environmentName'") {
		t.Fatalf("kebab-case name = %s", pkgWriteText(missing))
	}
	typed := pkgWriteCall(t, session, "pkg-to-db", map[string]any{"environmentName": 5}, nil)
	if !typed.IsError || !strings.Contains(pkgWriteText(typed), "invalid-parameter-type: argument 'environmentName' for MCP tool 'pkg-to-db' must be a string.") {
		t.Fatalf("typed = %s", pkgWriteText(typed))
	}
	if len(fake.recorded()) != 6 {
		t.Fatalf("refusals must not reach Creatio: %q", fake.recorded())
	}
}
