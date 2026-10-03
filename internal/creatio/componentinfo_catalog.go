package creatio

// The parsed component catalog behind get-component-info: clio's ComponentInfoCatalog (envelope parse,
// ordering and lookup), ComponentInfoGrouping (search, suggestions, list items) and TypeReferenceClosure.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// componentInfoBindings is a JSON object kept in document order with its values verbatim, the way clio
// carries the producer's inputs, outputs and type definitions (IReadOnlyDictionary<string, JsonElement>).
type componentInfoBindings []componentInfoBinding

type componentInfoBinding struct {
	Key   string
	Value json.RawMessage
}

// UnmarshalJSON keeps key order; a repeated key keeps its first position and its last value.
func (b *componentInfoBindings) UnmarshalJSON(data []byte) error {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		*b = nil
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '{' {
		return errors.New("expected a JSON object")
	}
	result := componentInfoBindings{}
	index := map[string]int{}
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return err
		}
		key, _ := keyToken.(string)
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return err
		}
		if position, seen := index[key]; seen {
			result[position].Value = value
			continue
		}
		index[key] = len(result)
		result = append(result, componentInfoBinding{key, value})
	}
	*b = result
	return nil
}

// MarshalJSON writes the object in its kept order.
func (b componentInfoBindings) MarshalJSON() ([]byte, error) {
	var buffer bytes.Buffer
	buffer.WriteByte('{')
	for index, binding := range b {
		if index > 0 {
			buffer.WriteByte(',')
		}
		key, err := json.Marshal(binding.Key)
		if err != nil {
			return nil, err
		}
		buffer.Write(key)
		buffer.WriteByte(':')
		value := binding.Value
		if len(bytes.TrimSpace(value)) == 0 {
			value = json.RawMessage("null")
		}
		buffer.Write(value)
	}
	buffer.WriteByte('}')
	return buffer.Bytes(), nil
}

func (b componentInfoBindings) lookup(key string) (json.RawMessage, bool) {
	for _, binding := range b {
		if binding.Key == key {
			return binding.Value, true
		}
	}
	return nil, false
}

// componentInfoPropertyDefinition is clio's ComponentPropertyDefinition (legacy registry shape).
type componentInfoPropertyDefinition struct {
	Type        string          `json:"type"`
	Description string          `json:"description"`
	Required    *bool           `json:"required,omitempty"`
	Default     json.RawMessage `json:"default,omitempty"`
	Values      []string        `json:"values,omitempty"`
}

// componentInfoProperties keeps the legacy property catalog in document order.
type componentInfoProperties []componentInfoProperty

type componentInfoProperty struct {
	Key   string
	Value componentInfoPropertyDefinition
}

func (p *componentInfoProperties) UnmarshalJSON(data []byte) error {
	var raw componentInfoBindings
	if err := raw.UnmarshalJSON(data); err != nil {
		return err
	}
	result := make(componentInfoProperties, 0, len(raw))
	for _, binding := range raw {
		var definition componentInfoPropertyDefinition
		if !bytes.Equal(bytes.TrimSpace(binding.Value), []byte("null")) {
			if err := json.Unmarshal(binding.Value, &definition); err != nil {
				return err
			}
		}
		result = append(result, componentInfoProperty{binding.Key, definition})
	}
	*p = result
	return nil
}

func (p componentInfoProperties) MarshalJSON() ([]byte, error) {
	var buffer bytes.Buffer
	buffer.WriteByte('{')
	for index, property := range p {
		if index > 0 {
			buffer.WriteByte(',')
		}
		key, _ := json.Marshal(property.Key)
		value, err := json.Marshal(property.Value)
		if err != nil {
			return nil, err
		}
		buffer.Write(key)
		buffer.WriteByte(':')
		buffer.Write(value)
	}
	buffer.WriteByte('}')
	return buffer.Bytes(), nil
}

