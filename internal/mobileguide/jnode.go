package mobileguide

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/big"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
)

// The engine works on two JSON stacks, as clio's does: Newtonsoft JToken (the tree walk, template rendering)
// and System.Text.Json JsonNode (built values, data sections). One ordered DOM stands in for both; the code
// that uses a node knows which stack it is imitating. The differences that reach the answer are kept:
//
//   - a Newtonsoft parse turns an ISO date-looking string into a Date token, normalizes number text (integers
//     re-printed, other numbers as .NET doubles with ".0") and keeps a JSON null as a token;
//   - a System.Text.Json parse keeps number text verbatim and stores a JSON null as a nil child.
//
// jsNull marks a Newtonsoft JValue null; a nil *jn is a System.Text.Json null (and an absent Newtonsoft token).

type jkind uint8

const (
	jsNull jkind = iota
	jsBool
	jsNumber
	jsString
	jsArray
	jsObject
	jsDate
)

type jn struct {
	k     jkind
	s     string // string value, number text, or a date's ISO text as Newtonsoft writes it
	b     bool
	float bool // Newtonsoft number type: Float (double) rather than Integer
	arr   []*jn
	keys  []string
	props map[string]*jn
	date  time.Time
	dkind dateKind
}

type dateKind uint8

const (
	dateUnspecified dateKind = iota
	dateUTC
	dateLocal
)

func newObj() *jn                { return &jn{k: jsObject, props: map[string]*jn{}} }
func newArr(items ...*jn) *jn    { return &jn{k: jsArray, arr: append([]*jn{}, items...)} }
func newStr(value string) *jn    { return &jn{k: jsString, s: value} }
func newBoolNode(value bool) *jn { return &jn{k: jsBool, b: value} }
func newIntNode(value int) *jn   { return &jn{k: jsNumber, s: strconv.Itoa(value)} }
func newTokenNull() *jn          { return &jn{k: jsNull} }

func (n *jn) isObj() bool  { return n != nil && n.k == jsObject }
func (n *jn) isArr() bool  { return n != nil && n.k == jsArray }
func (n *jn) isStr() bool  { return n != nil && n.k == jsString }
func (n *jn) isNull() bool { return n == nil || n.k == jsNull }

// get is the indexer of both JObject and JsonObject: an exact (case-sensitive) key, nil when absent or when
// the node is not an object.
func (n *jn) get(key string) *jn {
	if !n.isObj() {
		return nil
	}
	return n.props[key]
}

// has is ContainsKey / TryGetPropertyValue's found flag.
func (n *jn) has(key string) bool {
	if !n.isObj() {
		return false
	}
	_, ok := n.props[key]
	return ok
}

// set adds or replaces a property; a replaced key keeps its position (both stacks).
func (n *jn) set(key string, value *jn) {
	if _, exists := n.props[key]; !exists {
		n.keys = append(n.keys, key)
	}
	n.props[key] = value
}

func (n *jn) remove(key string) bool {
	if !n.isObj() {
		return false
	}
	if _, exists := n.props[key]; !exists {
		return false
	}
	delete(n.props, key)
	for index, name := range n.keys {
		if name == key {
			n.keys = append(n.keys[:index:index], n.keys[index+1:]...)
			break
		}
	}
	return true
}

func (n *jn) count() int {
	switch {
	case n.isObj():
		return len(n.keys)
	case n.isArr():
		return len(n.arr)
	}
	return 0
}

// keyList is a snapshot of the object's keys in order.
func (n *jn) keyList() []string {
	if !n.isObj() {
		return nil
	}
	return append([]string{}, n.keys...)
}

func (n *jn) insertAt(index int, value *jn) {
	n.arr = append(n.arr, nil)
	copy(n.arr[index+1:], n.arr[index:])
	n.arr[index] = value
}

func (n *jn) removeAt(index int) {
	n.arr = append(n.arr[:index:index], n.arr[index+1:]...)
}

func (n *jn) clone() *jn {
	if n == nil {
		return nil
	}
	copied := *n
	switch n.k {
	case jsArray:
		copied.arr = make([]*jn, len(n.arr))
		for index, item := range n.arr {
			copied.arr[index] = item.clone()
		}
	case jsObject:
		copied.keys = append([]string{}, n.keys...)
		copied.props = make(map[string]*jn, len(n.props))
		for key, value := range n.props {
			copied.props[key] = value.clone()
		}
	}
	return &copied
}

