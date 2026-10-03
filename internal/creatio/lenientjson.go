package creatio

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode"
)

// lenientJSON parses a page-body section the way clio's get-page does, with JsonhCs 7.8's
// JsonhReader.ParseElement, and returns strict JSON. JSONH is JSON plus: '#', '//' and '/* */' comments;
// single-quoted and triple-quoted strings; quoteless strings that run to the end of the line or the next
// reserved character; optional commas between elements; trailing commas; quoteless property names; a
// braceless root object; and numbers with hex/binary/octal prefixes, underscores, a leading '+' or '.',
// which are read as doubles. Only the first element is read: text after it is ignored, as JSONH ignores
// it. Errors carry clio's wording, `Result was error: "<JSONH message>"`. Behaviour was measured against
// JsonhCs 7.8 itself; the error raised for a few malformed inputs differs from JSONH's, but every input
// JSONH rejects is rejected here too.
func lenientJSON(text string) (json.RawMessage, error) {
	reader := &jsonhReader{runes: []rune(text)}
	var out bytes.Buffer
	if err := reader.root(&out); err != nil {
		return nil, fmt.Errorf("Result was error: %q", err.Error())
	}
	return json.RawMessage(out.Bytes()), nil
}

type jsonhReader struct {
	runes []rune
	index int
}

type jsonhError string

func (e jsonhError) Error() string { return string(e) }

const jsonhEndOfInput = jsonhError("Expected token, got end of input")

// jsonhReserved ends a quoteless string. '/' and '#' end it because they may start a comment.
const jsonhReserved = "\\,:[]{}/#\"'"

func (r *jsonhReader) peek() (rune, bool) {
	if r.index >= len(r.runes) {
		return 0, false
	}
	return r.runes[r.index], true
}

// skip passes whitespace and comments. A '/' that does not start a comment is an error.
func (r *jsonhReader) skip() error {
	for r.index < len(r.runes) {
		ch := r.runes[r.index]
		switch {
		case unicode.IsSpace(ch):
			r.index++
		case ch == '#':
			r.skipLine()
		case ch == '/' && r.index+1 < len(r.runes) && r.runes[r.index+1] == '/':
			r.skipLine()
		case ch == '/' && r.index+1 < len(r.runes) && r.runes[r.index+1] == '*':
			end := -1
			for scan := r.index + 2; scan+1 < len(r.runes); scan++ {
				if r.runes[scan] == '*' && r.runes[scan+1] == '/' {
					end = scan + 2
					break
				}
			}
			if end < 0 {
				return jsonhError("Expected end of block comment, got end of input")
			}
			r.index = end
		case ch == '/':
			return jsonhError("Unexpected `/`")
		default:
			return nil
		}
	}
	return nil
}

func (r *jsonhReader) skipLine() {
	for r.index < len(r.runes) && r.runes[r.index] != '\n' && r.runes[r.index] != '\r' {
		r.index++
	}
}

// root reads the first element; a string followed by ':' opens a braceless root object.
func (r *jsonhReader) root(out *bytes.Buffer) error {
	if err := r.skip(); err != nil {
		return err
	}
	if _, ok := r.peek(); !ok {
		return jsonhEndOfInput
	}
	start := r.index
	if ch, _ := r.peek(); ch != '{' && ch != '[' {
		if _, err := r.propertyName(); err == nil {
			if err := r.skip(); err == nil {
				if next, ok := r.peek(); ok && next == ':' {
					r.index = start
					return r.objectBody(out, false)
				}
			}
		}
		r.index = start
	}
	return r.element(out)
}

func (r *jsonhReader) element(out *bytes.Buffer) error {
	if err := r.skip(); err != nil {
		return err
	}
	ch, ok := r.peek()
	if !ok {
		return jsonhEndOfInput
	}
	switch ch {
	case '{':
		r.index++
		return r.objectBody(out, true)
	case '[':
		r.index++
		return r.array(out)
	case '"', '\'':
		value, err := r.quoted()
		if err != nil {
			return err
		}
		jsonhWriteString(out, value)
		return nil
	}
	value, err := r.quoteless()
	if err != nil {
		return err
	}
	jsonhWriteQuoteless(out, value)
	return nil
}

