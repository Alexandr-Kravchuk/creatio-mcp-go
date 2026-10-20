package knowledge

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"runtime"
	"strings"
)

// JSONObject is an order-preserving JSON object. clio rewrites appsettings.json from its whole settings
// model; this server edits only the sections a tool owns and keeps every other byte of meaning (unknown
// sections, key order, number literals) as it found it. Values are *JSONObject, []any, json.Number,
// string, bool or nil.
type JSONObject struct {
	keys   []string
	values map[string]any
}

// NewJSONObject returns an empty object.
func NewJSONObject() *JSONObject { return &JSONObject{values: map[string]any{}} }

// Get returns a member.
func (o *JSONObject) Get(key string) (any, bool) {
	value, ok := o.values[key]
	return value, ok
}

// Object returns a member that is an object, or nil.
func (o *JSONObject) Object(key string) *JSONObject {
	value, _ := o.values[key].(*JSONObject)
	return value
}

// Set replaces a member in place, or appends it.
func (o *JSONObject) Set(key string, value any) {
	if _, ok := o.values[key]; !ok {
		o.keys = append(o.keys, key)
	}
	o.values[key] = value
}

// Delete removes a member.
func (o *JSONObject) Delete(key string) bool {
	if _, ok := o.values[key]; !ok {
		return false
	}
	delete(o.values, key)
	for i, existing := range o.keys {
		if existing == key {
			o.keys = append(o.keys[:i], o.keys[i+1:]...)
			break
		}
	}
	return true
}

// Keys returns the member names in order.
func (o *JSONObject) Keys() []string { return append([]string{}, o.keys...) }

// FindFold returns the first member whose name equals key ignoring case.
func (o *JSONObject) FindFold(key string) (string, bool) {
	for _, existing := range o.keys {
		if strings.EqualFold(existing, key) {
			return existing, true
		}
	}
	return "", false
}

// EnsureObject returns the object member key, creating (or replacing a non-object value with) an empty one.
func (o *JSONObject) EnsureObject(key string) *JSONObject {
	if existing := o.Object(key); existing != nil {
		return existing
	}
	created := NewJSONObject()
	o.Set(key, created)
	return created
}

// ParseJSONObject parses a document whose root is an object.
func ParseJSONObject(data []byte) (*JSONObject, error) {
	decoder := json.NewDecoder(bytes.NewReader(bytes.TrimPrefix(data, []byte("\xEF\xBB\xBF"))))
	decoder.UseNumber()
	value, err := parseJSONValue(decoder)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("Additional text encountered after finished reading JSON content.")
	}
	object, ok := value.(*JSONObject)
	if !ok {
		return nil, errors.New("appsettings.json did not contain a settings object.")
	}
	return object, nil
}

func parseJSONValue(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	switch value := token.(type) {
	case json.Delim:
		switch value {
		case '{':
			object := NewJSONObject()
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return nil, err
				}
				key, ok := keyToken.(string)
				if !ok {
					return nil, fmt.Errorf("unexpected JSON token %v", keyToken)
				}
				member, err := parseJSONValue(decoder)
				if err != nil {
					return nil, err
				}
				object.Set(key, member)
			}
			_, err := decoder.Token()
			return object, err
		case '[':
			array := []any{}
			for decoder.More() {
				member, err := parseJSONValue(decoder)
				if err != nil {
					return nil, err
				}
				array = append(array, member)
			}
			_, err := decoder.Token()
			return array, err
		}
		return nil, fmt.Errorf("unexpected JSON delimiter %v", value)
	default:
		return value, nil
	}
}

// settingsNewLine is Environment.NewLine, which Newtonsoft's indented writer uses.
func settingsNewLine() string {
	if runtime.GOOS == "windows" {
		return "\r\n"
	}
	return "\n"
}

// MarshalIndented writes the object as Newtonsoft.Json's Formatting.Indented does: two-space indentation,
// "name": value, {} and [] for empty containers and no trailing newline.
func (o *JSONObject) MarshalIndented() ([]byte, error) {
	var buffer bytes.Buffer
	if err := writeJSONValue(&buffer, o, 0); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func writeJSONString(buffer *bytes.Buffer, value string) error {
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return err
	}
	buffer.Write(bytes.TrimRight(encoded.Bytes(), "\n"))
	return nil
}

func writeJSONValue(buffer *bytes.Buffer, value any, depth int) error {
	newline := settingsNewLine()
	indent := func(level int) { buffer.WriteString(newline + strings.Repeat("  ", level)) }
	switch typed := value.(type) {
	case *JSONObject:
		if len(typed.keys) == 0 {
			buffer.WriteString("{}")
			return nil
		}
		buffer.WriteByte('{')
		for i, key := range typed.keys {
			if i > 0 {
				buffer.WriteByte(',')
			}
			indent(depth + 1)
			if err := writeJSONString(buffer, key); err != nil {
				return err
			}
			buffer.WriteString(": ")
			if err := writeJSONValue(buffer, typed.values[key], depth+1); err != nil {
				return err
			}
		}
		indent(depth)
		buffer.WriteByte('}')
	case []any:
		if len(typed) == 0 {
			buffer.WriteString("[]")
			return nil
		}
		buffer.WriteByte('[')
		for i, member := range typed {
			if i > 0 {
				buffer.WriteByte(',')
			}
			indent(depth + 1)
			if err := writeJSONValue(buffer, member, depth+1); err != nil {
				return err
			}
		}
		indent(depth)
		buffer.WriteByte(']')
	case string:
		return writeJSONString(buffer, typed)
	case json.Number:
		buffer.WriteString(typed.String())
	case bool:
		if typed {
			buffer.WriteString("true")
		} else {
			buffer.WriteString("false")
		}
	case nil:
		buffer.WriteString("null")
	case int:
		buffer.WriteString(fmt.Sprint(typed))
	default:
		return fmt.Errorf("unsupported settings value %T", value)
	}
	return nil
}