// --- parsing ---------------------------------------------------------------------------------------------

// parseSTJ is JsonNode.Parse: number text verbatim, JSON null as a nil child. A root "null" is nil.
func parseSTJ(data []byte) (*jn, error) {
	return parseWith(data, false)
}

// parseNewtonsoft is JToken.Parse with Json.NET's default reader settings.
func parseNewtonsoft(data []byte) (*jn, error) {
	return parseWith(data, true)
}

func parseWith(data []byte, newtonsoft bool) (*jn, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	node, err := readNode(decoder, newtonsoft)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, fmt.Errorf("additional text found after the JSON value")
	}
	return node, nil
}

func mustSTJ(data []byte) *jn {
	node, err := parseSTJ(data)
	if err != nil {
		panic(err)
	}
	return node
}

func readNode(decoder *json.Decoder, newtonsoft bool) (*jn, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	switch value := token.(type) {
	case json.Delim:
		switch value {
		case '{':
			object := newObj()
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return nil, err
				}
				child, err := readNode(decoder, newtonsoft)
				if err != nil {
					return nil, err
				}
				object.set(keyToken.(string), child)
			}
			_, err := decoder.Token()
			return object, err
		case '[':
			array := newArr()
			for decoder.More() {
				child, err := readNode(decoder, newtonsoft)
				if err != nil {
					return nil, err
				}
				array.arr = append(array.arr, child)
			}
			_, err := decoder.Token()
			return array, err
		}
	case string:
		if newtonsoft {
			return newtonsoftString(value), nil
		}
		return newStr(value), nil
	case bool:
		return newBoolNode(value), nil
	case nil:
		if newtonsoft {
			return newTokenNull(), nil
		}
		return nil, nil
	case json.Number:
		if newtonsoft {
			return newtonsoftNumber(string(value)), nil
		}
		return &jn{k: jsNumber, s: string(value)}, nil
	}
	return nil, fmt.Errorf("unexpected JSON token %v", token)
}

// newtonsoftNumber is how JsonTextReader reads a number: an integer as Int64 (BigInteger past it), anything
// with a fraction or exponent as a double written back in .NET's round-trip form with ".0".
func newtonsoftNumber(text string) *jn {
	if !strings.ContainsAny(text, ".eE") {
		value, ok := new(big.Int).SetString(text, 10)
		if !ok {
			return &jn{k: jsNumber, s: text}
		}
		return &jn{k: jsNumber, s: value.String()}
	}
	value, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return &jn{k: jsNumber, s: text, float: true}
	}
	return &jn{k: jsNumber, s: dotnetDouble(value, true), float: true}
}

// dotnetDouble formats a double as .NET Core does (shortest round-trip, scientific below 1E-05 and from
// 1E+15); withPoint adds Json.NET's ".0" to an integral value.
func dotnetDouble(value float64, withPoint bool) string {
	if math.IsNaN(value) {
		return "NaN"
	}
	if math.IsInf(value, 1) {
		return "Infinity"
	}
	if math.IsInf(value, -1) {
		return "-Infinity"
	}
	absolute := math.Abs(value)
	var text string
	if absolute != 0 && (absolute < 1e-5 || absolute >= 1e15) {
		text = strconv.FormatFloat(value, 'E', -1, 64)
		mantissa, exponent, _ := strings.Cut(text, "E")
		sign := exponent[:1]
		digits := strings.TrimLeft(exponent[1:], "0")
		for len(digits) < 2 {
			digits = "0" + digits
		}
		text = mantissa + "E" + sign + digits
	} else {
		text = strconv.FormatFloat(value, 'f', -1, 64)
	}
	if withPoint && !strings.ContainsAny(text, ".E") {
		text += ".0"
	}
	return text
}

// newtonsoftString is a string token, or a Date token when Json.NET's default DateParseHandling reads it as one.
func newtonsoftString(value string) *jn {
	if when, kind, ok := parseNewtonsoftDate(value); ok {
		return &jn{k: jsDate, date: when, dkind: kind, s: formatNewtonsoftDate(when, kind)}
	}
	return newStr(value)
}

