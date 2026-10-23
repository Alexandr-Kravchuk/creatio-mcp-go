package workspace

// The page-text warning of push-workspace: clio's WorkspacePageTextInspector over
// SchemaValidationService.FindLocalizableTextViolations (web pages: the viewConfigDiff between the
// SCHEMA_VIEW_CONFIG_DIFF markers, parsed as System.Text.Json does with comments and trailing commas allowed)
// and FindMobileLocalizableTextViolations (mobile pages: plain JSON with a viewConfigDiff member). Both scans
// report, per Freedom UI page schema, the user-visible text properties set as inline literals and the
// literal-only properties bound to localizable resources. A body that does not parse is skipped, as clio does.

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

// pageTextFinding is PageTextFinding.
type pageTextFinding struct {
	packageName, schemaName string
	inline, literalOnly     []string
}

const (
	textViolationInline = iota
	textViolationLiteralOnly
)

type textViolation struct {
	node, property string
	kind           int
}

var (
	viewConfigDiffSection = regexp.MustCompile(`/\*\*SCHEMA_VIEW_CONFIG_DIFF\*/([\s\S]*?)/\*\*SCHEMA_VIEW_CONFIG_DIFF\*/`)
	schemaDiffSection     = regexp.MustCompile(`/\*\*SCHEMA_DIFF\*/([\s\S]*?)/\*\*SCHEMA_DIFF\*/`)
	resourceStringPattern = regexp.MustCompile(`#ResourceString\(([^)]+)\)#`)
	localizableTextNames  = []string{"label", "caption", "title", "tooltip", "placeholder"}
)

// inspectPageText is WorkspacePageTextInspector.Inspect over packages/<name>/Schemas/<schema>/*.js.
func inspectPageText(log *Log, packagesFolder string, packages []string) []pageTextFinding {
	var findings []pageTextFinding
	for _, packageName := range packages {
		schemas := combinePath(combinePath(packagesFolder, packageName), "Schemas")
		if !directoryExists(schemas) {
			continue
		}
		_, folders, err := directoryFiles(schemas)
		if err != nil {
			continue
		}
		sort.Strings(folders)
		for _, schema := range folders {
			violations := inspectSchemaFolder(log, schema)
			if len(violations) == 0 {
				continue
			}
			findings = append(findings, pageTextFinding{packageName: packageName, schemaName: lastSegment(schema),
				inline: formatViolations(violations, textViolationInline), literalOnly: formatViolations(violations, textViolationLiteralOnly)})
		}
	}
	return findings
}

