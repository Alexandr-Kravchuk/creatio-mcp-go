package creatio

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// pageValidateMaxBodyFileBytes is clio's MaxBodyFileBytes for body-file.
const pageValidateMaxBodyFileBytes = 4 * 1024 * 1024

const (
	pageValidateMissingBody      = "Either 'body' or 'body-file' must provide page body content."
	pageValidateMissingBodyFile  = "body-file was not found."
	pageValidateEmptyBodyFile    = "body-file is empty."
	pageValidateUnreadable       = "body-file could not be read."
	pageValidateNonLocalBodyFile = "body-file must be an absolute local path."
)

// PageValidateGapWarning names the checks of clio's validate-page this server does not run, so a passing
// verdict is never read as clio's full verdict.
const PageValidateGapWarning = "This server checked only JavaScript syntax (structurally: brackets, strings, comments, templates), " +
	"the section marker pairs, the JSON/object content of each section, and the resources argument. It did NOT run clio's " +
	"field and column binding, handler, converter, validator, schema-deps, context-await, localizable-text, AST lint, chart-widget, " +
	"run-process-button or parent-container (known-containers) checks; run clio validate-page before update-page for the full verdict."

// PageValidateMobileUnsupported is the refusal for a mobile (JSON) body, whose validation in clio applies the
// diff through the client-engine appliers and the component catalogs.
const PageValidateMobileUnsupported = "Mobile page bodies are not validated by this server: clio validates them by applying the diff " +
	"through the client-engine appliers and the mobile component catalog, which this server does not have. " +
	"Validate this body with clio validate-page."

type PageValidateRequest struct {
	Body      string
	BodyFile  string
	Resources string
}

// PageValidateResult is clio's validate-page envelope.
type PageValidateResult struct {
	Valid      bool                   `json:"valid"`
	Validation PageValidationOutcomes `json:"validation"`
}

// PageValidationOutcomes is clio's PageSyncValidationResult.
type PageValidationOutcomes struct {
	MarkersOK  bool     `json:"markers-ok"`
	JSSyntaxOK bool     `json:"js-syntax-ok"`
	ContentOK  bool     `json:"content-ok"`
	Errors     []string `json:"errors,omitempty"`
	Warnings   []string `json:"warnings,omitempty"`
}

var (
	pageValidateRequiredMarkers  = []string{"SCHEMA_DEPS", "SCHEMA_ARGS", "SCHEMA_VIEW_CONFIG_DIFF", "SCHEMA_HANDLERS", "SCHEMA_CONVERTERS", "SCHEMA_VALIDATORS"}
	pageValidateAlternatePairs   = [][2]string{{"SCHEMA_VIEW_MODEL_CONFIG_DIFF", "SCHEMA_VIEW_MODEL_CONFIG"}, {"SCHEMA_MODEL_CONFIG_DIFF", "SCHEMA_MODEL_CONFIG"}}
	pageValidateJSONArrayMarkers = []string{"SCHEMA_VIEW_CONFIG_DIFF", "SCHEMA_DIFF", "SCHEMA_VIEW_MODEL_CONFIG_DIFF", "SCHEMA_MODEL_CONFIG_DIFF", "SCHEMA_DEPS"}
	pageValidateJSONObjMarkers   = []string{"SCHEMA_VIEW_MODEL_CONFIG", "SCHEMA_MODEL_CONFIG"}
	pageValidateJSObjectMarkers  = []string{"SCHEMA_CONVERTERS", "SCHEMA_VALIDATORS"}
)

