package creatio

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func adminWriteIdentityStand(t *testing.T, status int, body string, seen *[]string) *Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/Login") {
			_, _ = w.Write([]byte(`{"Code":0}`))
			return
		}
		*seen = append(*seen, r.Method+" "+r.URL.Path)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return newFormsTestClient(t, server.URL)
}

func TestIdentityCommandsReadTheirAnswers(t *testing.T) {
	cases := []struct {
		tool     AdminWriteIdentityTool
		format   string
		status   int
		body     string
		route    string
		exitCode int
		messages []LogMessage
	}{
		{AdminWriteIdentityAssertion, "", 200, `{"assertion":"eyJ.a.b","assertionType":"urn:jwt","expiresIn":300,"expiresAt":"2026-10-10T12:00:00Z"}`,
			"POST /0/identityAssertion/currentUser", 0, []LogMessage{
				{"Info", "Assertion type: urn:jwt; expires in 300s (at 2026-10-10 12:00:00Z)."},
				{"None", "eyJ.a.b"}, {"Info", "Done get-identity-assertion"}}},
		{AdminWriteIdentityAssertion, "text", 200, `{"assertionType":"x"}`, "POST /0/identityAssertion/currentUser", 1, []LogMessage{
			{"Error", "Response did not contain an assertion token."}, {"None", `{"assertionType":"x"}`}}},
		{AdminWriteIdentityPublicJwk, "", 200, `{ "kty": "RSA", "n": "abc" }`, "GET /0/identityAssertion/publicJwk", 0, []LogMessage{
			{"None", `{"kty":"RSA","n":"abc"}`}, {"Info", "Done get-identity-public-jwk"}}},
		{AdminWriteIdentityPublicJwk, "JSON", 400, `{"error":"AccessDenied","error_description":"No rights"}`, "GET /0/identityAssertion/publicJwk", 1,
			[]LogMessage{{"None", "{\n  \"error\": \"AccessDenied\",\n  \"error_description\": \"No rights\"\n}"}}},
		{AdminWriteIdentityPublicJwk, "", 200, ``, "GET /0/identityAssertion/publicJwk", 1, []LogMessage{
			{"Error", "Empty response received from server."}, {"Error", "Endpoint: SERVER/0/identityAssertion/publicJwk"}}},
		{AdminWriteIdentityRegenerate, "", 204, ``, "POST /0/identityAssertion/regenerateSigningKey", 0, []LogMessage{
			{"None", "OK"}, {"Info", "Done regenerate-identity-signing-key"}}},
		{AdminWriteIdentityRegenerate, "json", 204, ``, "POST /0/identityAssertion/regenerateSigningKey", 0, []LogMessage{
			{"None", `{"status":"regenerated"}`}, {"Info", "Done regenerate-identity-signing-key"}}},
		{AdminWriteIdentityRegenerate, "", 400, `{"error":"FeatureDisabled"}`, "POST /0/identityAssertion/regenerateSigningKey", 1,
			[]LogMessage{{"Error", "FeatureDisabled: "}}},
	}
	for _, c := range cases {
		var seen []string
		client := adminWriteIdentityStand(t, c.status, c.body, &seen)
		result := client.IdentityCommand(context.Background(), c.tool, c.format)
		for index := range result.Messages {
			result.Messages[index].Value = strings.ReplaceAll(result.Messages[index].Value, client.config.BaseURL, "SERVER")
		}
		if result.ExitCode != c.exitCode || !reflect.DeepEqual(result.Messages, c.messages) {
			t.Errorf("%s %q = %#v", c.tool.Name, c.format, result)
		}
		if len(seen) != 1 || seen[0] != c.route {
			t.Errorf("%s requests = %v", c.tool.Name, seen)
		}
	}
}