// componentInfoEntry is clio's ComponentRegistryEntry. JSON names match case-insensitively, as clio's
// deserializer options do.
type componentInfoEntry struct {
	ComponentType           string                       `json:"componentType"`
	Category                string                       `json:"category"`
	Description             string                       `json:"description"`
	CompositeOnly           *bool                        `json:"compositeOnly"`
	Container               *bool                        `json:"container"`
	ParentTypes             []string                     `json:"parentTypes"`
	Properties              componentInfoProperties      `json:"properties"`
	Inputs                  componentInfoBindings        `json:"inputs"`
	Outputs                 componentInfoBindings        `json:"outputs"`
	TypicalChildren         []string                     `json:"typicalChildren"`
	Example                 json.RawMessage              `json:"example"`
	References              *componentInfoEntryReference `json:"references"`
	Synonyms                []string                     `json:"synonyms"`
	UseCases                []string                     `json:"useCases"`
	WhenToUse               *string                      `json:"whenToUse"`
	WhenNotToUse            *string                      `json:"whenNotToUse"`
	AppliesToCustomEntities *bool                        `json:"appliesToCustomEntities"`
	EntityCouplingNote      *string                      `json:"entityCouplingNote"`
}

type componentInfoEntryReference struct {
	Docs            []string              `json:"docs"`
	TypeDefinitions componentInfoBindings `json:"typeDefinitions"`
}

// componentInfoComposite is clio's CompositeDefinition: a Designer element assembled from several
// components, keyed by caption.
type componentInfoComposite struct {
	Caption     string   `json:"caption"`
	Description *string  `json:"description"`
	Docs        []string `json:"docs"`
}

type componentInfoGlobalReferences struct {
	BaseInputs      componentInfoBindings `json:"baseInputs"`
	TypeDefinitions componentInfoBindings `json:"typeDefinitions"`
}

// componentInfoCatalog is clio's ComponentCatalogState.
type componentInfoCatalog struct {
	entries         []*componentInfoEntry
	lookup          map[string]*componentInfoEntry
	composites      []*componentInfoComposite
	global          *componentInfoGlobalReferences
	resolvedVersion string
	source          componentInfoSource
}

const componentInfoStreamName = "Component registry stream"

// componentInfoParseCatalog is ComponentInfoCatalog.LoadFromStream: a top-level array of entries or an
// object carrying a 'components' array, then clio's duplicate checks and ordering.
func componentInfoParseCatalog(payload []byte, resolvedVersion string, source componentInfoSource) (*componentInfoCatalog, error) {
	shapeError := fmt.Errorf("%s must be either a JSON array of component entries or an object with a 'components' array.", componentInfoStreamName)
	trimmed := bytes.TrimSpace(bytes.TrimPrefix(payload, []byte("\xef\xbb\xbf")))
	var entries []*componentInfoEntry
	var composites []*componentInfoComposite
	var global *componentInfoGlobalReferences
	switch {
	case len(trimmed) > 0 && trimmed[0] == '[':
		if err := json.Unmarshal(trimmed, &entries); err != nil {
			return nil, err
		}
	case len(trimmed) > 0 && trimmed[0] == '{':
		var probe map[string]json.RawMessage
		if err := json.Unmarshal(trimmed, &probe); err != nil {
			return nil, err
		}
		components, ok := probe["components"]
		if trimmedComponents := bytes.TrimSpace(components); !ok || len(trimmedComponents) == 0 || trimmedComponents[0] != '[' {
			return nil, shapeError
		}
		var envelope struct {
			Components []*componentInfoEntry          `json:"components"`
			Composites []*componentInfoComposite      `json:"composites"`
			References *componentInfoGlobalReferences `json:"references"`
		}
		if err := json.Unmarshal(trimmed, &envelope); err != nil {
			return nil, err
		}
		entries, composites, global = envelope.Components, envelope.Composites, envelope.References
	default:
		if !json.Valid(trimmed) {
			return nil, errors.New("the component registry payload is not valid JSON")
		}
		return nil, shapeError
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("%s is empty or invalid.", componentInfoStreamName)
	}
	return componentInfoBuildCatalog(entries, composites, global, resolvedVersion, source)
}

