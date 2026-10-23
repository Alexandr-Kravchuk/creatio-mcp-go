package creatio

import (
	"fmt"
	"slices"
	"unicode/utf16"
)

// clio's PageParentNameValidation: the parentName of every insert and move in a web diff must name an element
// the diff defines or one the caller lists in known-containers (the page's inherited elements).

const (
	pageParentOperationKey   = "operation"
	pageParentUnverifiedHint = " Supply known-containers from the page's inherited bundle to verify template parents."
	// clio compares at most this many known names, each cut to pageParentDistanceUnits UTF-16 units.
	pageParentClosestCandidates = 512
	pageParentDistanceUnits     = 256
)

// pageParentCastError is the InvalidCastException Newtonsoft's Value<string>() throws for a container.
type pageParentCastError struct{ kind jsonKind }

func (e pageParentCastError) Error() string {
	name := "JObject"
	if e.kind == jkArray {
		name = "JArray"
	}
	return fmt.Sprintf("Cannot cast Newtonsoft.Json.Linq.%s to Newtonsoft.Json.Linq.JToken.", name)
}

// pageParentText is JToken.Value<string>(key) on an object: nil for a missing or null value, the text of a
// scalar, an error for a container.
func pageParentText(node *jnode, key string) (*string, error) {
	value := node.get(key)
	if value != nil && (value.kind == jkObject || value.kind == jkArray) {
		return nil, pageParentCastError{kind: value.kind}
	}
	return value.stringValue(), nil
}

func pageParentIs(node *jnode, operation string) (bool, error) {
	value, err := pageParentText(node, pageParentOperationKey)
	return value != nil && *value == operation, err
}

// pageParentSet is a HashSet<string> with .NET's enumeration order: entries in slot order, a removed slot
// reused by the next add (last freed, first reused). The order decides which names clio's closest-name hint
// considers when there are more than 512.
type pageParentSet struct {
	slots []string
	used  []bool
	index map[string]int
	free  []int
}

func newPageParentSet() *pageParentSet { return &pageParentSet{index: map[string]int{}} }

func (s *pageParentSet) add(value string) {
	if _, ok := s.index[value]; ok {
		return
	}
	if n := len(s.free); n > 0 {
		slot := s.free[n-1]
		s.free = s.free[:n-1]
		s.slots[slot], s.used[slot], s.index[value] = value, true, slot
		return
	}
	s.slots, s.used = append(s.slots, value), append(s.used, true)
	s.index[value] = len(s.slots) - 1
}

func (s *pageParentSet) remove(value string) {
	if slot, ok := s.index[value]; ok {
		delete(s.index, value)
		s.used[slot] = false
		s.free = append(s.free, slot)
	}
}

func (s *pageParentSet) has(value string) bool {
	_, ok := s.index[value]
	return ok
}

func (s *pageParentSet) values() []string {
	values := make([]string, 0, len(s.index))
	for slot, value := range s.slots {
		if s.used[slot] {
			values = append(values, value)
		}
	}
	return values
}

// pageParentNames adds every string "name" of an object anywhere in the tree, depth first: an object's own
// name before its properties' values (clio's Names/Collect).
func pageParentNames(node *jnode, names *pageParentSet) {
	if node.isObject() {
		if name := node.get("name"); name != nil && name.kind == jkString {
			names.add(name.text)
		}
		for _, key := range node.keys {
			pageParentNames(node.props[key], names)
		}
	}
	if node.isArray() {
		for _, item := range node.items {
			pageParentNames(item, names)
		}
	}
}

// PageParentResult is clio's SchemaValidationResult for the parent check.
type PageParentResult struct {
	Errors   []string
	Warnings []string
}

// ValidatePageParents is PageParentNameValidation.Validate for a web body whose sections passed: an unresolved
// parent warns when known is nil (no known-containers) and fails when it is not, even empty.
func ValidatePageParents(body string, known []string) (PageParentResult, error) {
	var result PageParentResult
	diff, err := pageParentDiff(body)
	if err != nil || diff == nil {
		return result, err
	}
	operations := []*jnode{}
	for _, item := range diff.items {
		if item.isObject() {
			operations = append(operations, item)
		}
	}
	names, err := pageParentKnownNames(operations, known)
	if err != nil {
		return result, err
	}
	parents, err := pageParentOperations(operations)
	if err != nil {
		return result, err
	}
	for _, operation := range parents {
		name, _ := pageParentText(operation, "name")
		move, _ := pageParentIs(operation, "move")
		if known != nil && move && !names.has(pageParentOr(name)) {
			continue
		}
		parent, err := pageParentText(operation, "parentName")
		if err != nil {
			return result, err
		}
		if parent == nil || *parent == "" || names.has(*parent) {
			continue
		}
		message := pageParentDiagnostic(name, *parent, names.values())
		if known == nil {
			result.Warnings = append(result.Warnings, message+pageParentUnverifiedHint)
		} else {
			result.Errors = append(result.Errors, message)
		}
	}
	return result, nil
}

