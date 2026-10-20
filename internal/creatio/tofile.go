package creatio

// The shared part of clio 8.1.0.142's *-to-file MCP tools (execute-esq-to-file, odata-read-to-file,
// get-component-info-to-file, get-request-info-to-file, list-entity-client-schemas-to-file): the output-file
// confinement and the no-overwrite write of McpOutputFileWriter / ODataFileContract, and the documentation
// projection of DocumentationFileProjection.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/redact"
)

// ToFileResolve is clio's McpOutputFileWriter.TryResolve: a blank path is "output-file is required.", any
// other path goes through ToFileResolveOutput. It writes nothing.
func ToFileResolve(outputFile string) (string, string) {
	if strings.TrimSpace(outputFile) == "" {
		return "", "output-file is required."
	}
	return ToFileResolveOutput(outputFile)
}

// ToFileResolveOutput is clio's OutputPathConfinement.ResolveCanonicalOutput: the symlink-followed path must
// stay inside the workspace anchor or the OS temp directory and outside clio's own configuration directory,
// and nothing (file or directory) may exist there yet. It returns the CANONICAL path, the one the write uses
// and the answer reports (on macOS a temp path reads /private/var/..., as clio reports it).
func ToFileResolveOutput(outputFile string) (string, string) {
	full, err := filepath.Abs(outputFile)
	if err != nil {
		return "", redact.Text("Failed to resolve output-file: " + err.Error())
	}
	real, refusal := toFileConfined(full, outputFile)
	if refusal != "" {
		return "", refusal
	}
	if _, err := os.Stat(real); err == nil {
		return "", schemaGetExistsMessage(outputFile)
	}
	return real, ""
}

// toFileConfined is OutputPathConfinement.ResolveConfined: the symlink-followed path of full, or clio's
// refusal. It is schemaGetResolveOutput without the existence rule, which the canonical variant applies to
// the canonical path instead of the lexical one.
func toFileConfined(full, outputFile string) (string, string) {
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
	if schemaGetWithin(realClioHome, real) {
		return "", fmt.Sprintf("output-file '%s' resolves inside clio's own configuration directory; refusing to continue.", outputFile)
	}
	if !schemaGetTrustedAnchor(realAnchor, realHome) {
		realAnchor = ""
	}
	if !schemaGetWithin(realAnchor, real) && !schemaGetWithin(realTemp, real) {
		return "", fmt.Sprintf("output-file '%s' resolves outside the allowed locations; it must be inside the workspace or the OS temp directory.", outputFile)
	}
	return real, ""
}

// ToFileWriteNew is clio's McpOutputFileWriter.TryWriteNew over IConfinedFileAccess.WriteNew: missing parent
// directories are created owner-only, the content is completed in an owner-only (0600) staging file and then
// published under the final name by a hard link, which fails instead of replacing a file created meanwhile.
// It returns "" on success, otherwise clio's (redacted) refusal; nothing is left behind on failure.
func ToFileWriteNew(path string, content []byte) string {
	if err := toFileWriteNew(path, content); err != nil {
		var exists toFileExistsError
		if errors.As(err, &exists) {
			return redact.Text(schemaGetExistsMessage(path))
		}
		return redact.Text("Failed to write output-file: " + err.Error())
	}
	return ""
}

type toFileExistsError struct{}

func (toFileExistsError) Error() string { return "the output file already exists" }

