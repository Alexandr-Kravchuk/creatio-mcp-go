package creatio

import (
	"errors"
	"syscall"
)

// wsaeconnrefused is WSAECONNREFUSED, the Winsock error of a refused connection; package syscall names
// WSAECONNRESET but not this one, and its ECONNREFUSED is an invented value no Windows call returns.
const wsaeconnrefused syscall.Errno = 10061

func isConnectionRefused(err error) bool {
	return errors.Is(err, wsaeconnrefused) || errors.Is(err, syscall.ECONNREFUSED)
}

func isConnectionReset(err error) bool {
	return errors.Is(err, syscall.WSAECONNRESET) || errors.Is(err, syscall.ECONNRESET)
}