// objectBody reads properties up to '}' (braced) or the end of input (braceless root).
func (r *jsonhReader) objectBody(out *bytes.Buffer, braced bool) error {
	out.WriteByte('{')
	first := true
	for {
		if err := r.skip(); err != nil {
			return err
		}
		ch, ok := r.peek()
		if !ok {
			if braced {
				return jsonhError("Expected `}` to end object, got end of input")
			}
			break
		}
		if braced && ch == '}' {
			r.index++
			break
		}
		name, err := r.propertyName()
		if err != nil {
			return err
		}
		if err := r.skip(); err != nil {
			return err
		}
		if next, ok := r.peek(); !ok || next != ':' {
			return jsonhError("Expected `:` after property name in object")
		}
		r.index++
		if !first {
			out.WriteByte(',')
		}
		first = false
		jsonhWriteString(out, name)
		out.WriteByte(':')
		if err := r.element(out); err != nil {
			return err
		}
		if err := r.optionalComma(); err != nil {
			return err
		}
	}
	out.WriteByte('}')
	return nil
}

func (r *jsonhReader) array(out *bytes.Buffer) error {
	out.WriteByte('[')
	first := true
	for {
		if err := r.skip(); err != nil {
			return err
		}
		ch, ok := r.peek()
		if !ok {
			return jsonhError("Expected `]` to end array, got end of input")
		}
		if ch == ']' {
			r.index++
			break
		}
		if !first {
			out.WriteByte(',')
		}
		first = false
		if err := r.element(out); err != nil {
			return err
		}
		if err := r.optionalComma(); err != nil {
			return err
		}
	}
	out.WriteByte(']')
	return nil
}

func (r *jsonhReader) optionalComma() error {
	if err := r.skip(); err != nil {
		return err
	}
	if ch, ok := r.peek(); ok && ch == ',' {
		r.index++
	}
	return nil
}

func (r *jsonhReader) propertyName() (string, error) {
	if ch, ok := r.peek(); ok && (ch == '"' || ch == '\'') {
		return r.quoted()
	}
	name, err := r.quoteless()
	return name.text, err
}

// quoted reads a single-, double- or triple-quoted string. A triple-quoted string spans lines.
func (r *jsonhReader) quoted() (string, error) {
	quote := r.runes[r.index]
	count := 0
	for r.index < len(r.runes) && r.runes[r.index] == quote && count < 3 {
		r.index++
		count++
	}
	if count == 2 {
		return "", nil
	}
	var value strings.Builder
	for r.index < len(r.runes) {
		ch := r.runes[r.index]
		if ch == quote {
			if count == 1 {
				r.index++
				return value.String(), nil
			}
			if r.index+2 < len(r.runes) && r.runes[r.index+1] == quote && r.runes[r.index+2] == quote {
				r.index += 3
				return value.String(), nil
			}
		}
		if ch == '\\' {
			if err := r.escape(&value); err != nil {
				return "", err
			}
			continue
		}
		value.WriteRune(ch)
		r.index++
	}
	return "", jsonhError("Expected end of string, got end of input")
}

// escape decodes the escape sequence at the backslash and advances past it.
func (r *jsonhReader) escape(value *strings.Builder) error {
	r.index++
	if r.index >= len(r.runes) {
		return jsonhError("Expected escape sequence, got end of input")
	}
	ch := r.runes[r.index]
	r.index++
	hex := func(digits int) error {
		if r.index+digits > len(r.runes) {
			return jsonhError("Expected escape sequence, got end of input")
		}
		code, err := strconv.ParseUint(string(r.runes[r.index:r.index+digits]), 16, 32)
		if err != nil {
			return jsonhError("Incorrect escape sequence")
		}
		r.index += digits
		value.WriteRune(rune(code))
		return nil
	}
	switch ch {
	case 'b':
		value.WriteRune('\b')
	case 'f':
		value.WriteRune('\f')
	case 'n':
		value.WriteRune('\n')
	case 'r':
		value.WriteRune('\r')
	case 't':
		value.WriteRune('\t')
	case 'v':
		value.WriteRune('\v')
	case '0':
		value.WriteRune(0)
	case 'a':
		value.WriteRune('\a')
	case 'e':
		value.WriteRune(0x1B)
	case 'x':
		return hex(2)
	case 'u':
		return hex(4)
	case 'U':
		return hex(8)
	case '\r':
		if r.index < len(r.runes) && r.runes[r.index] == '\n' {
			r.index++
		}
	case '\n', 0x2028, 0x2029:
	default:
		value.WriteRune(ch)
	}
	return nil
}

// jsonhQuoteless is a quoteless value: its text and whether an escape occurred, which keeps it a string.
type jsonhQuoteless struct {
	text    string
	escaped bool
}

