package creatio

// Page-body helpers of clio's update-page / sync-pages: PageSchemaSectionReader, PageBodyMerger (append mode),
// ResourceStringHelper, the view-model path collection of SchemaValidationService, PageParentNameValidation's
// diagnostic and the before-save ChartConfigKeyOrderPreprocessor. clio parses JavaScript with Acornima; this
// server reads the same shapes with the structural reader of classicpagejs.go.

import (
	"bytes"
	"fmt"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"unicode"
	"unicode/utf16"
)

const (
	pageWriteWebIncomingFullConfig = "Web append merge does not support an incoming body that uses the full 'SCHEMA_VIEW_MODEL_CONFIG' or 'SCHEMA_MODEL_CONFIG' form. " +
		"Use 'replace' mode, or convert the incoming body to the diff form (SCHEMA_VIEW_MODEL_CONFIG_DIFF / SCHEMA_MODEL_CONFIG_DIFF) before append."
	pageWriteWebCurrentFullConfig = "Web append merge cannot run because the page on the server is stored in the full 'SCHEMA_VIEW_MODEL_CONFIG' or 'SCHEMA_MODEL_CONFIG' form. " +
		"Use 'replace' mode — the server-side body is not authored by the caller, so it cannot be converted to the diff form (SCHEMA_VIEW_MODEL_CONFIG_DIFF / SCHEMA_MODEL_CONFIG_DIFF) from here."
	pageWriteMobileIncomingFullConfig = "Mobile append merge does not support an incoming body that uses the full 'viewModelConfig' or 'modelConfig' form. " +
		"Use 'replace' mode, or convert the incoming body to the diff form (viewModelConfigDiff / modelConfigDiff) before append."
	pageWriteMobileCurrentFullConfig = "Mobile append merge cannot run because the page on the server is stored in the full 'viewModelConfig' or 'modelConfig' form. " +
		"Use 'replace' mode — the server-side body is not authored by the caller, so it cannot be converted to the diff form (viewModelConfigDiff / modelConfigDiff) from here."
	pageWriteMaxNamedOperations = 25
)

// pageWriteIsMobileBody is clio's PageSchemaTypeExtensions.FromBody == Mobile: a body whose first non-blank
// character is '{'.
func pageWriteIsMobileBody(body string) bool {
	return strings.HasPrefix(strings.TrimLeftFunc(body, unicode.IsSpace), "{")
}

// pageWriteSectionPattern is PageSchemaSectionReader's lazy marker-pair pattern.
func pageWriteSectionPattern(marker string) *regexp.Regexp {
	quoted := regexp.QuoteMeta(marker)
	return regexp.MustCompile(`/\*\*` + quoted + `\*/([\s\S]*?)/\*\*` + quoted + `\*/`)
}

// pageWriteReplaceSection is PageBodyMerger.ReplaceSection: the first marker pair's content replaced.
func pageWriteReplaceSection(body, marker, content string) (string, bool) {
	location := pageWriteSectionPattern(marker).FindStringIndex(body)
	if location == nil {
		return body, false
	}
	return body[:location[0]] + "/**" + marker + "*/" + content + "/**" + marker + "*/" + body[location[1]:], true
}

// pageWriteUsesFullConfig is PageBodyMerger.UsesUnsupportedFullConfigForm.
func pageWriteUsesFullConfig(body string, current bool) (bool, string) {
	if strings.TrimSpace(body) == "" {
		return false, ""
	}
	_, viewModel := readPageSection(body, "SCHEMA_VIEW_MODEL_CONFIG")
	_, model := readPageSection(body, "SCHEMA_MODEL_CONFIG")
	if viewModel || model {
		if current {
			return true, pageWriteWebCurrentFullConfig
		}
		return true, pageWriteWebIncomingFullConfig
	}
	if pageWriteIsMobileBody(body) {
		parsed, err := pageWriteParseLenient(body)
		if err != nil || !parsed.isObject() {
			return false, ""
		}
		present := func(key string) bool { value := parsed.get(key); return value != nil && value.kind != jkNull }
		if present("viewModelConfig") || present("modelConfig") {
			if current {
				return true, pageWriteMobileCurrentFullConfig
			}
			return true, pageWriteMobileIncomingFullConfig
		}
	}
	return false, ""
}

// pageWriteParseLenient parses JSON as Newtonsoft's JToken.Parse accepts it: strict JSON first, then the
// relaxed forms (comments, unquoted names, single quotes, trailing commas) through the JSONH reader.
func pageWriteParseLenient(text string) (*jnode, error) {
	node, err := parseJNode([]byte(text))
	if err == nil {
		return node, nil
	}
	relaxed, relaxedErr := lenientJSON(text)
	if relaxedErr != nil {
		return nil, err
	}
	return parseJNode(relaxed)
}