// parseNewtonsoftDate is DateTimeUtils.TryParseDateTimeIso as JsonTextReader applies it: 19 to 40 characters,
// a digit first and 'T' at index 10, "yyyy-MM-ddTHH:mm:ss", up to seven fraction digits, an optional zone.
// An offset is converted to local time (Kind Local), "Z" is UTC, no zone is Unspecified.
func parseNewtonsoftDate(text string) (time.Time, dateKind, bool) {
	if len(text) < 19 || len(text) > 40 || text[0] < '0' || text[0] > '9' || text[10] != 'T' {
		return time.Time{}, 0, false
	}
	digits := func(from, count int) (int, bool) {
		if from+count > len(text) {
			return 0, false
		}
		value := 0
		for index := from; index < from+count; index++ {
			if text[index] < '0' || text[index] > '9' {
				return 0, false
			}
			value = value*10 + int(text[index]-'0')
		}
		return value, true
	}
	year, ok1 := digits(0, 4)
	month, ok2 := digits(5, 2)
	day, ok3 := digits(8, 2)
	if !ok1 || !ok2 || !ok3 || text[4] != '-' || text[7] != '-' || year < 1 || month < 1 || month > 12 || day < 1 ||
		day > time.Date(year, time.Month(month)+1, 0, 0, 0, 0, 0, time.UTC).Day() {
		return time.Time{}, 0, false
	}
	hour, ok4 := digits(11, 2)
	minute, ok5 := digits(14, 2)
	second, ok6 := digits(17, 2)
	if !ok4 || !ok5 || !ok6 || text[13] != ':' || text[16] != ':' || hour > 23 || minute > 59 || second > 59 {
		return time.Time{}, 0, false
	}
	position := 19
	ticks := 0
	if position < len(text) && text[position] == '.' {
		position++
		count := 0
		for position < len(text) && count < 7 && text[position] >= '0' && text[position] <= '9' {
			ticks = ticks*10 + int(text[position]-'0')
			count++
			position++
		}
		if count == 0 {
			return time.Time{}, 0, false
		}
		for ; count < 7; count++ {
			ticks *= 10
		}
	}
	base := time.Date(year, time.Month(month), day, hour, minute, second, ticks*100, time.UTC)
	if position == len(text) {
		return base, dateUnspecified, true
	}
	switch text[position] {
	case 'Z', 'z':
		if position+1 != len(text) {
			return time.Time{}, 0, false
		}
		return base, dateUTC, true
	case '+', '-':
		west := text[position] == '-'
		zoneHour, ok := digits(position+1, 2)
		if !ok {
			return time.Time{}, 0, false
		}
		position += 3
		zoneMinute := 0
		if position < len(text) && text[position] == ':' {
			if zoneMinute, ok = digits(position+1, 2); !ok {
				return time.Time{}, 0, false
			}
			position += 3
		} else if position < len(text) {
			if zoneMinute, ok = digits(position, 2); !ok {
				return time.Time{}, 0, false
			}
			position += 2
		}
		if position != len(text) {
			return time.Time{}, 0, false
		}
		offset := time.Duration(zoneHour)*time.Hour + time.Duration(zoneMinute)*time.Minute
		if west {
			offset = -offset
		}
		utc := base.Add(-offset)
		return utc.In(time.Local), dateLocal, true
	}
	return time.Time{}, 0, false
}

// formatNewtonsoftDate is how JsonTextWriter writes a DateTime (IsoDateFormat, RoundtripKind).
func formatNewtonsoftDate(when time.Time, kind dateKind) string {
	var buffer strings.Builder
	fmt.Fprintf(&buffer, "%04d-%02d-%02dT%02d:%02d:%02d", when.Year(), int(when.Month()), when.Day(), when.Hour(), when.Minute(), when.Second())
	if fraction := when.Nanosecond() / 100; fraction != 0 {
		digits := strings.TrimRight(fmt.Sprintf("%07d", fraction), "0")
		buffer.WriteString("." + digits)
	}
	switch kind {
	case dateUTC:
		buffer.WriteString("Z")
	case dateLocal:
		_, offset := when.Zone()
		sign := '+'
		if offset < 0 {
			sign = '-'
			offset = -offset
		}
		fmt.Fprintf(&buffer, "%c%02d:%02d", sign, offset/3600, offset%3600/60)
	}
	return buffer.String()
}

// --- conversions between the stacks ----------------------------------------------------------------------

