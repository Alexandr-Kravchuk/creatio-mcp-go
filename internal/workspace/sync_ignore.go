package workspace

// The package filters push-workspace and restore-workspace apply: clio's PackageIgnoreMatcher (the workspace's
// IgnorePackages masks) and the "Ignore" 0.2.1 library behind PackageArchiver.ApplyClioIgnore (the
// .clio/clioignore rules). Ignore turns each gitignore-style line into a .NET regular expression through a fixed
// chain of rewrites, some with look-behinds, so the chain runs on regexp2 exactly as the library writes it.

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/dlclark/regexp2"
)

// packageIgnored is PackageIgnoreMatcher.IsIgnored: a mask is a whole-name, case-insensitive pattern where '*'
// is any text and '?' one character.
func packageIgnored(packageName string, masks []string) bool {
	for _, mask := range masks {
		pattern := regexp.QuoteMeta(mask)
		pattern = strings.ReplaceAll(pattern, `\*`, ".*")
		pattern = strings.ReplaceAll(pattern, `\?`, ".")
		if compiled, err := regexp.Compile("(?is)^" + pattern + "$"); err == nil && compiled.MatchString(packageName) {
			return true
		}
	}
	return false
}

// filterPackages is WorkspacePackageFilter.FilterPackages with the workspace's IgnorePackages: the ignored names
// are logged once, in one line.
func filterPackages(log *Log, packages []string, ignore []string) []string {
	if len(ignore) == 0 {
		return packages
	}
	var kept, ignored []string
	for _, name := range packages {
		if packageIgnored(name, ignore) {
			ignored = append(ignored, name)
		} else {
			kept = append(kept, name)
		}
	}
	if len(ignored) > 0 {
		log.Info("Ignored " + strconv.Itoa(len(ignored)) + " package(s): " + strings.Join(ignored, ", "))
	}
	return kept
}

// withoutExternal drops the workspace's ExternalPackages (Workspace.GetFilteredPackages, WorkspaceRestorer).
func withoutExternal(packages, external []string) []string {
	if len(external) == 0 {
		return packages
	}
	var kept []string
	for _, name := range packages {
		found := false
		for _, other := range external {
			if other == name {
				found = true
				break
			}
		}
		if !found {
			kept = append(kept, name)
		}
	}
	return kept
}

// ignoreReplacer is one step of IgnoreRule's rewrite chain.
type ignoreReplacer struct {
	pattern *regexp2.Regexp
	replace func(regexp2.Match) string
}

func newIgnoreReplacer(pattern string, replace func(regexp2.Match) string) ignoreReplacer {
	return ignoreReplacer{pattern: regexp2.MustCompile(pattern, regexp2.None), replace: replace}
}

func ignoreLiteral(text string) func(regexp2.Match) string {
	return func(regexp2.Match) string { return text }
}

func ignoreGroup(prefix, suffix string) func(regexp2.Match) string {
	return func(match regexp2.Match) string { return prefix + match.GroupByNumber(1).String() + suffix }
}

// ignoreReplacers is IgnoreRule.Replacers in the library's order.
var ignoreReplacers = []ignoreReplacer{
	newIgnoreReplacer(`\\?\s+$`, func(match regexp2.Match) string { // TrailingSpaces
		if strings.HasPrefix(match.String(), `\`) {
			return " "
		}
		return ""
	}),
	newIgnoreReplacer(`\\\s`, ignoreLiteral(" ")),                      // EscapedSpaces
	newIgnoreReplacer(`\.`, ignoreLiteral(`\.`)),                       // LiteralDot
	newIgnoreReplacer(`\+`, ignoreLiteral(`\+`)),                       // LiteralPlus
	newIgnoreReplacer(`(?!\\)\?`, ignoreLiteral(`[^/]`)),               // QuestionMark
	newIgnoreReplacer(`(^[^/]*$)`, ignoreGroup(`(^|/)`, "")),           // NoSlash
	newIgnoreReplacer(`(?<!^)(?<!\*)\*(?!\*)`, ignoreLiteral(`[^/]*`)), // NonLeadingSingleStar
	newIgnoreReplacer(`^(?<!\*)\*(?!\*)`, ignoreLiteral(`.*`)),         // LeadingSingleStar
	newIgnoreReplacer(`^(\*\*/|\*\*)`, ignoreLiteral(`.*`)),            // LeadingDoubleStar
	newIgnoreReplacer(`(?<=/)\*\*/`, ignoreLiteral(`(.*/)?`)),          // MiddleDoubleStar
	newIgnoreReplacer(`^\/`, ignoreLiteral(`^`)),                       // LeadingSlash
	newIgnoreReplacer(`\*\*$`, ignoreLiteral(`.*$`)),                   // TrailingDoubleStar
	newIgnoreReplacer(`\*\*`, ignoreLiteral(`[^/]*`)),                  // OtherDoubleStar
	newIgnoreReplacer(`^([^/\^]+/[^/]+)`, ignoreGroup(`^`, "")),        // MiddleSlash
	newIgnoreReplacer(`^([^/]+)/$`, ignoreGroup(`(/|^)`, "/")),         // TrailingSlash
	newIgnoreReplacer(`([^/$]+)$`, ignoreGroup("", `(/.*)?$`)),         // NoTrailingSlash
	newIgnoreReplacer(`([^/$]+)$`, ignoreGroup("", `$`)),               // Ending
}

// ignoreRule is IgnoreRule: a blank or comment line has no expression and matches nothing.
type ignoreRule struct {
	negate     bool
	expression *regexp2.Regexp
}

func newIgnoreRule(pattern string) (ignoreRule, error) {
	if strings.TrimSpace(pattern) == "" || strings.HasPrefix(pattern, "#") {
		return ignoreRule{}, nil
	}
	rule := ignoreRule{}
	if strings.HasPrefix(pattern, `\!`) || strings.HasPrefix(pattern, `\#`) {
		pattern = pattern[1:]
	} else if strings.HasPrefix(pattern, "!") {
		rule.negate = true
		pattern = pattern[1:]
	}
	for _, replacer := range ignoreReplacers {
		replaced, err := replacer.pattern.ReplaceFunc(pattern, replacer.replace, -1, -1)
		if err != nil {
			return ignoreRule{}, err
		}
		pattern = replaced
	}
	expression, err := regexp2.Compile(pattern, regexp2.IgnoreCase)
	if err != nil {
		return ignoreRule{}, err
	}
	rule.expression = expression
	return rule, nil
}

func (r ignoreRule) matches(input string) bool {
	if r.expression == nil {
		return false
	}
	matched, err := r.expression.MatchString(input)
	return err == nil && matched
}

// ignoreSet is Ignore.Ignore: rules accumulate, a later negation un-ignores a path an earlier rule ignored.
type ignoreSet struct{ rules []ignoreRule }

func (s *ignoreSet) add(lines []string) error {
	for _, line := range lines {
		rule, err := newIgnoreRule(line)
		if err != nil {
			return err
		}
		s.rules = append(s.rules, rule)
	}
	return nil
}

func (s *ignoreSet) ignored(path string) bool {
	ignored := false
	for _, rule := range s.rules {
		if rule.negate {
			if ignored && rule.matches(path) {
				ignored = false
			}
		} else if !ignored && rule.matches(path) {
			ignored = true
		}
	}
	return ignored
}

// readAllLines is File.ReadAllLines: the text split at CR LF, LF or CR, without a final empty line.
func readAllLines(path string) ([]string, error) {
	text, err := readAllText(path)
	if err != nil {
		return nil, err
	}
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	if text == "" {
		return nil, nil
	}
	lines := strings.Split(text, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines, nil
}