// PageAppendProjection is clio's PageAppendProjection: what an append merge does to viewConfigDiff.
type PageAppendProjection struct {
	CurrentOperationCount           int      `json:"currentOperationCount"`
	IncomingOperationCount          int      `json:"incomingOperationCount"`
	ProjectedOperationCount         int      `json:"projectedOperationCount"`
	AddedOperationCount             int      `json:"addedOperationCount"`
	ReplacedOperations              []string `json:"replacedOperations"`
	ReplacedOperationCount          int      `json:"replacedOperationCount"`
	DroppedOperations               []string `json:"droppedOperations"`
	DroppedOperationCount           int      `json:"droppedOperationCount"`
	CollapsedIncomingOperations     []string `json:"collapsedIncomingOperations"`
	CollapsedIncomingOperationCount int      `json:"collapsedIncomingOperationCount"`
	ViewConfigDiffApplied           bool     `json:"viewConfigDiffApplied"`

	supersededDropWarnings []string
}

type pageWriteOperationIdentity struct {
	operation, name   string
	targetsProperties bool
}

func (identity pageWriteOperationIdentity) verb() string {
	switch {
	case identity.operation == "":
		return "(no operation)"
	case identity.targetsProperties:
		return identity.operation + "(properties)"
	}
	return identity.operation
}

func (identity pageWriteOperationIdentity) describe() string {
	return identity.verb() + " " + identity.name
}

func pageWriteIdentity(item *jnode) (pageWriteOperationIdentity, bool) {
	if !item.isObject() {
		return pageWriteOperationIdentity{}, false
	}
	name := item.get("name")
	if name == nil || name.kind != jkString || name.text == "" {
		return pageWriteOperationIdentity{}, false
	}
	operation := ""
	if value := item.get("operation"); value != nil && value.kind == jkString {
		operation = value.text
	}
	targets := (operation == "remove" || operation == "set") && item.get("properties").isArray()
	return pageWriteOperationIdentity{operation: operation, name: name.text, targetsProperties: targets}, true
}

// pageWriteMergeBodies is clio's PageBodyMerger.Merge.
func pageWriteMergeBodies(current, incoming string) (string, *PageAppendProjection, string, bool) {
	if strings.TrimSpace(current) == "" {
		return "", nil, "Current body is empty — cannot perform append merge.", false
	}
	if strings.TrimSpace(incoming) == "" {
		return "", nil, "Incoming body is empty — pass the new viewConfigDiff/handlers fragment.", false
	}
	if full, message := pageWriteUsesFullConfig(incoming, false); full {
		return "", nil, message, true
	}
	if full, message := pageWriteUsesFullConfig(current, true); full {
		return "", nil, message, true
	}
	projection := &PageAppendProjection{ReplacedOperations: []string{}, DroppedOperations: []string{}, CollapsedIncomingOperations: []string{}}
	var merged string
	var err error
	if pageWriteIsMobileBody(current) {
		merged, err = pageWriteMergeMobile(current, incoming, projection)
	} else {
		merged, err = pageWriteMergeWeb(current, incoming, projection)
	}
	if err != nil {
		return "", nil, err.Error(), false
	}
	return merged, projection, "", false
}

func pageWriteReadJSONArray(body, marker string) (*jnode, error) {
	content, ok := readPageSection(body, marker)
	if !ok {
		return newArray(), nil
	}
	trimmed := strings.TrimSpace(content)
	if trimmed == "" || trimmed == "[]" {
		return newArray(), nil
	}
	parsed, err := pageWriteParseLenient(trimmed)
	if err != nil {
		return nil, fmt.Errorf("Section '%s' is not valid JSON array: %s", marker, err.Error())
	}
	if !parsed.isArray() {
		return nil, fmt.Errorf("Section '%s' is not valid JSON array: Error reading JArray from JsonReader. Current JsonReader item is not an array.", marker)
	}
	return parsed, nil
}

func pageWriteRawSection(body, marker string) (string, bool) {
	content, ok := readPageSection(body, marker)
	if !ok {
		return "", false
	}
	return strings.TrimSpace(content), true
}

func pageWriteRawSectionOr(body, marker, fallback string) string {
	if content, ok := pageWriteRawSection(body, marker); ok {
		return content
	}
	return fallback
}

