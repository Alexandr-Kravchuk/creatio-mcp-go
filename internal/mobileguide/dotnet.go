package mobileguide

import (
	"bytes"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

// dict is a .NET Dictionary<string, V> / HashSet<string>: enumeration follows the entries array, a removed
// slot goes on a LIFO free list and is reused by the next add, so the order after a removal is .NET's and
// not plain insertion order. ci selects StringComparer.OrdinalIgnoreCase; the first spelling of a key stays.
type dict[V any] struct {
	ci      bool
	entries []dictEntry[V]
	index   map[string]int
	free    []int
}

type dictEntry[V any] struct {
	key   string
	value V
	used  bool
}

func newDict[V any](ci bool) *dict[V] { return &dict[V]{ci: ci, index: map[string]int{}} }

// newCI is a Dictionary with StringComparer.OrdinalIgnoreCase.
func newCI[V any]() *dict[V] { return newDict[V](true) }

func (d *dict[V]) fold(key string) string {
	if d.ci {
		return foldCase(key)
	}
	return key
}

func (d *dict[V]) get(key string) (V, bool) {
	var zero V
	if d == nil {
		return zero, false
	}
	position, ok := d.index[d.fold(key)]
	if !ok {
		return zero, false
	}
	return d.entries[position].value, true
}

func (d *dict[V]) value(key string) V {
	value, _ := d.get(key)
	return value
}

func (d *dict[V]) has(key string) bool {
	_, ok := d.get(key)
	return ok
}

// set is the indexer: a present key keeps its slot and spelling.
func (d *dict[V]) set(key string, value V) {
	folded := d.fold(key)
	if position, ok := d.index[folded]; ok {
		d.entries[position].value = value
		return
	}
	d.addNew(folded, key, value)
}

// tryAdd is TryAdd / HashSet.Add: false when the key is present.
func (d *dict[V]) tryAdd(key string, value V) bool {
	folded := d.fold(key)
	if _, ok := d.index[folded]; ok {
		return false
	}
	d.addNew(folded, key, value)
	return true
}

func (d *dict[V]) addNew(folded, key string, value V) {
	entry := dictEntry[V]{key: key, value: value, used: true}
	if count := len(d.free); count > 0 {
		position := d.free[count-1]
		d.free = d.free[:count-1]
		d.entries[position] = entry
		d.index[folded] = position
		return
	}
	d.entries = append(d.entries, entry)
	d.index[folded] = len(d.entries) - 1
}

func (d *dict[V]) remove(key string) bool {
	if d == nil {
		return false
	}
	folded := d.fold(key)
	position, ok := d.index[folded]
	if !ok {
		return false
	}
	delete(d.index, folded)
	var zero dictEntry[V]
	d.entries[position] = zero
	d.free = append(d.free, position)
	return true
}

func (d *dict[V]) len() int {
	if d == nil {
		return 0
	}
	return len(d.index)
}

// keys enumerates in .NET order.
func (d *dict[V]) keys() []string {
	if d == nil {
		return nil
	}
	keys := make([]string, 0, len(d.index))
	for _, entry := range d.entries {
		if entry.used {
			keys = append(keys, entry.key)
		}
	}
	return keys
}

// pairs enumerates in .NET order.
func (d *dict[V]) pairs() []dictEntry[V] {
	if d == nil {
		return nil
	}
	pairs := make([]dictEntry[V], 0, len(d.index))
	for _, entry := range d.entries {
		if entry.used {
			pairs = append(pairs, entry)
		}
	}
	return pairs
}

// set of strings (HashSet<string>).
type strSet = dict[struct{}]

func newSet(ci bool, items ...string) *strSet {
	set := newDict[struct{}](ci)
	for _, item := range items {
		set.add(item)
	}
	return set
}

func (d *dict[V]) add(key string) bool {
	var zero V
	return d.tryAdd(key, zero)
}

func (d *dict[V]) contains(key string) bool { return d.has(key) }

// exceptWith is HashSet.ExceptWith.
func (d *dict[V]) exceptWith(other []string) {
	for _, key := range other {
		d.remove(key)
	}
}

// foldCase is the key OrdinalIgnoreCase compares: every character upper-cased with the invariant mapping.
func foldCase(value string) string {
	ascii := true
	for index := 0; index < len(value); index++ {
		if value[index] >= utf8.RuneSelf {
			ascii = false
			break
		}
	}
	if ascii {
		return strings.ToUpper(value)
	}
	return strings.Map(unicode.ToUpper, value)
}

// eqi is string.Equals(a, b, StringComparison.OrdinalIgnoreCase).
func eqi(a, b string) bool {
	return a == b || foldCase(a) == foldCase(b)
}

// eqip compares two nullable strings ordinal-ignore-case (null equals only null).
func eqip(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return eqi(*a, *b)
}

// compareOrdinal is string.CompareOrdinal: UTF-16 code units.
func compareOrdinal(a, b string) int {
	ua, ub := utf16.Encode([]rune(a)), utf16.Encode([]rune(b))
	for index := 0; index < len(ua) && index < len(ub); index++ {
		if ua[index] != ub[index] {
			if ua[index] < ub[index] {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(ua) < len(ub):
		return -1
	case len(ua) > len(ub):
		return 1
	}
	return 0
}

// compareOI is StringComparer.OrdinalIgnoreCase.Compare.
func compareOI(a, b string) int { return compareOrdinal(foldCase(a), foldCase(b)) }

// stableSort is Enumerable.OrderBy: a stable sort.
func stableSort[T any](items []T, less func(a, b T) bool) {
	sort.SliceStable(items, func(i, j int) bool { return less(items[i], items[j]) })
}

// sortedSetOI is SortedSet<string>(StringComparer.OrdinalIgnoreCase): case-insensitively distinct, sorted.
type sortedSetOI struct{ items []string }

func (s *sortedSetOI) add(value string) bool {
	position := sort.Search(len(s.items), func(i int) bool { return compareOI(s.items[i], value) >= 0 })
	if position < len(s.items) && compareOI(s.items[position], value) == 0 {
		return false
	}
	s.items = append(s.items, "")
	copy(s.items[position+1:], s.items[position:])
	s.items[position] = value
	return true
}

func (s *sortedSetOI) contains(value string) bool {
	position := sort.Search(len(s.items), func(i int) bool { return compareOI(s.items[i], value) >= 0 })
	return position < len(s.items) && compareOI(s.items[position], value) == 0
}

// utf16Len is string.Length.
func utf16Len(value string) int { return len(utf16.Encode([]rune(value))) }

// isNullOrEmpty / isBlank are string.IsNullOrEmpty / IsNullOrWhiteSpace on a nullable string.
func isNullOrEmpty(value *string) bool { return value == nil || *value == "" }

func isBlank(value string) bool { return strings.TrimFunc(value, unicode.IsSpace) == "" }

// dotnetTrim is string.Trim().
func dotnetTrim(value string) string { return strings.TrimFunc(value, unicode.IsSpace) }

func strp(value string) *string { return &value }

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// nz is WebToMobileAnalysisService.Nz: null for an empty string.
func nz(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func nzp(value *string) *string {
	if value == nil || *value == "" {
		return nil
	}
	return value
}

// --- Json.NET JPath (the subset viewConfigTemplates use) -------------------------------------------------

type jpathPart struct {
	field   *string
	index   *int
	slice   *[3]*int
	indexes []int
	all     bool
}

var errJPath = errors.New("unexpected character while parsing path")

func parseJPath(path string) ([]jpathPart, error) {
	var parts []jpathPart
	position := 0
	if strings.HasPrefix(path, "$") {
		position = 1
	}
	readName := func() string {
		start := position
		for position < len(path) && path[position] != '.' && path[position] != '[' {
			position++
		}
		return path[start:position]
	}
	first := true
	for position < len(path) {
		switch path[position] {
		case '.':
			if position+1 < len(path) && path[position+1] == '.' {
				return nil, errJPath
			}
			position++
			name := readName()
			if name == "" {
				return nil, errJPath
			}
			if name == "*" {
				parts = append(parts, jpathPart{all: true})
			} else {
				parts = append(parts, jpathPart{field: &name})
			}
		case '[':
			end := strings.IndexByte(path[position:], ']')
			if end < 0 {
				return nil, fmt.Errorf("path ended with open indexer")
			}
			content := strings.TrimSpace(path[position+1 : position+end])
			position += end + 1
			part, err := parseIndexer(content)
			if err != nil {
				return nil, err
			}
			parts = append(parts, part)
		default:
			if !first {
				return nil, errJPath
			}
			name := readName()
			if name == "*" {
				parts = append(parts, jpathPart{all: true})
			} else {
				parts = append(parts, jpathPart{field: &name})
			}
		}
		first = false
	}
	return parts, nil
}

func parseIndexer(content string) (jpathPart, error) {
	if content == "*" {
		return jpathPart{all: true}, nil
	}
	if len(content) >= 2 && (content[0] == '\'' || content[0] == '"') && content[len(content)-1] == content[0] {
		name := content[1 : len(content)-1]
		return jpathPart{field: &name}, nil
	}
	if strings.Contains(content, ":") {
		pieces := strings.Split(content, ":")
		if len(pieces) > 3 {
			return jpathPart{}, errJPath
		}
		var slice [3]*int
		for index, piece := range pieces {
			piece = strings.TrimSpace(piece)
			if piece == "" {
				continue
			}
			value, err := strconv.Atoi(piece)
			if err != nil {
				return jpathPart{}, errJPath
			}
			slice[index] = &value
		}
		return jpathPart{slice: &slice}, nil
	}
	if strings.Contains(content, ",") {
		var indexes []int
		for _, piece := range strings.Split(content, ",") {
			value, err := strconv.Atoi(strings.TrimSpace(piece))
			if err != nil {
				return jpathPart{}, errJPath
			}
			indexes = append(indexes, value)
		}
		return jpathPart{indexes: indexes}, nil
	}
	value, err := strconv.Atoi(content)
	if err != nil {
		return jpathPart{}, errJPath
	}
	return jpathPart{index: &value}, nil
}

// selectTokens is JToken.SelectTokens(path) without errorWhenNoMatch.
func selectTokens(root *jn, path string) ([]*jn, error) {
	parts, err := parseJPath(path)
	if err != nil {
		return nil, err
	}
	current := []*jn{root}
	for _, part := range parts {
		var next []*jn
		for _, token := range current {
			switch {
			case part.field != nil:
				if token.isObj() {
					if value, ok := token.props[*part.field]; ok {
						next = append(next, value)
					}
				}
			case part.all:
				if token.isObj() {
					for _, key := range token.keys {
						next = append(next, token.props[key])
					}
				} else if token.isArr() {
					next = append(next, token.arr...)
				}
			case part.index != nil:
				if token.isArr() && *part.index >= 0 && *part.index < len(token.arr) {
					next = append(next, token.arr[*part.index])
				}
			case part.indexes != nil:
				if token.isArr() {
					for _, index := range part.indexes {
						if index >= 0 && index < len(token.arr) {
							next = append(next, token.arr[index])
						}
					}
				}
			case part.slice != nil:
				if token.isArr() {
					next = append(next, sliceOf(token.arr, *part.slice)...)
				}
			}
		}
		current = next
	}
	return current, nil
}

func sliceOf(items []*jn, slice [3]*int) []*jn {
	count := len(items)
	step := 1
	if slice[2] != nil {
		step = *slice[2]
		if step == 0 {
			return nil
		}
	}
	normalize := func(value *int, fallback int) int {
		if value == nil {
			return fallback
		}
		position := *value
		if position < 0 {
			position += count
		}
		return position
	}
	var result []*jn
	if step > 0 {
		start := max(normalize(slice[0], 0), 0)
		end := min(normalize(slice[1], count), count)
		for index := start; index < end; index += step {
			result = append(result, items[index])
		}
		return result
	}
	start := min(normalize(slice[0], count-1), count-1)
	end := max(normalize(slice[1], -1), -1)
	for index := start; index > end; index += step {
		result = append(result, items[index])
	}
	return result
}

// selectToken is JToken.SelectToken(path): nil for no match; an error for a malformed path or several matches.
func selectToken(root *jn, path string) (*jn, error) {
	tokens, err := selectTokens(root, path)
	if err != nil {
		return nil, err
	}
	if len(tokens) > 1 {
		return nil, errors.New("Path returned multiple tokens.")
	}
	if len(tokens) == 0 {
		return nil, nil
	}
	return tokens[0], nil
}

// writeRelaxedString is McpResultJsonEncoder (UnsafeRelaxedJsonEscaping plus every BMP Format character).
func writeRelaxedString(buffer *bytes.Buffer, value string) {
	buffer.WriteByte('"')
	for _, r := range value {
		switch r {
		case '"':
			buffer.WriteString(`\"`)
			continue
		case '\\':
			buffer.WriteString(`\\`)
			continue
		case '\n':
			buffer.WriteString(`\n`)
			continue
		case '\r':
			buffer.WriteString(`\r`)
			continue
		case '\t':
			buffer.WriteString(`\t`)
			continue
		case '\b':
			buffer.WriteString(`\b`)
			continue
		case '\f':
			buffer.WriteString(`\f`)
			continue
		}
		escape := r < 0x20 || r >= 0x10000 || unicode.In(r, unicode.Cc, unicode.Cf, unicode.Co, unicode.Zl, unicode.Zp) ||
			!unicode.In(r, unicode.L, unicode.M, unicode.N, unicode.P, unicode.S, unicode.Z, unicode.C)
		if !escape {
			buffer.WriteRune(r)
			continue
		}
		for _, unit := range utf16.Encode([]rune{r}) {
			fmt.Fprintf(buffer, `\u%04X`, unit)
		}
	}
	buffer.WriteByte('"')
}