// ValidatePage validates a Freedom UI page body offline, without calling Creatio, following clio's
// validate-page: the body or body-file is resolved with clio's rules and texts, then the JavaScript syntax
// gate, marker integrity and section content run. The remaining clio checks are named in
// PageValidateGapWarning, which every web verdict past the syntax gate carries.
func ValidatePage(input PageValidateRequest) PageValidateResult {
	body, refusal := pageValidateResolveBody(input)
	if refusal != "" {
		return pageValidateInvalidSource(refusal)
	}
	if strings.HasPrefix(strings.TrimSpace(body), "{") {
		return pageValidateInvalidSource(PageValidateMobileUnsupported)
	}
	if _, err := classicPageParse(body); err != nil {
		return pageValidateInvalidSource(pageValidateSyntaxMessage(body, err))
	}
	markers := pageValidateMarkerIntegrity(body)
	content := []string{}
	contentOK := true
	if len(markers) == 0 {
		content = pageValidateMarkerContent(body)
		contentOK = len(content) == 0
		if contentOK && !pageValidateResourcesValid(input.Resources) {
			contentOK = false
			content = append(content, "resources must be a valid JSON object string")
		}
	}
	errorsList := append(append([]string{}, markers...), content...)
	outcomes := PageValidationOutcomes{MarkersOK: len(markers) == 0, JSSyntaxOK: true, ContentOK: contentOK,
		Warnings: []string{PageValidateGapWarning}}
	if len(errorsList) > 0 {
		outcomes.Errors = errorsList
	}
	return PageValidateResult{Valid: outcomes.MarkersOK && outcomes.ContentOK, Validation: outcomes}
}

func pageValidateInvalidSource(message string) PageValidateResult {
	return PageValidateResult{Validation: PageValidationOutcomes{Errors: []string{message}}}
}

// pageValidateResolveBody is clio's ResolveBodyAsync: inline body wins; body-file must be an absolute local
// path to a regular, non-empty file of at most 4 MiB.
func pageValidateResolveBody(input PageValidateRequest) (string, string) {
	if strings.TrimSpace(input.Body) != "" {
		return input.Body, ""
	}
	if strings.TrimSpace(input.BodyFile) == "" {
		return "", pageValidateMissingBody
	}
	if !pageValidateAbsoluteLocal(input.BodyFile) {
		return "", pageValidateNonLocalBodyFile
	}
	path := filepath.Clean(input.BodyFile)
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", pageValidateMissingBodyFile
		}
		return "", pageValidateUnreadable
	}
	if info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode()&os.ModeDevice != 0 {
		return "", pageValidateUnreadable
	}
	if info.Size() == 0 {
		return "", pageValidateEmptyBodyFile
	}
	if info.Size() > pageValidateMaxBodyFileBytes {
		return "", fmt.Sprintf("body-file exceeds the %d-byte limit.", pageValidateMaxBodyFileBytes)
	}
	file, err := os.Open(path)
	if err != nil {
		return "", pageValidateUnreadable
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, pageValidateMaxBodyFileBytes+1))
	if err != nil {
		return "", pageValidateUnreadable
	}
	if len(data) == 0 {
		return "", pageValidateEmptyBodyFile
	}
	if len(data) > pageValidateMaxBodyFileBytes {
		return "", fmt.Sprintf("body-file exceeds the %d-byte limit.", pageValidateMaxBodyFileBytes)
	}
	body := pageValidateDecode(data)
	if strings.TrimSpace(body) == "" {
		return "", pageValidateEmptyBodyFile
	}
	return body, ""
}

// pageValidateAbsoluteLocal is Path.IsPathFullyQualified without UNC (\\ or //) paths.
func pageValidateAbsoluteLocal(path string) bool {
	if strings.HasPrefix(path, `\\`) || strings.HasPrefix(path, "//") {
		return false
	}
	if runtime.GOOS == "windows" {
		return len(path) >= 3 && hasWindowsDrivePrefix(path) && (path[2] == '\\' || path[2] == '/')
	}
	return strings.HasPrefix(path, "/")
}

// pageValidateDecode reads UTF-8 with BOM detection, as StreamReader(UTF8, detectEncodingFromByteOrderMarks).
func pageValidateDecode(data []byte) string {
	switch {
	case bytes.HasPrefix(data, []byte{0xEF, 0xBB, 0xBF}):
		return pageValidateUTF8(data[3:])
	case bytes.HasPrefix(data, []byte{0xFF, 0xFE}) || bytes.HasPrefix(data, []byte{0xFE, 0xFF}):
		bigEndian := data[0] == 0xFE
		units := make([]uint16, 0, len(data)/2)
		for index := 2; index+1 < len(data); index += 2 {
			if bigEndian {
				units = append(units, uint16(data[index])<<8|uint16(data[index+1]))
			} else {
				units = append(units, uint16(data[index+1])<<8|uint16(data[index]))
			}
		}
		return string(utf16.Decode(units))
	}
	return pageValidateUTF8(data)
}