func pageWriteMergeWeb(current, incoming string, projection *PageAppendProjection) (string, error) {
	currentDiff, err := pageWriteReadJSONArray(current, "SCHEMA_VIEW_CONFIG_DIFF")
	if err != nil {
		return "", err
	}
	incomingDiff, err := pageWriteReadJSONArray(incoming, "SCHEMA_VIEW_CONFIG_DIFF")
	if err != nil {
		return "", err
	}
	mergedViewConfig := pageWriteMergeOperations(currentDiff, incomingDiff, projection)
	appendSection := func(marker string) (*jnode, error) {
		left, err := pageWriteReadJSONArray(current, marker)
		if err != nil {
			return nil, err
		}
		right, err := pageWriteReadJSONArray(incoming, marker)
		if err != nil {
			return nil, err
		}
		merged := newArray()
		merged.items = append(append(merged.items, left.items...), right.items...)
		return merged, nil
	}
	mergedViewModel, err := appendSection("SCHEMA_VIEW_MODEL_CONFIG_DIFF")
	if err != nil {
		return "", err
	}
	mergedModel, err := appendSection("SCHEMA_MODEL_CONFIG_DIFF")
	if err != nil {
		return "", err
	}
	handlers := pageWriteMergeHandlers(pageWriteRawSectionOr(current, "SCHEMA_HANDLERS", "[]"), pageWriteRawSectionOr(incoming, "SCHEMA_HANDLERS", "[]"))
	converters, err := pageWriteMergeKeyedObject(pageWriteRawSectionOr(current, "SCHEMA_CONVERTERS", "{}"), pageWriteRawSectionOr(incoming, "SCHEMA_CONVERTERS", "{}"))
	if err != nil {
		return "", err
	}
	validators, err := pageWriteMergeKeyedObject(pageWriteRawSectionOr(current, "SCHEMA_VALIDATORS", "{}"), pageWriteRawSectionOr(incoming, "SCHEMA_VALIDATORS", "{}"))
	if err != nil {
		return "", err
	}
	result, applied := pageWriteReplaceSection(current, "SCHEMA_VIEW_CONFIG_DIFF", newtonsoftIndented(mergedViewConfig))
	projection.ViewConfigDiffApplied = applied
	result, _ = pageWriteReplaceSection(result, "SCHEMA_VIEW_MODEL_CONFIG_DIFF", newtonsoftIndented(mergedViewModel))
	result, _ = pageWriteReplaceSection(result, "SCHEMA_MODEL_CONFIG_DIFF", newtonsoftIndented(mergedModel))
	result, _ = pageWriteReplaceSection(result, "SCHEMA_HANDLERS", handlers)
	result, _ = pageWriteReplaceSection(result, "SCHEMA_CONVERTERS", converters)
	result, _ = pageWriteReplaceSection(result, "SCHEMA_VALIDATORS", validators)
	return result, nil
}

func pageWriteMergeMobile(current, incoming string, projection *PageAppendProjection) (string, error) {
	currentNode, err := pageWriteParseLenient(current)
	if err != nil || !currentNode.isObject() {
		return "", fmt.Errorf("Current mobile page body is not valid JSON: %s", pageWriteErrText(err))
	}
	incomingNode, err := pageWriteParseLenient(incoming)
	if err != nil || !incomingNode.isObject() {
		return "", fmt.Errorf("Incoming mobile page body is not valid JSON: %s", pageWriteErrText(err))
	}
	projection.ViewConfigDiffApplied = true
	arrayOf := func(node *jnode, key string) *jnode {
		if value := node.get(key); value.isArray() {
			return value
		}
		return newArray()
	}
	mergedView := pageWriteMergeOperations(arrayOf(currentNode, "viewConfigDiff"), arrayOf(incomingNode, "viewConfigDiff"), projection)
	appendArrays := func(key string) *jnode {
		merged := newArray()
		merged.items = append(append(merged.items, arrayOf(currentNode, key).items...), arrayOf(incomingNode, key).items...)
		return merged
	}
	mergedViewModel := appendArrays("viewModelConfigDiff")
	mergedModel := appendArrays("modelConfigDiff")
	currentNode.set("viewConfigDiff", mergedView)
	currentNode.set("viewModelConfigDiff", mergedViewModel)
	currentNode.set("modelConfigDiff", mergedModel)
	return newtonsoftIndented(currentNode), nil
}

func pageWriteErrText(err error) string {
	if err == nil {
		return "Error reading JObject from JsonReader. Current JsonReader item is not an object."
	}
	return err.Error()
}

