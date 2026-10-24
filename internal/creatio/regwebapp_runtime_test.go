package creatio

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// runtimeSite answers like the fake stands the detection was compared on against clio 8.1.0.142: a site of
// one runtime, or one whose every route fails with the same status.
func runtimeSite(t *testing.T, mode string) string {
	t.Helper()
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch mode {
		case "500":
			w.WriteHeader(http.StatusInternalServerError)
			return
		case "404":
			http.NotFound(w, r)
			return
		}
		prefix := "/0"
		if mode == "core" {
			prefix = ""
		}
		switch {
		case r.URL.Path == "/ServiceModel/AuthService.svc/Login":
			http.SetCookie(w, &http.Cookie{Name: ".ASPXAUTH", Value: "a", Path: "/"})
			_, _ = w.Write([]byte(`{"Code":0}`))
		case r.URL.Path == prefix+"/api/HealthCheck/Ping":
			_, _ = w.Write([]byte("Pong"))
		case mode == "core" && r.URL.Path == "/Login/Login.html", mode == "fw" && r.URL.Path == "/0/Login/NuiLogin.aspx":
			_, _ = w.Write([]byte("<html></html>"))
		case r.URL.Path == prefix+"/DataService/json/SyncReply/SelectQuery":
			_, _ = w.Write([]byte(`{"success":true,"rows":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(site.Close)
	return site.URL
}

func TestDetectRuntimeDecidesAsClio(t *testing.T) {
	for _, test := range []struct {
		mode         string
		credentials  bool
		wantNetCore  bool
		wantRefusal  string
		wantContains string
	}{
		{mode: "fw"}, {mode: "fw", credentials: true},
		{mode: "core", wantNetCore: true}, {mode: "core", credentials: true, wantNetCore: true},
		{mode: "500", credentials: true, wantRefusal: "Unable to auto-detect the Creatio runtime because the site at '{host}' is not serving requests: " +
			"every probe answered an HTTP server error. Verify that the Creatio application is started and finished warming up, then rerun reg-web-app. " +
			".NET Core / NET8: health {uri}/api/HealthCheck/Ping => The remote server returned an error: (500) Internal Server Error., " +
			"service {uri}/DataService/json/SyncReply/SelectQuery => The authentication endpoint returned HTTP 500 Internal Server Error., " +
			"UI marker {uri}/Login/Login.html => The remote server returned an error: (500) Internal Server Error.. .NET Framework: health " +
			"{uri}/0/api/HealthCheck/Ping => The remote server returned an error: (500) Internal Server Error., service " +
			"{uri}/0/DataService/json/SyncReply/SelectQuery => The authentication endpoint returned HTTP 500 Internal Server Error., UI marker " +
			"{uri}/0/Login/NuiLogin.aspx => The remote server returned an error: (500) Internal Server Error.."},
		{mode: "404", wantRefusal: "Unable to auto-detect the Creatio runtime because nothing at '{uri}' serves a Creatio route: every probe answered 404. " +
			"Verify the application path in the URL and that the application is still deployed, then rerun reg-web-app. .NET Core / NET8: health " +
			"{uri}/api/HealthCheck/Ping => The remote server returned an error: (404) Not Found., service {uri}/DataService/json/SyncReply/SelectQuery " +
			"=> skipped because credentials are missing, UI marker {uri}/Login/Login.html => The remote server returned an error: (404) Not Found.. " +
			".NET Framework: health {uri}/0/api/HealthCheck/Ping => The remote server returned an error: (404) Not Found., service " +
			"{uri}/0/DataService/json/SyncReply/SelectQuery => skipped because credentials are missing, UI marker {uri}/0/Login/NuiLogin.aspx => " +
			"The remote server returned an error: (404) Not Found.."},
		{mode: "closed", wantContains: "could not be reached from this machine"},
	} {
		uri := "http://127.0.0.1:9"
		if test.mode != "closed" {
			uri = runtimeSite(t, test.mode)
		} else {
			useRegistrationTransport(t, refusingTransport())
		}
		target := RegistrationTarget{URI: uri + "/"}
		if test.credentials {
			target.Login = "Supervisor"
			target.Password = "pw"
		}
		isNetCore, err := DetectRuntime(context.Background(), target)
		parsed, _ := url.Parse(uri)
		want := strings.NewReplacer("{uri}", uri, "{host}", parsed.Host).Replace(test.wantRefusal)
		switch {
		case test.wantContains != "":
			if err == nil || !strings.Contains(err.Error(), test.wantContains) || !strings.Contains(err.Error(), "=> "+refusedSentence()) {
				t.Fatalf("%s: err = %v", test.mode, err)
			}
		case want != "":
			if err == nil || err.Error() != want {
				t.Fatalf("%s: err\n got %v\nwant %s", test.mode, err, want)
			}
		case err != nil || isNetCore != test.wantNetCore:
			t.Fatalf("%s (credentials %v): isNetCore = %v, err = %v", test.mode, test.credentials, isNetCore, err)
		}
	}
}

// useRegistrationTransport makes reg-web-app's probes and logins use transport for one test: a closed port is
// simulated rather than dialed, since a real refused dial is worded differently on Windows.
func useRegistrationTransport(t *testing.T, transport http.RoundTripper) {
	t.Helper()
	previous := RegistrationTransport
	RegistrationTransport = transport
	t.Cleanup(func() { RegistrationTransport = previous })
}

func TestCheckRegistrationLoginTexts(t *testing.T) {
	useRegistrationTransport(t, refusingTransport())
	if err := CheckRegistrationLogin(context.Background(), RegistrationTarget{URI: "http://127.0.0.1:9", Login: "u", Password: "p"}); err == nil ||
		err.Error() != refusedSentence()+" (127.0.0.1:9)" {
		t.Fatalf("closed port: %v", err)
	}
	useRegistrationTransport(t, registrationTransport())
	uri := runtimeSite(t, "500")
	if err := CheckRegistrationLogin(context.Background(), RegistrationTarget{URI: uri, Login: "u", Password: "p"}); err == nil ||
		err.Error() != "The remote server returned an error: (500) Internal Server Error." {
		t.Fatalf("500: %v", err)
	}
	uri = runtimeSite(t, "404")
	err := CheckRegistrationLogin(context.Background(), RegistrationTarget{URI: uri, ClientID: "c", ClientSecret: "s", AuthAppURI: uri + "/connect/token"})
	if err == nil || err.Error() != "One or more errors occurred. (Response status code does not indicate success: 404 (Not Found).)" {
		t.Fatalf("oauth 404: %v", err)
	}
}
