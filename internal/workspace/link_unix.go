//go:build !windows

package workspace

import (
	"errors"
	"os"
	"syscall"
)

// isDirectoryEntry is how .NET's enumerator classifies an entry on Unix: a folder, or a link whose target is
// a folder (a dangling link is not one).
func isDirectoryEntry(parent string, entry os.DirEntry) bool {
	if entry.IsDir() {
		return true
	}
	if entry.Type()&os.ModeSymlink == 0 {
		return false
	}
	info, err := os.Stat(childPath(parent, entry.Name()))
	return err == nil && info.IsDir()
}

// createDirectorySymlink is Directory.CreateSymbolicLink(path, target): a symbolic link holding the target
// as given.
func createDirectorySymlink(path, target string) error {
	if err := os.Symlink(target, path); err != nil {
		return ioFailure(err, path, true)
	}
	return nil
}

// ioFailure is Interop.GetExceptionForIoErrno: the .NET text for the errno a call failed with.
func ioFailure(err error, path string, directory bool) error {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return err
	}
	if directory && (errno == syscall.ENOENT || errno == syscall.ENOTDIR) {
		return pathNotFound(path)
	}
	switch errno {
	case syscall.ENOENT:
		return &ioError{message: "Could not find file '" + path + "'."}
	case syscall.EACCES, syscall.EBADF, syscall.EPERM:
		return accessDenied(path)
	case syscall.ENAMETOOLONG:
		return pathTooLong(path)
	case syscall.EEXIST:
		return &ioError{message: "The file '" + path + "' already exists."}
	}
	return &ioError{message: capitalized(errno.Error()) + " : '" + path + "'"}
}