// pageWriteMergeOperations is MergeViewConfigDiffOperations: an incoming operation replaces the first current
// one of the same identity in place; later current duplicates of a superseded identity are dropped.
func pageWriteMergeOperations(current, incoming *jnode, projection *PageAppendProjection) *jnode {
	record := func(list *[]string, identity pageWriteOperationIdentity) {
		if len(*list) < pageWriteMaxNamedOperations {
			*list = append(*list, identity.describe())
		}
	}
	byIdentity := map[pageWriteOperationIdentity]*jnode{}
	for _, item := range incoming.items {
		identity, ok := pageWriteIdentity(item)
		if !ok {
			continue
		}
		if _, exists := byIdentity[identity]; exists {
			projection.CollapsedIncomingOperationCount++
			record(&projection.CollapsedIncomingOperations, identity)
		}
		byIdentity[identity] = item
	}
	replaced := map[pageWriteOperationIdentity]bool{}
	warned := map[pageWriteOperationIdentity]bool{}
	merged := newArray()
	for _, item := range current.items {
		identity, ok := pageWriteIdentity(item)
		replacement, matched := byIdentity[identity]
		if !ok || !matched {
			merged.items = append(merged.items, item)
			continue
		}
		if !replaced[identity] {
			replaced[identity] = true
			merged.items = append(merged.items, replacement)
			projection.ReplacedOperationCount++
			record(&projection.ReplacedOperations, identity)
			continue
		}
		projection.DroppedOperationCount++
		record(&projection.DroppedOperations, identity)
		if !warned[identity] {
			warned[identity] = true
			projection.supersededDropWarnings = append(projection.supersededDropWarnings, fmt.Sprintf(
				"Component '%s' carried more than one '%s' operation in the page's own body, and the appended fragment supersedes that operation. "+
					"Only the first occurrence was replaced; every later one was dropped, because keeping it would re-apply its values AFTER your replacement. "+
					"If they set different keys, the dropped entries' keys are gone from the saved page. Re-read the page with get-page and re-apply anything missing. "+
					"See docs://mcp/guides/page-modification.", identity.name, identity.verb()))
		}
	}
	emitted := map[pageWriteOperationIdentity]bool{}
	for _, item := range incoming.items {
		identity, ok := pageWriteIdentity(item)
		if !ok {
			merged.items = append(merged.items, item)
			continue
		}
		if !replaced[identity] && !emitted[identity] {
			emitted[identity] = true
			merged.items = append(merged.items, byIdentity[identity])
			projection.AddedOperationCount++
		}
	}
	projection.CurrentOperationCount = len(current.items)
	projection.IncomingOperationCount = len(incoming.items)
	projection.ProjectedOperationCount = len(merged.items)
	return merged
}

var pageWriteHandlerRequestPattern = regexp.MustCompile(`request\s*:\s*["']([^"']+)["']`)

// pageWriteMergeHandlers is MergeHandlersRaw: current handlers whose request the fragment also handles are
// dropped, then the fragment's handlers follow.
func pageWriteMergeHandlers(current, incoming string) string {
	current, incoming = strings.TrimSpace(current), strings.TrimSpace(incoming)
	if current == "[]" || current == "" {
		return incoming
	}
	if incoming == "[]" || incoming == "" {
		return current
	}
	strip := func(value string) string {
		value = strings.TrimSpace(value)
		value = strings.TrimPrefix(value, "[")
		value = strings.TrimSuffix(value, "]")
		return strings.TrimSpace(value)
	}
	currentInner, incomingInner := strip(current), strip(incoming)
	requests := map[string]bool{}
	for _, match := range pageWriteHandlerRequestPattern.FindAllStringSubmatch(incomingInner, -1) {
		requests[match[1]] = true
	}
	filtered := currentInner
	if len(requests) > 0 {
		kept := []string{}
		for _, block := range pageWriteTopLevelObjects(currentInner) {
			if match := pageWriteHandlerRequestPattern.FindStringSubmatch(block); match != nil && requests[match[1]] {
				continue
			}
			kept = append(kept, block)
		}
		filtered = strings.Join(kept, ",")
	}
	var joined string
	switch {
	case strings.TrimSpace(filtered) == "":
		joined = incomingInner
	case strings.TrimSpace(incomingInner) == "":
		joined = filtered
	default:
		joined = strings.TrimRight(filtered, ", \t\n\r") + "," + incomingInner
	}
	return "[" + joined + "]"
}

// pageWriteTopLevelObjects is SplitTopLevelObjects: a plain brace counter, as clio's.
func pageWriteTopLevelObjects(value string) []string {
	result := []string{}
	depth, start := 0, -1
	for index := 0; index < len(value); index++ {
		switch value[index] {
		case '{':
			if depth == 0 {
				start = index
			}
			depth++
		case '}':
			depth--
			if depth == 0 && start >= 0 {
				result = append(result, value[start:index+1])
				start = -1
			}
		}
	}
	return result
}

// pageWriteMergeKeyedObject is MergeKeyedObjectRaw: object entries keyed by property name, incoming wins.
func pageWriteMergeKeyedObject(current, incoming string) (string, error) {
	current, incoming = strings.TrimSpace(current), strings.TrimSpace(incoming)
	if current == "{}" || current == "" {
		return incoming, nil
	}
	if incoming == "{}" || incoming == "" {
		return current, nil
	}
	currentEntries, err := pageWriteObjectEntries(current)
	if err != nil {
		return "", err
	}
	incomingEntries, err := pageWriteObjectEntries(incoming)
	if err != nil {
		return "", err
	}
	incomingKeys := map[string]bool{}
	for _, entry := range incomingEntries {
		if entry.keyed {
			incomingKeys[entry.key] = true
		}
	}
	kept := []string{}
	for _, entry := range currentEntries {
		if !entry.keyed || !incomingKeys[entry.key] {
			kept = append(kept, entry.text)
		}
	}
	for _, entry := range incomingEntries {
		kept = append(kept, entry.text)
	}
	if len(kept) == 0 {
		return "{}", nil
	}
	return "{" + strings.Join(kept, ",") + "}", nil
}