// pageValidateUTF8 replaces invalid sequences with U+FFFD, as .NET's UTF-8 decoder does.
func pageValidateUTF8(data []byte) string {
	if utf8.Valid(data) {
		return string(data)
	}
	return strings.ToValidUTF8(string(data), "�")
}

// pageValidateSyntaxMessage is clio's PageBodySyntaxValidator.FormatError: a 1-based line and column.
func pageValidateSyntaxMessage(body string, err error) string {
	message, offset := "Unexpected token", 0
	var syntax classicPageSyntaxError
	if errors.As(err, &syntax) {
		message, offset = syntax.message, syntax.offset
	}
	line, column := 1, 0
	runes := []rune(body)
	for index := 0; index < offset && index < len(runes); index++ {
		switch runes[index] {
		case '\n', ' ', ' ':
			line, column = line+1, 0
		case '\r':
			if index+1 < len(runes) && runes[index+1] == '\n' {
				column++
				continue
			}
			line, column = line+1, 0
		default:
			column += len(utf16.Encode([]rune{runes[index]}))
		}
	}
	return fmt.Sprintf("JavaScript syntax error at line %d, column %d: %s. The body was NOT sent to Creatio.", line, column+1, message)
}

// pageValidateMarkerPatterns is clio's BuildMarkerPattern for every marker integrity looks for.
var pageValidateMarkerPatterns = func() map[string]*regexp.Regexp {
	patterns := map[string]*regexp.Regexp{}
	markers := append([]string{}, pageValidateRequiredMarkers...)
	for _, pair := range pageValidateAlternatePairs {
		markers = append(markers, pair[0], pair[1])
	}
	for _, marker := range markers {
		quoted := regexp.QuoteMeta(marker)
		patterns[marker] = regexp.MustCompile(`(?s)/\*\*` + quoted + `\*/(.*?)/\*\*` + quoted + `\*/`)
	}
	return patterns
}()

func pageValidateHasMarker(body, marker string) bool {
	return pageValidateMarkerPatterns[marker].MatchString(body)
}

// pageValidateMarkerIntegrity is clio's ValidateMarkerIntegrity: the missing marker names, in clio's order.
func pageValidateMarkerIntegrity(body string) []string {
	missing := []string{}
	for _, marker := range pageValidateRequiredMarkers {
		if !pageValidateHasMarker(body, marker) {
			missing = append(missing, marker)
		}
	}
	for _, pair := range pageValidateAlternatePairs {
		if !pageValidateHasMarker(body, pair[0]) && !pageValidateHasMarker(body, pair[1]) {
			missing = append(missing, pair[0]+" or "+pair[1])
		}
	}
	return missing
}

// pageValidateMarkerContent is clio's ValidateMarkerContent without the handler-structure rules: JSON
// sections must parse (comments and trailing commas allowed), object sections must stay object literals.
func pageValidateMarkerContent(body string) []string {
	for _, group := range [][]string{pageValidateJSONArrayMarkers, pageValidateJSONObjMarkers} {
		for _, marker := range group {
			content, ok := readPageSection(body, marker)
			if !ok {
				continue
			}
			if err := pageValidateJSON(content); err != nil {
				return []string{fmt.Sprintf("Invalid JSON in %s: %s", marker, err.Error())}
			}
		}
	}
	problems := []string{}
	for _, marker := range pageValidateJSObjectMarkers {
		content, ok := readPageSection(body, marker)
		if !ok {
			continue
		}
		trimmed := strings.TrimSpace(content)
		if trimmed == "" || !strings.HasPrefix(trimmed, "{") || !strings.HasSuffix(trimmed, "}") {
			problems = append(problems, fmt.Sprintf("Invalid JavaScript object section in %s: section must remain an object literal.", marker))
			continue
		}
		if failure := pageValidateBracketSyntax("const __clioSection = " + trimmed + ";"); failure != "" {
			problems = append(problems, fmt.Sprintf("Invalid JavaScript object section in %s: %s", marker, failure))
		}
	}
	return problems
}

// pageValidateJSON parses a section as clio does: strict JSON except that trailing commas are allowed.
// Comments are rejected, as they are by clio on a live stand.
func pageValidateJSON(content string) error {
	_, err := pageValidateCleanJSON(content)
	return err
}

