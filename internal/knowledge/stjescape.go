package knowledge

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf16"
)

// dotnetTimestamp is the form System.Text.Json writes a DateTimeOffset in; its converter writes the value
// without the encoder's escaping, while an ordinary string goes through it.
var dotnetTimestamp = regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d{1,7})?(?:Z|[+-]\d\d:\d\d)$`)

// IsDotnetTimestamp reports whether value is written the way System.Text.Json writes a DateTimeOffset.
func IsDotnetTimestamp(value string) bool { return dotnetTimestamp.MatchString(value) }

// STJEscape rewrites the string literals of Go-encoded JSON (made with SetEscapeHTML(false)) the way
// System.Text.Json's default JavaScriptEncoder writes them: \b \f \n \r \t \\ as short escapes, and
// \uXXXX (upper-case) for other controls, non-ASCII and " & ' + < > `. A literal keepRaw accepts is
// written unescaped, as System.Text.Json's DateTimeOffset converter writes its values.
func STJEscape(data []byte, keepRaw func(string) bool) []byte {
	var out bytes.Buffer
	for i := 0; i < len(data); i++ {
		if data[i] != '"' {
			out.WriteByte(data[i])
			continue
		}
		end := i + 1
		for end < len(data) && data[end] != '"' {
			if data[end] == '\\' {
				end++
			}
			end++
		}
		literal := data[i : end+1]
		var value string
		if err := json.Unmarshal(literal, &value); err != nil {
			out.Write(literal)
		} else if keepRaw != nil && keepRaw(value) {
			out.WriteByte('"')
			out.WriteString(value)
			out.WriteByte('"')
		} else {
			out.WriteString(stjString(value))
		}
		i = end
	}
	return out.Bytes()
}

func stjString(value string) string {
	var builder strings.Builder
	builder.WriteByte('"')
	for _, r := range value {
		switch r {
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
		case '\\':
			builder.WriteString(`\\`)
			continue
		}
		if r < 0x20 || r > 0x7E || strings.ContainsRune("\"&'+<>`", r) {
			for _, unit := range utf16.Encode([]rune{r}) {
				fmt.Fprintf(&builder, `\u%04X`, unit)
			}
			continue
		}
		builder.WriteRune(r)
	}
	builder.WriteByte('"')
	return builder.String()
}
