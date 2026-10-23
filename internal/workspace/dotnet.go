package workspace

// What clio's workspace commands get from .NET and have to be reproduced byte for byte: the host newline,
// File.WriteAllText (UTF-8 without BOM, an existing file keeps its mode), File.ReadAllText (BOM dropped),
// Newtonsoft's and System.Text.Json's indented writers, Path.GetRelativePath and the culture-aware string
// ordering of LINQ's OrderBy.

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// newLine is Environment.NewLine, which both JSON writers, XmlDocument.Save and clio's own texts use.
func newLine() string {
	if runtime.GOOS == "windows" {
		return "\r\n"
	}
	return "\n"
}

// writeAllText is File.WriteAllText with UTF-8 and no BOM. A new file gets the default mode; an existing one
// is truncated and keeps its mode, as .NET opens it with FileMode.Create.
func writeAllText(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o666)
}

// readAllText is File.ReadAllText: the UTF-8 byte order mark is not part of the text.
func readAllText(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})), nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func directoryExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// isEmptyDirectory is Directory.GetFileSystemEntries(path).Any() negated.
func isEmptyDirectory(path string) bool {
	entries, err := os.ReadDir(path)
	return err == nil && len(entries) == 0
}

// relativePath is Path.GetRelativePath: the target itself when it is on another root.
func relativePath(from, to string) string {
	relative, err := filepath.Rel(from, to)
	if err != nil {
		return to
	}
	return relative
}

// modifiedOnUtc is PackageDescriptor.ConvertToModifiedOnUtc: milliseconds since the Unix epoch, the
// milliseconds of the time itself cleared, in Microsoft's JSON date form.
func modifiedOnUtc(at time.Time) string {
	return fmt.Sprintf("/Date(%d)/", at.Unix()*1000)
}

// now is the clock the generated descriptors read; tests pin it.
var now = time.Now

var randRead = rand.Read

// newGUID is Guid.NewGuid in its "D" form; tests pin it.
var newGUID = func() string {
	var raw [16]byte
	if _, err := randRead(raw[:]); err != nil {
		panic(err)
	}
	raw[6] = raw[6]&0x0f | 0x40
	raw[8] = raw[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16])
}

// ---------------------------------------------------------------------------------------------- JSON

// jsonValue is an ordered JSON tree for the two .NET writers: string, bool, int, nil, jsonArray, *jsonObject.
type jsonValue = any

type jsonArray []jsonValue

type jsonMember struct {
	name  string
	value jsonValue
}

type jsonObject struct{ members []jsonMember }

func object(members ...jsonMember) *jsonObject { return &jsonObject{members: members} }

func member(name string, value jsonValue) jsonMember { return jsonMember{name: name, value: value} }

// jsonStyle is the string escaping of one writer.
type jsonStyle int

const (
	// newtonsoftStyle is JsonConvert.SerializeObject(value, Formatting.Indented): only control characters,
	// the quote, the backslash and U+0085/U+2028/U+2029 are escaped.
	newtonsoftStyle jsonStyle = iota
	// systemTextJSONStyle is JsonSerializer.Serialize with WriteIndented and the default encoder: everything
	// outside Basic Latin and the HTML-sensitive characters are escaped as \uXXXX.
	systemTextJSONStyle
)

// indentedJSON writes a value as both .NET writers do with indentation: two spaces, "name": value, empty
// arrays and objects as [] and {}, and the host newline.
func indentedJSON(value jsonValue, style jsonStyle) string {
	var builder strings.Builder
	writeJSON(&builder, value, style, 0)
	return builder.String()
}

