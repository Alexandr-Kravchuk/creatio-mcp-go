package creatio

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestListAppsFormsUsesRootLoginCSRFAndLegacyServiceRoute(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ServiceModel/AuthService.svc/Login":
			if r.Method != http.MethodPost {
				t.Fatalf("login method = %s", r.Method)
			}
			http.SetCookie(w, &http.Cookie{Name: "BPMCSRF", Value: "csrf-token", Path: "/"})
			_, _ = io.WriteString(w, `{"Code":0}`)
		case "/0/DataService/json/SyncReply/SelectQuery":
			if got := r.Header.Get("BPMCSRF"); got != "csrf-token" {
				t.Fatalf("BPMCSRF = %q", got)
			}
			if got := r.Header.Get("Authorization"); got != "" {
				t.Fatalf("unexpected Authorization = %q", got)
			}
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(body), `"rootSchemaName":"SysInstalledApp"`) {
				t.Fatalf("wrong query: %s", body)
			}
			_, _ = io.WriteString(w, `{"success":true,"rows":[{"Id":"id-1","Name":"Sales","Code":"Sales","Version":"","Description":"app"}]}`)
		default:
			t.Fatalf("unexpected route %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client, err := NewClient(Config{BaseURL: server.URL, Login: "user", Password: "pass"})
	if err != nil {
		t.Fatal(err)
	}
	apps, err := client.ListApps(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(apps) != 1 || apps[0] != (App{ID: "id-1", Name: "Sales", Code: "Sales", Version: "none", Description: "app"}) {
		t.Fatalf("apps = %#v", apps)
	}
}

func TestListAppsOAuthUsesBearerAndNetCoreRoute(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/connect/token":
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if got := r.Form.Get("grant_type"); got != "client_credentials" {
				t.Fatalf("grant_type = %q", got)
			}
			user, secret, ok := r.BasicAuth()
			if !ok || user != "id" || secret != "secret" {
				t.Fatalf("basic auth = %q %q %t", user, secret, ok)
			}
			_, _ = io.WriteString(w, `{"access_token":"token"}`)
		case "/DataService/json/SyncReply/SelectQuery":
			if got := r.Header.Get("Authorization"); got != "Bearer token" {
				t.Fatalf("Authorization = %q", got)
			}
			_, _ = io.WriteString(w, `{"success":true,"rows":[]}`)
		default:
			t.Fatalf("unexpected route %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client, err := NewClient(Config{BaseURL: server.URL, ClientID: "id", Secret: "secret", TokenURL: server.URL + "/connect/token", IsNetCore: true})
	if err != nil {
		t.Fatal(err)
	}
	if apps, err := client.ListApps(context.Background()); err != nil || len(apps) != 0 {
		t.Fatalf("apps = %#v, err = %v", apps, err)
	}
}