func (r *jsonhReader) quoteless() (jsonhQuoteless, error) {
	var value strings.Builder
	escaped := false
	for r.index < len(r.runes) {
		ch := r.runes[r.index]
		if ch == '\\' {
			escaped = true
			if err := r.escape(&value); err != nil {
				return jsonhQuoteless{}, err
			}
			continue
		}
		if ch == '\n' || ch == '\r' || ch == 0x2028 || ch == 0x2029 || strings.ContainsRune(jsonhReserved, ch) {
			break
		}
		value.WriteRune(ch)
		r.index++
	}
	text := strings.TrimRightFunc(strings.TrimLeftFunc(value.String(), unicode.IsSpace), unicode.IsSpace)
	if text == "" {
		return jsonhQuoteless{}, jsonhError("Empty quoteless string")
	}
	return jsonhQuoteless{text: text, escaped: escaped}, nil
}

// jsonhWriteQuoteless writes true/false/null and numbers as themselves, anything else as a string.
func jsonhWriteQuoteless(out *bytes.Buffer, value jsonhQuoteless) {
	if !value.escaped {
		switch value.text {
		case "true", "false", "null":
			out.WriteString(value.text)
			return
		}
		if number, ok := jsonhNumber(value.text); ok {
			if math.IsInf(number, 0) {
				// System.Text.Json writes a non-finite double as its .NET name.
				name := "Infinity"
				if number < 0 {
					name = "-Infinity"
				}
				jsonhWriteString(out, name)
				return
			}
			out.WriteString(jsonhFormatDouble(number))
			return
		}
	}
	jsonhWriteString(out, value.text)
}

// jsonhNumber parses JSONH number syntax: an optional sign, a 0x/0b/0o prefix or decimal digits with an
// optional fraction and exponent, '_' allowed between digits.
func jsonhNumber(text string) (float64, bool) {
	sign := 1.0
	if strings.HasPrefix(text, "-") || strings.HasPrefix(text, "+") {
		if text[0] == '-' {
			sign = -1
		}
		text = text[1:]
	}
	if text == "" {
		return 0, false
	}
	digits := func(part string, base int) (string, bool) {
		if part == "" || part[0] == '_' || part[len(part)-1] == '_' {
			return "", false
		}
		cleaned := strings.ReplaceAll(part, "_", "")
		for _, ch := range cleaned {
			if _, err := strconv.ParseUint(string(ch), base, 8); err != nil {
				return "", false
			}
		}
		return cleaned, true
	}
	lower := strings.ToLower(text)
	for prefix, base := range map[string]int{"0x": 16, "0b": 2, "0o": 8} {
		if strings.HasPrefix(lower, prefix) {
			cleaned, ok := digits(text[2:], base)
			if !ok {
				return 0, false
			}
			value, err := strconv.ParseUint(cleaned, base, 64)
			if err != nil {
				return 0, false
			}
			return sign * float64(value), true
		}
	}
	mantissa, exponent := text, ""
	if cut := strings.IndexAny(text, "eE"); cut >= 0 {
		mantissa, exponent = text[:cut], text[cut+1:]
		if exponent == "" {
			return 0, false
		}
	}
	whole, fraction, hasPoint := strings.Cut(mantissa, ".")
	if whole == "" && (!hasPoint || fraction == "") {
		return 0, false
	}
	number := ""
	if whole != "" {
		cleaned, ok := digits(whole, 10)
		if !ok {
			return 0, false
		}
		number = cleaned
	} else {
		number = "0"
	}
	if hasPoint && fraction != "" {
		cleaned, ok := digits(fraction, 10)
		if !ok {
			return 0, false
		}
		number += "." + cleaned
	}
	if exponent != "" {
		expSign := ""
		if exponent[0] == '-' || exponent[0] == '+' {
			expSign, exponent = exponent[:1], exponent[1:]
		}
		cleaned, ok := digits(exponent, 10)
		if !ok {
			return 0, false
		}
		number += "e" + expSign + cleaned
	}
	value, err := strconv.ParseFloat(number, 64)
	if err != nil && !strings.Contains(err.Error(), "out of range") {
		return 0, false
	}
	return sign * value, true
}

// jsonhFormatDouble is System.Text.Json's double text: .NET's shortest round-trip form, scientific below
// 1E-05 and from 1E+15, without a forced fraction.
func jsonhFormatDouble(value float64) string {
	if value == 0 && math.Signbit(value) {
		return "-0"
	}
	return strings.TrimSuffix(dotnetDouble(value), ".0")
}

func jsonhWriteString(out *bytes.Buffer, value string) {
	encoded, _ := json.Marshal(value)
	out.Write(encoded)
}