func writeJSON(builder *strings.Builder, value jsonValue, style jsonStyle, depth int) {
	indent := func(level int) { builder.WriteString(newLine() + strings.Repeat("  ", level)) }
	switch typed := value.(type) {
	case nil:
		builder.WriteString("null")
	case bool:
		if typed {
			builder.WriteString("true")
		} else {
			builder.WriteString("false")
		}
	case int:
		fmt.Fprintf(builder, "%d", typed)
	case jsonNumber:
		builder.WriteString(string(typed))
	case string:
		builder.WriteString(jsonString(typed, style))
	case jsonArray:
		if len(typed) == 0 {
			builder.WriteString("[]")
			return
		}
		builder.WriteString("[")
		for index, item := range typed {
			if index > 0 {
				builder.WriteString(",")
			}
			indent(depth + 1)
			writeJSON(builder, item, style, depth+1)
		}
		indent(depth)
		builder.WriteString("]")
	case *jsonObject:
		if len(typed.members) == 0 {
			builder.WriteString("{}")
			return
		}
		builder.WriteString("{")
		for index, item := range typed.members {
			if index > 0 {
				builder.WriteString(",")
			}
			indent(depth + 1)
			builder.WriteString(jsonString(item.name, style))
			builder.WriteString(": ")
			writeJSON(builder, item.value, style, depth+1)
		}
		indent(depth)
		builder.WriteString("}")
	default:
		panic(fmt.Sprintf("indentedJSON: unsupported %T", value))
	}
}

func jsonString(text string, style jsonStyle) string {
	var builder strings.Builder
	builder.WriteByte('"')
	for _, r := range text {
		switch r {
		case '"':
			if style == systemTextJSONStyle {
				builder.WriteString(`"`)
			} else {
				builder.WriteString(`\"`)
			}
			continue
		case '\\':
			builder.WriteString(`\\`)
			continue
		case '\n':
			builder.WriteString(`\n`)
			continue
		case '\r':
			builder.WriteString(`\r`)
			continue
		case '\t':
			builder.WriteString(`\t`)
			continue
		case '\b':
			builder.WriteString(`\b`)
			continue
		case '\f':
			builder.WriteString(`\f`)
			continue
		}
		escape := r < 0x20
		if style == newtonsoftStyle {
			escape = escape || r == 0x85 || r == 0x2028 || r == 0x2029
		} else {
			escape = escape || r > 0x7E || strings.ContainsRune("<>&'+`", r)
		}
		if !escape {
			builder.WriteRune(r)
			continue
		}
		if r > 0xFFFF {
			high, low := utf16Pair(r)
			fmt.Fprintf(&builder, `\u%04X\u%04X`, high, low)
			continue
		}
		if style == newtonsoftStyle {
			fmt.Fprintf(&builder, `\u%04x`, r)
		} else {
			fmt.Fprintf(&builder, `\u%04X`, r)
		}
	}
	builder.WriteByte('"')
	return builder.String()
}

func utf16Pair(r rune) (rune, rune) {
	r -= 0x10000
	return 0xD800 + (r>>10)&0x3FF, 0xDC00 + r&0x3FF
}

// ---------------------------------------------------------------------------------------------- ordering

// cultureLess approximates String.Compare under the current culture (ICU root collation), which LINQ's
// OrderBy uses for strings: punctuation sorts before digits and digits before letters, letters compare
// without case first, and on a tie a lower-case letter sorts before its upper-case twin.
func cultureLess(left, right string) bool {
	leftKeys, rightKeys := primaryKeys(left), primaryKeys(right)
	for index := 0; index < len(leftKeys) && index < len(rightKeys); index++ {
		if leftKeys[index] != rightKeys[index] {
			return leftKeys[index] < rightKeys[index]
		}
	}
	if len(leftKeys) != len(rightKeys) {
		return len(leftKeys) < len(rightKeys)
	}
	leftRunes, rightRunes := []rune(left), []rune(right)
	for index := 0; index < len(leftRunes) && index < len(rightRunes); index++ {
		if leftRunes[index] != rightRunes[index] {
			return unicode.IsLower(leftRunes[index])
		}
	}
	return false
}

// icuPunctuation is the ICU root order of the ASCII punctuation and symbols.
const icuPunctuation = "_-,;:!?.'\"()[]{}@*/\\&#%`^+<=>|~$"