// pageParentDiff is ReadDiff: the SCHEMA_VIEW_CONFIG_DIFF section, else SCHEMA_DIFF, as an array; nil when
// the body has neither. The content check already parsed the section, so it is valid here.
func pageParentDiff(body string) (*jnode, error) {
	text, ok := readPageSection(body, "SCHEMA_VIEW_CONFIG_DIFF", "SCHEMA_DIFF")
	if !ok {
		return nil, nil
	}
	cleaned, err := pageValidateCleanJSON(text)
	if err != nil {
		return nil, err
	}
	diff, err := parseJNode([]byte(cleaned))
	if err != nil {
		return nil, err
	}
	if !diff.isArray() {
		return nil, fmt.Errorf("Error reading JArray from JsonReader. Current JsonReader item is not an array.")
	}
	return diff, nil
}

func pageParentOr(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// pageParentRemovals are the names of remove operations that drop the element, not some of its properties.
func pageParentRemovals(operations []*jnode, each func(name *string)) error {
	for _, operation := range operations {
		remove, err := pageParentIs(operation, "remove")
		if err != nil {
			return err
		}
		if remove && !operation.get("properties").isArray() {
			name, err := pageParentText(operation, "name")
			if err != nil {
				return err
			}
			each(name)
		}
	}
	return nil
}

// pageParentKnownNames is CollectKnownNames: the inherited names without the removed elements, then every
// inserted element and every name inside its values (other than its own name key).
func pageParentKnownNames(operations []*jnode, known []string) (*pageParentSet, error) {
	names := newPageParentSet()
	for _, name := range known {
		names.add(name)
	}
	if err := pageParentRemovals(operations, func(name *string) { names.remove(pageParentOr(name)) }); err != nil {
		return nil, err
	}
	for _, operation := range operations {
		insert, err := pageParentIs(operation, "insert")
		if err != nil {
			return nil, err
		}
		if !insert {
			continue
		}
		name, err := pageParentText(operation, "name")
		if err != nil {
			return nil, err
		}
		if name != nil {
			names.add(*name)
		}
		if values := operation.get("values"); values.isObject() {
			for _, key := range values.keys {
				if key == "name" {
					continue
				}
				nested := newPageParentSet()
				pageParentNames(values.props[key], nested)
				for _, value := range nested.values() {
					names.add(value)
				}
			}
		}
	}
	return names, nil
}

// pageParentOperations is ParentOperations: inserts, and moves of an element no remove operation drops.
func pageParentOperations(operations []*jnode) ([]*jnode, error) {
	removed := map[string]bool{}
	removedNull := false
	if err := pageParentRemovals(operations, func(name *string) {
		if name == nil {
			removedNull = true
		} else {
			removed[*name] = true
		}
	}); err != nil {
		return nil, err
	}
	parents := []*jnode{}
	for _, operation := range operations {
		insert, err := pageParentIs(operation, "insert")
		if err != nil {
			return nil, err
		}
		move, _ := pageParentIs(operation, "move")
		if !insert && move {
			name, err := pageParentText(operation, "name")
			if err != nil {
				return nil, err
			}
			if name == nil && removedNull || name != nil && removed[*name] {
				continue
			}
		}
		if insert || move {
			parents = append(parents, operation)
		}
	}
	return parents, nil
}

// pageParentDiagnostic is clio's Diagnostic: the closest of the first 512 known names other than the child,
// by edit distance over the first 256 UTF-16 units, ties broken by ordinal order.
func pageParentDiagnostic(child *string, parent string, known []string) string {
	message := fmt.Sprintf("Element '%s' has unresolved parentName '%s'.", pageParentOr(child), parent)
	target := pageParentUnits(parent)
	var closest []uint16
	closestDistance, considered := 0, 0
	for _, name := range known {
		if name == "" || child != nil && name == *child {
			continue
		}
		if considered == pageParentClosestCandidates {
			break
		}
		considered++
		units := utf16.Encode([]rune(name))
		distance := pageParentDistance(target, units[:min(len(units), pageParentDistanceUnits)])
		if closest == nil || distance < closestDistance || distance == closestDistance && slices.Compare(units, closest) < 0 {
			closest, closestDistance = units, distance
		}
	}
	if closest == nil {
		return message
	}
	return message + fmt.Sprintf(" Closest known element: '%s'.", string(utf16.Decode(closest)))
}

func pageParentUnits(text string) []uint16 {
	units := utf16.Encode([]rune(text))
	return units[:min(len(units), pageParentDistanceUnits)]
}

// pageParentDistance is clio's single-row Levenshtein distance.
func pageParentDistance(left, right []uint16) int {
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
