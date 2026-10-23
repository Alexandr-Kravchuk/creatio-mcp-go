package creatio

// System.Text.Json's JsonSerializer.Deserialize<CreatioResponse>(body) as GenerateSourceCodeCommand reads a
// WorkspaceExplorerService answer: default options (case-sensitive names, no comments, no trailing commas, the
// last of a repeated member wins), members typed as clio declares them, and the exception texts measured
// against clio 8.1.0.142 (Path, LineNumber and BytePositionInLine included).

import (
	"encoding/json"
	"fmt"
	"strings"
)

// stjVerdict is the part of CreatioResponse the command reads.
type stjVerdict struct {
	success            bool
	errorCode, message string
}

// stjFault is a JsonException: the message, then where the reader stood.
type stjFault struct{ text string }

type stjReader struct {
	data      []byte
	pos       int
	line      int
	lineStart int
}

func (r *stjReader) fail(path, format string, args ...any) *stjFault {
	return r.failAt(r.pos, path, format, args...)
}

func (r *stjReader) failAt(offset int, path, format string, args ...any) *stjFault {
	line, lineStart := 0, 0
	for index := 0; index < offset && index < len(r.data); index++ {
		if r.data[index] == '\n' {
			line++
			lineStart = index + 1
		}
	}
	return &stjFault{text: fmt.Sprintf(format, args...) + fmt.Sprintf(" Path: %s | LineNumber: %d | BytePositionInLine: %d.",
		path, line, offset-lineStart)}
}

func (r *stjReader) eof() bool { return r.pos >= len(r.data) }

func (r *stjReader) skipSpace() {
	for !r.eof() {
		switch r.data[r.pos] {
		case ' ', '\t', '\r', '\n':
			r.pos++
		default:
			return
		}
	}
}

// printable is how the reader names a byte in a message: the character, or 0xHH outside printable ASCII.
func stjPrintable(b byte) string {
	if b >= 0x20 && b < 0x7F {
		return string(rune(b))
	}
	return fmt.Sprintf("0x%02X", b)
}

const stjDepthMessage = "Expected depth to be zero at the end of the JSON payload. There is an open JSON object or array that should be closed."

// stjCreatioResponse returns the verdict, or the JsonException message (or the NullReferenceException a null
// document leads to) that ProceedResponse logs.
func stjCreatioResponse(data []byte) (stjVerdict, string) {
	r := &stjReader{data: data}
	verdict, isNull, fault := r.root()
	if fault != nil {
		return stjVerdict{}, fault.text
	}
	r.skipSpace()
	if !r.eof() {
		return stjVerdict{}, r.fail("$", "'%s' is invalid after a single JSON value. Expected end of data.", stjPrintable(r.data[r.pos])).text
	}
	if isNull {
		return stjVerdict{}, "Object reference not set to an instance of an object."
	}
	return verdict, ""
}

func (r *stjReader) root() (stjVerdict, bool, *stjFault) {
	r.skipSpace()
	if r.eof() {
		return stjVerdict{}, false, r.fail("$", "The input does not contain any JSON tokens. Expected the input to start with a valid JSON token, while isFinalBlock is true.")
	}
	switch r.data[r.pos] {
	case '{':
		verdict, fault := r.creatioResponse()
		return verdict, false, fault
	case 'n':
		if fault := r.literal("$", "null"); fault != nil {
			return stjVerdict{}, false, fault
		}
		return stjVerdict{}, true, nil
	}
	if fault := r.mismatch("$", "Clio.Command.CreatioResponse"); fault != nil {
		return stjVerdict{}, false, fault
	}
	return stjVerdict{}, false, nil
}

// mismatch reads the token at the reader (a value of the wrong JSON kind for its member) and reports it the
// way the converter does: after the token, or after the opening bracket of a container.
func (r *stjReader) mismatch(path, typeName string) *stjFault {
	if r.eof() {
		return r.fail(path, stjDepthMessage)
	}
	switch b := r.data[r.pos]; {
	case b == '{' || b == '[':
		r.pos++
	case b == '"':
		if _, fault := r.stringToken(path); fault != nil {
			return fault
		}
	case b == '-' || (b >= '0' && b <= '9'):
		if _, fault := r.number(path); fault != nil {
			return fault
		}
	case b == 't':
		if fault := r.literal(path, "true"); fault != nil {
			return fault
		}
	case b == 'f':
		if fault := r.literal(path, "false"); fault != nil {
			return fault
		}
	case b == 'n':
		if fault := r.literal(path, "null"); fault != nil {
			return fault
		}
	default:
		return r.fail(path, "'%s' is an invalid start of a value.", stjPrintable(b))
	}
	return r.fail(path, "The JSON value could not be converted to %s.", typeName)
}

