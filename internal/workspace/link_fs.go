package workspace

// The file system calls of the link commands, as .NET makes them: Directory.GetDirectories (the file
// system's own order, links to folders counted as folders), Directory.Delete(path, true) (a link is removed,
// not followed), Directory.CreateSymbolicLink, and the IOException texts .NET raises when one of them fails.

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ioError is an exception message .NET raises for a failed file system call, exactly as clio prints it.
type ioError struct{ message string }

func (e *ioError) Error() string { return e.message }

func pathNotFound(path string) error {
	return &ioError{message: "Could not find a part of the path '" + path + "'."}
}

func accessDenied(path string) error {
	return &ioError{message: "Access to the path '" + path + "' is denied."}
}

func pathTooLong(path string) error {
	return &ioError{message: "The path '" + path + "' is too long, or a component of the specified path is too long."}
}

// capitalized is the platform's error sentence as the C library words it: Go's texts start in lower case.
func capitalized(text string) string {
	if text == "" {
		return text
	}
	return strings.ToUpper(text[:1]) + text[1:]
}

// enumerationRoot is the path .NET's enumerator names in its failures: the full path without a trailing
// separator.
func enumerationRoot(path string) string {
	full := fullPath(path)
	trimmed := strings.TrimRight(full, `/\`)
	if trimmed == "" || strings.HasSuffix(trimmed, ":") {
		return full
	}
	return trimmed
}

// listDirectories is Directory.GetDirectories(path) reduced to the names: every sub-folder, a link to a
// folder included, in the order the file system lists them (.NET does not sort).
func listDirectories(path string) ([]string, error) {
	directory, err := os.Open(path)
	if err != nil {
		return nil, ioFailure(err, enumerationRoot(path), true)
	}
	defer directory.Close()
	entries, err := directory.ReadDir(-1)
	if err != nil {
		return nil, ioFailure(err, enumerationRoot(path), true)
	}
	names := []string{}
	for _, entry := range entries {
		if isDirectoryEntry(path, entry) {
			names = append(names, entry.Name())
		}
	}
	return names, nil
}

// childPath is how .NET's enumerator builds an entry's path: the folder as given joined with the name.
func childPath(parent, name string) string {
	if strings.HasSuffix(parent, "/") || strings.HasSuffix(parent, string(filepath.Separator)) {
		return parent + name
	}
	return parent + string(filepath.Separator) + name
}

// deleteDirectory is Directory.Delete(path, true): a link is removed without touching its target.
func deleteDirectory(path string) error {
	if err := os.RemoveAll(path); err != nil {
		failed := path
		var pathErr *fs.PathError
		if errors.As(err, &pathErr) {
			failed = pathErr.Path
		}
		return ioFailure(err, failed, true)
	}
	return nil
}