// toJToken is JToken.Parse(node.ToJsonString()): a deep copy re-read by Json.NET.
func toJToken(n *jn) *jn {
	if n == nil {
		return newTokenNull()
	}
	switch n.k {
	case jsNull:
		return newTokenNull()
	case jsNumber:
		return newtonsoftNumber(n.s)
	case jsString:
		return newtonsoftString(n.s)
	case jsDate:
		return newtonsoftString(n.s)
	case jsArray:
		array := &jn{k: jsArray, arr: make([]*jn, len(n.arr))}
		for index, item := range n.arr {
			array.arr[index] = toJToken(item)
		}
		return array
	case jsObject:
		object := newObj()
		for _, key := range n.keys {
			object.set(key, toJToken(n.props[key]))
		}
		return object
	}
	return n.clone()
}

// toJsonNode is JsonNode.Parse(token.ToString(Formatting.None)): a deep copy re-read by System.Text.Json. A
// null token becomes a nil child (a root null token gives nil).
func toJsonNode(n *jn) *jn {
	if n == nil || n.k == jsNull {
		return nil
	}
	switch n.k {
	case jsDate:
		return newStr(n.s)
	case jsNumber:
		return &jn{k: jsNumber, s: n.s}
	case jsArray:
		array := &jn{k: jsArray, arr: make([]*jn, len(n.arr))}
		for index, item := range n.arr {
			array.arr[index] = toJsonNode(item)
		}
		return array
	case jsObject:
		object := newObj()
		for _, key := range n.keys {
			object.set(key, toJsonNode(n.props[key]))
		}
		return object
	}
	return &jn{k: n.k, s: n.s, b: n.b}
}

// stjValue is a JsonValue built from a .NET value (JsonValue.Create); the stack only matters for numbers.
func stjString(value *string) *jn {
	if value == nil {
		return nil
	}
	return newStr(*value)
}

// --- writing ---------------------------------------------------------------------------------------------

type jsonStyle uint8

const (
	styleSTJ        jsonStyle = iota // System.Text.Json default encoder (ToJsonString)
	styleNewtonsoft                  // Json.NET Formatting.None
	styleRelaxed                     // clio's MCP result encoder
)

// toJSON writes compact JSON in the given style.
func (n *jn) toJSON(style jsonStyle) string {
	var buffer bytes.Buffer
	n.write(&buffer, style)
	return buffer.String()
}

func (n *jn) write(buffer *bytes.Buffer, style jsonStyle) {
	if n == nil {
		buffer.WriteString("null")
		return
	}
	switch n.k {
	case jsNull:
		buffer.WriteString("null")
	case jsBool:
		buffer.WriteString(strconv.FormatBool(n.b))
	case jsNumber:
		buffer.WriteString(n.s)
	case jsString, jsDate:
		writeString(buffer, n.s, style)
	case jsArray:
		buffer.WriteByte('[')
		for index, item := range n.arr {
			if index > 0 {
				buffer.WriteByte(',')
			}
			item.write(buffer, style)
		}
		buffer.WriteByte(']')
	case jsObject:
		buffer.WriteByte('{')
		for index, key := range n.keys {
			if index > 0 {
				buffer.WriteByte(',')
			}
			writeString(buffer, key, style)
			buffer.WriteByte(':')
			n.props[key].write(buffer, style)
		}
		buffer.WriteByte('}')
	}
}

func writeString(buffer *bytes.Buffer, value string, style jsonStyle) {
	if style == styleRelaxed {
		writeRelaxedString(buffer, value)
		return
	}
	buffer.WriteByte('"')
	for _, r := range value {
		switch r {
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
		case '\\':
			buffer.WriteString(`\\`)
			continue
		case '"':
			if style == styleNewtonsoft {
				buffer.WriteString(`\"`)
				continue
			}
		}
		escape := r < 0x20
		if style == styleSTJ {
			escape = escape || r > 0x7E || strings.ContainsRune("\"&'+<>`", r)
		} else {
			escape = escape || r == 0x85 || r == 0x2028 || r == 0x2029
		}
		if !escape {
			buffer.WriteRune(r)
			continue
		}
		for _, unit := range utf16.Encode([]rune{r}) {
			if style == styleNewtonsoft {
				fmt.Fprintf(buffer, `\u%04x`, unit)
			} else {
				fmt.Fprintf(buffer, `\u%04X`, unit)
			}
		}
	}
	buffer.WriteByte('"')
}