// object walks an object's members, calling member for each value (the reader at its first byte).
func (r *stjReader) object(path string, member func(name, memberPath string) *stjFault) *stjFault {
	r.pos++ // '{'
	afterComma := false
	for {
		r.skipSpace()
		if r.eof() {
			return r.fail("$", stjDepthMessage)
		}
		if r.data[r.pos] == '}' {
			if afterComma {
				return r.fail(path, "The JSON object contains a trailing comma at the end which is not supported in this mode. Change the reader options.")
			}
			r.pos++
			return nil
		}
		if r.data[r.pos] != '"' {
			return r.fail(path, "'%s' is an invalid start of a property name. Expected a '\"'.", stjPrintable(r.data[r.pos]))
		}
		name, fault := r.stringToken(path)
		if fault != nil {
			return fault
		}
		r.skipSpace()
		if r.eof() {
			return r.fail("$", stjDepthMessage)
		}
		if r.data[r.pos] != ':' {
			return r.fail(path, "'%s' is invalid after a property name. Expected a ':'.", stjPrintable(r.data[r.pos]))
		}
		r.pos++
		r.skipSpace()
		if fault := member(name, path+"."+name); fault != nil {
			return fault
		}
		r.skipSpace()
		if r.eof() {
			return r.fail("$", stjDepthMessage)
		}
		switch r.data[r.pos] {
		case ',':
			r.pos++
			afterComma = true
		case '}':
			afterComma = false
		default:
			return r.fail(path, "'%s' is invalid after a value. Expected either ',', '}', or ']'.", stjPrintable(r.data[r.pos]))
		}
	}
}

func (r *stjReader) creatioResponse() (stjVerdict, *stjFault) {
	var verdict stjVerdict
	fault := r.object("$", func(name, path string) *stjFault {
		switch name {
		case "success":
			value, fault := r.boolean(path)
			verdict.success = value
			return fault
		case "buildResult":
			return r.int32(path)
		case "errorInfo":
			code, message, fault := r.errorInfo(path)
			verdict.errorCode, verdict.message = code, message
			return fault
		}
		// errors and message are object (any value); anything else is skipped. Both read the whole value.
		return r.skip(path)
	})
	return verdict, fault
}

func (r *stjReader) errorInfo(path string) (string, string, *stjFault) {
	if r.eof() {
		return "", "", r.fail("$", stjDepthMessage)
	}
	switch r.data[r.pos] {
	case 'n':
		return "", "", r.literal(path, "null")
	case '{':
	default:
		return "", "", r.mismatch(path, "Clio.Command.ErrorInfo")
	}
	var code, message string
	fault := r.object(path, func(name, memberPath string) *stjFault {
		switch name {
		case "errorCode":
			value, fault := r.nullableString(memberPath)
			code = value
			return fault
		case "message":
			value, fault := r.nullableString(memberPath)
			message = value
			return fault
		}
		return r.skip(memberPath)
	})
	return code, message, fault
}

func (r *stjReader) boolean(path string) (bool, *stjFault) {
	if !r.eof() {
		switch r.data[r.pos] {
		case 't':
			return true, r.literal(path, "true")
		case 'f':
			return false, r.literal(path, "false")
		}
	}
	return false, r.mismatch(path, "System.Boolean")
}

func (r *stjReader) int32(path string) *stjFault {
	if r.eof() || (r.data[r.pos] != '-' && (r.data[r.pos] < '0' || r.data[r.pos] > '9')) {
		return r.mismatch(path, "System.Int32")
	}
	text, fault := r.number(path)
	if fault != nil {
		return fault
	}
	var value int64
	if _, err := fmt.Sscan(text, &value); err != nil || strings.ContainsAny(text, ".eE") || value > 2147483647 || value < -2147483648 {
		return r.fail(path, "The JSON value could not be converted to System.Int32.")
	}
	return nil
}

func (r *stjReader) nullableString(path string) (string, *stjFault) {
	if !r.eof() {
		switch r.data[r.pos] {
		case 'n':
			return "", r.literal(path, "null")
		case '"':
			return r.stringToken(path)
		}
	}
	return "", r.mismatch(path, "System.String")
}

