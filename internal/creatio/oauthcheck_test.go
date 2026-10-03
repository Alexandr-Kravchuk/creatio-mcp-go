package creatio

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestResolveOAuthSystemUserQueriesByIDBeforeName(t *testing.T) {
	var filter map[string]any
	server := groupCServer(t, func(path string, body map[string]any) (int, string) {
		if path != "/0/DataService/json/SyncReply/SelectQuery" || body["rootSchemaName"] != "SysAdminUnit" || body["rowCount"] != float64(1) {
			t.Errorf("unexpected request %s %v", path, body)
		}
		filter = body["filters"].(map[string]any)["items"].(map[string]any)["filter0"].(map[string]any)
		return http.StatusOK, `{"success":true,"rows":[{"Id":"7f3b869f-34f3-4f20-ab4d-7480a5fdf647","Name":"Supervisor"}]}`
	})
	defer server.Close()
	result := newFormsTestClient(t, server.URL).ResolveOAuthSystemUser(context.Background(), "Other", " 7F3B869F-34F3-4F20-AB4D-7480A5FDF647 ")
	encoded, _ := json.Marshal(result)
	if want := `{"success":true,"user":{"systemUserId":"7f3b869f-34f3-4f20-ab4d-7480a5fdf647","name":"Supervisor","found":true}}`; string(encoded) != want {
		t.Fatalf("result = %s, want %s", encoded, want)
	}
	parameter := filter["rightExpression"].(map[string]any)["parameter"].(map[string]any)
	if filter["leftExpression"].(map[string]any)["columnPath"] != "Id" || parameter["dataValueType"] != float64(0) ||
		parameter["value"] != "7F3B869F-34F3-4F20-AB4D-7480A5FDF647" {
		t.Fatalf("filter = %v", filter)
	}
}

func TestResolveOAuthSystemUserDefaultsToSupervisorAndReportsAMiss(t *testing.T) {
	var value any
	server := groupCServer(t, func(_ string, body map[string]any) (int, string) {
		filter := body["filters"].(map[string]any)["items"].(map[string]any)["filter0"].(map[string]any)
		value = filter["rightExpression"].(map[string]any)["parameter"].(map[string]any)["value"]
		return http.StatusOK, `{"success":true,"rows":[]}`
	})
	defer server.Close()
	encoded, _ := json.Marshal(newFormsTestClient(t, server.URL).ResolveOAuthSystemUser(context.Background(), "  ", ""))
	if string(encoded) != `{"success":true,"user":{"found":false}}` || value != "Supervisor" {
		t.Fatalf("result = %s, filter value = %v", encoded, value)
	}
}

func TestResolveOAuthSystemUserReportsTheRejectedBody(t *testing.T) {
	body := `{"responseStatus":{"ErrorCode":"FormatException","Message":"Guid should contain 32 digits"},"success":false}`
	server := groupCServer(t, func(string, map[string]any) (int, string) { return http.StatusInternalServerError, body })
	defer server.Close()
	result := newFormsTestClient(t, server.URL).ResolveOAuthSystemUser(context.Background(), "", "not-a-guid")
	if result.Success || result.User != nil || result.Error != "SelectQuery failed: "+body {
		t.Fatalf("result = %#v", result)
	}
}