// indented writes Json.NET's Formatting.Indented (JToken.ToString()) or System.Text.Json's WriteIndented
// (JsonNode.ToString()); both indent by two spaces with the platform newline.
func (n *jn) indented(style jsonStyle) string {
	var buffer bytes.Buffer
	n.writeIndented(&buffer, style, 0)
	return buffer.String()
}

func platformNewline() string {
	if runtime.GOOS == "windows" {
		return "\r\n"
	}
	return "\n"
}

func (n *jn) writeIndented(buffer *bytes.Buffer, style jsonStyle, depth int) {
	newline := platformNewline()
	indent := func(level int) {
		buffer.WriteString(newline)
		buffer.WriteString(strings.Repeat("  ", level))
	}
	switch {
	case n.isArr():
		if len(n.arr) == 0 {
			buffer.WriteString("[]")
			return
		}
		buffer.WriteByte('[')
		for index, item := range n.arr {
			if index > 0 {
				buffer.WriteByte(',')
			}
			indent(depth + 1)
			item.writeIndented(buffer, style, depth+1)
		}
		indent(depth)
		buffer.WriteByte(']')
	case n.isObj():
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
			writeString(buffer, key, style)
			buffer.WriteString(": ")
			n.props[key].writeIndented(buffer, style, depth+1)
		}
		indent(depth)
		buffer.WriteByte('}')
	default:
		n.write(buffer, style)
	}
}

// tokenString is JToken.ToString(): a string's own text, a number/bool/date as .NET prints the value, a
// container as indented JSON. nil (an absent token) is reported as absent.
func (n *jn) tokenString() (string, bool) {
	if n == nil {
		return "", false
	}
	switch n.k {
	case jsNull:
		return "", true
	case jsString:
		return n.s, true
	case jsBool:
		if n.b {
			return "True", true
		}
		return "False", true
	case jsNumber:
		if n.float {
			value, err := strconv.ParseFloat(n.s, 64)
			if err == nil {
				return dotnetDouble(value, false), true
			}
		}
		return n.s, true
	case jsDate:
		hour := n.date.Hour() % 12
		if hour == 0 {
			hour = 12
		}
		meridiem := "AM"
		if n.date.Hour() >= 12 {
			meridiem = "PM"
		}
		return fmt.Sprintf("%d/%d/%d %d:%02d:%02d %s", int(n.date.Month()), n.date.Day(), n.date.Year(), hour,
			n.date.Minute(), n.date.Second(), meridiem), true
	}
	return n.indented(styleNewtonsoft), true
}

// tstr is `token?.ToString()` with null read as "".
func (n *jn) tstr() string {
	text, _ := n.tokenString()
	return text
}

// tstrp is `token?.ToString()` as a nullable string.
func (n *jn) tstrp() *string {
	text, ok := n.tokenString()
	if !ok {
		return nil
	}
	return &text
}

// nodeString is JsonNode.ToString(): a string's own text, other values as their JSON, a container indented.
func (n *jn) nodeString() (string, bool) {
	if n == nil {
		return "", false
	}
	switch n.k {
	case jsString:
		return n.s, true
	case jsArray, jsObject:
		return n.indented(styleSTJ), true
	}
	return n.toJSON(styleSTJ), true
}

// stjString is JsonValue.TryGetValue(out string): the value of a string node.
func (n *jn) stjStringValue() (string, bool) {
	if n != nil && n.k == jsString {
		return n.s, true
	}
	return "", false
}

// tryInt is JsonValue.TryGetValue(out int): an integral number literal within Int32.
func (n *jn) tryInt() (int, bool) {
	if n == nil || n.k != jsNumber {
		return 0, false
	}
	value, err := strconv.ParseInt(n.s, 10, 32)
	if err != nil {
		return 0, false
	}
	return int(value), true
}

// tryDouble is JsonValue.TryGetValue(out double).
func (n *jn) tryDouble() (float64, bool) {
	if n == nil || n.k != jsNumber {
		return 0, false
	}
	value, err := strconv.ParseFloat(n.s, 64)
	if err != nil || math.IsInf(value, 0) {
		return 0, false
	}
	return value, true
}

