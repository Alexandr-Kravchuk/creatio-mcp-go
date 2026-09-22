package creatio

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type authSessionProbe struct {
	logins       atomic.Int32
	requests     atomic.Int32
	rejections   atomic.Int32
	mu           sync.Mutex
	csrfByCookie map[string]string
	requestCount map[string]int
	expireAfter  int
	rejectAll    bool
}

func newAuthSessionProbe(t *testing.T, expireAfter int, rejectAll bool, loginDelay time.Duration) (*httptest.Server, *authSessionProbe) {
	t.Helper()
	probe := &authSessionProbe{
		csrfByCookie: map[string]string{}, requestCount: map[string]int{}, expireAfter: expireAfter, rejectAll: rejectAll,
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ServiceModel/AuthService.svc/Login":
			n := probe.logins.Add(1)
			sid, csrf := fmt.Sprintf("s%d", n), fmt.Sprintf("c%d", n)
			probe.mu.Lock()
			probe.csrfByCookie[sid] = csrf
			probe.mu.Unlock()
			if loginDelay > 0 {
				time.Sleep(loginDelay)
			}
			http.SetCookie(w, &http.Cookie{Name: ".ASPXAUTH", Value: sid, Path: "/"})
			http.SetCookie(w, &http.Cookie{Name: "BPMCSRF", Value: csrf, Path: "/"})
			_, _ = w.Write([]byte(`{"Code":0}`))
		case "/0/DataService/json/SyncReply/SelectQuery", "/0/DataService/json/SyncReply/InsertQuery":
			probe.requests.Add(1)
			cookie, cookieErr := r.Cookie(".ASPXAUTH")
			probe.mu.Lock()
			want := ""
			requestNo := 0
			if cookieErr == nil {
				want = probe.csrfByCookie[cookie.Value]
				probe.requestCount[cookie.Value]++
				requestNo = probe.requestCount[cookie.Value]
			}
			probe.mu.Unlock()
			if cookieErr != nil || want == "" || r.Header.Get("BPMCSRF") != want || probe.rejectAll ||
				(probe.expireAfter > 0 && cookie.Value == "s1" && requestNo > probe.expireAfter) {
				probe.rejections.Add(1)
				w.WriteHeader(http.StatusForbidden)
				return
			}
			if r.URL.Path == "/0/DataService/json/SyncReply/InsertQuery" {
				_, _ = w.Write([]byte(`{"success":true,"rowsAffected":1}`))
				return
			}
			var query struct {
				RootSchema string `json:"rootSchemaName"`
			}
			if err := json.NewDecoder(r.Body).Decode(&query); err != nil {
				t.Errorf("decode SelectQuery: %v", err)
				http.Error(w, "bad query", http.StatusBadRequest)
				return
			}
			if query.RootSchema == "SysInstalledApp" {
				_, _ = w.Write([]byte(`{"success":true,"rows":[{"Id":"app-1","Name":"App","Code":"App","Version":"1"}]}`))
				return
			}
			_, _ = w.Write([]byte(`{"success":true,"rows":[]}`))
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	return server, probe
}

func TestParallelListPackagesUsesMatchingSessionCookieAndCSRF(t *testing.T) {
	server, probe := newAuthSessionProbe(t, 0, false, 2*time.Millisecond)
	defer server.Close()
	client := newFormsTestClient(t, server.URL)
	const callers = 32
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := client.ListPackages(context.Background(), PackageListRequest{})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("parallel ListPackages: %v", err)
		}
	}
	if got := probe.rejections.Load(); got != 0 {
		t.Fatalf("session/CSRF mismatch refusals = %d", got)
	}
}

func TestParallelListPackagesSharesOneInitialLogin(t *testing.T) {
	server, probe := newAuthSessionProbe(t, 0, false, 2*time.Millisecond)
	defer server.Close()
	client := newFormsTestClient(t, server.URL)
	const callers = 32
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := client.ListPackages(context.Background(), PackageListRequest{})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("parallel ListPackages: %v", err)
		}
	}
	if got := probe.logins.Load(); got != 1 {
		t.Fatalf("login count = %d, want 1", got)
	}
}

