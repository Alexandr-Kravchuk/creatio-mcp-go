//go:build !windows

package knowledge

import (
	"errors"
	"os"
	"syscall"
)

func processLockKey(path string) string { return path }

// tryOSLock takes the exclusive, non-blocking flock .NET takes on Unix for FileShare.None.
func tryOSLock(path string) (release func(), busy bool, err error) {
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o666)
	if err != nil {
		return nil, false, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, errors.Is(err, syscall.EWOULDBLOCK), err
	}
	return func() {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
	}, false, nil
}

// replaceFile publishes source over target atomically: rename(2), as .NET's File.Move/File.Replace do on Unix.
func replaceFile(source, target string) error { return os.Rename(source, target) }

// isPublishContention is true for a failure a retry can clear; rename(2) has none on Unix.
func isPublishContention(error) bool { return false }
