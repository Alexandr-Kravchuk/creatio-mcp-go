package creatio

import (
	"context"
	"net"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// The calls below are made with a session the client already holds, as after a first call: the host goes away
// (or resets the connection) afterwards. Measured against clio 8.1.0.142 with a local stand stopped after the
// session was established: clio reuses its session too, and Creatio.Client hands the call's fault back inside
// the AggregateException of Task.Result.

// holdSession gives the client a forms session, so its next call sends the request without logging in.
func holdSession(client *Client) {
	client.generation = 1
	client.session = &authSession{generation: 1, cookies: []*http.Cookie{{Name: ".ASPXAUTH", Value: "x"}}, csrf: "x"}
}

type failingRoundTripper struct{ err error }

func (f failingRoundTripper) RoundTrip(*http.Request) (*http.Response, error) { return nil, f.err }

// resetTransport fails every request the way a reset connection does in the given socket operation.
func resetTransport(op string) http.RoundTripper {
	return failingRoundTripper{err: &net.OpError{Op: op, Net: "tcp", Err: os.NewSyscallError(op, syscall.ECONNRESET)}}
}

// pingRequest builds a GET to the client's own site.
func pingRequest(client *Client) func() (*http.Request, error) {
	return func() (*http.Request, error) { return http.NewRequest(http.MethodGet, client.serviceURL("ping"), nil) }
}

func refusedSentence() string { return connectSentence(syscall.ECONNREFUSED, runtime.GOOS) }

func resetSentence() string { return connectSentence(syscall.ECONNRESET, runtime.GOOS) }

func TestCallOfAHostThatWentAwayCarriesTheCallsException(t *testing.T) {
	cases := []struct {
		name      string
		baseURL   string
		transport http.RoundTripper
		sentence  string
	}{
		{"does not resolve", "http://nonexistent.invalid", dnsFailingTransport(), hostNotFoundSentence()},
		{"refuses", "http://127.0.0.1:9", refusingTransport(), refusedSentence()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := newFormsTestClient(t, tc.baseURL)
			client.http.Transport = tc.transport
			holdSession(client)
			endpoint := strings.TrimPrefix(tc.baseURL, "http://")
			if !strings.Contains(endpoint, ":") {
				endpoint += ":80"
			}
			inner := tc.sentence + " (" + endpoint + ")"
			outer := "One or more errors occurred. (" + inner + ")"

			_, _, err := client.doAuthenticated(context.Background(), client.requestClient(), pingRequest(client))
			if err == nil || err.Error() != outer || !isTransportError(err) || isLoginError(err) || isAuthenticationError(err) {
				t.Fatalf("err = %v (transport %v, login %v)", err, isTransportError(err), isLoginError(err))
			}
			// No route prefix: clio's tools get Creatio.Client's exception, which names none.
			if isRequestTransportError(err) {
				t.Fatal("a call's connect fault must not take a helper's route prefix")
			}
			chain := "[AggregateException] " + outer + " → [HttpRequestException] " + inner + " → [SocketException] " + tc.sentence
			if got := CommandExceptionText(err); got != chain {
				t.Fatalf("exception chain = %q, want %q", got, chain)
			}
			if apps := client.ListAppsResponse(context.Background()); apps.Error != outer {
				t.Fatalf("list-apps = %#v", apps)
			}
			// PageSchemaMetadataHelper catches the login's WebException only; the call's exception passes through.
			if page := client.GetPage(context.Background(), PageGetRequest{SchemaName: "Contacts_FormPage"}); page.Error != outer {
				t.Fatalf("get-page = %q", page.Error)
			}
			if rights := client.GetRecordRights(context.Background(), "Contact", "6ae2bd8b-dc6a-46b0-b478-47e95d994cc0"); !strings.Contains(rights.Error, outer) {
				t.Fatalf("get-record-rights = %#v", rights)
			}
			// ExecuteGetRequest catches its GET's HttpRequestException and reads an empty body.
			templates := client.ListPageTemplates(context.Background(), "")
			wantTemplates := "Page template catalog endpoint returned an unparseable response (URL: " + tc.baseURL +
				"/0/rest/schema.template.api/templates?schemaType=" + strconv.Itoa(pageSchemaTypeWeb) + "). Parser error: Error reading JObject from JsonReader. " +
				"Path '', line 0, position 0.. Response preview: <empty body>"
			if templates.Error != wantTemplates {
				t.Fatalf("list-page-templates = %q", templates.Error)
			}
			wantLog := "The input does not contain any JSON tokens. Expected the input to start with a valid JSON token, " +
				"when isFinalBlock is true. Path: $ | LineNumber: 0 | BytePositionInLine: 0."
			if log := client.GetLastCompilationLog(context.Background()); log.Error != wantLog {
				t.Fatalf("last-compilation-log = %q", log.Error)
			}
			email := client.GetEmailTemplate(context.Background(), "bfd68356-c72b-4c33-b29f-067eb77cb474", "", "")
			if email.Error == nil || *email.Error != "Creatio OData BulkEmail response was empty." {
				t.Fatalf("get-email-template = %#v", email)
			}
		})
	}
}

