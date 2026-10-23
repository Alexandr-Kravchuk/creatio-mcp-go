package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// t16aSession is an MCP client of a server whose clio settings live in a temporary directory.
func t16aSession(t *testing.T) (*mcp.ClientSession, *environments, string) {
	t.Helper()
	clearCreatioVariables(t)
	path := filepath.Join(t.TempDir(), "clio", "appsettings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	envs := newEnvironments(path)
	session := connectTestClient(t, newMCPServerWithHiddenTools(envs, defaultHiddenToolServices()),
		mcp.NewClient(&mcp.Implementation{Name: "probe-client", Version: "test"}, nil))
	return session, envs, path
}

func clioRun(t *testing.T, session *mcp.ClientSession, tool string, args map[string]any) (*mcp.CallToolResult, string) {
	t.Helper()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "clio-run", Arguments: map[string]any{"command": tool, "args": args}})
	if err != nil {
		t.Fatal(err)
	}
	return result, result.Content[0].(*mcp.TextContent).Text
}

func commandEnvelope(t *testing.T, text string) creatio.CommandResult {
	t.Helper()
	var envelope creatio.CommandResult
	if err := json.Unmarshal([]byte(text), &envelope); err != nil {
		t.Fatalf("not a command envelope: %s", text)
	}
	return envelope
}

func messagesOf(envelope creatio.CommandResult) string {
	var lines []string
	for _, message := range envelope.Messages {
		lines = append(lines, message.MessageType+": "+message.Value)
	}
	return strings.Join(lines, "\n")
}

func readSettingsFile(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatalf("settings file: %v\n%s", err, data)
	}
	return root
}

func TestRegWebAppRegistersAnEnvironmentTheNextCallResolves(t *testing.T) {
	session, envs, path := t16aSession(t)
	fake := newFakeCreatio(t, "registered", false)
	if _, err := envs.client("alpha", creatio.ConnectionOverrides{}); err == nil {
		t.Fatal("alpha resolved before it was registered")
	}
	_, text := clioRun(t, session, "reg-web-app", map[string]any{"environment-name": "alpha", "uri": fake.server.URL + "/",
		"login": "example-user", "password": "pw", "is-net-core": false, "unknown-key": 1})
	envelope := commandEnvelope(t, text)
	if envelope.ExitCode != 0 || messagesOf(envelope) != "Info: Environment alpha was configured..." {
		t.Fatalf("reg-web-app = %s", text)
	}
	client, err := envs.client("ALPHA", creatio.ConnectionOverrides{})
	if err != nil {
		t.Fatalf("alpha after registration, same process: %v", err)
	}
	if rows, err := odataProbe(client); err != nil || rows.Rows[0]["Name"] != "registered" {
		t.Fatalf("alpha rows = %#v, err = %v", rows, err)
	}
	root := readSettingsFile(t, path)
	entry := root["Environments"].(map[string]any)["alpha"].(map[string]any)
	if root["ActiveEnvironmentKey"] != "alpha" || entry["Uri"] != fake.server.URL || entry["Safe"] != false ||
		entry["AuthFlow"] != float64(0) || root["SettingsVersion"] != float64(3) {
		t.Fatalf("settings = %v", root)
	}
	if _, present := entry["EnvironmentPath"]; present {
		t.Fatalf("a new environment without environment-path must not store one: %v", entry)
	}
	if info, err := os.Stat(path); err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o600) {
		t.Fatalf("settings file mode: %v, %v", info, err)
	}
	if _, err := os.Stat(path + ".lock"); err != nil {
		t.Fatalf("clio's settings lock file was not used: %v", err)
	}

	// A second registration merges: the blank login keeps the stored one, Safe is reset to false, the client
	// secret that is not passed again is dropped, and the other environment gains clio's defaults.
	clioRun(t, session, "reg-web-app", map[string]any{"environment-name": "beta", "uri": fake.server.URL, "is-net-core": true,
		"client-id": "example-client", "client-secret": "s", "safe": true})
	_, text = clioRun(t, session, "reg-web-app", map[string]any{"environment-name": "beta", "maintainer": "M", "login": ""})
	if envelope := commandEnvelope(t, text); envelope.ExitCode != 0 {
		t.Fatalf("merge = %s", text)
	}
	root = readSettingsFile(t, path)
	beta := root["Environments"].(map[string]any)["beta"].(map[string]any)
	alpha := root["Environments"].(map[string]any)["alpha"].(map[string]any)
	if beta["Maintainer"] != "M" || beta["Safe"] != false || beta["ClientId"] != "example-client" || beta["ClientSecret"] != nil ||
		beta["IsNetCore"] != true || alpha["EnvironmentPath"] != "" || root["ActiveEnvironmentKey"] != "alpha" {
		t.Fatalf("after merge: alpha %v, beta %v, active %v", alpha, beta, root["ActiveEnvironmentKey"])
	}
}

