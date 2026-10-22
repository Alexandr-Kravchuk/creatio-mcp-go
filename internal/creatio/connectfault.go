package creatio

import (
	"errors"
	"net"
	"net/url"
	"runtime"
	"strings"
	"syscall"
)

// connectFault is a request that got no HTTP answer at all - the host did not resolve, or the connection was
// refused or reset - worded as .NET's socket stack words the same fault, because clio reports what
// Creatio.Client raised (measured against clio 8.1.0.142 with a host that does not resolve and a closed port).
// Creatio.Client raises a WebException whose message is HttpRequestException's, "<socket sentence> (host:port)",
// over the SocketException that carries the sentence alone.
type connectFault struct {
	sentence string // SocketException.Message
	endpoint string // host:port being dialed, with the scheme's port when the URL names none
	err      error  // the Go transport error
}

// Error is the WebException / HttpRequestException message: the sentence and the endpoint in parentheses.
func (f *connectFault) Error() string { return f.sentence + " (" + f.endpoint + ")" }

func (f *connectFault) Unwrap() error { return f.err }

// exceptionChain is CommandExecutionResult.FormatExceptionChain over the exceptions Creatio.Client raises for
// this fault, the text of clio's exit -1 envelope.
func (f *connectFault) exceptionChain() string {
	return "[WebException] " + f.Error() + " → [HttpRequestException] " + f.Error() + " → [SocketException] " + f.sentence
}

// providerMessage is the ErrorMessage ATF.Repository's RemoteDataProvider keeps for this fault: the WebException
// message followed by its inner HttpRequestException's, which here are the same text.
func (f *connectFault) providerMessage() string { return f.Error() + f.Error() }

// newConnectFault words err as .NET does when it is a transport failure with a known .NET sentence, or
// returns nil (a timeout, a TLS failure, a DNS error other than "no such host") so the caller keeps Go's text.
func newConnectFault(err error) *connectFault { return connectFaultOn(err, runtime.GOOS) }

func connectFaultOn(err error, goos string) *connectFault {
	var urlErr *url.Error
	if !errors.As(err, &urlErr) {
		return nil
	}
	sentence := connectSentence(urlErr.Err, goos)
	if sentence == "" {
		return nil
	}
	parsed, parseErr := url.Parse(urlErr.URL)
	if parseErr != nil || parsed.Hostname() == "" {
		return nil
	}
	return &connectFault{sentence: sentence, endpoint: net.JoinHostPort(parsed.Hostname(), connectPort(parsed)), err: err}
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
	case errors.Is(err, syscall.ECONNREFUSED):
		return "Connection refused"
	case errors.Is(err, syscall.ECONNRESET):
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

// SocketExceptionText is the innermost exception message clio's MCP error filter shows for a raised failure
// that is a connect fault ("MCP tool 'x' failed: <sentence>"); ok is false for any other failure.
func SocketExceptionText(err error) (string, bool) {
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