type pageWriteObjectEntry struct {
	key   string
	keyed bool
	text  string
}

// pageWriteObjectEntries is ParseObjectEntries: the top-level properties of an object literal, each with
// its source text.
func pageWriteObjectEntries(source string) ([]pageWriteObjectEntry, error) {
	wrapped := "(" + source + ")"
	runes := []rune(wrapped)
	script, err := classicPageParse(wrapped)
	if err != nil {
		return nil, err
	}
	if len(script.tokens) < 3 || !script.punct(0, "(") || !script.punct(1, "{") || script.match[1] != len(script.tokens)-2 ||
		script.match[0] != len(script.tokens)-1 {
		return nil, fmt.Errorf("Expected a JavaScript object expression.")
	}
	end := script.match[1]
	entries := []pageWriteObjectEntry{}
	for _, segment := range script.splitTopLevel(2, end) {
		if segment[0] >= segment[1] {
			continue
		}
		property, ok := script.property(segment[0], segment[1])
		if !ok {
			return nil, fmt.Errorf("Expected a JavaScript object expression.")
		}
		startOffset := script.tokens[segment[0]].offset
		endOffset := script.tokens[segment[1]].offset
		text := strings.TrimSpace(string(runes[startOffset:endOffset]))
		if text == "" {
			continue
		}
		keyed := property.name != "" && !script.punct(segment[0], "...") && !script.punct(segment[0], "[") &&
			script.tokens[segment[0]].kind != classicPageNumber
		entries = append(entries, pageWriteObjectEntry{key: property.name, keyed: keyed, text: text})
	}
	return entries, nil
}

// newtonsoftIndented writes JSON as Newtonsoft's Formatting.Indented does: two-space indentation, "key": value.
func newtonsoftIndented(n *jnode) string {
	var buffer bytes.Buffer
	newtonsoftIndentedWrite(&buffer, n, 0)
	return buffer.String()
}

func newtonsoftIndentedWrite(buffer *bytes.Buffer, n *jnode, depth int) {
	indent := func(level int) {
		buffer.WriteString(pageWriteNewLine)
		buffer.WriteString(strings.Repeat("  ", level))
	}
	if n == nil {
		buffer.WriteString("null")
		return
	}
	switch n.kind {
	case jkArray:
		if len(n.items) == 0 {
			buffer.WriteString("[]")
			return
		}
		buffer.WriteByte('[')
		for index, item := range n.items {
			if index > 0 {
				buffer.WriteByte(',')
			}
			indent(depth + 1)
			newtonsoftIndentedWrite(buffer, item, depth+1)
		}
		indent(depth)
		buffer.WriteByte(']')
	case jkObject:
		if len(n.keys) == 0 {
			buffer.WriteString("{}")
			return
		}
		buffer.WriteByte('{')
		for index, key := range n.keys {
			if index > 0 {
				buffer.WriteByte(',')
			}
			indent(depth + 1)
			writeJSONString(buffer, key, false)
			buffer.WriteString(": ")
			newtonsoftIndentedWrite(buffer, n.props[key], depth+1)
		}
		indent(depth)
		buffer.WriteByte('}')
	default:
		n.writeJSON(buffer, false)
	}
}

// pageWriteNewLine is the line break Newtonsoft writes: Environment.NewLine of the host ("\r\n" on Windows).
var pageWriteNewLine = func() string {
	if runtime.GOOS == "windows" {
		return "\r\n"
	}
	return "\n"
}()

// pageWriteResourceMacro and pageWriteResourceDollar are ResourceStringHelper's key patterns.
var (
	pageWriteResourceMacro  = regexp.MustCompile(`#ResourceString\(([^)]+)\)#`)
	pageWriteResourceDollar = regexp.MustCompile(`\$Resources\.Strings\.([A-Za-z0-9_]+)`)
	pageWriteCaptionBound   = regexp.MustCompile(`([a-z])([A-Z])`)
)

// pageWriteBodyResourceKeys is ResourceStringHelper.ExtractKeys.
func pageWriteBodyResourceKeys(body string) map[string]bool {
	keys := map[string]bool{}
	for _, match := range pageWriteResourceMacro.FindAllStringSubmatch(body, -1) {
		keys[match[1]] = true
	}
	for _, match := range pageWriteResourceDollar.FindAllStringSubmatch(body, -1) {
		keys[match[1]] = true
	}
	return keys
}

// pageWriteDeriveCaption is ResourceStringHelper.DeriveCaption.
func pageWriteDeriveCaption(key string) string {
	result := strings.TrimSuffix(key, "_caption")
	result = strings.TrimPrefix(result, "Usr")
	result = pageWriteCaptionBound.ReplaceAllString(result, "$1 $2")
	return strings.TrimSpace(strings.ReplaceAll(result, "_", " "))
}

