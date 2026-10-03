package creatio

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
)

// ruleWriteObject builds an ordered JSON object from key/value pairs and drops nil values, the way clio's
// DTOs serialize with JsonIgnoreCondition.WhenWritingNull. A *string that is nil is dropped too.
func ruleWriteObject(pairs ...any) *orderedObject {
	object := newOrdered()
	for index := 0; index+1 < len(pairs); index += 2 {
		key := pairs[index].(string)
		value := pairs[index+1]
		switch typed := value.(type) {
		case nil:
			continue
		case *string:
			if typed == nil {
				continue
			}
			value = *typed
		case *bool:
			if typed == nil {
				continue
			}
			value = *typed
		case *int:
			if typed == nil {
				continue
			}
			value = *typed
		case *orderedObject:
			if typed == nil {
				continue
			}
		}
		object.set(key, value)
	}
	return object
}

// ruleWriteSTJ writes a value the way System.Text.Json writes it with clio's options: indented with two
// spaces when indent is true, compact otherwise, and every non-ASCII or HTML-sensitive character escaped.
func ruleWriteSTJ(value any, indent bool) string {
	var buffer bytes.Buffer
	ruleWriteSTJValue(&buffer, value, indent, 0)
	return buffer.String()
}

func ruleWriteSTJValue(buffer *bytes.Buffer, value any, indent bool, depth int) {
	newline := func(level int) {
		if indent {
			buffer.WriteByte('\n')
			for i := 0; i < level; i++ {
				buffer.WriteString("  ")
			}
		}
	}
	switch typed := value.(type) {
	case nil:
		buffer.WriteString("null")
	case bool:
		buffer.WriteString(strconv.FormatBool(typed))
	case string:
		writeJSONString(buffer, typed, true)
	case json.Number:
		buffer.WriteString(typed.String())
	case int:
		buffer.WriteString(strconv.Itoa(typed))
	case int64:
		buffer.WriteString(strconv.FormatInt(typed, 10))
	case float64:
		buffer.WriteString(ruleWriteNumberText(typed))
	case *orderedObject:
		if typed == nil {
			buffer.WriteString("null")
			return
		}
		if len(typed.keys) == 0 {
			buffer.WriteString("{}")
			return
		}
		buffer.WriteByte('{')
		for index, key := range typed.keys {
			if index > 0 {
				buffer.WriteByte(',')
			}
			newline(depth + 1)
			writeJSONString(buffer, key, true)
			buffer.WriteByte(':')
			if indent {
				buffer.WriteByte(' ')
			}
			ruleWriteSTJValue(buffer, typed.values[key], indent, depth+1)
		}
		newline(depth)
		buffer.WriteByte('}')
	case []any:
		if len(typed) == 0 {
			buffer.WriteString("[]")
			return
		}
		buffer.WriteByte('[')
		for index, item := range typed {
			if index > 0 {
				buffer.WriteByte(',')
			}
			newline(depth + 1)
			ruleWriteSTJValue(buffer, item, indent, depth+1)
		}
		newline(depth)
		buffer.WriteByte(']')
	case map[string]any:
		// Only reached for caller-supplied JSON (a constant object); keep Go's key order.
		encoded, _ := json.Marshal(typed)
		parsed, err := parseOrderedJSON(encoded)
		if err != nil {
			buffer.WriteString("null")
			return
		}
		ruleWriteSTJValue(buffer, parsed, indent, depth)
	default:
		panic(fmt.Sprintf("ruleWriteSTJ: unsupported %T", value))
	}
}

// ruleWriteNumberText renders a JSON number the caller sent. The MCP layer decodes arguments to float64,
// so the original text is gone; integers print without a fraction, other values in their shortest form.
func ruleWriteNumberText(value float64) string {
	if value == float64(int64(value)) && value < 1e18 && value > -1e18 {
		return strconv.FormatInt(int64(value), 10)
	}
	return strconv.FormatFloat(value, 'f', -1, 64)
}

// ruleWriteToOrdered converts a decoded JSON value (maps, slices, float64) into the ordered tree.
func ruleWriteToOrdered(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		encoded, _ := json.Marshal(typed)
		parsed, err := parseOrderedJSON(encoded)
		if err != nil {
			return nil
		}
		return parsed
	case []any:
		items := make([]any, len(typed))
		for index, item := range typed {
			items[index] = ruleWriteToOrdered(item)
		}
		return items
	case float64:
		return json.Number(ruleWriteNumberText(typed))
	}
	return value
}
