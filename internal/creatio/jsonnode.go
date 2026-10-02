package creatio

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"unicode/utf16"
)

// jsonKind is the token type of a jnode, mirroring the Newtonsoft JToken types the page bundle code uses.
type jsonKind int

const (
	jkNull jsonKind = iota
	jkBool
	jkInteger
	jkFloat
	jkString
	jkArray
	jkObject
)

// jnode is an ordered, mutable JSON tree with reference identity, standing in for Newtonsoft's JToken: object
// keys keep insertion order, replacing a key keeps its position, and nodes are pointers so the diff applier
// can find and remove an item by reference as clio does.
type jnode struct {
	kind  jsonKind
	text  string // string value, or the number text as Newtonsoft writes it
	flag  bool
	items []*jnode
	keys  []string
	props map[string]*jnode
}

func newObject() *jnode { return &jnode{kind: jkObject, props: map[string]*jnode{}} }
func newArray() *jnode  { return &jnode{kind: jkArray, items: []*jnode{}} }
func newString(value string) *jnode {
	return &jnode{kind: jkString, text: value}
}
func newBool(value bool) *jnode { return &jnode{kind: jkBool, flag: value} }
func newInt(value int) *jnode   { return &jnode{kind: jkInteger, text: strconv.Itoa(value)} }
func jsonNullNode() *jnode      { return &jnode{kind: jkNull} }

func (n *jnode) isObject() bool { return n != nil && n.kind == jkObject }
func (n *jnode) isArray() bool  { return n != nil && n.kind == jkArray }

// get returns the property value, or nil when the node is not an object or has no such key.
func (n *jnode) get(key string) *jnode {
	if !n.isObject() {
		return nil
	}
	return n.props[key]
}

// set adds or replaces a property; a replaced key keeps its position, as JObject's indexer does.
func (n *jnode) set(key string, value *jnode) {
	if value == nil {
		value = jsonNullNode()
	}
	if _, exists := n.props[key]; !exists {
		n.keys = append(n.keys, key)
	}
	n.props[key] = value
}

func (n *jnode) remove(key string) bool {
	if !n.isObject() {
		return false
	}
	if _, exists := n.props[key]; !exists {
		return false
	}
	delete(n.props, key)
	for index, name := range n.keys {
		if name == key {
			n.keys = append(n.keys[:index], n.keys[index+1:]...)
			break
		}
	}
	return true
}

func (n *jnode) insertAt(index int, value *jnode) {
	n.items = append(n.items, nil)
	copy(n.items[index+1:], n.items[index:])
	n.items[index] = value
}

func (n *jnode) removeAt(index int) {
	n.items = append(n.items[:index], n.items[index+1:]...)
}

func (n *jnode) clone() *jnode {
	if n == nil {
		return nil
	}
	copied := &jnode{kind: n.kind, text: n.text, flag: n.flag}
	switch n.kind {
	case jkArray:
		copied.items = make([]*jnode, len(n.items))
		for index, item := range n.items {
			copied.items[index] = item.clone()
		}
	case jkObject:
		copied.keys = append([]string{}, n.keys...)
		copied.props = make(map[string]*jnode, len(n.props))
		for key, value := range n.props {
			copied.props[key] = value.clone()
		}
	}
	return copied
}

// stringValue mirrors JToken.Value<string>(): nil for an absent or null token, the text of a scalar. A
// container cannot be cast in Newtonsoft; that case panics with the same message.
func (n *jnode) stringValue() *string {
	if n == nil || n.kind == jkNull {
		return nil
	}
	switch n.kind {
	case jkString, jkInteger, jkFloat:
		value := n.text
		return &value
	case jkBool:
		value := "False"
		if n.flag {
			value = "True"
		}
		return &value
	}
	panic(applierFault{message: "Cannot cast Newtonsoft.Json.Linq.JObject to Newtonsoft.Json.Linq.JToken."})
}

// str is stringValue with nil read as an empty string.
func (n *jnode) str() string {
	if value := n.stringValue(); value != nil {
		return *value
	}
	return ""
}

// tokenString mirrors JToken.ToString() as clio uses it on names and captions: a scalar's text, or the
// container's JSON.
func (n *jnode) tokenString() *string {
	if n == nil {
		return nil
	}
	switch n.kind {
	case jkNull:
		value := ""
		return &value
	case jkArray, jkObject:
		value := string(n.newtonsoftJSON())
		return &value
	}
	return n.stringValue()
}

// parseJNode parses strict JSON the way Newtonsoft's JToken.Parse does: integers stay integers, other
// numbers become doubles and are written back in .NET's round-trip form, and a repeated key replaces the
// earlier value in place.
func parseJNode(data []byte) (*jnode, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	node, err := readJNode(decoder)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, fmt.Errorf("additional text found after the JSON value")
	}
	return node, nil
}