func componentInfoBuildCatalog(raw []*componentInfoEntry, rawComposites []*componentInfoComposite,
	global *componentInfoGlobalReferences, resolvedVersion string, source componentInfoSource) (*componentInfoCatalog, error) {
	entries := make([]*componentInfoEntry, 0, len(raw))
	for _, entry := range raw {
		if entry != nil && !componentInfoBlank(entry.ComponentType) {
			entries = append(entries, entry)
		}
	}
	if duplicates := componentInfoDuplicates(entries, func(entry *componentInfoEntry) string { return entry.ComponentType }); len(duplicates) > 0 {
		return nil, fmt.Errorf("%s contains duplicate component types: %s.", componentInfoStreamName, strings.Join(duplicates, ", "))
	}
	componentInfoSortStable(entries, func(entry *componentInfoEntry) string { return entry.ComponentType })
	if len(entries) == 0 {
		return nil, fmt.Errorf("%s does not contain valid component types.", componentInfoStreamName)
	}
	lookup := make(map[string]*componentInfoEntry, len(entries))
	for _, entry := range entries {
		lookup[componentInfoFold(entry.ComponentType)] = entry
	}
	composites := make([]*componentInfoComposite, 0, len(rawComposites))
	blank := 0
	for _, composite := range rawComposites {
		if composite == nil || componentInfoBlank(composite.Caption) {
			blank++
			continue
		}
		composites = append(composites, composite)
	}
	if blank > 0 {
		return nil, fmt.Errorf("%s contains %d composite(s) with a blank caption. Each composite must declare a non-empty caption (its lookup key).",
			componentInfoStreamName, blank)
	}
	if duplicates := componentInfoDuplicates(composites, func(composite *componentInfoComposite) string { return composite.Caption }); len(duplicates) > 0 {
		return nil, fmt.Errorf("%s contains duplicate composite captions: %s.", componentInfoStreamName, strings.Join(duplicates, ", "))
	}
	componentInfoSortStable(composites, func(composite *componentInfoComposite) string { return composite.Caption })
	return &componentInfoCatalog{entries: entries, lookup: lookup, composites: composites, global: global,
		resolvedVersion: resolvedVersion, source: source}, nil
}

// componentInfoDuplicates names the keys that occur more than once (case-insensitively), each by its first
// spelling, in OrdinalIgnoreCase order.
func componentInfoDuplicates[T any](items []T, key func(T) string) []string {
	first := map[string]string{}
	counts := map[string]int{}
	for _, item := range items {
		folded := componentInfoFold(key(item))
		if _, seen := first[folded]; !seen {
			first[folded] = key(item)
		}
		counts[folded]++
	}
	var duplicates []string
	for folded, count := range counts {
		if count > 1 {
			duplicates = append(duplicates, first[folded])
		}
	}
	sort.SliceStable(duplicates, func(i, j int) bool { return componentInfoCompare(duplicates[i], duplicates[j]) < 0 })
	return duplicates
}

func componentInfoSortStable[T any](items []T, key func(T) string) {
	sort.SliceStable(items, func(i, j int) bool { return componentInfoCompare(key(items[i]), key(items[j])) < 0 })
}

// componentInfoFold and componentInfoCompare are StringComparer.OrdinalIgnoreCase: code units compared
// after upper-casing.
func componentInfoFold(value string) string {
	return strings.Map(unicode.ToUpper, value)
}

func componentInfoCompare(left, right string) int {
	return strings.Compare(componentInfoFold(left), componentInfoFold(right))
}

func componentInfoBlank(value string) bool {
	return strings.TrimFunc(value, unicode.IsSpace) == ""
}

func componentInfoContains(value, query string) bool {
	return !componentInfoBlank(value) && strings.Contains(componentInfoFold(value), componentInfoFold(query))
}

func componentInfoContainsPtr(value *string, query string) bool {
	return value != nil && componentInfoContains(*value, query)
}

