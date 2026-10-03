package creatio

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// schemaGetResolveOutput mirrors clio's OutputPathConfinement.Resolve for a caller-supplied output-file: the
// symlink-followed path must stay inside the workspace anchor or the OS temp directory, never inside clio's
// own configuration directory, and the target must not exist yet. It returns the absolute lexical path, or
// clio's refusal text.
func schemaGetResolveOutput(outputFile string) (string, string) {
	full, err := filepath.Abs(outputFile)
	if err != nil {
		return "", fmt.Sprintf("output-file '%s' resolves outside the allowed locations; it must be inside the workspace or the OS temp directory.", outputFile)
	}
	home, _ := os.UserHomeDir()
	current, _ := os.Getwd()
	anchor := schemaGetWorkspaceAnchor(current, home)
	unresolvable := fmt.Sprintf("output-file '%s' resolves through an unresolvable symbolic link; refusing to continue.", outputFile)
	real, ok := schemaGetRealPath(full)
	if !ok {
		return "", unresolvable
	}
	realTemp, ok := schemaGetRealPath(schemaGetAbs(os.TempDir()))
	if !ok {
		return "", unresolvable
	}
	realClioHome, ok := schemaGetRealPath(schemaGetAbs(clioHomeDirectory()))
	if !ok {
		return "", unresolvable
	}
	realHome, realAnchor := home, anchor
	if home != "" {
		if realHome, ok = schemaGetRealPath(home); !ok {
			return "", unresolvable
		}
	}
	if anchor != "" {
		if realAnchor, ok = schemaGetRealPath(anchor); !ok {
			return "", unresolvable
		}
	}
	// clio's configuration directory is denied as a whole subtree before the allowed zones are consulted.
	if schemaGetWithin(realClioHome, real) {
		return "", fmt.Sprintf("output-file '%s' resolves inside clio's own configuration directory; refusing to continue.", outputFile)
	}
	if !schemaGetTrustedAnchor(realAnchor, realHome) {
		realAnchor = ""
	}
	if !schemaGetWithin(realAnchor, real) && !schemaGetWithin(realTemp, real) {
		return "", fmt.Sprintf("output-file '%s' resolves outside the allowed locations; it must be inside the workspace or the OS temp directory.", outputFile)
	}
	if _, err := os.Stat(full); err == nil {
		return "", schemaGetExistsMessage(outputFile)
	}
	return full, ""
}

func schemaGetExistsMessage(path string) string {
	return fmt.Sprintf("output-file '%s' already exists; refusing to overwrite it. Choose a different path or remove the existing file.", path)
}

// schemaGetWorkspaceAnchor is clio's PageOutputDirectoryResolver.ResolveAnchor without a home fallback: the
// nearest workspace root above the current directory, else the current directory unless it is the bare home.
func schemaGetWorkspaceAnchor(current, home string) string {
	if current == "" {
		return ""
	}
	for directory := current; ; {
		if info, err := os.Stat(filepath.Join(directory, ".clio", "workspaceSettings.json")); err == nil && !info.IsDir() {
			return directory
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			break
		}
		directory = parent
	}
	if home != "" && schemaGetSamePath(current, home) {
		return ""
	}
	return current
}

// schemaGetTrustedAnchor refuses a filesystem root and any ancestor of (or the) home directory: such an anchor
// would confine the write to the whole volume.
func schemaGetTrustedAnchor(anchor, home string) bool {
	if anchor == "" {
		return false
	}
	if filepath.Dir(anchor) == anchor {
		return false
	}
	return home == "" || !schemaGetWithin(anchor, home)
}

// schemaGetRealPath follows symlinks in the deepest existing ancestor of path and re-appends the missing tail.
// A dangling or cyclic link fails closed (false) instead of degrading to the lexical path.
func schemaGetRealPath(path string) (string, bool) {
	tail := []string{}
	current := path
	for {
		if _, err := os.Lstat(current); err == nil {
			break
		}
		parent := filepath.Dir(current)
		if parent == current {
			return path, true
		}
		tail = append(tail, filepath.Base(current))
		current = parent
	}
	resolved, err := filepath.EvalSymlinks(current)
	if err != nil {
		return "", false
	}
	for index := len(tail) - 1; index >= 0; index-- {
		resolved = filepath.Join(resolved, tail[index])
	}
	return resolved, true
}

func schemaGetAbs(path string) string {
	if absolute, err := filepath.Abs(path); err == nil {
		return absolute
	}
	return path
}

// schemaGetWithin reports whether target is base or below it. macOS and Windows compare case-insensitively,
// as .NET's Path.GetRelativePath does there.
func schemaGetWithin(base, target string) bool {
	if base == "" {
		return false
	}
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		base, target = strings.ToLower(base), strings.ToLower(target)
	}
	relative, err := filepath.Rel(base, target)
	if err != nil || filepath.IsAbs(relative) {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func schemaGetSamePath(left, right string) bool {
	left, right = filepath.Clean(left), filepath.Clean(right)
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

// schemaGetWriteAtomic is clio's OutputPathConfinement.WriteAtomic: the content is completed in an owner-only
// sibling temporary file and then published under the final name without replacing an existing file, so the
// target is only ever absent or complete.
func schemaGetWriteAtomic(path string, content []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temporary := path + "." + randomHex(16) + ".tmp"
	file, err := os.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(content)
	closeErr := file.Close()
	defer os.Remove(temporary)
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	// A hard link publishes the name exclusively: it fails instead of replacing a file created meanwhile.
	if err := os.Link(temporary, path); err != nil {
		if _, statErr := os.Stat(path); statErr == nil {
			return errors.New(schemaGetExistsMessage(path))
		}
		if !errors.Is(err, fs.ErrExist) {
			// Filesystems without hard links: the name was checked absent just above.
			return os.Rename(temporary, path)
		}
		return err
	}
	return nil
}

// stjIndentedJSON writes the node as System.Text.Json does with WriteIndented: two-space indentation, ": "
// after a key, empty containers inline, the platform newline, and the default encoder's escaping.
func (n *jnode) stjIndentedJSON() []byte {
	newline := "\n"
	if runtime.GOOS == "windows" {
		newline = "\r\n"
	}
	var buffer bytes.Buffer
	n.writeIndentedJSON(&buffer, 0, newline)
	return buffer.Bytes()
}

func (n *jnode) writeIndentedJSON(buffer *bytes.Buffer, depth int, newline string) {
	indent := func(level int) {
		buffer.WriteString(newline)
		buffer.WriteString(strings.Repeat("  ", level))
	}
	switch {
	case n.isArray() && len(n.items) > 0:
		buffer.WriteByte('[')
		for index, item := range n.items {
			if index > 0 {
				buffer.WriteByte(',')
			}
			indent(depth + 1)
			item.writeIndentedJSON(buffer, depth+1, newline)
		}
		indent(depth)
		buffer.WriteByte(']')
	case n.isObject() && len(n.keys) > 0:
		buffer.WriteByte('{')
		for index, key := range n.keys {
			if index > 0 {
				buffer.WriteByte(',')
			}
			indent(depth + 1)
			writeJSONString(buffer, key, true)
			buffer.WriteString(": ")
			n.props[key].writeIndentedJSON(buffer, depth+1, newline)
		}
		indent(depth)
		buffer.WriteByte('}')
	default:
		n.writeJSON(buffer, true)
	}
}