func readJNode(decoder *json.Decoder) (*jnode, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	switch value := token.(type) {
	case json.Delim:
		switch value {
		case '{':
			object := newObject()
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return nil, err
				}
				child, err := readJNode(decoder)
				if err != nil {
					return nil, err
				}
				object.set(keyToken.(string), child)
			}
			_, err := decoder.Token()
			return object, err
		case '[':
			array := newArray()
			for decoder.More() {
				child, err := readJNode(decoder)
				if err != nil {
					return nil, err
				}
				array.items = append(array.items, child)
			}
			_, err := decoder.Token()
			return array, err
		}
	case string:
		return newString(value), nil
	case bool:
		return newBool(value), nil
	case nil:
		return jsonNullNode(), nil
	case json.Number:
		return numberNode(string(value)), nil
	}
	return nil, fmt.Errorf("unexpected JSON token %v", token)
}

func numberNode(text string) *jnode {
	if !strings.ContainsAny(text, ".eE") {
		return &jnode{kind: jkInteger, text: text}
	}
	value, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return &jnode{kind: jkFloat, text: text}
	}
	return &jnode{kind: jkFloat, text: dotnetDouble(value)}
}

// dotnetDouble formats a double as Newtonsoft writes it: .NET's shortest round-trip text, scientific below
// 1E-05 and from 1E+15, with ".0" appended to a value that would otherwise read as an integer.
func dotnetDouble(value float64) string {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return strconv.FormatFloat(value, 'g', -1, 64)
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
	if !strings.ContainsAny(text, ".E") {
		text += ".0"
	}
	return text
}

// newtonsoftJSON writes compact JSON with Newtonsoft's escaping (quotes, backslash and control characters).
func (n *jnode) newtonsoftJSON() []byte {
	var buffer bytes.Buffer
	n.writeJSON(&buffer, false)
	return buffer.Bytes()
}

// stjJSON writes compact JSON as System.Text.Json does with its default encoder, which clio uses for
// bundle.json and meta.json: everything outside printable ASCII, and the HTML-sensitive characters, becomes
// an upper-case \uXXXX escape.
func (n *jnode) stjJSON() []byte {
	var buffer bytes.Buffer
	n.writeJSON(&buffer, true)
	return buffer.Bytes()
}

func (n *jnode) writeJSON(buffer *bytes.Buffer, stj bool) {
	if n == nil {
		buffer.WriteString("null")
		return
	}
	switch n.kind {
	case jkNull:
		buffer.WriteString("null")
	case jkBool:
		buffer.WriteString(strconv.FormatBool(n.flag))
	case jkInteger, jkFloat:
		buffer.WriteString(n.text)
	case jkString:
		writeJSONString(buffer, n.text, stj)
	case jkArray:
		buffer.WriteByte('[')
		for index, item := range n.items {
			if index > 0 {
				buffer.WriteByte(',')
			}
			item.writeJSON(buffer, stj)
		}
		buffer.WriteByte(']')
	case jkObject:
		buffer.WriteByte('{')
		for index, key := range n.keys {
			if index > 0 {
				buffer.WriteByte(',')
			}
			writeJSONString(buffer, key, stj)
			buffer.WriteByte(':')
			n.props[key].writeJSON(buffer, stj)
		}
		buffer.WriteByte('}')
	}
}

func writeJSONString(buffer *bytes.Buffer, value string, stj bool) {
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
			if !stj {
				buffer.WriteString(`\"`)
				continue
			}
		}
		escape := r < 0x20
		if stj {
			escape = escape || r > 0x7E || strings.ContainsRune("\"&'+<>`", r)
		}
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

// toJNode converts a Go value built from maps, slices and scalars; it is used for the fixed-shape parts of
// bundle.json and meta.json. Object key order follows the orderedFields slice.
type orderedFields []field

type field struct {
	key   string
	value any
}

func toJNode(value any) *jnode {
	switch typed := value.(type) {
	case nil:
		return jsonNullNode()
	case *jnode:
		if typed == nil {
			return jsonNullNode()
		}
		return typed
	case orderedFields:
		object := newObject()
		for _, entry := range typed {
			object.set(entry.key, toJNode(entry.value))
		}
		return object
	case []any:
		array := newArray()
		for _, item := range typed {
			array.items = append(array.items, toJNode(item))
		}
		return array
	case string:
		return newString(typed)
	case *string:
		if typed == nil {
			return jsonNullNode()
		}
		return newString(*typed)
	case bool:
		return newBool(typed)
	case int:
		return newInt(typed)
	case *int:
		if typed == nil {
			return jsonNullNode()
		}
		return newInt(*typed)
	}
	panic(fmt.Sprintf("toJNode: unsupported %T", value))
}
