package creatio

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"
)

// CommandResultText is the text clio's tool answer carries for a CommandExecutionResult (minus its
// correlation-id): System.Text.Json with UnsafeRelaxedJsonEscaping, which leaves HTML characters and the
// apostrophe as they are and escapes quotes, backslashes, control and format characters, separators other
// than the space, unassigned and private-use code points and everything outside the BMP (measured against
// clio 8.1.0.142: "<url>", "'timeout'", " ").
func CommandResultText(result CommandResult) string {
	var builder strings.Builder
	builder.WriteString(`{"exit-code":`)
	builder.WriteString(strconv.Itoa(result.ExitCode))
	builder.WriteString(`,"execution-log-messages":[`)
	for index, message := range result.Messages {
		if index > 0 {
			builder.WriteByte(',')
		}
		builder.WriteString(`{"message-type":`)
		writeRelaxedString(&builder, message.MessageType)
		builder.WriteString(`,"value":`)
		writeRelaxedString(&builder, message.Value)
		builder.WriteByte('}')
	}
	builder.WriteString("]}")
	return builder.String()
}

func writeRelaxedString(builder *strings.Builder, value string) {
	builder.WriteByte('"')
	for _, r := range value {
		switch r {
		case '"':
			builder.WriteString(`\"`)
		case '\\':
			builder.WriteString(`\\`)
		case '\n':
			builder.WriteString(`\n`)
		case '\r':
			builder.WriteString(`\r`)
		case '\t':
			builder.WriteString(`\t`)
		case '\b':
			builder.WriteString(`\b`)
		case '\f':
			builder.WriteString(`\f`)
		default:
			if relaxedAllowed(r) {
				builder.WriteRune(r)
				continue
			}
			for _, unit := range utf16.Encode([]rune{r}) {
				fmt.Fprintf(builder, `\u%04X`, unit)
			}
		}
	}
	builder.WriteByte('"')
}

func relaxedAllowed(r rune) bool {
	switch {
	case r == ' ':
		return true
	case r < 0x20 || r >= 0x10000 || r == unicode.ReplacementChar:
		return false
	case unicode.In(r, unicode.Cc, unicode.Cf, unicode.Co, unicode.Cs, unicode.Zs, unicode.Zl, unicode.Zp):
		return false
	}
	return unicode.In(r, unicode.L, unicode.M, unicode.N, unicode.P, unicode.S)
}