func TestResetAfterTheRequestWasSentIsTheFailedRead(t *testing.T) {
	sentence := resetSentence()
	inner := "[HttpRequestException] An error occurred while sending the request. → [IOException] Unable to read data from the transport connection: " +
		sentence + ". → [SocketException] " + sentence

	login := newFormsTestClient(t, "http://127.0.0.1:9")
	login.http.Transport = resetTransport("read")
	_, _, err := login.doAuthenticated(context.Background(), login.requestClient(), pingRequest(login))
	if err == nil || err.Error() != "An error occurred while sending the request." || !isLoginError(err) {
		t.Fatalf("login: err = %v", err)
	}
	if got, want := CommandExceptionText(err), "[WebException] An error occurred while sending the request. → "+inner; got != want {
		t.Fatalf("login chain = %q, want %q", got, want)
	}

	call := newFormsTestClient(t, "http://127.0.0.1:9")
	call.http.Transport = resetTransport("read")
	holdSession(call)
	_, _, err = call.doAuthenticated(context.Background(), call.requestClient(), pingRequest(call))
	outer := "One or more errors occurred. (An error occurred while sending the request.)"
	if err == nil || err.Error() != outer || isLoginError(err) {
		t.Fatalf("call: err = %v", err)
	}
	if got, want := CommandExceptionText(err), "[AggregateException] "+outer+" → "+inner; got != want {
		t.Fatalf("call chain = %q, want %q", got, want)
	}

	// A reset while the request was still being written has no measured .NET wording: Go's text is kept.
	written := newFormsTestClient(t, "http://127.0.0.1:9")
	written.http.Transport = resetTransport("write")
	holdSession(written)
	_, _, err = written.doAuthenticated(context.Background(), written.requestClient(), pingRequest(written))
	if _, ok := asConnectFault(err); ok || !isRequestTransportError(err) {
		t.Fatalf("write reset: err = %v", err)
	}
}

func TestPackageHotfixReportsTheExceptionChain(t *testing.T) {
	client := newFormsTestClient(t, "http://127.0.0.1:9")
	client.http.Transport = refusingTransport()
	result := client.SetPackageHotfix(context.Background(), "UsrParityPkg", true)
	sentence := refusedSentence()
	want := "[WebException] " + sentence + " (127.0.0.1:9) → [HttpRequestException] " + sentence + " (127.0.0.1:9) → [SocketException] " + sentence
	if result.ExitCode != -1 || len(result.Messages) != 2 || result.Messages[1].Value != want {
		t.Fatalf("unlock-for-hotfix = %#v", result)
	}
}

func TestRestartReportsClioReadableMessage(t *testing.T) {
	// RemoteCommand logs GetReadableMessageException: a login's WebException(ConnectFailure) is enriched (measured
	// against clio 8.1.0.142 with a closed port and a resetting stand); a call's AggregateException is rendered by
	// its inner exception (read in clio's source).
	pkgWriteFastRestart(t)
	cases := []struct {
		name      string
		transport http.RoundTripper
		session   bool
		want      string
	}{
		{"refused at the login", refusingTransport(), false, "Cannot connect to the application: " + refusedSentence() +
			" (127.0.0.1:9) (WebException: ConnectFailure). Make sure the site is running and accessible."},
		{"reset at the login", resetTransport("read"), false, "Cannot connect to the application: An error occurred while sending " +
			"the request. (WebException: ConnectFailure). Make sure the site is running and accessible."},
		{"refused with a session", refusingTransport(), true, refusedSentence() + " (127.0.0.1:9)"},
	}
	for _, tc := range cases {
		client := newFormsTestClient(t, "http://127.0.0.1:9")
		client.http.Transport = tc.transport
		if tc.session {
			holdSession(client)
		}
		result := client.RestartApplication(context.Background())
		if result.ExitCode != 1 || len(result.Messages) != 1 || result.Messages[0].Value != tc.want {
			t.Errorf("%s: restart = %#v", tc.name, result)
		}
	}
}