// pageValidateCleanJSON removes trailing commas and returns strict JSON that decodes.
func pageValidateCleanJSON(content string) (string, error) {
	var cleaned strings.Builder
	runes := []rune(content)
	for index := 0; index < len(runes); index++ {
		ch := runes[index]
		switch {
		case ch == '"':
			start := index
			for index++; index < len(runes) && runes[index] != '"'; index++ {
				if runes[index] == '\\' {
					index++
				}
			}
			cleaned.WriteString(string(runes[start:min(index+1, len(runes))]))
		case ch == ']' || ch == '}':
			// Drop a trailing comma before the closer.
			if text := strings.TrimRight(cleaned.String(), " \t\r\n"); strings.HasSuffix(text, ",") {
				cleaned.Reset()
				cleaned.WriteString(strings.TrimSuffix(text, ","))
			}
			cleaned.WriteRune(ch)
		default:
			cleaned.WriteRune(ch)
		}
	}
	var probe any
	decoder := json.NewDecoder(strings.NewReader(cleaned.String()))
	if err := decoder.Decode(&probe); err != nil {
		return "", err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return "", errors.New("unexpected data after the JSON value")
	}
	return cleaned.String(), nil
}

// pageValidateBracketSyntax is clio's legacy brace counter ValidateJsSyntax, used for object sections. It
// reports positions as UTF-16 offsets, as .NET string indexes are.
func pageValidateBracketSyntax(text string) string {
	units := utf16.Encode([]rune(text))
	type opened struct {
		bracket  uint16
		position int
	}
	stack := []opened{}
	length := len(units)
	for index := 0; index < length; {
		ch := units[index]
		switch {
		case ch == '/' && index+1 < length && units[index+1] == '/':
			for index += 2; index < length && units[index] != '\n' && units[index] != '\r'; index++ {
			}
		case ch == '/' && index+1 < length && units[index+1] == '*':
			start := index
			closed := false
			for index += 2; index+1 < length; index++ {
				if units[index] == '*' && units[index+1] == '/' {
					index += 2
					closed = true
					break
				}
			}
			if !closed {
				return fmt.Sprintf("Unterminated block comment at position %d.", start)
			}
		case ch == '\'' || ch == '"':
			start := index
			for index++; index < length; {
				if units[index] == '\\' {
					index += 2
					continue
				}
				if units[index] == ch {
					index++
					break
				}
				if units[index] == '\n' || units[index] == '\r' {
					return fmt.Sprintf("Unterminated string literal at position %d.", start)
				}
				index++
			}
		case ch == '`':
			start := index
			for index++; index < length; {
				if units[index] == '\\' {
					index += 2
					continue
				}
				if units[index] == '`' {
					index++
					break
				}
				index++
			}
			if index > length || (index == length && units[length-1] != '`') {
				return fmt.Sprintf("Unterminated template literal at position %d.", start)
			}
		case ch == '(' || ch == '{' || ch == '[':
			stack = append(stack, opened{ch, index})
			index++
		case ch == ')' || ch == '}' || ch == ']':
			if len(stack) == 0 {
				return fmt.Sprintf("Unexpected closing '%c' at position %d with no matching opening bracket.", rune(ch), index)
			}
			top := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			expected := map[uint16]uint16{')': '(', '}': '{', ']': '['}[ch]
			if top.bracket != expected {
				return fmt.Sprintf("Mismatched bracket: '%c' at position %d closed by '%c' at position %d.", rune(top.bracket), top.position, rune(ch), index)
			}
			index++
		default:
			index++
		}
	}
	if len(stack) > 0 {
		top := stack[len(stack)-1]
		return fmt.Sprintf("Unclosed '%c' at position %d.", rune(top.bracket), top.position)
	}
	return ""
}

// pageValidateResourcesValid is clio's TryParseResources: absent or blank is fine, otherwise a JSON object
// whose values are all strings.
func pageValidateResourcesValid(resources string) bool {
	if strings.TrimSpace(resources) == "" {
		return true
	}
	cleaned, err := pageValidateCleanJSON(resources)
	if err != nil {
		return false
	}
	var object map[string]any
	if json.Unmarshal([]byte(cleaned), &object) != nil || object == nil {
		return false
	}
	for _, value := range object {
		if _, isString := value.(string); !isString {
			return false
		}
	}
	return true
}