func lastSegment(path string) string {
	trimmed := strings.TrimRight(path, `/\`)
	return trimmed[strings.LastIndexAny(trimmed, `/\`)+1:]
}

// caseInsensitiveNames is .NET's MatchCasing.PlatformDefault: names match without case on Windows and macOS.
func caseInsensitiveNames() bool { return runtime.GOOS == "windows" || runtime.GOOS == "darwin" }

func inspectSchemaFolder(log *Log, schema string) []textViolation {
	files, _, err := directoryFiles(schema)
	if err != nil {
		return nil
	}
	var violations []textViolation
	for _, file := range files {
		name := file
		if caseInsensitiveNames() {
			name = strings.ToLower(name)
		}
		if !strings.HasSuffix(name, ".js") {
			continue
		}
		data, err := os.ReadFile(file)
		if err != nil {
			log.Warning(fmt.Sprintf("Could not check '%s' for non-localizable text: %s", file, err.Error()))
			continue
		}
		violations = append(violations, findTextViolations(decodeText(data))...)
	}
	return violations
}

// decodeText is File.ReadAllText's UTF-8 decoding: the BOM dropped, invalid bytes replaced.
func decodeText(data []byte) string {
	text := strings.TrimPrefix(string(data), "\uFEFF")
	if utf8.ValidString(text) {
		return text
	}
	return strings.ToValidUTF8(text, "�")
}

func formatViolations(violations []textViolation, kind int) []string {
	seen := map[string]bool{}
	var elements []string
	for _, violation := range violations {
		if violation.kind != kind {
			continue
		}
		element := violation.property
		if strings.TrimSpace(violation.node) != "" {
			element = violation.node + "." + violation.property
		}
		if !seen[element] {
			seen[element] = true
			elements = append(elements, element)
		}
	}
	return elements
}

// findTextViolations is WorkspacePageTextInspector.FindViolations.
func findTextViolations(body string) []textViolation {
	trimmed := strings.TrimLeftFunc(body, unicode.IsSpace)
	if trimmed == "" {
		return nil
	}
	if strings.HasPrefix(trimmed, "{") {
		if !strings.Contains(body, `"viewConfigDiff"`) {
			return nil
		}
		root, err := parseOrderedJSON(body, false)
		if err != nil || root.kind != jsonKindObject {
			return nil
		}
		diff := root.member("viewConfigDiff")
		if diff == nil {
			return nil
		}
		return scanViewConfigDiff(diff)
	}
	if !strings.Contains(body, "SCHEMA_VIEW_CONFIG_DIFF") {
		return nil
	}
	match := viewConfigDiffSection.FindStringSubmatch(body)
	if match == nil {
		match = schemaDiffSection.FindStringSubmatch(body)
	}
	if match == nil {
		return nil
	}
	diff, err := parseOrderedJSON(match[1], true)
	if err != nil {
		return nil
	}
	return scanViewConfigDiff(diff)
}

func scanViewConfigDiff(diff *orderedJSON) []textViolation {
	if diff.kind != jsonKindArray {
		return nil
	}
	types := map[string]string{}
	for _, entry := range diff.items {
		if entry.kind != jsonKindObject {
			continue
		}
		name, named := nodeName(entry)
		values := entry.member("values")
		if !named || values == nil || values.kind != jsonKindObject {
			continue
		}
		if componentType, ok := componentType(values); ok {
			if _, exists := types[name]; !exists {
				types[name] = componentType
			}
		}
	}
	var violations []textViolation
	for _, entry := range diff.items {
		if entry.kind != jsonKindObject {
			continue
		}
		values := entry.member("values")
		if values == nil || values.kind != jsonKindObject {
			continue
		}
		owner, _ := nodeName(entry)
		rootType, ok := componentType(values)
		if !ok {
			rootType = types[owner]
			if owner == "" {
				rootType = ""
			}
		}
		scanTextNode(values, owner, rootType, false, &violations)
	}
	return violations
}

func nodeName(node *orderedJSON) (string, bool) {
	name := node.member("name")
	if name != nil && name.kind == jsonKindString && strings.TrimSpace(name.text) != "" {
		return name.text, true
	}
	return "", false
}

func componentType(node *orderedJSON) (string, bool) {
	value := node.member("type")
	if value != nil && value.kind == jsonKindString && strings.TrimSpace(value.text) != "" {
		return value.text, true
	}
	return "", false
}

func scanTextNode(node *orderedJSON, owner, rootType string, galleryItemConfig bool, violations *[]textViolation) {
	switch node.kind {
	case jsonKindObject:
		current := owner
		if name, ok := nodeName(node); ok {
			current = name
		}
		currentType := rootType
		if value, ok := componentType(node); ok {
			currentType = value
		}
		for _, member := range node.members {
			if textScanExempt(currentType, member, galleryItemConfig) {
				continue
			}
			scanTextProperty(current, currentType, member, violations)
			scanTextNode(member.value, current, "", currentType == "crt.Gallery" && member.name == "itemConfig", violations)
		}
	case jsonKindArray:
		for _, item := range node.items {
			scanTextNode(item, owner, "", false, violations)
		}
	}
}

func textScanExempt(componentType string, member orderedMember, galleryItemConfig bool) bool {
	if member.name == "_designOptions" {
		return true
	}
	if componentType != "" && member.name == "data" && member.value.kind == jsonKindObject {
		if typeName := member.value.member("typeName"); typeName != nil && typeName.kind == jsonKindString {
			return true
		}
	}
	return galleryItemConfig && member.name == "templateValuesMapping"
}

func scanTextProperty(node, componentType string, member orderedMember, violations *[]textViolation) {
	if member.value.kind != jsonKindString || !localizableTextProperty(member.name) {
		return
	}
	value := member.value.text
	if strings.EqualFold(componentType, "crt.ImageInput") && strings.EqualFold(member.name, "tooltip") {
		if resourceReference(value) {
			*violations = append(*violations, textViolation{node, member.name, textViolationLiteralOnly})
		}
		return
	}
	if inlineTextLiteral(value) {
		*violations = append(*violations, textViolation{node, member.name, textViolationInline})
	}
}

func localizableTextProperty(name string) bool {
	for _, known := range localizableTextNames {
		if strings.EqualFold(name, known) {
			return true
		}
	}
	return false
}

func blank(value string) bool { return strings.TrimFunc(value, unicode.IsSpace) == "" }

func resourceReference(value string) bool {
	if blank(value) {
		return false
	}
	return len(value) >= len("$Resources.Strings.") && strings.EqualFold(value[:len("$Resources.Strings.")], "$Resources.Strings.") ||
		resourceStringPattern.MatchString(value)
}

func inlineTextLiteral(value string) bool {
	if blank(value) {
		return false
	}
	units := utf16.Encode([]rune(value))
	if len(units) >= 2 && units[0] == '$' && (units[1] == '_' || (!utf16.IsSurrogate(rune(units[1])) && unicode.IsLetter(rune(units[1])))) {
		return false
	}
	return !resourceStringPattern.MatchString(value)
}

// pageTextWarnings is PushWorkspaceCommand.WarnAboutNonLocalizablePageText's two messages per finding.
func pageTextWarnings(log *Log, findings []pageTextFinding) {
	for _, finding := range findings {
		if len(finding.inline) > 0 {
			log.Warning(fmt.Sprintf("Page schema '%s' (package '%s') sets user-visible text as inline literals: %s. "+
				"push-workspace installs it anyway, but the MCP update-page tool rejects the same body: bind each value "+
				"via $Resources.Strings.<Key> or #ResourceString(<Key>)# and register the key in the schema resources. "+
				"See the page-schema-resources guide.", finding.schemaName, finding.packageName, strings.Join(finding.inline, ", ")))
		}
		if len(finding.literalOnly) > 0 {
			log.Warning(fmt.Sprintf("Page schema '%s' (package '%s') binds literal-only text properties to localizable "+
				"resources: %s. These components never read localizable strings, so the text renders empty at runtime, "+
				"and the MCP update-page tool rejects the same body: set each value as a plain literal. See the "+
				"page-schema-resources guide.", finding.schemaName, finding.packageName, strings.Join(finding.literalOnly, ", ")))
		}
	}
}

// ---------------------------------------------------------------------------------------------- JSON

// orderedJSON is a JsonDocument element: members in document order, duplicates kept, the last one found.
type orderedJSON struct {
	kind    int
	text    string
	members []orderedMember
	items   []*orderedJSON
}

type orderedMember struct {
	name  string
	value *orderedJSON
}

const (
	jsonKindNull = iota
	jsonKindBool
	jsonKindNumber
	jsonKindString
	jsonKindArray
	jsonKindObject
)

func (j *orderedJSON) member(name string) *orderedJSON {
	var found *orderedJSON
	for _, member := range j.members {
		if member.name == name {
			found = member.value
		}
	}
	return found
}

// parseOrderedJSON is JsonDocument.Parse; lenient adds JsonCommentHandling.Skip and AllowTrailingCommas.
func parseOrderedJSON(text string, lenient bool) (*orderedJSON, error) {
	parser := &orderedParser{text: text, lenient: lenient}
	if err := parser.skip(); err != nil {
		return nil, err
	}
	value, err := parser.value(0)
	if err != nil {
		return nil, err
	}
	if err := parser.skip(); err != nil {
		return nil, err
	}
	if parser.position != len(parser.text) {
		return nil, errors.New("'" + string(parser.text[parser.position]) + "' is invalid after a single JSON value.")
	}
	return value, nil
}

type orderedParser struct {
	text     string
	position int
	lenient  bool
}

var errJSON = errors.New("invalid JSON")

func (p *orderedParser) skip() error {
	for p.position < len(p.text) {
		switch p.text[p.position] {
		case ' ', '\t', '\n', '\r':
			p.position++
		case '/':
			if !p.lenient || p.position+1 >= len(p.text) {
				return errJSON
			}
			switch p.text[p.position+1] {
			case '/':
				end := strings.IndexAny(p.text[p.position:], "\n\r")
				if end < 0 {
					p.position = len(p.text)
				} else {
					p.position += end
				}
			case '*':
				end := strings.Index(p.text[p.position+2:], "*/")
				if end < 0 {
					return errJSON
				}
				p.position += end + 4
			default:
				return errJSON
			}
		default:
			return nil
		}
	}
	return nil
}

func (p *orderedParser) value(depth int) (*orderedJSON, error) {
	if depth > 64 || p.position >= len(p.text) {
		return nil, errJSON
	}
	switch character := p.text[p.position]; {
	case character == '{':
		return p.object(depth)
	case character == '[':
		return p.array(depth)
	case character == '"':
		text, err := p.stringValue()
		return &orderedJSON{kind: jsonKindString, text: text}, err
	case strings.HasPrefix(p.text[p.position:], "true"), strings.HasPrefix(p.text[p.position:], "false"):
		word := "true"
		if character == 'f' {
			word = "false"
		}
		p.position += len(word)
		return &orderedJSON{kind: jsonKindBool, text: word}, nil
	case strings.HasPrefix(p.text[p.position:], "null"):
		p.position += 4
		return &orderedJSON{kind: jsonKindNull}, nil
	case character == '-' || (character >= '0' && character <= '9'):
		return p.number()
	}
	return nil, errJSON
}

var jsonNumber = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?`)

func (p *orderedParser) number() (*orderedJSON, error) {
	match := jsonNumber.FindString(p.text[p.position:])
	if match == "" {
		return nil, errJSON
	}
	p.position += len(match)
	return &orderedJSON{kind: jsonKindNumber, text: match}, nil
}

func (p *orderedParser) stringValue() (string, error) {
	p.position++
	var builder strings.Builder
	for p.position < len(p.text) {
		character := p.text[p.position]
		switch {
		case character == '"':
			p.position++
			return builder.String(), nil
		case character < 0x20:
			return "", errJSON
		case character == '\\':
			if p.position+1 >= len(p.text) {
				return "", errJSON
			}
			escape := p.text[p.position+1]
			p.position += 2
			switch escape {
			case '"', '\\', '/':
				builder.WriteByte(escape)
			case 'b':
				builder.WriteByte('\b')
			case 'f':
				builder.WriteByte('\f')
			case 'n':
				builder.WriteByte('\n')
			case 'r':
				builder.WriteByte('\r')
			case 't':
				builder.WriteByte('\t')
			case 'u':
				unit, err := p.hexUnit()
				if err != nil {
					return "", err
				}
				if utf16.IsSurrogate(unit) && strings.HasPrefix(p.text[p.position:], `\u`) {
					p.position += 2
					low, err := p.hexUnit()
					if err != nil {
						return "", err
					}
					builder.WriteRune(utf16.DecodeRune(unit, low))
					continue
				}
				builder.WriteRune(unit)
			default:
				return "", errJSON
			}
		default:
			builder.WriteByte(character)
			p.position++
		}
	}
	return "", errJSON
}

func (p *orderedParser) hexUnit() (rune, error) {
	if p.position+4 > len(p.text) {
		return 0, errJSON
	}
	value, err := strconv.ParseUint(p.text[p.position:p.position+4], 16, 16)
	if err != nil {
		return 0, errJSON
	}
	p.position += 4
	return rune(value), nil
}

func (p *orderedParser) object(depth int) (*orderedJSON, error) {
	p.position++
	node := &orderedJSON{kind: jsonKindObject}
	first := true
	for {
		if err := p.skip(); err != nil {
			return nil, err
		}
		if p.position >= len(p.text) {
			return nil, errJSON
		}
		if p.text[p.position] == '}' && (first || p.lenient) {
			p.position++
			return node, nil
		}
		if p.text[p.position] != '"' {
			return nil, errJSON
		}
		name, err := p.stringValue()
		if err != nil {
			return nil, err
		}
		if err := p.skip(); err != nil {
			return nil, err
		}
		if p.position >= len(p.text) || p.text[p.position] != ':' {
			return nil, errJSON
		}
		p.position++
		if err := p.skip(); err != nil {
			return nil, err
		}
		value, err := p.value(depth + 1)
		if err != nil {
			return nil, err
		}
		node.members = append(node.members, orderedMember{name: name, value: value})
		first = false
		if err := p.skip(); err != nil {
			return nil, err
		}
		if p.position >= len(p.text) {
			return nil, errJSON
		}
		switch p.text[p.position] {
		case ',':
			p.position++
		case '}':
			p.position++
			return node, nil
		default:
			return nil, errJSON
		}
	}
}

func (p *orderedParser) array(depth int) (*orderedJSON, error) {
	p.position++
	node := &orderedJSON{kind: jsonKindArray}
	first := true
	for {
		if err := p.skip(); err != nil {
			return nil, err
		}
		if p.position >= len(p.text) {
			return nil, errJSON
		}
		if p.text[p.position] == ']' && (first || p.lenient) {
			p.position++
			return node, nil
		}
		value, err := p.value(depth + 1)
		if err != nil {
			return nil, err
		}
		node.items = append(node.items, value)
		first = false
		if err := p.skip(); err != nil {
			return nil, err
		}
		if p.position >= len(p.text) {
			return nil, errJSON
		}
		switch p.text[p.position] {
		case ',':
			p.position++
		case ']':
			p.position++
			return node, nil
		default:
			return nil, errJSON
		}
	}
}
