package knowledge

import (
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// Reference examples are the "reference-example" articles: small YAML catalog entries that point at an
// immutable revision of a public example repository (clio's KnowledgeReferenceExampleService).

// ReferenceExample is one validated catalog entry, serialized like clio's KnowledgeReferenceExample.
type ReferenceExample struct {
	SourceAlias         string `json:"sourceAlias"`
	LibraryID           string `json:"libraryId"`
	SourcePriority      int    `json:"sourcePriority"`
	SourceParticipation string `json:"sourceParticipation"`
	BundleSequence      uint64 `json:"bundleSequence"`
	BundleDigest        string `json:"bundleDigest"`
	CatalogItemID       string `json:"catalogItemId"`
	SchemaVersion       int    `json:"schemaVersion"`
	ID                  string `json:"id"`
	Title               string `json:"title"`
	Status              string `json:"status"`
	PrimaryUseCase      struct {
		ID      string `json:"id"`
		Summary string `json:"summary"`
	} `json:"primaryUseCase"`
	Source struct {
		Repository    string `json:"repository"`
		Revision      string `json:"revision"`
		DefaultBranch string `json:"defaultBranch"`
	} `json:"source"`
	EntryPoints            map[string]string `json:"entryPoints"`
	SupportingCapabilities []string          `json:"supportingCapabilities"`
	Compatibility          struct {
		Status  string `json:"status"`
		Details string `json:"details"`
	} `json:"compatibility"`
	Trust struct {
		Publisher string `json:"publisher"`
		Level     string `json:"level"`
	} `json:"trust"`
	Notes []string `json:"notes"`
}

// ExampleQuery filters reference examples.
type ExampleQuery struct {
	Source, Search, Capability, Status string
}

// ExampleList is clio's KnowledgeReferenceExampleListResult.
type ExampleList struct {
	Success     bool               `json:"success"`
	Examples    []ReferenceExample `json:"examples"`
	Diagnostics []string           `json:"diagnostics"`
}

var (
	immutableRevisionPattern = regexp.MustCompile(`^(?:[0-9a-fA-F]{40}|[0-9a-fA-F]{64})$`)
	entryPointKeyPattern     = regexp.MustCompile(`^[a-z][A-Za-z0-9]*$`)
)

// ListExamples lists the reference examples of the active libraries. neutralize renders a diagnostic that a
// repository can influence (clio fences such text as untrusted).
func (r *Runtime) ListExamples(query ExampleQuery, neutralize func(string) string) ExampleList {
	trim := func(value string) string { return strings.TrimFunc(value, unicode.IsSpace) }
	query = ExampleQuery{trim(query.Source), trim(query.Search), trim(query.Capability), trim(query.Status)}
	for _, value := range []string{query.Source, query.Search, query.Capability, query.Status} {
		if len([]rune(value)) > 200 {
			return ExampleList{Examples: []ReferenceExample{}, Diagnostics: []string{"Reference-example filters cannot exceed 200 characters."}}
		}
	}
	articles := r.ArticlesByRole(referenceExampleRole)
	diagnostics := []string{}
	if diagnostic := r.LastDiagnostic(); strings.TrimSpace(diagnostic) != "" {
		diagnostics = append(diagnostics, neutralize(diagnostic))
	}
	features := map[string]bool{}
	if settings := r.Settings(); settings != nil {
		features = settings.Features
	}
	examples := []ReferenceExample{}
	for _, item := range articles {
		enabled := true
		for _, feature := range item.Article.RequiredFeatures {
			if !featureEnabled(features, feature) {
				enabled = false
			}
		}
		if !enabled || (query.Source != "" && !strings.EqualFold(item.Provenance.SourceAlias, query.Source)) {
			continue
		}
		document, err := parseExampleYAML(item.Article.Text)
		if err != nil {
			diagnostics = append(diagnostics, fmt.Sprintf("Catalog item '%s' is invalid YAML: %s", item.Article.URI, neutralize(err.Error())))
			continue
		}
		example, problem := mapExample(item, document)
		if problem != "" {
			diagnostics = append(diagnostics, fmt.Sprintf("Catalog item '%s' is invalid: %s", item.Article.URI, neutralize(problem)))
			continue
		}
		if exampleMatches(example, query) {
			examples = append(examples, example)
		}
	}
	sort.SliceStable(examples, func(i, j int) bool {
		a, b := examples[i], examples[j]
		if a.SourcePriority != b.SourcePriority {
			return a.SourcePriority > b.SourcePriority
		}
		if a.LibraryID != b.LibraryID {
			return a.LibraryID < b.LibraryID
		}
		return a.ID < b.ID
	})
	return ExampleList{Success: len(diagnostics) == 0, Examples: examples, Diagnostics: diagnostics}
}

func exampleMatches(example ReferenceExample, query ExampleQuery) bool {
	if query.Capability != "" {
		found := false
		for _, capability := range example.SupportingCapabilities {
			if strings.EqualFold(capability, query.Capability) {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	if query.Status != "" && !strings.EqualFold(example.Status, query.Status) {
		return false
	}
	if query.Search == "" {
		return true
	}
	contains := func(value string) bool {
		return strings.Contains(strings.ToLower(value), strings.ToLower(query.Search))
	}
	if contains(example.ID) || contains(example.Title) || contains(example.PrimaryUseCase.ID) || contains(example.PrimaryUseCase.Summary) ||
		contains(example.SourceAlias) || contains(example.LibraryID) {
		return true
	}
	for _, capability := range example.SupportingCapabilities {
		if contains(capability) {
			return true
		}
	}
	return false
}

func hasControlCharacters(value string) bool {
	for _, r := range value {
		if unicode.IsControl(r) || r > 0xFFFF || (r != ' ' && unicode.In(r, unicode.Zs, unicode.Zl, unicode.Zp)) || unicode.Is(unicode.Cf, r) {
			return true
		}
	}
	return false
}

func safeText(value string) bool {
	return !blank(value) && len([]rune(trimDotNet(value))) <= 4096 && !hasControlCharacters(value)
}

func stable(value string) bool {
	trimmed := trimDotNet(value)
	return !blank(value) && len(trimmed) <= 160 && stableIDPattern.MatchString(trimmed)
}

func validRepository(value string) bool {
	trimmed := trimDotNet(value)
	if blank(value) || len(trimmed) > 2048 || hasControlCharacters(value) {
		return false
	}
	parsed, err := url.Parse(trimmed)
	return err == nil && parsed.IsAbs() && strings.EqualFold(parsed.Scheme, "https") && parsed.Host != "" &&
		parsed.User == nil && parsed.RawQuery == "" && !parsed.ForceQuery && parsed.Fragment == ""
}

func safeRepositoryPath(value string) bool {
	if blank(value) {
		return false
	}
	path := trimDotNet(value)
	if len(path) > 512 || strings.HasPrefix(path, "/") || hasControlCharacters(path) || strings.Contains(path, `\`) {
		return false
	}
	for _, segment := range strings.Split(path, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

func mapExample(item RoleArticle, doc map[string]any) (ReferenceExample, string) {
	var example ReferenceExample
	text := func(m map[string]any, key string) string { value, _ := m[key].(string); return value }
	child := func(key string) map[string]any { value, _ := doc[key].(map[string]any); return value }
	version := 0
	if raw, ok := doc["schemaVersion"].(string); ok {
		parsed, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil {
			return example, "schemaVersion must be an integer"
		}
		version = parsed
	}
	if version != 0 {
		return example, fmt.Sprintf("unsupported schemaVersion '%d'", version)
	}
	if !stable(text(doc, "id")) || !safeText(text(doc, "title")) || !stable(text(doc, "status")) {
		return example, "id, title, and status are required"
	}
	useCase := child("primaryUseCase")
	if useCase == nil || !stable(text(useCase, "id")) || !safeText(text(useCase, "summary")) {
		return example, "primaryUseCase.id and primaryUseCase.summary are required"
	}
	source := child("source")
	if source == nil || !validRepository(text(source, "repository")) || !safeText(text(source, "defaultBranch")) ||
		blank(text(source, "revision")) || !immutableRevisionPattern.MatchString(trimDotNet(text(source, "revision"))) {
		return example, "source must contain a credential-free HTTPS repository, default branch, and immutable full commit revision"
	}
	entryPoints := child("entryPoints")
	entryValid := entryPoints != nil && len(entryPoints) > 0 && len(entryPoints) <= 128
	trimmedKeys := map[string]bool{}
	for key, value := range entryPoints {
		path, _ := value.(string)
		trimmed := trimDotNet(key)
		if blank(key) || len(trimmed) > 160 || !entryPointKeyPattern.MatchString(trimmed) || !safeRepositoryPath(path) {
			entryValid = false
		}
		trimmedKeys[trimmed] = true
	}
	if !entryValid || len(trimmedKeys) != len(entryPoints) {
		return example, "entryPoints must contain named safe repository-relative paths"
	}
	capabilities, _ := doc["supportingCapabilities"].([]string)
	capabilityValid := len(capabilities) > 0 && len(capabilities) <= 128
	distinct := map[string]bool{}
	for _, capability := range capabilities {
		if !stable(capability) {
			capabilityValid = false
		}
		distinct[trimDotNet(capability)] = true
	}
	if !capabilityValid || len(distinct) != len(capabilities) {
		return example, "supportingCapabilities must contain unique non-empty stable tags"
	}
	compatibility := child("compatibility")
	if compatibility == nil || !stable(text(compatibility, "status")) || !safeText(text(compatibility, "details")) {
		return example, "compatibility status and details are required"
	}
	trust := child("trust")
	if trust == nil || !safeText(text(trust, "publisher")) || !stable(text(trust, "level")) {
		return example, "trust publisher and level are required"
	}
	notes, _ := doc["notes"].([]string)
	if len(notes) > 128 {
		return example, "notes cannot contain empty values"
	}
	for _, note := range notes {
		if !safeText(note) {
			return example, "notes cannot contain empty values"
		}
	}
	example = ReferenceExample{SourceAlias: item.Provenance.SourceAlias, LibraryID: item.Provenance.LibraryID,
		SourcePriority: item.Priority, SourceParticipation: string(item.Participation), BundleSequence: item.Provenance.Sequence,
		BundleDigest: item.Provenance.BundleDigest, CatalogItemID: item.Article.ItemID, SchemaVersion: version,
		ID: trimDotNet(text(doc, "id")), Title: trimDotNet(text(doc, "title")), Status: trimDotNet(text(doc, "status"))}
	example.PrimaryUseCase.ID, example.PrimaryUseCase.Summary = trimDotNet(text(useCase, "id")), trimDotNet(text(useCase, "summary"))
	example.Source.Repository = trimDotNet(text(source, "repository"))
	example.Source.Revision = strings.ToLower(trimDotNet(text(source, "revision")))
	example.Source.DefaultBranch = trimDotNet(text(source, "defaultBranch"))
	example.EntryPoints = map[string]string{}
	for key, value := range entryPoints {
		path, _ := value.(string)
		example.EntryPoints[trimDotNet(key)] = trimDotNet(path)
	}
	for _, capability := range capabilities {
		example.SupportingCapabilities = append(example.SupportingCapabilities, trimDotNet(capability))
	}
	sort.Strings(example.SupportingCapabilities)
	example.Compatibility.Status, example.Compatibility.Details = trimDotNet(text(compatibility, "status")), trimDotNet(text(compatibility, "details"))
	example.Trust.Publisher, example.Trust.Level = trimDotNet(text(trust, "publisher")), trimDotNet(text(trust, "level"))
	example.Notes = []string{}
	for _, note := range notes {
		example.Notes = append(example.Notes, trimDotNet(note))
	}
	return example, ""
}

// exampleSchema is the document shape: nested mappings, scalar lists ("[]") and scalars ("").
var exampleSchema = map[string]any{
	"schemaVersion": "", "id": "", "title": "", "status": "",
	"primaryUseCase": map[string]any{"id": "", "summary": ""},
	"source":         map[string]any{"repository": "", "revision": "", "defaultBranch": ""},
	"entryPoints":    "map", "supportingCapabilities": "[]",
	"compatibility": map[string]any{"status": "", "details": ""},
	"trust":         map[string]any{"publisher": "", "level": ""},
	"notes":         "[]",
}

type yamlLine struct {
	number int
	indent int
	text   string
}

// parseExampleYAML reads the block-style YAML subset reference examples use: mappings, sequences of
// scalars, plain and quoted scalars, comments. Anything else (flow collections, anchors, tags, block
// scalars, multi-line scalars) is refused rather than guessed at, as is a key the schema does not declare.
func parseExampleYAML(text string) (map[string]any, error) {
	var lines []yamlLine
	for i, raw := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		trimmed := strings.TrimRight(raw, " \t")
		content := strings.TrimLeft(trimmed, " ")
		if content == "" || strings.HasPrefix(content, "#") || (i == 0 && content == "---") {
			continue
		}
		if strings.HasPrefix(trimmed, "\t") || strings.Contains(trimmed[:len(trimmed)-len(content)], "\t") {
			return nil, fmt.Errorf("(Line: %d) tabs are not allowed as indentation", i+1)
		}
		lines = append(lines, yamlLine{number: i + 1, indent: len(trimmed) - len(content), text: content})
	}
	position := 0
	value, err := parseYAMLMapping(lines, &position, 0, exampleSchema)
	if err != nil {
		return nil, err
	}
	if position < len(lines) {
		return nil, fmt.Errorf("(Line: %d) unexpected content", lines[position].number)
	}
	return value, nil
}

func parseYAMLMapping(lines []yamlLine, position *int, indent int, schema map[string]any) (map[string]any, error) {
	result := map[string]any{}
	for *position < len(lines) && lines[*position].indent == indent {
		line := lines[*position]
		if strings.HasPrefix(line.text, "- ") || line.text == "-" {
			return nil, fmt.Errorf("(Line: %d) a sequence item is not allowed here", line.number)
		}
		key, rest, ok := splitYAMLKey(line.text)
		if !ok {
			return nil, fmt.Errorf("(Line: %d) expected 'key: value'", line.number)
		}
		if _, duplicate := result[key]; duplicate {
			return nil, fmt.Errorf("(Line: %d) duplicate key '%s'", line.number, key)
		}
		var expected any = "" // a free mapping (entryPoints) holds scalars
		if schema != nil {
			var known bool
			expected, known = schema[key]
			if !known {
				return nil, fmt.Errorf("(Line: %d) Property '%s' not found on the reference-example document.", line.number, key)
			}
		}
		*position++
		if rest != "" {
			if expected != "" {
				return nil, fmt.Errorf("(Line: %d) '%s' must be a block collection", line.number, key)
			}
			scalar, err := parseYAMLScalar(rest, line.number)
			if err != nil {
				return nil, err
			}
			result[key] = scalar
			continue
		}
		if *position >= len(lines) || lines[*position].indent < indent ||
			(lines[*position].indent == indent && !strings.HasPrefix(lines[*position].text, "-")) {
			result[key] = nil
			continue
		}
		next := lines[*position]
		switch expected {
		case "[]":
			items, err := parseYAMLSequence(lines, position, next.indent)
			if err != nil {
				return nil, err
			}
			result[key] = items
		case "":
			return nil, fmt.Errorf("(Line: %d) '%s' must be a scalar", line.number, key)
		default:
			if next.indent <= indent {
				return nil, fmt.Errorf("(Line: %d) '%s' must be a mapping", line.number, key)
			}
			nested, _ := expected.(map[string]any)
			value, err := parseYAMLMapping(lines, position, next.indent, nested)
			if err != nil {
				return nil, err
			}
			result[key] = value
		}
	}
	if *position < len(lines) && lines[*position].indent > indent {
		return nil, fmt.Errorf("(Line: %d) unexpected indentation", lines[*position].number)
	}
	return result, nil
}

func parseYAMLSequence(lines []yamlLine, position *int, indent int) ([]string, error) {
	items := []string{}
	for *position < len(lines) && lines[*position].indent == indent && strings.HasPrefix(lines[*position].text, "-") {
		line := lines[*position]
		item := strings.TrimLeft(strings.TrimPrefix(line.text, "-"), " ")
		if line.text != "-" && !strings.HasPrefix(line.text, "- ") {
			return nil, fmt.Errorf("(Line: %d) expected '- item'", line.number)
		}
		scalar, err := parseYAMLScalar(item, line.number)
		if err != nil {
			return nil, err
		}
		items = append(items, scalar)
		*position++
	}
	return items, nil
}

func splitYAMLKey(text string) (string, string, bool) {
	index := strings.Index(text, ":")
	for index >= 0 && index+1 < len(text) && text[index+1] != ' ' {
		next := strings.Index(text[index+1:], ":")
		if next < 0 {
			return "", "", false
		}
		index += next + 1
	}
	if index <= 0 {
		return "", "", false
	}
	key := strings.TrimSpace(text[:index])
	if strings.ContainsAny(key[:1], `"'{[&*!|>%@`+"`") {
		return "", "", false
	}
	return key, strings.TrimSpace(text[index+1:]), true
}

func parseYAMLScalar(text string, line int) (string, error) {
	if text == "" {
		return "", nil
	}
	switch text[0] {
	case '"':
		end := strings.LastIndex(text, `"`)
		if end <= 0 || strings.TrimSpace(stripYAMLComment(text[end+1:])) != "" {
			return "", fmt.Errorf("(Line: %d) unterminated double-quoted scalar", line)
		}
		value, err := strconv.Unquote(text[:end+1])
		if err != nil {
			return "", fmt.Errorf("(Line: %d) invalid double-quoted scalar", line)
		}
		return value, nil
	case '\'':
		end := strings.LastIndex(text, "'")
		if end <= 0 || strings.TrimSpace(stripYAMLComment(text[end+1:])) != "" {
			return "", fmt.Errorf("(Line: %d) unterminated single-quoted scalar", line)
		}
		return strings.ReplaceAll(text[1:end], "''", "'"), nil
	case '{', '[', '&', '*', '!', '|', '>', '%', '@', '`':
		return "", fmt.Errorf("(Line: %d) unsupported YAML construct '%c'", line, text[0])
	}
	return strings.TrimSpace(stripYAMLComment(text)), nil
}

func stripYAMLComment(text string) string {
	if index := strings.Index(text, " #"); index >= 0 {
		return text[:index]
	}
	return text
}