func toFileWriteNew(path string, content []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	staging := path + "." + randomHex(16) + ".tmp"
	file, err := os.OpenFile(staging, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer os.Remove(staging)
	_, writeErr := file.Write(content)
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	if err := os.Link(staging, path); err != nil {
		if _, statErr := os.Lstat(path); statErr == nil || errors.Is(err, fs.ErrExist) {
			return toFileExistsError{}
		}
		// A filesystem without hard links: the name was checked absent just above.
		if err := os.Rename(staging, path); err != nil {
			if _, statErr := os.Lstat(path); statErr == nil {
				return toFileExistsError{}
			}
			return err
		}
	}
	return nil
}

// STJDefaultJSON writes value as compact JSON escaped the way System.Text.Json's default encoder escapes it.
func STJDefaultJSON(value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	node, err := parseJNode(encoded)
	if err != nil {
		return nil, err
	}
	return node.stjJSON(), nil
}

// ---- DocumentationFileProjection ----

// ToFileDocumentationFailure is DocumentationFileProjection.Failure: {"success":false,"error":...}.
func ToFileDocumentationFailure(message string) json.RawMessage {
	encoded, _ := json.Marshal(struct {
		Success bool   `json:"success"`
		Error   string `json:"error"`
	}{false, message})
	return encoded
}

// ToFileProjectDocumentation is DocumentationFileProjection.Project over the wire JSON of an inline
// get-component-info / get-request-info answer: without documentation the answer is returned unchanged and
// nothing is written; otherwise documentation is removed, documentationFile and documentationSections are
// appended, and the markdown is written to path (the answer is built before the write).
func ToFileProjectDocumentation(response any, documentation *string, path string) json.RawMessage {
	encoded, err := json.Marshal(response)
	if err != nil {
		return ToFileDocumentationFailure(err.Error())
	}
	if documentation == nil || *documentation == "" {
		return encoded
	}
	node, err := parseJNode(encoded)
	if err != nil || !node.isObject() {
		return ToFileDocumentationFailure("the response could not be projected")
	}
	node.remove("documentation")
	node.set("documentationFile", newString(path))
	sections := newArray()
	for _, heading := range ToFileExtractHeadings(*documentation) {
		sections.items = append(sections.items, newString(heading))
	}
	node.set("documentationSections", sections)
	var projected bytes.Buffer
	writeMcpResultJSON(&projected, node)
	if refusal := ToFileWriteNew(path, []byte(*documentation)); refusal != "" {
		return ToFileDocumentationFailure(refusal)
	}
	return projected.Bytes()
}

// ToFileExtractHeadings is DocumentationFileProjection.ExtractHeadings: the ATX headings (# to ######) in
// document order with their # prefix, skipping lines inside fenced code blocks (CommonMark fence rules).
func ToFileExtractHeadings(markdown string) []string {
	headings := []string{}
	var open *toFileFence
	for _, rawLine := range strings.Split(markdown, "\n") {
		line := strings.TrimRight(rawLine, "\r")
		trimmed := strings.TrimLeftFunc(line, dotnetIsWhiteSpace)
		withinIndent := toFileUTF16Len(line)-toFileUTF16Len(trimmed) <= 3
		if withinIndent {
			if fence, ok := toFileReadFence(trimmed); ok {
				open = toFileNextOpenFence(open, fence)
				continue
			}
		}
		if open == nil && withinIndent {
			if heading, ok := toFileParseHeading(trimmed); ok {
				headings = append(headings, heading)
			}
		}
	}
	return headings
}

type toFileFence struct {
	character byte
	length    int
	hasInfo   bool
}

func toFileReadFence(trimmed string) (toFileFence, bool) {
	if len(trimmed) < 3 || (trimmed[0] != '`' && trimmed[0] != '~') {
		return toFileFence{}, false
	}
	character := trimmed[0]
	length := 0
	for length < len(trimmed) && trimmed[length] == character {
		length++
	}
	fence := toFileFence{character: character, length: length, hasInfo: strings.TrimFunc(trimmed[length:], dotnetIsWhiteSpace) != ""}
	return fence, length >= 3
}

func toFileNextOpenFence(open *toFileFence, fence toFileFence) *toFileFence {
	if open == nil {
		return &fence
	}
	if fence.character == open.character && fence.length >= open.length && !fence.hasInfo {
		return nil
	}
	return open
}

func toFileParseHeading(trimmed string) (string, bool) {
	level := 0
	for level < len(trimmed) && trimmed[level] == '#' {
		level++
	}
	if level < 1 || level > 6 || level >= len(trimmed) || trimmed[level] != ' ' {
		return "", false
	}
	heading := strings.TrimRightFunc(trimmed, dotnetIsWhiteSpace)
	end := len(heading)
	for end > level && heading[end-1] == '#' {
		end--
	}
	if end < len(heading) && heading[end-1] == ' ' {
		return strings.TrimRightFunc(heading[:end], dotnetIsWhiteSpace), true
	}
	return heading, true
}

// dotnetIsWhiteSpace is char.IsWhiteSpace, which string.Trim uses: Unicode White_Space.
func dotnetIsWhiteSpace(r rune) bool {
	switch r {
	case ' ', '\t', '\n', '\v', '\f', '\r', 0x85, 0xA0, 0x1680, 0x2028, 0x2029, 0x202F, 0x205F, 0x3000:
		return true
	}
	return r >= 0x2000 && r <= 0x200A
}

func toFileUTF16Len(text string) int {
	length := 0
	for _, r := range text {
		if r >= 0x10000 {
			length += 2
		} else {
			length++
		}
	}
	return length
}
