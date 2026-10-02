package creatio

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
)

// orderedObject is a JSON object that keeps its keys in document order. Business-rule metadata and
// ESQ filter envelopes are read the way clio's System.Text.Json JsonNode reads them: item order decides
// output order, and a Go map would lose it.
type orderedObject struct {
	keys   []string
	values map[string]any
}

func (o *orderedObject) get(key string) any {
	if o == nil {
		return nil
	}
	return o.values[key]
}

func (o *orderedObject) len() int {
	if o == nil {
		return 0
	}
	return len(o.keys)
}

// MarshalJSON writes the object back with its original key order.
func (o *orderedObject) MarshalJSON() ([]byte, error) {
	var buffer bytes.Buffer
	buffer.WriteByte('{')
	for index, key := range o.keys {
		if index > 0 {
			buffer.WriteByte(',')
		}
		encodedKey, err := json.Marshal(key)
		if err != nil {
			return nil, err
		}
		buffer.Write(encodedKey)
		buffer.WriteByte(':')
		encodedValue, err := json.Marshal(o.values[key])
		if err != nil {
			return nil, err
		}
		buffer.Write(encodedValue)
	}
	buffer.WriteByte('}')
	return buffer.Bytes(), nil
}

// parseOrderedJSON decodes one JSON document into orderedObject, []any, string, json.Number, bool or nil.
func parseOrderedJSON(data []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	value, err := readOrderedValue(decoder)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("unexpected data after the JSON value")
	}
	return value, nil
}

func readOrderedValue(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	switch typed := token.(type) {
	case json.Delim:
		switch typed {
		case '{':
			object := &orderedObject{values: map[string]any{}}
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return nil, err
				}
				key, ok := keyToken.(string)
				if !ok {
					return nil, fmt.Errorf("object key is not a string")
				}
				value, err := readOrderedValue(decoder)
				if err != nil {
					return nil, err
				}
				if _, seen := object.values[key]; !seen {
					object.keys = append(object.keys, key)
				}
				object.values[key] = value
			}
			if _, err := decoder.Token(); err != nil {
				return nil, err
			}
			return object, nil
		case '[':
			items := []any{}
			for decoder.More() {
				value, err := readOrderedValue(decoder)
				if err != nil {
					return nil, err
				}
				items = append(items, value)
			}
			if _, err := decoder.Token(); err != nil {
				return nil, err
			}
			return items, nil
		default:
			return nil, fmt.Errorf("unexpected delimiter %q", typed)
		}
	default:
		return typed, nil
	}
}

// jsonString mirrors JsonValue.TryGetValue<string>: only a JSON string counts.
func jsonString(object *orderedObject, key string) (string, bool) {
	value, ok := object.get(key).(string)
	return value, ok
}

// jsonStringOrNil returns nil when the property is absent or not a JSON string.
func jsonStringOrNil(object *orderedObject, key string) *string {
	if value, ok := jsonString(object, key); ok {
		return &value
	}
	return nil
}

// jsonInt mirrors JsonValue.TryGetValue<int>: a JSON number that is a 32-bit integer.
func jsonInt(object *orderedObject, key string, fallback int) int {
	number, ok := object.get(key).(json.Number)
	if !ok {
		return fallback
	}
	parsed, err := strconv.ParseInt(number.String(), 10, 32)
	if err != nil {
		return fallback
	}
	return int(parsed)
}

// jsonBool mirrors JsonValue.TryGetValue<bool>: only JSON true or false counts.
func jsonBool(object *orderedObject, key string, fallback bool) bool {
	value, ok := object.get(key).(bool)
	if !ok {
		return fallback
	}
	return value
}

func jsonObjectAt(object *orderedObject, key string) *orderedObject {
	value, _ := object.get(key).(*orderedObject)
	return value
}

func jsonArrayAt(object *orderedObject, key string) ([]any, bool) {
	value, ok := object.get(key).([]any)
	return value, ok
}

// rawJSON re-encodes a parsed value, preserving key order and number text.
func rawJSON(value any) (json.RawMessage, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return encoded, nil
}
