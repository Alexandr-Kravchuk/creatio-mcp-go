package creatio

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

// dnsFailingTransport answers every dial with the error the resolver gives a host that does not exist, so no
// test depends on real DNS (which is slow and flaky on Windows CI).
func dnsFailingTransport() *http.Transport {
	return &http.Transport{DialContext: func(_ context.Context, _, address string) (net.Conn, error) {
		host, _, _ := net.SplitHostPort(address)
		return nil, &net.OpError{Op: "dial", Net: "tcp", Err: &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}}
	}}
}

// refusingTransport answers every dial as a closed port does.
func refusingTransport() *http.Transport {
	return &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) {
		return nil, &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}
	}}
}

func unresolvableFormsClient(t *testing.T) *Client {
	t.Helper()
	client := newFormsTestClient(t, "http://nonexistent.invalid")
	client.http.Transport = dnsFailingTransport()
	return client
}

func hostNotFoundSentence() string {
	return connectSentence(&net.DNSError{IsNotFound: true}, runtime.GOOS)
}

func noRequest() (*http.Request, error) {
	return http.NewRequest(http.MethodGet, "http://nonexistent.invalid/0/ping", nil)
}

func TestConnectSentenceIsDotNetSocketTextPerPlatform(t *testing.T) {
	notFound := &net.DNSError{Err: "no such host", Name: "nonexistent.invalid", IsNotFound: true}
	cases := []struct {
		err  error
		goos string
		want string
	}{
		{notFound, "darwin", "nodename nor servname provided, or not known"},
		{notFound, "linux", "Name or service not known"},
		{notFound, "windows", "No such host is known."},
		{os.NewSyscallError("connect", syscall.ECONNREFUSED), "darwin", "Connection refused"},
		{os.NewSyscallError("read", syscall.ECONNRESET), "linux", "Connection reset by peer"},
		// A resolver timeout or a TLS failure has no .NET sentence here: Go's own text is kept.
		{&net.DNSError{Err: "i/o timeout", Name: "x", IsTimeout: true}, "darwin", ""},
		{errors.New("tls: handshake failure"), "darwin", ""},
	}
	for _, tc := range cases {
		if got := connectSentence(tc.err, tc.goos); got != tc.want {
			t.Errorf("connectSentence(%v, %s) = %q, want %q", tc.err, tc.goos, got, tc.want)
		}
	}
}

func TestFormsLoginToUnresolvableHostIsATransportFailure(t *testing.T) {
	client := unresolvableFormsClient(t)
	_, _, err := client.doAuthenticated(context.Background(), client.requestClient(), noRequest)
	if err == nil || !isTransportError(err) || isAuthenticationError(err) || !isLoginError(err) || isRequestTransportError(err) {
		t.Fatalf("err = %v (transport %v, auth %v)", err, isTransportError(err), isAuthenticationError(err))
	}
	if want := hostNotFoundSentence() + " (nonexistent.invalid:80)"; err.Error() != want {
		t.Fatalf("err = %q, want %q", err.Error(), want)
	}
	var dnsErr *net.DNSError
	if !errors.As(err, &dnsErr) {
		t.Fatalf("the Go cause must stay reachable: %v", err)
	}
	if text, ok := SocketExceptionText(err); !ok || text != hostNotFoundSentence() {
		t.Fatalf("SocketExceptionText = %q, %v", text, ok)
	}
}

func TestOAuthTokenRequestToUnresolvableHostIsATransportFailure(t *testing.T) {
	client, err := NewClient(Config{BaseURL: "https://nonexistent.invalid", ClientID: "id", Secret: "replace-me",
		TokenURL: "https://nonexistent.invalid/connect/token"})
	if err != nil {
		t.Fatal(err)
	}
	client.http.Transport = dnsFailingTransport()
	_, _, err = client.doAuthenticated(context.Background(), client.requestClient(), noRequest)
	if !isTransportError(err) || isAuthenticationError(err) {
		t.Fatalf("err = %v", err)
	}
	if want := hostNotFoundSentence() + " (nonexistent.invalid:443)"; err.Error() != want {
		t.Fatalf("err = %q, want %q", err.Error(), want)
	}
}

