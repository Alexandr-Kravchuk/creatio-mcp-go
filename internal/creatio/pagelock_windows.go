//go:build windows

package creatio

import (
	"errors"
	"fmt"
	"syscall"
	"time"
)

const errorSharingViolation syscall.Errno = 32

// lockPageFile opens the lock file with no sharing, as .NET's FileShare.None does on Windows, so a clio
// process and this server serialize on the same .clio-pages\.locks\{schema}.lock file.
func lockPageFile(path string, timeout time.Duration) (func(), error) {
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(timeout)
	for {
		handle, err := syscall.CreateFile(name, syscall.GENERIC_READ|syscall.GENERIC_WRITE, 0, nil,
			syscall.OPEN_ALWAYS, syscall.FILE_ATTRIBUTE_NORMAL, 0)
		if err == nil {
			return func() { _ = syscall.CloseHandle(handle) }, nil
		}
		if !errors.Is(err, errorSharingViolation) || time.Now().After(deadline) {
			return nil, fmt.Errorf("timed out waiting for the file lock '%s'", path)
		}
		time.Sleep(25 * time.Millisecond)
	}
}
