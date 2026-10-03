package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakeCreatio answers forms login, OAuth token and one OData route, and counts the authentications, so a
// test can prove which server answered and that a client is reused.
type fakeCreatio struct {
	server *httptest.Server
	logins atomic.Int32
	tokens atomic.Int32
}

func newFakeCreatio(t *testing.T, name string, oauth bool) *fakeCreatio {
	t.Helper()
	fake := &fakeCreatio{}
	fake.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ServiceModel/AuthService.svc/Login":
			fake.logins.Add(1)
			http.SetCookie(w, &http.Cookie{Name: "BPMCSRF", Value: "csrf-" + name})
			_, _ = w.Write([]byte(`{"Code":0}`))
		case "/connect/token":
			fake.tokens.Add(1)
			_, _ = w.Write([]byte(`{"access_token":"token-` + name + `","expires_in":3600}`))
		case "/0/odata/Contact":
			if oauth && r.Header.Get("Authorization") != "Bearer token-"+name {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = fmt.Fprintf(w, `{"value":[{"Name":%q}]}`, name)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(fake.server.Close)
	return fake
}

// writeSettings writes a clio appsettings.json with invented names and the fake servers' addresses.
func writeSettings(t *testing.T, path, active string, environments map[string]map[string]any) {
	t.Helper()
	encoded, err := json.Marshal(map[string]any{"ActiveEnvironmentKey": active, "Environments": environments})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	// Change detection reads the modification time; move it forward so two writes in one tick still differ.
	stamp := time.Now().Add(time.Duration(len(encoded)) * time.Millisecond)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
}

func clearCreatioVariables(t *testing.T) {
	for _, name := range []string{"CREATIO_URL", "CREATIO_MCP_BASE_URL", "CREATIO_LOGIN", "CREATIO_MCP_LOGIN", "CREATIO_PASSWORD",
		"CREATIO_MCP_PASSWORD", "CREATIO_CLIENT_ID", "CREATIO_CLIENT_SECRET", "CREATIO_AUTH_APP_URI", "CREATIO_TOKEN_URL", "CREATIO_IS_NET_CORE"} {
		t.Setenv(name, "")
	}
}

type twoEnvironments struct {
	path         string
	forms, oauth *fakeCreatio
	envs         *environments
}

func newTwoEnvironments(t *testing.T) twoEnvironments {
	t.Helper()
	clearCreatioVariables(t)
	setup := twoEnvironments{path: filepath.Join(t.TempDir(), "appsettings.json"),
		forms: newFakeCreatio(t, "forms", false), oauth: newFakeCreatio(t, "oauth", true)}
	writeSettings(t, setup.path, "Beta-oauth", map[string]map[string]any{
		"alpha-forms": {"Uri": setup.forms.server.URL, "Login": "example-user", "Password": "replace-me", "IsNetCore": false},
		"Beta-oauth": {"Uri": setup.oauth.server.URL, "ClientId": "example-client", "ClientSecret": "replace-me",
			"AuthAppUri": setup.oauth.server.URL + "/connect/token", "DeveloperModeEnabled": false},
	})
	setup.envs = newEnvironments(setup.path)
	return setup
}

func odataName(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()
	var decoded struct {
		Rows []map[string]any `json:"rows"`
	}
	if result.IsError || json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &decoded) != nil || len(decoded.Rows) != 1 {
		t.Fatalf("odata-read result = %#v", result.Content[0])
	}
	name, _ := decoded.Rows[0]["Name"].(string)
	return name
}