// componentInfoFilterEntries is ComponentInfoGrouping.FilterEntries: positive selection signals only
// (whenNotToUse is deliberately not searched).
func componentInfoFilterEntries(entries []*componentInfoEntry, search string) []*componentInfoEntry {
	if componentInfoBlank(search) {
		return entries
	}
	query := strings.TrimSpace(search)
	matched := []*componentInfoEntry{}
	for _, entry := range entries {
		if componentInfoMatches(entry, query) {
			matched = append(matched, entry)
		}
	}
	return matched
}

func componentInfoMatches(entry *componentInfoEntry, query string) bool {
	if componentInfoContains(entry.ComponentType, query) || componentInfoContains(entry.Description, query) ||
		componentInfoContainsPtr(entry.WhenToUse, query) {
		return true
	}
	for _, list := range [][]string{entry.Synonyms, entry.UseCases, entry.ParentTypes, entry.TypicalChildren} {
		for _, value := range list {
			if componentInfoContains(value, query) {
				return true
			}
		}
	}
	for _, property := range entry.Properties {
		if componentInfoContains(property.Key, query) || componentInfoContains(property.Value.Type, query) ||
			componentInfoContains(property.Value.Description, query) {
			return true
		}
		for _, value := range property.Value.Values {
			if componentInfoContains(value, query) {
				return true
			}
		}
	}
	return componentInfoBindingsMatch(entry.Inputs, query) || componentInfoBindingsMatch(entry.Outputs, query)
}

// componentInfoBindingsMatch looks at a binding's key and its well-known string fields: type, description
// and the string members of values.
func componentInfoBindingsMatch(bindings componentInfoBindings, query string) bool {
	for _, binding := range bindings {
		if componentInfoContains(binding.Key, query) {
			return true
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(binding.Value, &fields) != nil || fields == nil {
			continue
		}
		for _, name := range []string{"type", "description"} {
			var text string
			if raw, ok := fields[name]; ok && json.Unmarshal(raw, &text) == nil && componentInfoContains(text, query) {
				return true
			}
		}
		var values []json.RawMessage
		if raw, ok := fields["values"]; ok && json.Unmarshal(raw, &values) == nil {
			for _, value := range values {
				var text string
				if json.Unmarshal(value, &text) == nil && componentInfoContains(text, query) {
					return true
				}
			}
		}
	}
	return false
}

// componentInfoFilterComposites matches caption and description, case-insensitively.
func componentInfoFilterComposites(composites []*componentInfoComposite, search string) []*componentInfoComposite {
	if len(composites) == 0 {
		return []*componentInfoComposite{}
	}
	if componentInfoBlank(search) {
		return composites
	}
	query := strings.TrimSpace(search)
	matched := []*componentInfoComposite{}
	for _, composite := range composites {
		if componentInfoContains(composite.Caption, query) || componentInfoContainsPtr(composite.Description, query) {
			matched = append(matched, composite)
		}
	}
	return matched
}

// componentInfoSuggest is SuggestForUnknown: the closest types by case-insensitive edit distance, ties in
// OrdinalIgnoreCase order, at most max.
func componentInfoSuggest(entries []*componentInfoEntry, componentType, search string, max int) []*componentInfoEntry {
	pool := entries
	if !componentInfoBlank(search) {
		pool = componentInfoFilterEntries(entries, search)
	}
	target := strings.TrimSpace(componentType)
	type ranked struct {
		entry    *componentInfoEntry
		distance int
	}
	ordered := make([]ranked, 0, len(pool))
	for _, entry := range pool {
		ordered = append(ordered, ranked{entry, componentInfoLevenshtein(entry.ComponentType, target)})
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].distance != ordered[j].distance {
			return ordered[i].distance < ordered[j].distance
		}
		return componentInfoCompare(ordered[i].entry.ComponentType, ordered[j].entry.ComponentType) < 0
	})
	if max < 0 {
		max = 0
	}
	if len(ordered) > max {
		ordered = ordered[:max]
	}
	result := make([]*componentInfoEntry, 0, len(ordered))
	for _, item := range ordered {
		result = append(result, item.entry)
	}
	return result
}

