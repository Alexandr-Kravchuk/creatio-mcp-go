//go:build windows

package workspace

import (
	"errors"
	"os"
	"syscall"
)

const (
	symbolicLinkFlagDirectory         = 0x1
	symbolicLinkFlagAllowUnprivileged = 0x2
	errorFileExists                   = syscall.Errno(80)
	errorAlreadyExists                = syscall.Errno(183)
	errorFilenameExceedsRange         = syscall.Errno(206)
	errorSharingViolation             = syscall.Errno(32)
	errorPathNotFound                 = syscall.Errno(3)
	errorAccessDenied                 = syscall.Errno(5)
	errorFileNotFound                 = syscall.Errno(2)
)

// isDirectoryEntry is how .NET's enumerator classifies an entry on Windows: by the entry's own directory
// attribute, which a directory symbolic link carries whether or not its target exists.
func isDirectoryEntry(_ string, entry os.DirEntry) bool {
	if entry.IsDir() {
		return true
	}
	info, err := entry.Info()
	if err != nil {
		return false
	}
	if data, ok := info.Sys().(*syscall.Win32FileAttributeData); ok {
		return data.FileAttributes&syscall.FILE_ATTRIBUTE_DIRECTORY != 0
	}
	return false
}

// createDirectorySymlink is Directory.CreateSymbolicLink(path, target) on Windows: a directory symbolic link
// (not a junction), created without elevation where developer mode allows it, whether or not the target
// exists. An existing path fails with ERROR_ALREADY_EXISTS.
func createDirectorySymlink(path, target string) error {
	link, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	to, err := syscall.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	if err := syscall.CreateSymbolicLink(link, to, symbolicLinkFlagDirectory|symbolicLinkFlagAllowUnprivileged); err != nil {
		return ioFailure(err, path, true)
	}
	return nil
}

// ioFailure is Win32Marshal.GetExceptionForWin32Error: the .NET text for the Win32 error a call failed with.
func ioFailure(err error, path string, directory bool) error {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return err
	}
	switch errno {
	case errorFileNotFound:
		if directory {
			return pathNotFound(path)
		}
		return &ioError{message: "Could not find file '" + path + "'."}
	case errorPathNotFound:
		return pathNotFound(path)
	case errorAccessDenied:
		return accessDenied(path)
	case errorAlreadyExists:
		return &ioError{message: "Cannot create '" + path + "' because a file or directory with the same name already exists."}
	case errorFileExists:
		return &ioError{message: "The file '" + path + "' already exists."}
	case errorFilenameExceedsRange:
		return pathTooLong(path)
	case errorSharingViolation:
		return &ioError{message: "The process cannot access the file '" + path + "' because it is being used by another process."}
	}
	return &ioError{message: errno.Error() + " : '" + path + "'"}
}
