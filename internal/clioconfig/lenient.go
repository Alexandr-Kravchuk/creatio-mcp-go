package clioconfig

import "strings"

// withoutCommentsAndTrailingCommas rewrites the two relaxations Json.NET's reader accepts in a settings file
// and Go's decoder does not: // and /* */ comments, and a comma before a closing } or ]. Text inside
// strings is kept as it is. ok is false when nothing was relaxed, so the caller keeps its first error.
func withoutCommentsAndTrailingCommas(text string) (string, bool) {
	var out strings.Builder
	changed := false
	inString := false
	for index := 0; index < len(text); index++ {
		ch := text[index]
		if inString {
			out.WriteByte(ch)
			if ch == '\\' && index+1 < len(text) {
				index++
				out.WriteByte(text[index])
			} else if ch == '"' {
				inString = false
			}
			continue
		}
		switch {
		case ch == '"':
			inString = true
			out.WriteByte(ch)
		case ch == '/' && index+1 < len(text) && text[index+1] == '/':
			changed = true
			for index < len(text) && text[index] != '\n' {
				index++
			}
			out.WriteByte('\n')
		case ch == '/' && index+1 < len(text) && text[index+1] == '*':
			changed = true
			end := strings.Index(text[index+2:], "*/")
			if end < 0 {
				return text, false
			}
			index += end + 3
			out.WriteByte(' ')
		case ch == ',':
			next := index + 1
			for next < len(text) && strings.IndexByte(" \t\r\n", text[next]) >= 0 {
				next++
			}
			if next < len(text) && (text[next] == '}' || text[next] == ']') {
				changed = true
				continue
			}
			out.WriteByte(ch)
		default:
			out.WriteByte(ch)
		}
	}
	return out.String(), changed
}