// pageWriteResources is a parsed `resources` argument, in the caller's order.
type pageWriteResources struct {
	keys   []string
	values map[string]string
}

func (r *pageWriteResources) count() int {
	if r == nil {
		return 0
	}
	return len(r.keys)
}

func (r *pageWriteResources) lookup(key string) (string, bool) {
	if r == nil {
		return "", false
	}
	value, ok := r.values[key]
	return value, ok
}

// pageWriteParseResources is SchemaValidationService.TryParseResources: blank is absent; otherwise a JSON
// object (comments and trailing commas allowed) whose values are all strings.
func pageWriteParseResources(text string) (*pageWriteResources, bool) {
	if strings.TrimSpace(text) == "" {
		return nil, true
	}
	node, err := pageWriteParseDocument(text)
	if err != nil || !node.isObject() {
		return nil, false
	}
	resources := &pageWriteResources{values: map[string]string{}}
	for _, key := range node.keys {
		value := node.props[key]
		if value.kind != jkString {
			return nil, false
		}
		if _, seen := resources.values[key]; !seen {
			resources.keys = append(resources.keys, key)
		}
		resources.values[key] = value.text
	}
	return resources, true
}

func pageWriteLocalizableEntry(key, value string) *jnode {
	entry := newObject()
	entry.set("uId", newString(schemaWriteNewGUID()))
	entry.set("name", newString(key))
	entry.set("values", newArray())
	pageWriteSetCultureValue(entry, schemaWriteDefaultCulture, value)
	return entry
}

// pageWriteSetCultureValue is ResourceStringHelper.SetCultureValue.
func pageWriteSetCultureValue(entry *jnode, culture, value string) {
	values := entry.get("values")
	if !values.isArray() {
		values = newArray()
		entry.set("values", values)
	}
	for _, item := range values.items {
		if item.isObject() && strings.EqualFold(pageWriteTokenText(item.get("cultureName")), culture) {
			if current := item.get("value"); current != nil && current.kind == jkString && current.text == value {
				return
			}
			item.set("value", newString(value))
			return
		}
	}
	item := newObject()
	item.set("cultureName", newString(culture))
	item.set("value", newString(value))
	values.items = append(values.items, item)
}

// pageWriteCleanAndMerge is ResourceStringHelper.CleanAndMerge: existing strings are kept (their en-US value
// updated from resources), body keys that are missing are registered from resources or derived from a Usr
// key unless a data-source attribute provides them, and the remaining resources are added.
func pageWriteCleanAndMerge(existing *jnode, resources *pageWriteResources, bodyKeys []string, dsBound map[string]bool) (*jnode, []string) {
	result := newArray()
	present := map[string]bool{}
	registered := []string{}
	if existing.isArray() {
		for _, entry := range existing.items {
			if !entry.isObject() {
				continue
			}
			name := pageWriteTokenText(entry.get("name"))
			if name == "" {
				continue
			}
			copied := entry.clone()
			if value, ok := resources.lookup(name); ok {
				pageWriteSetCultureValue(copied, schemaWriteDefaultCulture, value)
			}
			result.items = append(result.items, copied)
			present[name] = true
		}
	}
	bodySet := map[string]bool{}
	for _, key := range bodyKeys {
		bodySet[key] = true
		if present[key] {
			continue
		}
		value, ok := resources.lookup(key)
		if !ok {
			if dsBound[strings.ToLower(key)] || !strings.HasPrefix(key, "Usr") {
				continue
			}
			value = pageWriteDeriveCaption(key)
		}
		result.items = append(result.items, pageWriteLocalizableEntry(key, value))
		registered = append(registered, key)
	}
	if resources != nil {
		for _, key := range resources.keys {
			if present[key] || bodySet[key] {
				continue
			}
			result.items = append(result.items, pageWriteLocalizableEntry(key, resources.values[key]))
			registered = append(registered, key)
		}
	}
	return result, registered
}

// pageWriteSortedKeys orders body keys as .NET's HashSet enumerates small string sets: insertion order. The
// regex matches are inserted macro-first, then $Resources, so that order is kept.
func pageWriteOrderedBodyKeys(body string) []string {
	seen := map[string]bool{}
	keys := []string{}
	for _, pattern := range []*regexp.Regexp{pageWriteResourceMacro, pageWriteResourceDollar} {
		for _, match := range pattern.FindAllStringSubmatch(body, -1) {
			if !seen[match[1]] {
				seen[match[1]] = true
				keys = append(keys, match[1])
			}
		}
	}
	return keys
}