func TestRegWebAppRefusalsMatchClio(t *testing.T) {
	session, _, path := t16aSession(t)
	for _, test := range []struct {
		args map[string]any
		want string
	}{
		{map[string]any{}, "Error: " + regWebAppMissingTarget},
		{map[string]any{"environment-name": "  "}, "Error: " + regWebAppMissingTarget},
		{map[string]any{"active-environment": "nope"}, "Error: Not found environment nope in settings"},
	} {
		_, text := clioRun(t, session, "reg-web-app", test.args)
		if envelope := commandEnvelope(t, text); envelope.ExitCode != 1 || messagesOf(envelope) != test.want {
			t.Fatalf("%v = %s", test.args, text)
		}
	}
	for _, test := range []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"environment-name": 5}, "invalid-parameter-type: argument 'environment-name' for MCP tool 'reg-web-app' must be a string."},
		{map[string]any{"environment-name": "z", "is-net-core": "true"}, "invalid-parameter-type: argument 'is-net-core' for MCP tool 'reg-web-app' must be a boolean."},
		{map[string]any{"environment-name": "z", "check-login": nil}, "invalid-parameter-type: argument 'check-login' for MCP tool 'reg-web-app' must be a boolean."},
	} {
		result, text := clioRun(t, session, "reg-web-app", test.args)
		if !result.IsError || !strings.Contains(text, test.want) {
			t.Fatalf("%v = %s", test.args, text)
		}
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatalf("a refused call wrote %s", path)
	}
}

func TestRegWebAppSafeActiveAndOpen(t *testing.T) {
	session, _, path := t16aSession(t)
	_, text := clioRun(t, session, "reg-web-app", map[string]any{"environment-name": "prod", "uri": "http://127.0.0.1:9",
		"login": "u", "password": "p", "is-net-core": false, "safe": true})
	envelope := commandEnvelope(t, text)
	want := "Info: Environment prod was configured...\nError: Safe environment confirmation required but it was declined or the " +
		"context is non-interactive. Environment: 'http://127.0.0.1:9'."
	if envelope.ExitCode != 1 || messagesOf(envelope) != want {
		t.Fatalf("safe registration = %s", text)
	}
	_, text = clioRun(t, session, "reg-web-app", map[string]any{"environment-name": "dev", "uri": "http://127.0.0.1:9", "is-net-core": false})
	if commandEnvelope(t, text).ExitCode != 0 {
		t.Fatalf("dev = %s", text)
	}
	// The name is written as given, so a differently cased one leaves the active key unresolved, as in clio.
	_, text = clioRun(t, session, "reg-web-app", map[string]any{"environment-name": "ignored", "active-environment": "DEV"})
	if envelope := commandEnvelope(t, text); envelope.ExitCode != 0 || messagesOf(envelope) != "Info: Active environment set to DEV" {
		t.Fatalf("active = %s", text)
	}
	if root := readSettingsFile(t, path); root["ActiveEnvironmentKey"] != "DEV" || root["Environments"].(map[string]any)["ignored"] != nil {
		t.Fatalf("settings = %v", root)
	}
	_, text = clioRun(t, session, "check-settings-health", map[string]any{"ignored": 1})
	var report map[string]any
	_ = json.Unmarshal([]byte(text), &report)
	if report["status"] != "issues-detected" || report["can-execute-env-tools"] != false || report["active-environment-key"] != "DEV" {
		t.Fatalf("health = %s", text)
	}

	opened := ""
	previous := openSettingsFile
	openSettingsFile = func(file string) error { opened = file; return nil }
	t.Cleanup(func() { openSettingsFile = previous })
	_, text = clioRun(t, session, "reg-web-app", map[string]any{"environment-name": "open"})
	if envelope := commandEnvelope(t, text); envelope.ExitCode != 0 || opened != path || messagesOf(envelope) != "None: Open "+path+"..." {
		t.Fatalf("open = %s, opened %q", text, opened)
	}
}