func TestOneProcessServesTwoEnvironmentsInParallel(t *testing.T) {
	setup := newTwoEnvironments(t)
	session := connectTestClient(t, newMCPServerWithHiddenTools(setup.envs, defaultHiddenToolServices()),
		mcp.NewClient(&mcp.Implementation{Name: "probe-client", Version: "test"}, nil))
	var wait sync.WaitGroup
	errs := make(chan string, 40)
	for i := 0; i < 40; i++ {
		wait.Add(1)
		go func(i int) {
			defer wait.Done()
			// Names match case-insensitively, as clio matches them.
			name, want := "ALPHA-forms", "forms"
			if i%2 == 1 {
				name, want = "beta-OAUTH", "oauth"
			}
			result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "odata-read",
				Arguments: map[string]any{"environment-name": name, "entity": "Contact", "top": 1}})
			if err != nil {
				errs <- err.Error()
				return
			}
			var decoded struct {
				Rows []map[string]any `json:"rows"`
			}
			if result.IsError || json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &decoded) != nil ||
				len(decoded.Rows) != 1 || decoded.Rows[0]["Name"] != want {
				errs <- fmt.Sprintf("%s answered %#v", name, result.Content[0])
			}
		}(i)
	}
	wait.Wait()
	close(errs)
	for message := range errs {
		t.Error(message)
	}
	// One authenticated client per environment: one forms login and one token for all forty calls.
	if setup.forms.logins.Load() != 1 || setup.oauth.tokens.Load() != 1 {
		t.Fatalf("logins = %d, tokens = %d", setup.forms.logins.Load(), setup.oauth.tokens.Load())
	}
}

func TestUnknownEnvironmentAnswersWithClioText(t *testing.T) {
	setup := newTwoEnvironments(t)
	_, err := setup.envs.client("zz-nope", creatio.ConnectionOverrides{})
	want := "Environment with key 'zz-nope' not found. Available environments: alpha-forms, Beta-oauth (use `list-environments` to inspect them). " +
		`To register it from this MCP session, call the clio-run tool with {"command":"reg-web-app","args":{"environment-name":"zz-nope","uri":"<url>","login":"<login>","password":"<password>"}} ` +
		"— that writes appsettings.json and updates this running server in one step. This server holds the environment list it loaded from " +
		"appsettings.json at start; `list-environments` and environment resolution re-read that file at call time, but tools bound at server " +
		"start still answer from the loaded copy, so an edit made outside this process (Bash, or `clio reg-web-app` in another process) is not " +
		"guaranteed to be seen before a restart."
	if err == nil || err.Error() != want || !isEnvironmentError(err) {
		t.Fatalf("err = %v", err)
	}
	if got := redacted(err); !strings.Contains(got, `"password":"[redacted]"`) || strings.Contains(got, "<password>") {
		t.Fatalf("redacted = %s", got)
	}
	session := connectTestClient(t, newMCPServerWithHiddenTools(setup.envs, defaultHiddenToolServices()),
		mcp.NewClient(&mcp.Implementation{Name: "probe-client", Version: "test"}, nil))
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "list-apps", Arguments: map[string]any{"environment-name": "zz-nope"}})
	if err != nil {
		t.Fatal(err)
	}
	var envelope creatio.AppListResponse
	if json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &envelope) != nil || envelope.Success ||
		envelope.Error != strings.ReplaceAll(want, `"password":"<password>"`, `"password":"[redacted]"`) {
		t.Fatalf("list-apps = %#v", result.Content[0])
	}
}

