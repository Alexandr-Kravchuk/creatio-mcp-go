//go:build !windows

package creatio

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"
)

// lockPageFile takes the exclusive advisory lock .NET takes on Unix for FileShare.None (flock), so a clio
// process and this server serialize on the same .clio-pages/.locks/{schema}.lock file.
func lockPageFile(path string, timeout time.Duration) (func(), error) {
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(timeout)
	for {
		err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() {
				_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
				_ = file.Close()
			}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) || time.Now().After(deadline) {
			_ = file.Close()
			return nil, fmt.Errorf("timed out waiting for the file lock '%s'", path)
		}
		time.Sleep(25 * time.Millisecond)
	}
}