func TestRegWebAppDetectsTheRuntimeAndChecksTheLogin(t *testing.T) {
	session, _, _ := t16aSession(t)
	logins := 0
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ServiceModel/AuthService.svc/Login":
			logins++
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["UserPassword"] != "pw" {
				_, _ = w.Write([]byte(`{"Code":1,"Message":"Invalid user name or password"}`))
				return
			}
			http.SetCookie(w, &http.Cookie{Name: ".ASPXAUTH", Value: "a", Path: "/"})
			http.SetCookie(w, &http.Cookie{Name: "BPMCSRF", Value: "c", Path: "/"})
			_, _ = w.Write([]byte(`{"Code":0}`))
		case "/0/Login/NuiLogin.aspx", "/0/api/HealthCheck/Ping":
			_, _ = w.Write([]byte("ok"))
		case "/0/DataService/json/SyncReply/SelectQuery":
			if r.Header.Get("BPMCSRF") != "c" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"success":true,"rows":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(site.Close)
	_, text := clioRun(t, session, "reg-web-app", map[string]any{"environment-name": "fw", "uri": site.URL, "login": "Supervisor", "password": "pw", "check-login": true})
	envelope := commandEnvelope(t, text)
	want := "Info: Auto-detected runtime: .NET Framework\nInfo: Environment fw was configured...\n" +
		"Info: Try login to " + site.URL + " with Supervisor credentials ...\nInfo: Login successful"
	if envelope.ExitCode != 0 || messagesOf(envelope) != want {
		t.Fatalf("detect + check-login = %s", text)
	}
	_, text = clioRun(t, session, "reg-web-app", map[string]any{"environment-name": "fw", "password": "bad", "check-login": true})
	envelope = commandEnvelope(t, text)
	want = "Info: Environment fw was configured...\nInfo: Try login to " + site.URL + " with Supervisor credentials ...\n" +
		"Error: Unauthorized Supervisor for " + site.URL
	if envelope.ExitCode != 1 || messagesOf(envelope) != want {
		t.Fatalf("rejected check-login = %s", text)
	}

	// A detection refusal on the uri already recorded keeps that runtime, with clio's warning.
	previous := detectRuntime
	detectRuntime = func(context.Context, creatio.RegistrationTarget) (bool, error) {
		return false, errors.New("Unable to decide.")
	}
	t.Cleanup(func() { detectRuntime = previous })
	_, text = clioRun(t, session, "reg-web-app", map[string]any{"environment-name": "fw", "uri": site.URL + "/"})
	envelope = commandEnvelope(t, text)
	want = "Warning: Unable to decide. Keeping the runtime already recorded for fw: .NET Framework. Pass --IsNetCore to change it.\n" +
		"Info: Environment fw was configured..."
	if envelope.ExitCode != 0 || messagesOf(envelope) != want {
		t.Fatalf("kept runtime = %s", text)
	}
	_, text = clioRun(t, session, "reg-web-app", map[string]any{"environment-name": "other", "uri": site.URL})
	if envelope = commandEnvelope(t, text); envelope.ExitCode != 1 || messagesOf(envelope) != "Error: Unable to decide." {
		t.Fatalf("refused detection = %s", text)
	}
}