func TestListAppSectionsReusesOneLoginForTwoRequests(t *testing.T) {
	server, probe := newAuthSessionProbe(t, 0, false, 0)
	defer server.Close()
	result := newFormsTestClient(t, server.URL).ListAppSections(context.Background(), "App")
	if !result.Success {
		t.Fatalf("ListAppSections = %#v", result)
	}
	if got := probe.logins.Load(); got != 1 {
		t.Fatalf("login count = %d, want 1", got)
	}
	if got := probe.requests.Load(); got != 2 {
		t.Fatalf("DataService request count = %d, want 2", got)
	}
}

func TestExpiredSessionRefreshesAndRetriesOnce(t *testing.T) {
	server, probe := newAuthSessionProbe(t, 1, false, 0)
	defer server.Close()
	result := newFormsTestClient(t, server.URL).ListAppSections(context.Background(), "App")
	if !result.Success {
		t.Fatalf("ListAppSections = %#v", result)
	}
	if got := probe.logins.Load(); got != 2 {
		t.Fatalf("login count = %d, want 2", got)
	}
	if got := probe.requests.Load(); got != 3 {
		t.Fatalf("DataService request count = %d, want 3", got)
	}
	if got := probe.rejections.Load(); got != 1 {
		t.Fatalf("initial expired-session refusals = %d, want 1", got)
	}
}

func TestSecondAuthenticationRefusalIsReturnedAsAuthFailure(t *testing.T) {
	server, probe := newAuthSessionProbe(t, 0, true, 0)
	defer server.Close()
	outcome, err := newFormsTestClient(t, server.URL).Insert(context.Background(), "Contact", map[string]any{"Name": "synthetic"})
	if err == nil {
		t.Fatal("Insert unexpectedly succeeded")
	}
	if outcome.FailureClass != "auth" {
		t.Fatalf("failure class = %q, want auth", outcome.FailureClass)
	}
	if outcome.HTTPStatusCode != http.StatusForbidden || err.Error() != "InsertQuery: HTTP 403" {
		t.Fatalf("second refusal = outcome %#v, error %q", outcome, err)
	}
	if got := probe.logins.Load(); got != 2 {
		t.Fatalf("login count = %d, want 2", got)
	}
	if got := probe.requests.Load(); got != 2 {
		t.Fatalf("request count = %d, want 2", got)
	}
}

func TestHTMLLoginPageTriggersOneAuthenticationRetry(t *testing.T) {
	var logins atomic.Int32
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ServiceModel/AuthService.svc/Login":
			n := logins.Add(1)
			http.SetCookie(w, &http.Cookie{Name: ".ASPXAUTH", Value: fmt.Sprintf("s%d", n), Path: "/"})
			http.SetCookie(w, &http.Cookie{Name: "BPMCSRF", Value: fmt.Sprintf("c%d", n), Path: "/"})
			_, _ = w.Write([]byte(`{"Code":0}`))
		case "/0/DataService/json/SyncReply/SelectQuery":
			if requests.Add(1) == 1 {
				w.Header().Set("Content-Type", "text/html")
				_, _ = w.Write([]byte(`<!doctype html><html><title>Login</title></html>`))
				return
			}
			_, _ = w.Write([]byte(`{"success":true,"rows":[]}`))
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	if _, err := newFormsTestClient(t, server.URL).ListPackages(context.Background(), PackageListRequest{}); err != nil {
		t.Fatal(err)
	}
	if got := logins.Load(); got != 2 {
		t.Fatalf("login count = %d, want 2", got)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("request count = %d, want 2", got)
	}
}

func TestNonAuthenticationHTTPFailureIsNotRetried(t *testing.T) {
	var logins atomic.Int32
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ServiceModel/AuthService.svc/Login":
			logins.Add(1)
			_, _ = w.Write([]byte(`{"Code":0}`))
		case "/0/DataService/json/SyncReply/SelectQuery":
			requests.Add(1)
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`<!doctype html><html><title>Server Error</title></html>`))
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	if _, err := newFormsTestClient(t, server.URL).ListPackages(context.Background(), PackageListRequest{}); err == nil {
		t.Fatal("HTTP 500 unexpectedly succeeded")
	}
	if got := logins.Load(); got != 1 {
		t.Fatalf("login count = %d, want 1", got)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("request count = %d, want 1", got)
	}
}

func TestExpiredSessionDuringParallelCallsUsesOneRefresh(t *testing.T) {
	server, probe := newAuthSessionProbe(t, 5, false, 2*time.Millisecond)
	defer server.Close()
	client := newFormsTestClient(t, server.URL)
	const callers = 32
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := client.ListPackages(context.Background(), PackageListRequest{})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("parallel ListPackages: %v", err)
		}
	}
	if got := probe.logins.Load(); got != 2 {
		t.Fatalf("login count = %d, want one refresh after the initial login", got)
	}
}