// pageWriteViewModelPaths is SchemaValidationService.CollectViewModelPaths / CollectMobileViewModelPaths:
// the lower-cased names of attributes, at any depth, whose modelConfig.path binds them to a data-source column.
func pageWriteViewModelPaths(body string, mobile bool) map[string]bool {
	paths := map[string]bool{}
	var collect func(*jnode)
	collect = func(element *jnode) {
		switch {
		case element.isObject():
			for _, key := range element.keys {
				value := element.props[key]
				if value.isObject() {
					if path := value.get("modelConfig").get("path"); path != nil && path.kind == jkString && strings.TrimSpace(path.text) != "" {
						paths[strings.ToLower(key)] = true
					}
				}
				collect(value)
			}
		case element.isArray():
			for _, item := range element.items {
				collect(item)
			}
		}
	}
	if !mobile {
		for _, marker := range []string{"SCHEMA_VIEW_MODEL_CONFIG_DIFF", "SCHEMA_VIEW_MODEL_CONFIG"} {
			content, ok := readPageSection(body, marker)
			if !ok {
				continue
			}
			if node, err := pageWriteParseDocument(content); err == nil {
				collect(node)
			}
		}
		return paths
	}
	root, err := parseJNode([]byte(body))
	if err != nil || !root.isObject() {
		return paths
	}
	for _, key := range []string{"viewModelConfigDiff", "modelConfigDiff"} {
		diff := root.get(key)
		if !diff.isArray() {
			continue
		}
		for _, entry := range diff.items {
			if !entry.isObject() || !pageWriteAttributesContainer(entry) {
				continue
			}
			if values := entry.get("values"); values.isObject() {
				collect(values)
			}
		}
	}
	for _, key := range []string{"viewModelConfig", "modelConfig"} {
		if attributes := root.get(key).get("attributes"); attributes.isObject() {
			collect(attributes)
		}
	}
	return paths
}

// pageWriteAttributesContainer is ShouldScanAsAttributesContainer: no path, or exactly ["attributes"].
func pageWriteAttributesContainer(operation *jnode) bool {
	path, has := operation.props["path"]
	if !has {
		return true
	}
	if !path.isArray() || len(path.items) != 1 {
		return false
	}
	first := path.items[0]
	return first.kind == jkString && strings.EqualFold(first.text, "attributes")
}

// pageWriteParseDocument is TryParseJsonDocument: JSON with comments and trailing commas allowed.
func pageWriteParseDocument(content string) (*jnode, error) {
	cleaned, err := pageValidateCleanJSON(pageWriteStripComments(content))
	if err != nil {
		return nil, err
	}
	return parseJNode([]byte(cleaned))
}

// pageWriteStripComments removes // and /* */ comments outside strings.
func pageWriteStripComments(text string) string {
	var out strings.Builder
	runes := []rune(text)
	for index := 0; index < len(runes); index++ {
		ch := runes[index]
		switch {
		case ch == '"':
			start := index
			for index++; index < len(runes) && runes[index] != '"'; index++ {
				if runes[index] == '\\' {
					index++
				}
			}
			out.WriteString(string(runes[start:min(index+1, len(runes))]))
		case ch == '/' && index+1 < len(runes) && runes[index+1] == '/':
			for index < len(runes) && runes[index] != '\n' {
				index++
			}
			if index < len(runes) {
				out.WriteRune('\n')
			}
		case ch == '/' && index+1 < len(runes) && runes[index+1] == '*':
			index += 2
			for index+1 < len(runes) && !(runes[index] == '*' && runes[index+1] == '/') {
				index++
			}
			index++
			out.WriteRune(' ')
		default:
			out.WriteRune(ch)
		}
	}
	return out.String()
}

// pageWriteParentDiagnostic is PageParentNameValidation.Diagnostic: the unresolved parent and the closest
// known element name by edit distance.
func pageWriteParentDiagnostic(child, parent string, known []string) string {
	message := fmt.Sprintf("Element '%s' has unresolved parentName '%s'.", child, parent)
	candidates := []string{}
	for _, name := range known {
		if name != "" && name != child {
			candidates = append(candidates, name)
			if len(candidates) == 512 {
				break
			}
		}
	}
	if len(candidates) == 0 {
		return message
	}
	head := func(text string) []uint16 {
		units := utf16.Encode([]rune(text))
		if len(units) > 256 {
			units = units[:256]
		}
		return units
	}
	target := head(parent)
	sort.SliceStable(candidates, func(i, j int) bool {
		left, right := pageWriteDistance(target, head(candidates[i])), pageWriteDistance(target, head(candidates[j]))
		if left != right {
			return left < right
		}
		return candidates[i] < candidates[j]
	})
	return message + fmt.Sprintf(" Closest known element: '%s'.", candidates[0])
}

func pageWriteDistance(left, right []uint16) int {
	row := make([]int, len(right)+1)
	for index := range row {
		row[index] = index
	}
	for i := 1; i <= len(left); i++ {
		diagonal := row[0]
		row[0] = i
		for j := 1; j <= len(right); j++ {
			old := row[j]
			cost := 1
			if left[i-1] == right[j-1] {
				cost = 0
			}
			row[j] = min(row[j]+1, row[j-1]+1, diagonal+cost)
			diagonal = old
		}
	}
	return row[len(right)]
}

