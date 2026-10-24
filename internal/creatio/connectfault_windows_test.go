package creatio

import (
	"net"
	"net/url"
	"os"
	"strings"
	"syscall"
	"testing"
)

// On Windows a refused or reset connection is a Winsock error, which syscall.ECONNREFUSED and ECONNRESET do not
// match; the sentences are the system's texts .NET shows for them (not measured on Windows).
func TestWindowsSocketErrorsAreDotNetFaults(t *testing.T) {
	refused := &url.Error{Op: "Post", URL: "http://127.0.0.1:9/0/ping",
		Err: &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connectex", wsaeconnrefused)}}
	fault := newConnectFault(refused)
	if fault == nil || fault.Error() != "No connection could be made because the target machine actively refused it. (127.0.0.1:9)" {
		t.Fatalf("refused: fault = %v", fault)
	}
	reset := &url.Error{Op: "Post", URL: "http://127.0.0.1:9/0/ping",
		Err: &net.OpError{Op: "read", Net: "tcp", Err: os.NewSyscallError("wsarecv", syscall.WSAECONNRESET)}}
	fault = newConnectFault(reset)
	if fault == nil || fault.kind != faultReset || fault.Error() != "An error occurred while sending the request." {
		t.Fatalf("reset: fault = %v", fault)
	}
	if chain := fault.exceptionChain(); !strings.HasSuffix(chain, "[SocketException] An existing connection was forcibly closed by the remote host.") {
		t.Fatalf("reset chain = %q", chain)
	}
}