// componentInfoLevenshtein is McpToolArgumentSupport.LevenshteinDistance over lower-cased text.
func componentInfoLevenshtein(source, target string) int {
	left := []rune(strings.Map(unicode.ToLower, source))
	right := []rune(strings.Map(unicode.ToLower, target))
	if len(left) == 0 {
		return len(right)
	}
	if len(right) == 0 {
		return len(left)
	}
	previous := make([]int, len(right)+1)
	current := make([]int, len(right)+1)
	for j := range previous {
		previous[j] = j
	}
	for i := 1; i <= len(left); i++ {
		current[0] = i
		for j := 1; j <= len(right); j++ {
			cost := 1
			if left[i-1] == right[j-1] {
				cost = 0
			}
			current[j] = min(previous[j]+1, current[j-1]+1, previous[j-1]+cost)
		}
		previous, current = current, previous
	}
	return previous[len(right)]
}

// componentInfoMergeBindings puts the per-component bindings over the global ones: a shared key keeps the
// global position and takes the component's value. Nil when both are empty.
func componentInfoMergeBindings(global, local componentInfoBindings) componentInfoBindings {
	switch {
	case len(global) == 0 && len(local) == 0:
		return nil
	case len(global) == 0:
		return local
	case len(local) == 0:
		return global
	}
	merged := append(componentInfoBindings{}, global...)
	index := make(map[string]int, len(merged))
	for position, binding := range merged {
		index[binding.Key] = position
	}
	for _, binding := range local {
		if position, ok := index[binding.Key]; ok {
			merged[position].Value = binding.Value
			continue
		}
		index[binding.Key] = len(merged)
		merged = append(merged, binding)
	}
	return merged
}

var componentInfoIdentifier = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)

// componentInfoTypeClosure is TypeReferenceClosure.Resolve: every per-component type definition, plus the
// definitions reachable from the type strings of the inputs, outputs and those definitions, looked up in the
// per-component bag first and then the global one.
func componentInfoTypeClosure(inputs, outputs, local, global componentInfoBindings) componentInfoBindings {
	if len(local) == 0 && len(global) == 0 {
		return nil
	}
	resolved := componentInfoBindings{}
	seen := map[string]bool{}
	var queue []string
	for _, definition := range local {
		if !seen[definition.Key] {
			seen[definition.Key] = true
			resolved = append(resolved, definition)
		} else {
			for index := range resolved {
				if resolved[index].Key == definition.Key {
					resolved[index].Value = definition.Value
				}
			}
		}
		queue = componentInfoQueueIdentifiers(definition.Value, queue)
	}
	for _, bindings := range []componentInfoBindings{inputs, outputs} {
		for _, binding := range bindings {
			queue = componentInfoQueueIdentifiers(binding.Value, queue)
		}
	}
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		if seen[name] {
			continue
		}
		value, ok := local.lookup(name)
		if !ok {
			value, ok = global.lookup(name)
		}
		if !ok {
			continue
		}
		seen[name] = true
		resolved = append(resolved, componentInfoBinding{name, value})
		queue = componentInfoQueueIdentifiers(value, queue)
	}
	if len(resolved) == 0 {
		return nil
	}
	return resolved
}

func componentInfoQueueIdentifiers(raw json.RawMessage, queue []string) []string {
	value, err := parseOrderedJSON(raw)
	if err != nil {
		return queue
	}
	return componentInfoWalkIdentifiers(value, queue)
}

func componentInfoWalkIdentifiers(value any, queue []string) []string {
	switch node := value.(type) {
	case *orderedObject:
		for _, key := range node.keys {
			switch key {
			case "description", "default", "values", "valueSource":
				continue
			}
			child := node.values[key]
			if text, ok := child.(string); ok && (key == "type" || key == "keyType" || key == "valueType") {
				for _, token := range componentInfoIdentifier.FindAllString(text, -1) {
					if token[0] >= 'A' && token[0] <= 'Z' {
						queue = append(queue, token)
					}
				}
				continue
			}
			queue = componentInfoWalkIdentifiers(child, queue)
		}
	case []any:
		for _, item := range node {
			queue = componentInfoWalkIdentifiers(item, queue)
		}
	}
	return queue
}
