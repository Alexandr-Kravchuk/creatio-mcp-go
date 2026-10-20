//go:build windows

package knowledge

import (
	"errors"
	"os"
	"strings"
	"syscall"
	"unsafe"
)

const (
	errorSharingViolation syscall.Errno = 32
	errorLockViolation    syscall.Errno = 33
	errorAccessDenied     syscall.Errno = 5
	errorUnableToMove     syscall.Errno = 1176 // ERROR_UNABLE_TO_MOVE_REPLACEMENT
	errorUnableToMove2    syscall.Errno = 1177 // ERROR_UNABLE_TO_MOVE_REPLACEMENT_2
	errorUnableToRemove   syscall.Errno = 1175 // ERROR_UNABLE_TO_REMOVE_REPLACED
	replaceIgnoreMerge                  = 0x00000002
)

var procReplaceFileW = syscall.NewLazyDLL("kernel32.dll").NewProc("ReplaceFileW")

// clio keys ProcessLocks with OrdinalIgnoreCase on Windows.
func processLockKey(path string) string { return strings.ToLower(path) }

// tryOSLock opens the lock file with no sharing, as .NET's FileShare.None does on Windows.
func tryOSLock(path string) (release func(), busy bool, err error) {
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, false, err
	}
	handle, err := syscall.CreateFile(name, syscall.GENERIC_READ|syscall.GENERIC_WRITE, 0, nil,
		syscall.OPEN_ALWAYS, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, errors.Is(err, errorSharingViolation) || errors.Is(err, errorLockViolation), err
	}
	return func() { _ = syscall.CloseHandle(handle) }, false, nil
}

// replaceFile publishes source over target the way clio publishes appsettings.json on Windows: ReplaceFile
// when the target exists (a reader holding FileShare.Read refuses MoveFileEx but not ReplaceFile), a move
// otherwise. ReplaceFile keeps the target's ACL, as File.Replace does.
func replaceFile(source, target string) error {
	if _, err := os.Lstat(target); err != nil {
		return os.Rename(source, target)
	}
	from, err := syscall.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	to, err := syscall.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	ok, _, callErr := procReplaceFileW.Call(uintptr(unsafe.Pointer(to)), uintptr(unsafe.Pointer(from)), 0,
		replaceIgnoreMerge, 0, 0)
	if ok == 0 {
		return &os.LinkError{Op: "replace", Old: source, New: target, Err: callErr}
	}
	return nil
}

// isPublishContention is a refusal caused by another handle on the target, which clio retries.
func isPublishContention(err error) bool {
	return errors.Is(err, errorSharingViolation) || errors.Is(err, errorLockViolation) ||
		errors.Is(err, errorAccessDenied) || errors.Is(err, errorUnableToMove) ||
		errors.Is(err, errorUnableToMove2) || errors.Is(err, errorUnableToRemove)
}
