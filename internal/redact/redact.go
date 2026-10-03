// Package redact is a port of clio's SensitiveErrorTextRedactor.Redact (clio master 914dab286,
// clio/Common/SensitiveErrorTextRedactor.cs): it replaces URIs, tokens, e-mail addresses, scheme-less
// host:port endpoints, absolute paths and credential values in failure text with stable placeholders,
// while the human-readable reason survives.
//
// The patterns are clio's .NET regular expressions verbatim. They need lookbehind and backreferences,
// which Go's regexp does not have, so they run on regexp2, a port of the .NET engine. The rule order is
// clio's and is load-bearing (see Text).
package redact

import (
	"time"

	"github.com/dlclark/regexp2"
)

const (
	redactedURI   = "[redacted-uri]"
	redactedPath  = "[redacted-path]"
	redactedValue = "[redacted]"

	// regexTimeout is clio's RegexTimeoutMilliseconds. A rule that runs out of time fails closed: the
	// whole text becomes the placeholder, never the unscanned input.
	regexTimeout = time.Second
)

// credentialSecretKeys and connectionStringPartKeys are clio's two key sets. The JSON property rule takes
// the secret set plus only server/hostname/host/database, because "uid" is how Creatio spells every
// schema identifier property.
const (
	credentialSecretKeys = `password|pwd|pass|secret|(?:access|refresh|id|auth|bearer|api|client|session|[xc]srf)?[_-]?token|` +
		`api[_-]?key|client[_-]?secret|client[_-]?id|private[_-]?key|access[_-]?key|connection ?string|` +
		`authorization|auth|bearer|set-cookie|cookie|asp\.net_sessionid|aspxauth|bpmcsrf|jsessionid|` +
		`phpsessid|session[_-]?id|[xc]srf[_-]?token`
	connectionStringPartKeys = `data ?source|server|hostname|host|initial ?catalog|database|uid|user ?id`
	jsonCredentialKeyPattern = `(?:[A-Za-z0-9]+[_.\-])*(?:` + credentialSecretKeys + `|server|hostname|host|database)(?:[_.\-][A-Za-z0-9]+)*`
	// A JSON quote as it appears in text this rule sees: \u0022 (System.Text.Json), \" or a plain quote.
	jsonQuoteSpellings = `\\u0022|\\"|"`
	jsonStringContent  = `(?:(?!\k<q>)(?:[^"\\]|\\.))*`
	jsonLiteralValue   = `null|true|false|-?\d+(?:\.\d+)?(?:[eE][+\-]?\d+)?`
)

var (
	// scheme://[user[:pass]@]host[:port][/path]. The leading guard refuses a start inside a \uXXXX escape
	// of already-serialized JSON, so the escape stays valid.
	uriPattern = compile(`(?<!\\u?[0-9A-Fa-f]{0,3})(?:(?<=\\u[0-9A-Fa-f]{4})|\b)[a-zA-Z][a-zA-Z0-9+.\-]*://`+
		`(?:(?:[A-Za-z0-9._~!$&'()*+,;=:\-]|%[0-9A-Fa-f]{2}|\\u002[67bB])*@)?[^\s"'<>)\\]+`, 0)
	windowsPathPattern = compile(`(?:[A-Za-z]:\\|\\\\)[^\s"'<>|]*`, 0)
	posixPathPattern   = compile(`/(?:Users|home|root|var|etc|opt|usr|tmp|private|mnt|srv|Library|Applications|System|app|data|config)(?:/[^\s"'<>:]*)+`, 0)
	credentialPair     = compile(`\b(`+credentialSecretKeys+`|`+connectionStringPartKeys+`)\b\s*[=:]\s*(?:"[^"]*"|'[^']*'|[^\s,;"']+)`, regexp2.IgnoreCase)
	jsonCredential     = compile(`(?<q>`+jsonQuoteSpellings+`)(?<key>`+jsonCredentialKeyPattern+`)\k<q>\s*:\s*(?:\k<q>`+jsonStringContent+`\k<q>|`+jsonLiteralValue+`)`, regexp2.IgnoreCase)
	bearerPattern      = compile(`\bBearer\s+[^\s,;"']+`, regexp2.IgnoreCase)
	jwtPattern         = compile(`\beyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`, 0)
	hostPortPattern    = compile(`(?<!\\u?[0-9A-Fa-f]{0,3})(?:(?<=\\u[0-9A-Fa-f]{4})|(?<![\w:./@-]))`+
		`(?:\[[0-9A-Fa-f:]+\]|(?:[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?\.)+[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?|\d{1,3}(?:\.\d{1,3}){3}):\d{1,5}\b`, 0)
	emailPattern = compile(`(?<!\\u?[0-9A-Fa-f]{0,3})[A-Za-z0-9._%+\-]+@(?:\[[^\]\s]{1,45}\]|`+
		`[A-Za-z0-9](?:[A-Za-z0-9\-]*[A-Za-z0-9])?(?:\.[A-Za-z0-9](?:[A-Za-z0-9\-]*[A-Za-z0-9])?)*\.[A-Za-z]{2,}|`+
		`\d{1,3}(?:\.\d{1,3}){3}(?=:\d)|[A-Za-z][A-Za-z0-9\-]*[A-Za-z0-9](?![A-Za-z0-9\-]))`, 0)
)

func compile(pattern string, options regexp2.RegexOptions) *regexp2.Regexp {
	re := regexp2.MustCompile(pattern, options)
	re.MatchTimeout = regexTimeout
	return re
}

// Text returns text with credential-keyed JSON properties, URIs, JWT and Bearer tokens, e-mail addresses,
// host:port endpoints, Windows and POSIX absolute paths and key=value secrets replaced, in clio's order:
// the JSON rule first, because the later rules would eat the closing escape of an escaped value; URIs
// before tokens; e-mail before host:port. Empty input returns "".
func Text(text string) string {
	if text == "" {
		return ""
	}
	result, err := jsonCredential.ReplaceFunc(text, redactJSONProperty, -1, -1)
	steps := []struct {
		pattern     *regexp2.Regexp
		replacement string
	}{
		{uriPattern, redactedURI}, {jwtPattern, redactedValue}, {bearerPattern, redactedValue},
		{emailPattern, redactedValue}, {hostPortPattern, redactedURI},
		{windowsPathPattern, redactedPath}, {posixPathPattern, redactedPath},
	}
	for _, step := range steps {
		if err != nil {
			return redactedValue
		}
		result, err = step.pattern.ReplaceFunc(result, constant(step.replacement), -1, -1)
	}
	if err != nil {
		return redactedValue
	}
	result, err = credentialPair.ReplaceFunc(result, func(match regexp2.Match) string {
		return match.GroupByNumber(1).String() + "=" + redactedValue
	}, -1, -1)
	if err != nil {
		return redactedValue
	}
	return result
}

// All redacts every entry, in order.
func All(texts []string) []string {
	redacted := make([]string, len(texts))
	for i, text := range texts {
		redacted[i] = Text(text)
	}
	return redacted
}

// redactJSONProperty rewrites "key":"value" as "key":"[redacted]" in the quote spelling of the match.
func redactJSONProperty(match regexp2.Match) string {
	quote := match.GroupByName("q").String()
	return quote + match.GroupByName("key").String() + quote + ":" + quote + redactedValue + quote
}

// constant is a replacement that does not interpret "$" the way a .NET replacement pattern would; the
// placeholders contain none, so this is equivalent to clio's string replacement.
func constant(value string) regexp2.MatchEvaluator {
	return func(regexp2.Match) string { return value }
}
