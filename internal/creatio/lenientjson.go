package creatio

import (
	"encoding/json"
	"errors"
	"strings"
)

// lenientJSON parses a page-body section the way clio's JSONH reader accepts it: strict JSON plus comments,
// single-quoted strings, unquoted keys, quoteless words and trailing commas. It returns strict JSON.
func lenientJSON(text string) (json.RawMessage, error) {
	if json.Valid([]byte(text)) {
		return json.RawMessage(text), nil
	}
	normalized, err := normalizeLenientJSON(text)
	if err != nil {
		return nil, err
	}
	var probe any
	if err := json.Unmarshal([]byte(normalized), &probe); err != nil {
		return nil, err
	}
	return json.RawMessage(normalized), nil
}

func normalizeLenientJSON(text string) (string, error) {
	runes := []rune(text)
	var out strings.Builder
	for index := 0; index < len(runes); {
		ch := runes[index]
		switch {
		case ch == '/' && index+1 < len(runes) && runes[index+1] == '/':
			for index < len(runes) && runes[index] != '\n' {
				index++
			}
		case ch == '/' && index+1 < len(runes) && runes[index+1] == '*':
			end := strings.Index(string(runes[index+2:]), "*/")
			if end < 0 {
				return "", errors.New("unterminated block comment")
			}
			index += 2 + len([]rune(string(runes[index+2:])[:end])) + 2
		case ch == '#':
			for index < len(runes) && runes[index] != '\n' {
				index++
			}
		case ch == '"' || ch == '\'':
			value, next, err := readQuoted(runes, index)
			if err != nil {
				return "", err
			}
			encoded, _ := json.Marshal(value)
			out.Write(encoded)
			index = next
		case ch == ',':
			next := skipLenientSpace(runes, index+1)
			if next < len(runes) && (runes[next] == ']' || runes[next] == '}') {
				index++
				continue
			}
			out.WriteRune(ch)
			index++
		case strings.ContainsRune("{}[]:", ch) || ch == ' ' || ch == '\t' || ch == '\r' || ch == '\n':
			out.WriteRune(ch)
			index++
		default:
			start := index
			for index < len(runes) && !strings.ContainsRune(",:[]{}\r\n", runes[index]) {
				if runes[index] == '/' && index+1 < len(runes) && (runes[index+1] == '/' || runes[index+1] == '*') {
					break
				}
				index++
			}
			word := strings.TrimSpace(string(runes[start:index]))
			next := skipLenientSpace(runes, index)
			isKey := next < len(runes) && runes[next] == ':'
			if !isKey && (word == "true" || word == "false" || word == "null" || json.Valid([]byte(word))) {
				out.WriteString(word)
			} else {
				encoded, _ := json.Marshal(word)
				out.Write(encoded)
			}
		}
	}
	return out.String(), nil
}

func readQuoted(runes []rune, start int) (string, int, error) {
	quote := runes[start]
	var value strings.Builder
	for index := start + 1; index < len(runes); index++ {
		ch := runes[index]
		if ch == quote {
			return value.String(), index + 1, nil
		}
		if ch != '\\' {
			value.WriteRune(ch)
			continue
		}
		index++
		if index >= len(runes) {
			break
		}
		switch escaped := runes[index]; escaped {
		case 'n':
			value.WriteRune('\n')
		case 't':
			value.WriteRune('\t')
		case 'r':
			value.WriteRune('\r')
		case 'b':
			value.WriteRune('\b')
		case 'f':
			value.WriteRune('\f')
		case 'u':
			if index+4 < len(runes) {
				var decoded string
				if json.Unmarshal([]byte(`"\u`+string(runes[index+1:index+5])+`"`), &decoded) == nil {
					value.WriteString(decoded)
					index += 4
					continue
				}
			}
			return "", 0, errors.New("invalid unicode escape")
		default:
			value.WriteRune(escaped)
		}
	}
	return "", 0, errors.New("unterminated string")
}

func skipLenientSpace(runes []rune, index int) int {
	for index < len(runes) {
		switch {
		case runes[index] == ' ' || runes[index] == '\t' || runes[index] == '\r' || runes[index] == '\n':
			index++
		case runes[index] == '/' && index+1 < len(runes) && runes[index+1] == '/':
			for index < len(runes) && runes[index] != '\n' {
				index++
			}
		case runes[index] == '/' && index+1 < len(runes) && runes[index+1] == '*':
			end := strings.Index(string(runes[index+2:]), "*/")
			if end < 0 {
				return len(runes)
			}
			index += 2 + len([]rune(string(runes[index+2:])[:end])) + 2
		default:
			return index
		}
	}
	return index
}
