package creatio

import (
	"context"
	"errors"
	"net"
	"net/url"
	"runtime"
	"strings"
)

// connectFault is a request that got no HTTP answer - the host did not resolve, the connection was refused,
// reset after the request was sent, or the login got no answer in time - worded as .NET words the same fault,
// because clio reports what Creatio.Client raised (measured against clio 8.1.0.142 with a host that does not
// resolve, a closed port, a stand that resets each connection after reading the request and one that never
// answers).
//
// The outer exception depends on where the fault happened. The login before a call raises a WebException that
// carries the inner exception's message as is. A synchronous call made with an established session raises the
// AggregateException of Task.Result, which adds its own sentence: clio reuses its session across calls, so a
// host that goes away after the first call is met there (measured: a stand stopped after the session was
// established).
type connectFault struct {
	kind     connectFaultKind
	sentence string // the innermost exception's message: SocketException's, or OperationCanceledException's
	endpoint string // host:port being dialed, with the scheme's port when the URL names none
	request  bool   // met by a call made with an established session, not by the login
	err      error  // the Go transport error
}

type connectFaultKind int

const (
	faultConnect connectFaultKind = iota // no connection: the host did not resolve or refused it
	faultReset                           // the connection was reset after the request was sent
	faultTimeout                         // the login got no answer within its timeout
)

const (
	httpSendFailure   = "An error occurred while sending the request."
	operationCanceled = "The operation was canceled."
)

// httpMessage is the message of the exception HttpClient raised: HttpRequestException's ("<sentence>
// (host:port)" when no connection was made), or OperationCanceledException's for a login that timed out.
func (f *connectFault) httpMessage() string {
	switch f.kind {
	case faultReset:
		return httpSendFailure
	case faultTimeout:
		return operationCanceled
	}
	return f.sentence + " (" + f.endpoint + ")"
}

// Error is the outer exception's message.
func (f *connectFault) Error() string {
	if f.request {
		return "One or more errors occurred. (" + f.httpMessage() + ")"
	}
	return f.httpMessage()
}

func (f *connectFault) Unwrap() error { return f.err }

// exceptionChain is CommandExecutionResult.FormatExceptionChain over the exceptions Creatio.Client raises for
// this fault, the text of clio's exit -1 envelope.
func (f *connectFault) exceptionChain() string {
	outer := "[WebException] "
	if f.request {
		outer = "[AggregateException] "
	}
	chain := outer + f.Error() + " → "
	switch f.kind {
	case faultReset:
		return chain + "[HttpRequestException] " + httpSendFailure + " → [IOException] Unable to read data from the transport connection: " +
			f.sentence + ". → [SocketException] " + f.sentence
	case faultTimeout:
		return chain + "[OperationCanceledException] " + operationCanceled
	}
	return chain + "[HttpRequestException] " + f.httpMessage() + " → [SocketException] " + f.sentence
}

// providerMessage is the ErrorMessage ATF.Repository's RemoteDataProvider keeps for this fault: the outer
// exception's message followed by its inner exception's.
func (f *connectFault) providerMessage() string { return f.Error() + f.httpMessage() }

// newConnectFault words err as .NET does when it is a transport failure with a known .NET sentence, or
// returns nil (a timeout, a TLS failure, a DNS error other than "no such host") so the caller keeps Go's text.
func newConnectFault(err error) *connectFault { return connectFaultOn(err, runtime.GOOS) }

func connectFaultOn(err error, goos string) *connectFault {
	var urlErr *url.Error
	if !errors.As(err, &urlErr) {
		return nil
	}
	kind := faultConnect
	if isConnectionReset(urlErr.Err) {
		var opErr *net.OpError
		if !errors.As(urlErr.Err, &opErr) {
			return nil
		}
		switch opErr.Op {
		case "read":
			// The request went out and the answer never came: .NET reports the read that failed.
			kind = faultReset
		case "dial":
		default:
			// A reset while the request was being written: .NET's wording for it was not measured.
			return nil
		}
	}
	sentence := connectSentence(urlErr.Err, goos)
	if sentence == "" {
		return nil
	}
	parsed, parseErr := url.Parse(urlErr.URL)
	if parseErr != nil || parsed.Hostname() == "" {
		return nil
	}
	return &connectFault{kind: kind, sentence: sentence, endpoint: net.JoinHostPort(parsed.Hostname(), connectPort(parsed)), err: err}
}

// loginTimeoutFault is a login that got no answer within Creatio.Client's login timeout: the cancelled wait
// surfaces as OperationCanceledException inside a WebException (measured against clio 8.1.0.142 with a stand
// that accepts connections and never answers).
func loginTimeoutFault(err error) error {
	return transportError{err: &connectFault{kind: faultTimeout, sentence: operationCanceled, err: err}}
}

