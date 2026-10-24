package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// identitySite is a .NET Framework Creatio that logs in, answers canUseAuthorizationCodeFlow with answer,
// serves the Shell to a logged-in browser and holds OAuth20IdentityServerUrl = identityURL.
func identitySite(t *testing.T, answer, identityURL string) *httptest.Server {
	t.Helper()
	value := ""
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authed := strings.Contains(r.Header.Get("Cookie"), ".ASPXAUTH=a")
		switch r.URL.Path {
		case "/ServiceModel/AuthService.svc/Login":
			http.SetCookie(w, &http.Cookie{Name: ".ASPXAUTH", Value: "a", Path: "/", HttpOnly: true})
			http.SetCookie(w, &http.Cookie{Name: "BPMCSRF", Value: "c", Path: "/"})
			_, _ = w.Write([]byte(`{"Code":0}`))
		case "/0/identityServiceInfo/canUseAuthorizationCodeFlow":
			_, _ = w.Write([]byte(answer))
		case "/0/Shell/":
			if !authed {
				w.WriteHeader(http.StatusFound)
				return
			}
			_, _ = w.Write([]byte("<html></html>"))
		case "/0/DataService/json/SyncReply/SelectQuery":
			var body string
			if data, err := readAll(r); err == nil {
				body = data
			}
			switch {
			case strings.Contains(body, `"rootSchemaName":"SysSettingsValue"`):
				_, _ = w.Write([]byte(`{"success":true,"rows":[{"TextValue":"` + value + `"}]}`))
			case strings.Contains(body, "OAuth20IdentityServerUrl") && identityURL != "":
				value = identityURL
				_, _ = w.Write([]byte(`{"success":true,"rows":[{"Id":"11111111-1111-1111-1111-111111111111","ValueTypeName":"Text"}]}`))
			default:
				_, _ = w.Write([]byte(`{"success":true,"rows":[]}`))
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(site.Close)
	return site
}

func readAll(r *http.Request) (string, error) {
	var builder strings.Builder
	buffer := make([]byte, 4096)
	for {
		n, err := r.Body.Read(buffer)
		builder.Write(buffer[:n])
		if err != nil {
			return builder.String(), nil
		}
	}
}

func payloadOf(t *testing.T, text string) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		t.Fatalf("answer is not JSON: %s", text)
	}
	return payload
}

func TestBrowserSessionToolsMatchClio(t *testing.T) {
	session, _, path := t16aSession(t)
	site := identitySite(t, "true", "")
	writeSettings(t, path, "fw", map[string]map[string]any{
		"fw":    {"Uri": site.URL, "Login": "Supervisor", "Password": "pw", "IsNetCore": false},
		"oauth": {"Uri": site.URL, "ClientId": "c", "ClientSecret": "s"},
		"prod":  {"Uri": site.URL, "Login": "Supervisor", "Password": "pw", "Safe": true},
	})
	_, text := clioRun(t, session, "get-browser-session", map[string]any{"environment-name": "FW", "other": 1})
	answer := payloadOf(t, text)
	file, _ := answer["session-file-path"].(string)
	if answer["success"] != true || filepath.Dir(file) != filepath.Join(filepath.Dir(path), "sessions") || strings.Contains(text, `"error"`) {
		t.Fatalf("get-browser-session = %s", text)
	}
	if data, err := os.ReadFile(file); err != nil || !strings.Contains(string(data), `"name":".ASPXAUTH","value":"a"`) || strings.Contains(text, `"value"`) {
		t.Fatalf("session file %s: %v (answer %s)", data, err, text)
	}
	for _, test := range []struct {
		args map[string]any
		want string
	}{
		{map[string]any{}, "Failed to obtain the browser session: " + missingTargetMessage},
		{map[string]any{"environment-name": "nope"}, "Failed to obtain the browser session: Environment with key 'nope' not found."},
		{map[string]any{"environment-name": "prod"}, "Safe environment confirmation required but it was declined or the context is non-interactive. Environment: '[redacted-uri]'."},
		{map[string]any{"environment-name": "oauth"}, "browser session handoff requires forms-auth credentials (login + password) for environment '[redacted-uri]'"},
	} {
		_, text := clioRun(t, session, "get-browser-session", test.args)
		answer := payloadOf(t, text)
		if message, _ := answer["error"].(string); answer["success"] != false || !strings.HasPrefix(message, test.want) || answer["session-file-path"] != nil {
			t.Fatalf("%v = %s", test.args, text)
		}
	}
	if result, text := clioRun(t, session, "get-browser-session", map[string]any{"environment-name": "fw", "force-refresh": "yes"}); !result.IsError ||
		!strings.Contains(text, "argument 'force-refresh' for MCP tool 'get-browser-session' must be a boolean") {
		t.Fatalf("bad force-refresh = %s", text)
	}

	_, text = clioRun(t, session, "clear-browser-session", map[string]any{"environment-name": "Fw"})
	if answer := payloadOf(t, text); answer["success"] != true || answer["message"] != "Browser session for 'Fw' cleared." {
		t.Fatalf("clear = %s", text)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("session file after clear: %v", err)
	}
	for name, want := range map[string]string{"": clearBrowserSessionFailure, "nope": clearBrowserSessionFailure,
		"prod": "Safe environment confirmation required but it was declined or the context is non-interactive. Environment: '[redacted-uri]'."} {
		_, text := clioRun(t, session, "clear-browser-session", map[string]any{"environment-name": name})
		if answer := payloadOf(t, text); answer["success"] != false || answer["error"] != want {
			t.Fatalf("clear %q = %s", name, text)
		}
	}
	direct, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "clear-browser-session", Arguments: map[string]any{"environment-name": "fw"}})
	if err != nil || !direct.IsError || !strings.Contains(direct.Content[0].(*mcp.TextContent).Text, "can overwrite or delete existing state") {
		t.Fatalf("direct clear-browser-session = %#v, %v", direct, err)
	}
}