func TestRefusedLoginIsATransportFailureWithTheSocketText(t *testing.T) {
	client := newFormsTestClient(t, "http://127.0.0.1:9")
	client.http.Transport = refusingTransport()
	_, _, err := client.doAuthenticated(context.Background(), client.requestClient(), noRequest)
	if !isTransportError(err) || err.Error() != "Connection refused (127.0.0.1:9)" {
		t.Fatalf("err = %v", err)
	}
}

func TestLoginRejectionsStayAuthenticationFailures(t *testing.T) {
	cases := map[string]http.HandlerFunc{
		"forms login rejects the credentials": func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"Code":1,"Message":"Invalid username or password"}`))
		},
		"forms login answers 401": func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusUnauthorized) },
		"forms login answers 403": func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusForbidden) },
	}
	for name, handler := range cases {
		server := httptest.NewServer(handler)
		client := newFormsTestClient(t, server.URL)
		_, _, err := client.doAuthenticated(context.Background(), client.requestClient(), noRequest)
		server.Close()
		if !isAuthenticationError(err) || isTransportError(err) || !isLoginError(err) {
			t.Errorf("%s: err = %v (auth %v, transport %v)", name, err, isAuthenticationError(err), isTransportError(err))
		}
	}
	for _, status := range []int{http.StatusBadRequest, http.StatusUnauthorized} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":"invalid_client"}`))
		}))
		client, err := NewClient(Config{BaseURL: server.URL, ClientID: "id", Secret: "replace-me", TokenURL: server.URL + "/connect/token"})
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = client.doAuthenticated(context.Background(), client.requestClient(), noRequest)
		server.Close()
		if !isAuthenticationError(err) || isTransportError(err) {
			t.Errorf("OAuth HTTP %d: err = %v", status, err)
		}
	}
}

func TestLoginWaitKeepsItsCategory(t *testing.T) {
	// A caller whose deadline runs out while another call is logging in got no answer in time.
	client := newFormsTestClient(t, "http://nonexistent.invalid")
	client.authFlight = &authFlight{done: make(chan struct{})}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, _, err := client.doAuthenticated(ctx, client.requestClient(), noRequest)
	if !isTransportError(err) || isAuthenticationError(err) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiter: err = %v", err)
	}
	// Every waiter of a failed login gets the leader's error with the category it was given.
	failed := &authFlight{done: make(chan struct{}), err: loginTransportFailure("forms login transport failure",
		&net.OpError{Op: "dial", Net: "tcp", Err: &net.DNSError{IsNotFound: true}})}
	close(failed.done)
	client.authFlight = failed
	_, _, err = client.doAuthenticated(context.Background(), client.requestClient(), noRequest)
	if !isTransportError(err) || isAuthenticationError(err) {
		t.Fatalf("shared flight: err = %v", err)
	}
}

func TestSysSettingReadsOfAnUnresolvableHostAreClioProviderFailures(t *testing.T) {
	client := unresolvableFormsClient(t)
	// clio reads through ATF's GetItems, which keeps the WebException message and its inner one.
	sentence := hostNotFoundSentence()
	want := "Failed reading records from entity schema 'SysSettings': [untrusted-source-text begin] " + sentence +
		" ([redacted-uri])" + sentence + " ([redacted-uri]) [untrusted-source-text end]"
	read := client.GetSysSetting(context.Background(), "SchemaNamePrefix")
	if read.Success || read.ErrorCategory != "ProviderFailure" || read.Error != want || read.Cause != want ||
		read.RecoveryAction != sysSettingToolProviderRecovery {
		t.Fatalf("get-sys-setting = %#v", read)
	}
	list := client.ListSysSettings(context.Background())
	if list.ErrorCategory != "ProviderFailure" || list.Error != want {
		t.Fatalf("list-sys-settings = %#v", list)
	}
	value := "x"
	code := "UsrParityNotCreated"
	update := client.UpdateSysSetting(context.Background(), AdminWriteUpdateSetting{Code: &code, Value: &value})
	if update.ErrorCategory != "ProviderFailure" || update.Error != want {
		t.Fatalf("update-sys-setting = %#v", update)
	}
}