func TestSettingsAreReloadedWhenTheFileChanges(t *testing.T) {
	setup := newTwoEnvironments(t)
	if _, err := setup.envs.client("gamma", creatio.ConnectionOverrides{}); err == nil {
		t.Fatal("gamma resolved before it was registered")
	}
	writeSettings(t, setup.path, "", map[string]map[string]any{
		"gamma": {"Uri": setup.forms.server.URL, "Login": "example-user", "Password": "replace-me"},
	})
	client, err := setup.envs.client("gamma", creatio.ConnectionOverrides{})
	if err != nil {
		t.Fatalf("gamma after the file changed: %v", err)
	}
	if rows, err := client.ODataRead(context.Background(), creatio.ODataReadRequest{Entity: "Contact"}); err != nil || rows.Rows[0]["Name"] != "forms" {
		t.Fatalf("gamma rows = %#v, err = %v", rows, err)
	}
	_, err = setup.envs.client("alpha-forms", creatio.ConnectionOverrides{})
	if err == nil || !strings.Contains(err.Error(), "Available environments: gamma (use") {
		t.Fatalf("removed environment err = %v", err)
	}
	// A file that becomes unreadable keeps the last good snapshot and says so in list-environments.
	if err := os.WriteFile(setup.path, []byte("{ not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Hour)
	_ = os.Chtimes(setup.path, future, future)
	if _, err := setup.envs.client("gamma", creatio.ConnectionOverrides{}); err != nil {
		t.Fatalf("gamma after a broken write: %v", err)
	}
	listed := listEnvironmentsJSON(t, setup.envs)
	if warnings, _ := listed["warnings"].([]any); len(warnings) != 1 || !strings.Contains(warnings[0].(string), "The previously loaded settings are still in use.") {
		t.Fatalf("warnings = %#v", listed["warnings"])
	}
}

func listEnvironmentsJSON(t *testing.T, envs *environments) map[string]any {
	t.Helper()
	result := envs.listEnvironments()
	if result.StructuredContent != nil {
		t.Fatal("list-environments must answer with text only, as clio does")
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded
}

func TestListEnvironmentsMasksSecretsLikeClio(t *testing.T) {
	setup := newTwoEnvironments(t)
	text := setup.envs.listEnvironments().Content[0].(*mcp.TextContent).Text
	if strings.Contains(text, "replace-me") {
		t.Fatalf("a secret leaked: %s", text)
	}
	listed := listEnvironmentsJSON(t, setup.envs)
	if _, ok := listed["warnings"]; ok || listed["settingsFilePath"] != setup.path {
		t.Fatalf("envelope = %#v", listed)
	}
	environments := listed["environments"].([]any)
	forms, oauth := environments[0].(map[string]any), environments[1].(map[string]any)
	if forms["name"] != "alpha-forms" || forms["isActive"] != false || forms["password"] != "****" || forms["login"] != "example-user" ||
		forms["simpleLoginUri"] != setup.forms.server.URL+"/0/Shell/?simplelogin=true" || forms["isDevMode"] != false {
		t.Fatalf("forms = %#v", forms)
	}
	if _, ok := forms["developerModeEnabled"]; ok {
		t.Fatalf("an absent DeveloperModeEnabled was written: %#v", forms)
	}
	if oauth["name"] != "Beta-oauth" || oauth["isActive"] != true || oauth["clientId"] != "example-client" ||
		oauth["clientSecret"] != "****" || oauth["developerModeEnabled"] != false {
		t.Fatalf("oauth = %#v", oauth)
	}
	if _, ok := oauth["password"]; ok {
		t.Fatalf("an empty password was written: %#v", oauth)
	}
	// The element keys follow clio's member order.
	if !strings.Contains(text, `{"name":"alpha-forms","isActive":false,"uri":`) {
		t.Fatalf("key order = %s", text)
	}
}

func TestMissingSettingsFileMeansNoEnvironments(t *testing.T) {
	clearCreatioVariables(t)
	envs := newEnvironments(filepath.Join(t.TempDir(), "appsettings.json"))
	_, err := envs.client("dev", creatio.ConnectionOverrides{})
	if err == nil || !strings.HasPrefix(err.Error(), "Environment with key 'dev' not found. No environments are registered. To register it") {
		t.Fatalf("err = %v", err)
	}
	if _, err := envs.client("", creatio.ConnectionOverrides{}); err == nil || err.Error() != missingTargetMessage {
		t.Fatalf("no target err = %v", err)
	}
	if listed := listEnvironmentsJSON(t, envs); len(listed["environments"].([]any)) != 0 {
		t.Fatalf("listed = %#v", listed)
	}
}

func TestDefaultTargetIsCreatioVariablesThenTheActiveEnvironment(t *testing.T) {
	setup := newTwoEnvironments(t)
	client, err := setup.envs.client("", creatio.ConnectionOverrides{})
	if err != nil {
		t.Fatal(err)
	}
	if rows, err := client.ODataRead(context.Background(), creatio.ODataReadRequest{Entity: "Contact"}); err != nil || rows.Rows[0]["Name"] != "oauth" {
		t.Fatalf("active environment rows = %#v, err = %v", rows, err)
	}
	t.Setenv("CREATIO_URL", setup.forms.server.URL)
	t.Setenv("CREATIO_LOGIN", "example-user")
	t.Setenv("CREATIO_PASSWORD", "replace-me")
	withVariables := newEnvironments(setup.path)
	client, err = withVariables.client("", creatio.ConnectionOverrides{})
	if err != nil {
		t.Fatal(err)
	}
	if rows, err := client.ODataRead(context.Background(), creatio.ODataReadRequest{Entity: "Contact"}); err != nil || rows.Rows[0]["Name"] != "forms" {
		t.Fatalf("CREATIO_* rows = %#v, err = %v", rows, err)
	}
	// A named environment still wins over the CREATIO_* default.
	client, err = withVariables.client("beta-oauth", creatio.ConnectionOverrides{})
	if err != nil {
		t.Fatal(err)
	}
	if rows, err := client.ODataRead(context.Background(), creatio.ODataReadRequest{Entity: "Contact"}); err != nil || rows.Rows[0]["Name"] != "oauth" {
		t.Fatalf("named rows = %#v, err = %v", rows, err)
	}
}

func TestDirectConnectionArgumentsBuildAOneOffClient(t *testing.T) {
	setup := newTwoEnvironments(t)
	args := map[string]any{"uri": setup.forms.server.URL, "login": "example-user", "password": "replace-me"}
	client, refusal, err := setup.envs.resolve("get-page", args, scopeDirect)
	if err != nil || refusal != nil {
		t.Fatalf("refusal = %v, err = %v", refusal, err)
	}
	if rows, err := client.ODataRead(context.Background(), creatio.ODataReadRequest{Entity: "Contact"}); err != nil || rows.Rows[0]["Name"] != "forms" {
		t.Fatalf("direct rows = %#v, err = %v", rows, err)
	}
	// A tool whose clio counterpart has no uri argument ignores it and serves the default target.
	client, _, _ = setup.envs.resolve("get-app-info", args, scopeName)
	if rows, err := client.ODataRead(context.Background(), creatio.ODataReadRequest{Entity: "Contact"}); err != nil || rows.Rows[0]["Name"] != "oauth" {
		t.Fatalf("name-only rows = %#v, err = %v", rows, err)
	}
	// uri next to a registered name repoints it, as clio's EnvironmentSettings.Fill does.
	client, _, _ = setup.envs.resolve("get-page", map[string]any{"environment-name": "alpha-forms", "uri": setup.oauth.server.URL}, scopeDirect)
	if _, err := client.ODataRead(context.Background(), creatio.ODataReadRequest{Entity: "Contact"}); err == nil {
		t.Fatal("forms credentials were accepted by the OAuth-only fake")
	}
}

func TestSafeEnvironmentIsRefusedLikeClio(t *testing.T) {
	clearCreatioVariables(t)
	path := filepath.Join(t.TempDir(), "appsettings.json")
	writeSettings(t, path, "", map[string]map[string]any{"prod": {"Uri": "https://prod.example.com", "Login": "u", "Password": "p", "Safe": true}})
	_, err := newEnvironments(path).client("prod", creatio.ConnectionOverrides{})
	want := "Safe environment confirmation required but it was declined or the context is non-interactive. Environment: 'https://prod.example.com'."
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v", err)
	}
	if text := resolverErrorText(err); text != "[SafeEnvironmentConfirmationRequiredException] "+want {
		t.Fatalf("command text = %s", text)
	}
}