func TestOAuthTokenIsCachedUntilExpiry(t *testing.T) {
	var tokenCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/connect/token":
			n := tokenCalls.Add(1)
			_, _ = fmt.Fprintf(w, `{"access_token":"token-%d","expires_in":60}`, n)
		case "/0/DataService/json/SyncReply/SelectQuery":
			_, _ = w.Write([]byte(`{"success":true,"rows":[]}`))
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client, err := NewClient(Config{BaseURL: server.URL, ClientID: "id", Secret: "secret", TokenURL: server.URL + "/connect/token"})
	if err != nil {
		t.Fatal(err)
	}
	const callers = 32
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := client.ListApps(context.Background())
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("parallel OAuth ListApps: %v", err)
		}
	}
	if got := tokenCalls.Load(); got != 1 {
		t.Fatalf("token requests before expiry = %d, want 1", got)
	}
	client.authMu.Lock()
	client.session.expiresAt = time.Now().Add(-time.Second)
	client.authMu.Unlock()
	if _, err := client.ListApps(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := tokenCalls.Load(); got != 2 {
		t.Fatalf("token requests after expiry = %d, want 2", got)
	}
}

func TestOAuthUnauthorizedResponseRefreshesTokenOnce(t *testing.T) {
	var tokenCalls atomic.Int32
	var serviceCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/connect/token":
			n := tokenCalls.Add(1)
			_, _ = fmt.Fprintf(w, `{"access_token":"token-%d","expires_in":3600}`, n)
		case "/0/DataService/json/SyncReply/SelectQuery":
			if serviceCalls.Add(1) == 1 {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if r.Header.Get("Authorization") != "Bearer token-2" {
				t.Errorf("Authorization after refresh = %q", r.Header.Get("Authorization"))
			}
			_, _ = w.Write([]byte(`{"success":true,"rows":[]}`))
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client, err := NewClient(Config{BaseURL: server.URL, ClientID: "id", Secret: "secret", TokenURL: server.URL + "/connect/token"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.ListApps(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := tokenCalls.Load(); got != 2 {
		t.Fatalf("token requests = %d, want 2", got)
	}
	if got := serviceCalls.Load(); got != 2 {
		t.Fatalf("service requests = %d, want 2", got)
	}
}

func TestCancelledLoginDoesNotLeaveConcurrentCallsWaiting(t *testing.T) {
	var loginCalls atomic.Int32
	started := make(chan struct{}, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ServiceModel/AuthService.svc/Login":
			n := loginCalls.Add(1)
			started <- struct{}{}
			sid, csrf := fmt.Sprintf("s%d", n), fmt.Sprintf("c%d", n)
			http.SetCookie(w, &http.Cookie{Name: ".ASPXAUTH", Value: sid, Path: "/"})
			http.SetCookie(w, &http.Cookie{Name: "BPMCSRF", Value: csrf, Path: "/"})
			if n == 1 {
				time.Sleep(100 * time.Millisecond)
			}
			_, _ = w.Write([]byte(`{"Code":0}`))
		case "/0/DataService/json/SyncReply/SelectQuery":
			_, _ = w.Write([]byte(`{"success":true,"rows":[]}`))
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := newFormsTestClient(t, server.URL)
	firstCtx, cancelFirst := context.WithCancel(context.Background())
	firstDone := make(chan error, 1)
	go func() {
		_, err := client.ListPackages(firstCtx, PackageListRequest{})
		firstDone <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first login did not start")
	}
	secondDone := make(chan error, 1)
	go func() {
		_, err := client.ListPackages(context.Background(), PackageListRequest{})
		secondDone <- err
	}()
	time.Sleep(20 * time.Millisecond)
	cancelFirst()
	select {
	case <-firstDone:
	case <-time.After(time.Second):
		t.Fatal("cancelled login caller did not finish")
	}
	select {
	case <-secondDone:
	case <-time.After(time.Second):
		t.Fatal("concurrent caller remained blocked after login cancellation")
	}
	if got := loginCalls.Load(); got > 2 {
		t.Fatalf("login calls after cancellation = %d, want at most 2", got)
	}
}