// oauthCheckServers stands up an IdentityService and a Creatio host that accepts only the issued token.
func oauthCheckServers(t *testing.T, smokeStatus int, smokeBody string) (identity, creatio *httptest.Server, tokenForm *url.Values) {
	t.Helper()
	tokenForm = &url.Values{}
	identity = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		*tokenForm, _ = url.ParseQuery(string(raw))
		if r.URL.Path != "/connect/token" || r.Header.Get("Authorization") != "" || tokenForm.Get("client_secret") != "right" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"issued-token","token_type":"bearer","expires_in":3600}`))
	}))
	creatio = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/0/DataService/json/SyncReply/SelectQuery" || r.Header.Get("Authorization") != "Bearer issued-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		if string(raw) != oauthCheckSmokeQuery {
			t.Errorf("smoke body = %s", raw)
		}
		w.WriteHeader(smokeStatus)
		_, _ = w.Write([]byte(smokeBody))
	}))
	return identity, creatio, tokenForm
}

func TestVerifyOAuthAppUsesConfiguredCredentialsAndTokenEndpoint(t *testing.T) {
	identity, creatioServer, form := oauthCheckServers(t, http.StatusOK, `{"success":true,"rows":[]}`)
	defer identity.Close()
	defer creatioServer.Close()
	client, err := NewClient(Config{BaseURL: creatioServer.URL, ClientID: "app", Secret: "right", TokenURL: identity.URL + "/CONNECT/TOKEN/"})
	if err != nil {
		t.Fatal(err)
	}
	result := client.VerifyOAuthApp(context.Background(), VerifyOAuthAppRequest{})
	want := VerifyOAuthAppOutcome{TokenAcquired: true, DataServiceStatus: http.StatusOK, OK: true, IdentityServerURL: identity.URL}
	if !result.Success || result.Result == nil || *result.Result != want {
		t.Fatalf("result = %#v", result)
	}
	if form.Get("grant_type") != "client_credentials" || form.Get("client_id") != "app" {
		t.Fatalf("token form = %v", form)
	}
	if encoded, _ := json.Marshal(result); strings.Contains(string(encoded), "issued-token") {
		t.Fatalf("token leaked: %s", encoded)
	}
}

func TestVerifyOAuthAppReportsRejectedTokenAndSmokeStatus(t *testing.T) {
	identity, creatioServer, _ := oauthCheckServers(t, http.StatusOK, `{"success":true}`)
	defer identity.Close()
	defer creatioServer.Close()
	client := newFormsTestClient(t, creatioServer.URL)
	id, wrong := "app", "wrong"
	result := client.VerifyOAuthApp(context.Background(), VerifyOAuthAppRequest{ClientID: &id, ClientSecret: &wrong, IdentityServerURL: identity.URL})
	if !result.Success || result.Result.TokenAcquired || result.Result.DataServiceStatus != 0 || result.Result.OK {
		t.Fatalf("rejected token result = %#v", result.Result)
	}

	rejecting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) }))
	defer rejecting.Close()
	right := "right"
	result = newFormsTestClient(t, rejecting.URL).VerifyOAuthApp(context.Background(),
		VerifyOAuthAppRequest{ClientID: &id, ClientSecret: &right, IdentityServerURL: identity.URL + "/"})
	if !result.Success || !result.Result.TokenAcquired || result.Result.DataServiceStatus != http.StatusUnauthorized || result.Result.OK {
		t.Fatalf("rejected smoke result = %#v", result.Result)
	}
}

func TestVerifyOAuthAppFailsWithClioTextForEveryCause(t *testing.T) {
	identity, creatioServer, _ := oauthCheckServers(t, http.StatusOK, `{"success":false}`)
	defer identity.Close()
	defer creatioServer.Close()
	id, right, blank := "app", "right", ""
	cases := map[string]VerifyOAuthAppRequest{
		"no configured credentials": {},
		"blank secret is explicit":  {ClientID: &id, ClientSecret: &blank, IdentityServerURL: identity.URL},
		"url with query":            {ClientID: &id, ClientSecret: &right, IdentityServerURL: identity.URL + "/?a=1"},
		"unsuccessful smoke":        {ClientID: &id, ClientSecret: &right, IdentityServerURL: identity.URL},
	}
	client := newFormsTestClient(t, creatioServer.URL)
	for name, request := range cases {
		if result := client.VerifyOAuthApp(context.Background(), request); result.Success || result.Result != nil || result.Error != oauthCheckFailureMessage {
			t.Errorf("%s: result = %#v", name, result)
		}
	}
}

func TestOAuthCheckDeriveIdentityURLInsertsSuffixIntoFirstLabel(t *testing.T) {
	cases := map[string]string{
		"https://186843-crm-bundle.creatio.com/0/": "https://186843-crm-bundle-is.creatio.com",
		"http://localhost:8080":                    "http://localhost-is:8080",
		"https://Host.Example.com:443/x":           "https://host-is.example.com",
		"not a url":                                "",
	}
	for input, want := range cases {
		if got := oauthCheckDeriveIdentityURL(input); got != want {
			t.Errorf("derive(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestVerifyOAuthAppKeepsTheConfiguredSecretOnTheConfiguredIdentityService(t *testing.T) {
	identity, creatioServer, _ := oauthCheckServers(t, http.StatusOK, `{"success":true,"rows":[]}`)
	defer identity.Close()
	defer creatioServer.Close()
	received := false
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		received = true
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer foreign.Close()
	client, err := NewClient(Config{BaseURL: creatioServer.URL, ClientID: "app", Secret: "right", TokenURL: identity.URL + "/connect/token"})
	if err != nil {
		t.Fatal(err)
	}
	if result := client.VerifyOAuthApp(context.Background(), VerifyOAuthAppRequest{IdentityServerURL: foreign.URL}); result.Success || received {
		t.Fatalf("configured secret sent to a caller-chosen host: result = %#v, received = %v", result, received)
	}
	if result := client.VerifyOAuthApp(context.Background(), VerifyOAuthAppRequest{IdentityServerURL: identity.URL + "/"}); !result.Success || !result.Result.OK {
		t.Fatalf("naming the configured IdentityService = %#v", result)
	}
}

func TestVerifyOAuthAppDoesNotFollowTokenRedirects(t *testing.T) {
	received := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { received = true }))
	defer target.Close()
	redirecting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/connect/token", http.StatusTemporaryRedirect)
	}))
	defer redirecting.Close()
	id, key := "app", "right"
	result := newFormsTestClient(t, redirecting.URL).VerifyOAuthApp(context.Background(),
		VerifyOAuthAppRequest{ClientID: &id, ClientSecret: &key, IdentityServerURL: redirecting.URL})
	if received || (result.Success && result.Result.TokenAcquired) {
		t.Fatalf("token redirect followed: result = %#v, received = %v", result, received)
	}
}