// tryBool is JsonValue.TryGetValue(out bool).
func (n *jn) tryBool() (bool, bool) {
	if n == nil || n.k != jsBool {
		return false, false
	}
	return n.b, true
}

// getStringValue is JsonNode.GetValue<string>(): throws on anything but a string.
func (n *jn) getStringValue() string {
	if n == nil || n.k != jsString {
		panic(engineFault{"An element of type '" + n.valueKindName() + "' cannot be converted to a 'System.String'."})
	}
	return n.s
}

func (n *jn) valueKindName() string {
	if n == nil {
		return "Null"
	}
	switch n.k {
	case jsNumber:
		return "Number"
	case jsBool:
		if n.b {
			return "True"
		}
		return "False"
	case jsArray:
		return "Array"
	case jsObject:
		return "Object"
	}
	return "String"
}

// engineFault is an exception clio's analysis would raise; Analyze turns it into the tool's failure text.
type engineFault struct{ message string }

func (f engineFault) Error() string { return f.message }

// --- equality --------------------------------------------------------------------------------------------

// jtokenEquals is JToken.DeepEquals: integers and doubles compare by value, strings ordinally, objects
// key-for-key in any order, arrays in order; a string never equals a number.
func jtokenEquals(a, b *jn) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.k != b.k {
		return false
	}
	switch a.k {
	case jsNull:
		return true
	case jsBool:
		return a.b == b.b
	case jsString:
		return a.s == b.s
	case jsDate:
		return a.date.Equal(b.date) || (a.date.Year() == b.date.Year() && a.date.YearDay() == b.date.YearDay() &&
			a.date.Hour() == b.date.Hour() && a.date.Minute() == b.date.Minute() && a.date.Second() == b.date.Second() &&
			a.date.Nanosecond() == b.date.Nanosecond())
	case jsNumber:
		if !a.float && !b.float {
			x, okX := new(big.Int).SetString(a.s, 10)
			y, okY := new(big.Int).SetString(b.s, 10)
			if okX && okY {
				return x.Cmp(y) == 0
			}
		}
		x, errX := strconv.ParseFloat(a.s, 64)
		y, errY := strconv.ParseFloat(b.s, 64)
		return errX == nil && errY == nil && x == y
	case jsArray:
		if len(a.arr) != len(b.arr) {
			return false
		}
		for index := range a.arr {
			if !jtokenEquals(a.arr[index], b.arr[index]) {
				return false
			}
		}
		return true
	case jsObject:
		if len(a.keys) != len(b.keys) {
			return false
		}
		for _, key := range a.keys {
			other, ok := b.props[key]
			if !ok || !jtokenEquals(a.props[key], other) {
				return false
			}
		}
		return true
	}
	return false
}

// nodeEquals is JsonNode.DeepEquals: numbers compare as decimal values (1 equals 1.0), objects key-for-key in
// any order, arrays in order, nil equals only nil.
func nodeEquals(a, b *jn) bool {
	if a == nil || a.k == jsNull {
		return b == nil || b.k == jsNull
	}
	if b == nil || b.k == jsNull {
		return false
	}
	ak, bk := a.k, b.k
	if ak == jsDate {
		ak = jsString
	}
	if bk == jsDate {
		bk = jsString
	}
	if ak != bk {
		return false
	}
	switch ak {
	case jsBool:
		return a.b == b.b
	case jsString:
		return a.s == b.s
	case jsNumber:
		return decimalEqual(a.s, b.s)
	case jsArray:
		if len(a.arr) != len(b.arr) {
			return false
		}
		for index := range a.arr {
			if !nodeEquals(a.arr[index], b.arr[index]) {
				return false
			}
		}
		return true
	case jsObject:
		if len(a.keys) != len(b.keys) {
			return false
		}
		for _, key := range a.keys {
			other, ok := b.props[key]
			if !ok || !nodeEquals(a.props[key], other) {
				return false
			}
		}
		return true
	}
	return false
}

func decimalEqual(x, y string) bool {
	a, okA := new(big.Rat).SetString(x)
	b, okB := new(big.Rat).SetString(y)
	if !okA || !okB {
		return x == y
	}
	return a.Cmp(b) == 0
}

// MarshalJSON lets a node sit inside the answer types; nil is written as null by encoding/json itself.
func (n *jn) MarshalJSON() ([]byte, error) { return []byte(n.toJSON(styleRelaxed)), nil }