func primaryKeys(text string) []int {
	keys := make([]int, 0, utf8.RuneCountInString(text))
	for _, r := range text {
		switch {
		case r == ' ':
			keys = append(keys, 0)
		case strings.ContainsRune(icuPunctuation, r):
			keys = append(keys, 1+strings.IndexRune(icuPunctuation, r))
		case r >= '0' && r <= '9':
			keys = append(keys, 100+int(r-'0'))
		case unicode.IsLetter(r):
			keys = append(keys, 200+int(unicode.ToLower(r)))
		default:
			keys = append(keys, 0x200000+int(r))
		}
	}
	return keys
}

func sortCulture(values []string) {
	sort.SliceStable(values, func(i, j int) bool { return cultureLess(values[i], values[j]) })
}

// ---------------------------------------------------------------------------------------------- paths

// errorf builds an error whose text is clio's message as is.
func errorf(format string, args ...any) error { return fmt.Errorf(format, args...) }

// isPathRooted is Path.IsPathRooted: a leading separator, or on Windows also a drive letter.
func isPathRooted(path string) bool {
	if path == "" {
		return false
	}
	if path[0] == '/' {
		return true
	}
	if runtime.GOOS != "windows" {
		return false
	}
	return path[0] == '\\' || (len(path) >= 2 && path[1] == ':' && unicode.IsLetter(rune(path[0])))
}

// IsFullyQualified is Path.IsPathFullyQualified: rooted and, on Windows, not drive- or root-relative.
func IsFullyQualified(path string) bool {
	if runtime.GOOS != "windows" {
		return strings.HasPrefix(path, "/")
	}
	if strings.HasPrefix(path, `\\`) || strings.HasPrefix(path, "//") {
		return true
	}
	return len(path) >= 3 && path[1] == ':' && (path[2] == '\\' || path[2] == '/') && unicode.IsLetter(rune(path[0]))
}

// combinePath is Path.Combine of two parts: the second when it is rooted.
func combinePath(first, second string) string {
	if isPathRooted(second) || first == "" {
		return second
	}
	if strings.HasSuffix(first, string(filepath.Separator)) || strings.HasSuffix(first, "/") {
		return first + second
	}
	return first + string(filepath.Separator) + second
}

// fullPath is Path.GetFullPath: relative to the working directory, "." and ".." resolved, separators
// normalized, a trailing separator kept.
func fullPath(path string) string {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	if strings.HasSuffix(path, "/") || strings.HasSuffix(path, string(filepath.Separator)) {
		if !strings.HasSuffix(absolute, string(filepath.Separator)) {
			absolute += string(filepath.Separator)
		}
	}
	return absolute
}

// mkdirAll is Directory.CreateDirectory.
func mkdirAll(path string) error { return os.MkdirAll(path, 0o777) }

// jsonNumber is a number kept as the text it was parsed from, as JsonNode keeps it.
type jsonNumber string

func (o *jsonObject) get(name string) jsonValue {
	for _, item := range o.members {
		if item.name == name {
			return item.value
		}
	}
	return nil
}

// set replaces a member's value in place or appends the member, as JsonObject's indexer does.
func (o *jsonObject) set(name string, value jsonValue) {
	for index, item := range o.members {
		if item.name == name {
			o.members[index].value = value
			return
		}
	}
	o.members = append(o.members, member(name, value))
}

// parseJSONNode is JsonNode.Parse into the ordered tree the writers take.
func parseJSONNode(data []byte) (jsonValue, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	value, err := parseJSONValue(decoder)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, errorf("'%s' is invalid after a single JSON value.", "extra")
	}
	return value, nil
}

func parseJSONValue(decoder *json.Decoder) (jsonValue, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	switch typed := token.(type) {
	case json.Delim:
		if typed == '[' {
			array := jsonArray{}
			for decoder.More() {
				item, err := parseJSONValue(decoder)
				if err != nil {
					return nil, err
				}
				array = append(array, item)
			}
			_, err := decoder.Token()
			return array, err
		}
		result := object()
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			item, err := parseJSONValue(decoder)
			if err != nil {
				return nil, err
			}
			result.members = append(result.members, member(key.(string), item))
		}
		_, err := decoder.Token()
		return result, err
	case json.Number:
		return jsonNumber(typed), nil
	default:
		return typed, nil
	}
}