func TestIdentityServiceToolsMatchClio(t *testing.T) {
	session, _, path := t16aSession(t)
	discovery := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		base := "http://" + r.Host
		_, _ = w.Write([]byte(`{"issuer":"` + base + `","token_endpoint":"` + base + `/connect/token","jwks_uri":"` + base +
			`/jwks","authorization_endpoint":"` + base + `/connect/authorize"}`))
	}))
	t.Cleanup(discovery.Close)
	yes := identitySite(t, "true", discovery.URL+"/")
	refused := identitySite(t, `{"error":"invalid_client","error_description":"bad client"}`, "")
	writeSettings(t, path, "yes", map[string]map[string]any{
		"yes":     {"Uri": yes.URL, "Login": "Supervisor", "Password": "pw"},
		"refused": {"Uri": refused.URL, "Login": "Supervisor", "Password": "pw"},
		"prod":    {"Uri": yes.URL, "Login": "Supervisor", "Password": "pw", "Safe": true},
	})
	for _, test := range []struct {
		args map[string]any
		want string
		exit int
	}{
		{map[string]any{"environmentName": "yes"}, "None: true\nInfo: Done check-auth-code-flow", 0},
		{map[string]any{"environmentName": "yes", "format": "JSON"}, `None: {"canUseAuthorizationCodeFlow":true}` + "\nInfo: Done check-auth-code-flow", 0},
		{map[string]any{"environmentName": "refused"}, "Error: invalid_client: bad client", 1},
		{map[string]any{"environmentName": "refused", "format": "json"}, "None: {\n  \"error\": \"invalid_client\",\n  \"error_description\": \"bad client\"\n}", 1},
		{map[string]any{"environmentName": " "}, "Error: environment-name is required and cannot be empty.", 1},
		{map[string]any{"environmentName": "nope"}, "Error: [EnvironmentResolutionException] Environment with key 'nope' not found.", 1},
	} {
		_, text := clioRun(t, session, "check-auth-code-flow", test.args)
		envelope := commandEnvelope(t, text)
		if envelope.ExitCode != test.exit || !strings.HasPrefix(messagesOf(envelope), test.want) {
			t.Fatalf("%v = %s", test.args, text)
		}
	}
	for args, want := range map[string]string{
		"{}":                         "The arguments dictionary is missing a value for the required parameter 'environmentName'.",
		`{"environmentName":"prod"}`: "Safe environment confirmation required",
	} {
		var decoded map[string]any
		_ = json.Unmarshal([]byte(args), &decoded)
		result, text := clioRun(t, session, "check-auth-code-flow", decoded)
		if !result.IsError || !strings.Contains(text, want) {
			t.Fatalf("%s = %s", args, text)
		}
	}

	_, text := clioRun(t, session, "get-identity-service-config", map[string]any{"environment-name": "yes"})
	want := `{"success":true,"config":{"identityServerUrl":"` + discovery.URL + `","source":"setting","tokenEndpoint":"` + discovery.URL +
		`/connect/token","discoveryEndpoint":"` + discovery.URL + `/.well-known/openid-configuration","reachable":true}}`
	if text != want {
		t.Fatalf("get-identity-service-config\n got %s\nwant %s", text, want)
	}
	_, text = clioRun(t, session, "get-identity-service-config", map[string]any{"environment-name": "refused"})
	if answer := payloadOf(t, text); answer["success"] != true || answer["config"].(map[string]any)["source"] != "derived" ||
		answer["config"].(map[string]any)["reachable"] != false {
		t.Fatalf("derived = %s", text)
	}
	for name, want := range map[string]string{"": missingTargetMessage, "nope": "Environment with key 'nope' not found.",
		"prod": "Safe environment confirmation required but it was declined or the context is non-interactive. Environment: '[redacted-uri]'."} {
		_, text := clioRun(t, session, "get-identity-service-config", map[string]any{"environment-name": name})
		if answer := payloadOf(t, text); answer["success"] != false || !strings.HasPrefix(answer["error"].(string), want) || answer["config"] != nil {
			t.Fatalf("%q = %s", name, text)
		}
	}
}