// skip reads any value; a fault inside it names the member, not the place inside the value.
func (r *stjReader) skip(path string) *stjFault {
	if r.eof() {
		return r.fail("$", stjDepthMessage)
	}
	switch b := r.data[r.pos]; {
	case b == '{':
		return r.object(path, func(_, _ string) *stjFault { return r.skip(path) })
	case b == '[':
		r.pos++
		afterComma := false
		for {
			r.skipSpace()
			if r.eof() {
				return r.fail("$", stjDepthMessage)
			}
			if r.data[r.pos] == ']' {
				if afterComma {
					return r.fail(path, "The JSON array contains a trailing comma at the end which is not supported in this mode. Change the reader options.")
				}
				r.pos++
				return nil
			}
			if fault := r.skip(path); fault != nil {
				return fault
			}
			r.skipSpace()
			if r.eof() {
				return r.fail("$", stjDepthMessage)
			}
			switch r.data[r.pos] {
			case ',':
				r.pos++
				afterComma = true
			case ']':
				afterComma = false
			default:
				return r.fail(path, "'%s' is invalid after a value. Expected either ',', '}', or ']'.", stjPrintable(r.data[r.pos]))
			}
		}
	case b == '"':
		_, fault := r.stringToken(path)
		return fault
	case b == '-' || (b >= '0' && b <= '9'):
		_, fault := r.number(path)
		return fault
	case b == 't':
		return r.literal(path, "true")
	case b == 'f':
		return r.literal(path, "false")
	case b == 'n':
		return r.literal(path, "null")
	}
	return r.fail(path, "'%s' is an invalid start of a value.", stjPrintable(r.data[r.pos]))
}

// literal reads true, false or null; a mismatch names the rest of the input from the literal on.
func (r *stjReader) literal(path, word string) *stjFault {
	start := r.pos
	for index := 0; index < len(word); index++ {
		if r.pos+index >= len(r.data) {
			r.pos = len(r.data)
			return r.fail(path, "'%s' is an invalid JSON literal. Expected the literal '%s'.", string(r.data[start:]), word)
		}
		if r.data[r.pos+index] != word[index] {
			return r.failAt(r.pos+index, path, "'%s' is an invalid JSON literal. Expected the literal '%s'.", string(r.data[start:]), word)
		}
	}
	r.pos += len(word)
	return nil
}

// number reads a JSON number token and returns its text.
func (r *stjReader) number(path string) (string, *stjFault) {
	start := r.pos
	digits := func() int {
		count := 0
		for !r.eof() && r.data[r.pos] >= '0' && r.data[r.pos] <= '9' {
			r.pos++
			count++
		}
		return count
	}
	if r.data[r.pos] == '-' {
		r.pos++
	}
	if r.eof() {
		return "", r.fail(path, "Expected a digit ('0'-'9') after the minus sign, but instead reached end of data.")
	}
	if r.data[r.pos] == '0' {
		r.pos++
	} else if digits() == 0 {
		return "", r.fail(path, "'%s' is invalid within a number, immediately after a sign character ('+' or '-'). Expected a digit ('0'-'9').", stjPrintable(r.data[r.pos]))
	}
	if !r.eof() && r.data[r.pos] == '.' {
		r.pos++
		if digits() == 0 {
			return "", r.fail(path, "'%s' is invalid within a number, immediately after a decimal point ('.'). Expected a digit ('0'-'9').", r.charAt())
		}
	}
	if !r.eof() && (r.data[r.pos] == 'e' || r.data[r.pos] == 'E') {
		r.pos++
		if !r.eof() && (r.data[r.pos] == '+' || r.data[r.pos] == '-') {
			r.pos++
		}
		if digits() == 0 {
			return "", r.fail(path, "'%s' is invalid within a number, immediately after a sign character ('+' or '-'). Expected a digit ('0'-'9').", r.charAt())
		}
	}
	if !r.eof() {
		switch b := r.data[r.pos]; b {
		case ' ', '\t', '\r', '\n', ',', '}', ']', '/':
		default:
			return "", r.fail(path, "'%s' is invalid at the end of the number. Expected a delimiter.", stjPrintable(b))
		}
	}
	return string(r.data[start:r.pos]), nil
}

func (r *stjReader) charAt() string {
	if r.eof() {
		return ""
	}
	return stjPrintable(r.data[r.pos])
}

// stringToken reads a string token (the reader at its opening quote) and returns its value.
func (r *stjReader) stringToken(path string) (string, *stjFault) {
	start := r.pos
	r.pos++
	for !r.eof() {
		switch b := r.data[r.pos]; {
		case b == '"':
			r.pos++
			var value string
			if err := json.Unmarshal(r.data[start:r.pos], &value); err != nil {
				return "", r.failAt(start, path, "The JSON string could not be read.")
			}
			return value, nil
		case b == '\\':
			r.pos += 2
		case b < 0x20:
			return "", r.fail(path, "'%s' is invalid within a JSON string. The string should be correctly escaped.", stjPrintable(b))
		default:
			r.pos++
		}
	}
	r.pos = len(r.data)
	return "", r.fail(path, "Expected end of string, but instead reached end of data.")
}