// pageWriteTreeNames is PageParentNameValidation.Names: every string "name" in the tree, in document order.
func pageWriteTreeNames(tree *jnode) []string {
	seen := map[string]bool{}
	names := []string{}
	var walk func(*jnode)
	walk = func(node *jnode) {
		if node == nil {
			return
		}
		if node.isObject() {
			if name := node.get("name"); name != nil && name.kind == jkString && !seen[name.text] {
				seen[name.text] = true
				names = append(names, name.text)
			}
			for _, key := range node.keys {
				walk(node.props[key])
			}
		} else if node.isArray() {
			for _, item := range node.items {
				walk(item)
			}
		}
	}
	walk(tree)
	return names
}

// pageWriteReadDiff is PageParentNameValidation.ReadDiff: the view-config diff read through JSONH.
func pageWriteReadDiff(body string) (*jnode, error) {
	content, ok := readPageSection(body, "SCHEMA_VIEW_CONFIG_DIFF", "SCHEMA_DIFF")
	if !ok {
		return newArray(), nil
	}
	raw, err := lenientJSON(content)
	if err != nil {
		return nil, err
	}
	node, err := parseJNode(raw)
	if err != nil {
		return nil, err
	}
	if !node.isArray() {
		return nil, fmt.Errorf("Error reading JArray from JsonReader. Current JsonReader item is not an array.")
	}
	return node, nil
}

// pageWriteChartPreprocess is clio's ChartConfigKeyOrderPreprocessor: in a crt.ChartWidget config whose last
// key would make the designer's json-differ flatten it, a name-free key (series first) is moved last.
func pageWriteChartPreprocess(body string) string {
	if body == "" {
		return body
	}
	for _, marker := range []string{"SCHEMA_VIEW_CONFIG_DIFF", "SCHEMA_DIFF"} {
		location := pageWriteSectionPattern(marker).FindStringSubmatchIndex(body)
		if location == nil {
			continue
		}
		root, err := parseJNode([]byte(body[location[2]:location[3]]))
		if err != nil || root == nil || !pageWriteFixCharts(root) {
			return body
		}
		return body[:location[2]] + pageWriteRelaxedJSON(root) + body[location[3]:]
	}
	return body
}

func pageWriteFixCharts(node *jnode) bool {
	changed := false
	if node.isObject() {
		if kind := node.get("type"); kind != nil && kind.kind == jkString && strings.EqualFold(kind.text, "crt.ChartWidget") {
			if config := node.get("config"); config.isObject() && pageWriteNeedsFlatten(config) {
				changed = pageWriteMoveNameFreeKeyLast(config) || changed
			}
		}
		for _, key := range node.keys {
			changed = pageWriteFixCharts(node.props[key]) || changed
		}
	} else if node.isArray() {
		for _, item := range node.items {
			changed = pageWriteFixCharts(item) || changed
		}
	}
	return changed
}

func pageWriteMoveNameFreeKeyLast(config *jnode) bool {
	target := ""
	if series := config.get("series"); series != nil && pageWriteNoNameSlot(series) {
		target = "series"
	} else {
		for _, key := range config.keys {
			if pageWriteNoNameSlot(config.props[key]) {
				target = key
				break
			}
		}
	}
	if target == "" {
		return false
	}
	value := config.get(target).clone()
	config.remove(target)
	config.set(target, value)
	return true
}

func pageWriteNoNameSlot(node *jnode) bool {
	switch {
	case node.isObject():
		if _, has := node.props["name"]; has {
			return false
		}
		if len(node.keys) == 0 {
			return true
		}
		return pageWriteNoNameSlot(node.props[node.keys[len(node.keys)-1]])
	case node.isArray():
		if len(node.items) == 0 || !node.items[0].isObject() {
			return true
		}
		_, has := node.items[0].props["name"]
		return !has
	}
	return true
}

func pageWriteNeedsFlatten(node *jnode) bool {
	switch {
	case node.isObject():
		if pageWriteHasNonEmptyName(node) {
			return true
		}
		return len(node.keys) > 0 && pageWriteNeedsFlatten(node.props[node.keys[len(node.keys)-1]])
	case node.isArray():
		return len(node.items) > 0 && node.items[0].isObject() && pageWriteHasNonEmptyName(node.items[0])
	}
	return false
}

func pageWriteHasNonEmptyName(node *jnode) bool {
	name, has := node.props["name"]
	if !has || name == nil || name.kind == jkNull {
		return false
	}
	switch name.kind {
	case jkString:
		return name.text != ""
	case jkArray:
		return len(name.items) > 0
	}
	return true
}

// pageWriteRelaxedJSON writes compact JSON with System.Text.Json's UnsafeRelaxedJsonEscaping: only quotes,
// backslashes and control characters are escaped.
func pageWriteRelaxedJSON(node *jnode) string {
	var buffer bytes.Buffer
	node.writeJSON(&buffer, false)
	return buffer.String()
}