// connectSentence is the SocketException message .NET gives the fault on that platform: the resolver's
// gai_strerror text for a host that does not exist (Windows has its own), and the errno text otherwise.
func connectSentence(err error, goos string) string {
	var dnsErr *net.DNSError
	switch {
	case errors.As(err, &dnsErr) && dnsErr.IsNotFound:
		switch goos {
		case "darwin":
			return "nodename nor servname provided, or not known"
		case "windows":
			return "No such host is known."
		}
		return "Name or service not known"
	case isConnectionRefused(err):
		if goos == "windows" {
			return "No connection could be made because the target machine actively refused it."
		}
		return "Connection refused"
	case isConnectionReset(err):
		if goos == "windows" {
			return "An existing connection was forcibly closed by the remote host."
		}
		return "Connection reset by peer"
	}
	return ""
}

func connectPort(u *url.URL) string {
	if port := u.Port(); port != "" {
		return port
	}
	if strings.EqualFold(u.Scheme, "https") {
		return "443"
	}
	return "80"
}

func asConnectFault(err error) (*connectFault, bool) {
	var fault *connectFault
	if err == nil || !errors.As(err, &fault) {
		return nil, false
	}
	return fault, true
}

// asLoginConnectFault is a fault the login met, which clio's helpers that catch WebException word their own
// way; a call's AggregateException passes through them with its own message.
func asLoginConnectFault(err error) (*connectFault, bool) {
	fault, ok := asConnectFault(err)
	if !ok || fault.request {
		return nil, false
	}
	return fault, true
}

// requestConnectFailure classifies a connect or reset fault of a call made with an established session, or
// returns ok=false for any other failure. A tool whose clio counterpart logs in before every call meets a host
// it cannot connect to at that login, so its connect fault keeps the login's wording (measured: those tools
// logged in again on every call to a stand that answered, and answered the login's text once it went down).
// A reset after the request was sent stays the call's: such a host accepted the connection, and clio's login
// to it succeeded before the call was reset (measured).
func requestConnectFailure(ctx context.Context, err error) (error, bool) {
	fault := newConnectFault(err)
	if fault == nil {
		return nil, false
	}
	if fault.kind == faultConnect && logsInBeforeEachCall(ctx) {
		return loginError{err: transportError{err: fault}}, true
	}
	fault.request = true
	return transportError{err: fault}, true
}

// getAnswersEmpty is Creatio.Client's ExecuteGetRequest, which catches the HttpRequestException of its GET and
// answers an empty body instead: a call made with an established session that met a connect or reset fault
// reads "" in clio, and the tool reports what an empty body gives (measured: list-page-templates,
// last-compilation-log and get-email-template against a stand stopped after the session was established).
func getAnswersEmpty(err error) bool {
	fault, ok := asConnectFault(err)
	return ok && fault.request
}

type loginBeforeEachCallKey struct{}

// WithLoginBeforeEachCall marks a call whose clio counterpart logs in before it runs instead of reusing the
// session (a worker process or a client of its own per call): a host it cannot connect to fails that login,
// so this server words such a fault of the call's requests as the login's, although it holds a session.
func WithLoginBeforeEachCall(ctx context.Context) context.Context {
	return context.WithValue(ctx, loginBeforeEachCallKey{}, true)
}

func logsInBeforeEachCall(ctx context.Context) bool {
	marked, _ := ctx.Value(loginBeforeEachCallKey{}).(bool)
	return marked
}

// InnermostExceptionText is the innermost exception message clio's MCP error filter shows for a raised failure
// that is a connect fault ("MCP tool 'x' failed: <sentence>"); ok is false for any other failure.
func InnermostExceptionText(err error) (string, bool) {
	fault, ok := asConnectFault(err)
	if !ok {
		return "", false
	}
	return fault.sentence, true
}

// CommandExceptionText is the text of clio's exit -1 envelope (CommandExecutionResult.FromException) for an
// unexpected failure: the exception chain of a connect fault, otherwise the failure's own text.
func CommandExceptionText(err error) string {
	if fault, ok := asConnectFault(err); ok {
		return fault.exceptionChain()
	}
	return err.Error()
}

// providerReadFailure is ClassifyingDataProvider's DataProviderFailureException for a read whose ATF provider
// caught a connect fault and kept only its text: clio reports it as a ProviderFailure that names the schema,
// with the provider's text fenced as untrusted data.
type providerReadFailure struct {
	message string
	err     error
}

func (e *providerReadFailure) Error() string { return e.message }
func (e *providerReadFailure) Unwrap() error { return e.err }

// atfReadFailure words a failed read the way clio's ATF data provider path does when the read never got an
// answer; any other failure is returned unchanged.
func atfReadFailure(err error, schemaName string) error {
	fault, ok := asConnectFault(err)
	if !ok {
		return err
	}
	message := "Failed reading records from entity schema '" + schemaName + "'"
	if fenced := DataWriteUntrusted(fault.providerMessage()); fenced != nil {
		message += ": " + *fenced
	}
	return &providerReadFailure{message: message, err: err}
}