func TestSysSettingWritesAndPrefixOfAnUnresolvableHostAreNetworkFailures(t *testing.T) {
	client := unresolvableFormsClient(t)
	code, name, valueType := "UsrParityNotCreated", "Example", "ShortText"
	created := client.CreateSysSetting(context.Background(), AdminWriteCreateSetting{Code: &code, Name: &name, ValueTypeName: &valueType})
	if created.ErrorCategory != "Network" || created.Error != "Network error creating sys-setting." ||
		created.Cause != sysSettingToolNetworkCause || created.RecoveryAction != sysSettingToolNetworkRecovery {
		t.Fatalf("create-sys-setting = %#v", created)
	}
	prefix := client.GetSchemaNamePrefix(context.Background())
	if prefix.Success || prefix.ErrorCategory != "Network" || prefix.Error != "Network error reading SchemaNamePrefix." ||
		prefix.Cause != sysSettingToolNetworkCause || prefix.RecoveryAction != sysSettingToolNetworkRecovery {
		t.Fatalf("get-schema-name-prefix = %#v", prefix)
	}
}

func TestSchemaNamePrefixOfARejectedLoginIsAnAuthenticationFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"Code":1}`))
	}))
	defer server.Close()
	prefix := newFormsTestClient(t, server.URL).GetSchemaNamePrefix(context.Background())
	if prefix.ErrorCategory != "Authentication" || prefix.Error != "Authentication error reading SchemaNamePrefix." ||
		prefix.Cause != sysSettingToolAuthenticationCause {
		t.Fatalf("get-schema-name-prefix = %#v", prefix)
	}
}

func TestEnvironmentProbesOfAnUnresolvableHostReportNoConnection(t *testing.T) {
	client := unresolvableFormsClient(t)
	described := client.DescribeEnvironment(context.Background(), time.Second)
	want := "Could not connect to the Creatio application at 'http://nonexistent.invalid'. Verify the URL and make sure the application is running."
	if described.ExitCode != 1 || len(described.Messages) != 1 || described.Messages[0].Value != want {
		t.Fatalf("describe-environment = %#v", described)
	}
	if culture := client.GetUserCulture(context.Background()); culture.Reason != "unreachable" {
		t.Fatalf("get-user-culture = %#v", culture)
	}
	if _, status := client.themeWriteResolveVersion(context.Background()); status != themeWriteProbeFailed {
		t.Fatalf("a version probe whose login got no answer must fail, status = %v", status)
	}
}

func TestReadsOfAnUnresolvableHostCarryClioTexts(t *testing.T) {
	client := unresolvableFormsClient(t)
	sentence := hostNotFoundSentence()
	if apps := client.ListAppsResponse(context.Background()); apps.Error != sentence+" (nonexistent.invalid:80)" {
		t.Fatalf("list-apps = %#v", apps)
	}
	page := client.GetPage(context.Background(), PageGetRequest{SchemaName: "Contacts_FormPage"})
	want := "SelectQuery could not be completed against the environment (URL: http://nonexistent.invalid/0/DataService/json/SyncReply/SelectQuery). " +
		"Transport error: Cannot connect to the application: " + sentence + " ([redacted-uri]) (WebException: ConnectFailure). " +
		"Make sure the site is running and accessible.. This says nothing about the requested schema or package"
	if !strings.HasPrefix(page.Error, want) {
		t.Fatalf("get-page = %q", page.Error)
	}
	if text := CommandExceptionText(errors.New("plain")); text != "plain" {
		t.Fatalf("a failure that is not a connect fault keeps its text: %q", text)
	}
}
