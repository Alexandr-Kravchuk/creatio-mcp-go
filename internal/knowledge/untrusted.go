package knowledge

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf16"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/redact"
)

const (
	untrustedPrefix      = "[untrusted-source-text begin] "
	untrustedSuffix      = " [untrusted-source-text end]"
	untrustedLimit       = 300
	untrustedInputLimit  = untrustedLimit * 8
	untrustedFenceMarker = "(fence removed)"
)

var untrustedFenceToken = regexp.MustCompile(`(?i)\[\s*untrusted-source-text[^\]]*\]|untrusted-source-text\s*(?:begin|end)`)

// Untrusted is clio's SensitiveErrorTextRedactor.RedactUntrustedOrNull for text a knowledge repository or
// a remote publisher controls: an outer fence is unwrapped, the text is clamped, redacted, its
// display-hostile runs (controls, separators, format characters, astral characters) collapse to one
// space, fence tokens are defused, the result is clamped to 300 UTF-16 units and fenced as data. Blank
// text yields "".
func Untrusted(text string) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	if strings.HasPrefix(text, untrustedPrefix) && strings.HasSuffix(text, untrustedSuffix) &&
		len(text) >= len(untrustedPrefix)+len(untrustedSuffix) {
		text = text[len(untrustedPrefix) : len(text)-len(untrustedSuffix)]
	}
	text = clampUTF16(text, untrustedInputLimit)
	flattened := flattenDisplayHostile(redact.Text(text))
	if flattened == "" {
		return ""
	}
	flattened = untrustedFenceToken.ReplaceAllString(flattened, untrustedFenceMarker)
	if units := utf16.Encode([]rune(flattened)); len(units) > untrustedLimit {
		flattened = string(utf16.Decode(units[:untrustedLimit])) + "…"
	}
	return untrustedPrefix + flattened + untrustedSuffix
}

func clampUTF16(text string, limit int) string {
	units := utf16.Encode([]rune(text))
	if len(units) <= limit {
		return text
	}
	return string(utf16.Decode(units[:limit]))
}

func displayHostile(r rune) bool {
	return unicode.IsControl(r) || unicode.In(r, unicode.Zs, unicode.Zl, unicode.Zp) || r > 0xFFFF ||
		unicode.Is(unicode.Cf, r)
}

func flattenDisplayHostile(text string) string {
	var builder strings.Builder
	lastSpace := false
	for _, r := range text {
		if displayHostile(r) {
			r = ' '
		}
		if r == ' ' && lastSpace {
			continue
		}
		lastSpace = r == ' '
		builder.WriteRune(r)
	}
	return strings.TrimSpace(builder.String())
}
