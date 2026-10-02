package creatio

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
)

// Creatio's EntitySchemaColumnDefSource ordinals, as the runtime schema endpoint reports them.
const (
	defaultSourceNone        = 0
	defaultSourceConst       = 1
	defaultSourceSettings    = 2
	defaultSourceSystemValue = 3
	defaultSourceSequence    = 4
)

var defaultValueSourceNames = map[int]string{
	defaultSourceNone: "None", defaultSourceConst: "Const", defaultSourceSettings: "Settings",
	defaultSourceSystemValue: "SystemValue", defaultSourceSequence: "Sequence",
}

// DefaultValueConfig is clio's default-value-config read shape. It round-trips into the sync-schemas write
// surfaces, so a lookup Const default keeps the record GUID in value and display fields only add to it.
type DefaultValueConfig struct {
	Source                string          `json:"source"`
	Value                 json.RawMessage `json:"value,omitempty"`
	ValueSource           *string         `json:"value-source,omitempty"`
	ResolvedValueSource   *string         `json:"resolved-value-source,omitempty"`
	SequencePrefix        *string         `json:"sequence-prefix,omitempty"`
	SequenceNumberOfChars *int            `json:"sequence-number-of-chars,omitempty"`
	DisplayValue          *string         `json:"display-value,omitempty"`
	RecordResolution      *string         `json:"record-resolution,omitempty"`
	SourceResolution      *string         `json:"source-resolution,omitempty"`
}

// runtimeDefaultValue is the defValue object of a RuntimeEntitySchemaRequest column. valueSource stays raw
// because the endpoint is not guaranteed to send it as a string.
type runtimeDefaultValue struct {
	ValueSourceType       int             `json:"valueSourceType"`
	Value                 json.RawMessage `json:"value"`
	ValueSource           json.RawMessage `json:"valueSource"`
	SequencePrefix        *string         `json:"sequencePrefix"`
	SequenceNumberOfChars int             `json:"sequenceNumberOfChars"`
}

// newDefaultValueConfig projects a runtime default the way clio's CreateDefaultValueConfig does: no default
// and a None default both read back as nil, Const keeps its scalar JSON type, Settings and SystemValue carry
// the trimmed selector twice, and Sequence keeps its prefix verbatim (edge whitespace is significant).
func newDefaultValueConfig(defValue *runtimeDefaultValue, value json.RawMessage) *DefaultValueConfig {
	if defValue == nil {
		return nil
	}
	source, known := defaultValueSourceNames[defValue.ValueSourceType]
	if !known {
		return &DefaultValueConfig{Source: strconv.Itoa(defValue.ValueSourceType)}
	}
	switch defValue.ValueSourceType {
	case defaultSourceNone:
		return nil
	case defaultSourceConst:
		if trimmed := bytes.TrimSpace(value); len(trimmed) == 0 || string(trimmed) == "null" {
			value = nil
		}
		return &DefaultValueConfig{Source: source, Value: value}
	case defaultSourceSettings, defaultSourceSystemValue:
		selector := trimmedOrNil(rawJSONText(defValue.ValueSource))
		return &DefaultValueConfig{Source: source, ValueSource: selector, ResolvedValueSource: selector}
	default:
		config := &DefaultValueConfig{Source: source}
		if defValue.SequencePrefix != nil && *defValue.SequencePrefix != "" {
			config.SequencePrefix = stringPointer(*defValue.SequencePrefix)
		}
		if defValue.SequenceNumberOfChars > 0 {
			chars := defValue.SequenceNumberOfChars
			config.SequenceNumberOfChars = &chars
		}
		return config
	}
}

// friendlyDefaultValue is clio's flat default-value string: the Const value as .NET ToString() prints it
// (booleans as True/False), or the Settings/SystemValue selector. Other sources have no flat value.
func friendlyDefaultValue(config *DefaultValueConfig) *string {
	if config == nil {
		return nil
	}
	switch config.Source {
	case "Const":
		return scalarDisplayText(config.Value)
	case "Settings", "SystemValue":
		return config.ValueSource
	default:
		return nil
	}
}

// scalarDisplayText renders a JSON scalar as .NET object.ToString() would after clio normalized it.
func scalarDisplayText(value json.RawMessage) *string {
	trimmed := bytes.TrimSpace(value)
	switch {
	case len(trimmed) == 0 || string(trimmed) == "null":
		return nil
	case string(trimmed) == "true":
		return stringPointer("True")
	case string(trimmed) == "false":
		return stringPointer("False")
	case trimmed[0] == '"':
		var text string
		if json.Unmarshal(trimmed, &text) != nil {
			return nil
		}
		return &text
	default:
		return stringPointer(string(trimmed))
	}
}

// isJSONScalar reports whether a raw JSON value is a string, number, boolean or null.
func isJSONScalar(value json.RawMessage) bool {
	trimmed := bytes.TrimSpace(value)
	return len(trimmed) == 0 || (trimmed[0] != '{' && trimmed[0] != '[')
}

// rawJSONText returns a JSON string's text, a non-string scalar's literal, or "" for null and absent values.
func rawJSONText(value json.RawMessage) string {
	trimmed := bytes.TrimSpace(value)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return ""
	}
	var text string
	if json.Unmarshal(trimmed, &text) == nil {
		return text
	}
	return string(trimmed)
}

func trimmedOrNil(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}

// localizedEntry is one culture/value pair of a localized object, in the order the server sent it.
type localizedEntry struct {
	culture string
	value   json.RawMessage
}

// orderedLocalizedEntries reads a localized object without losing key order. clio deserializes these into
// .NET dictionaries, which keep insertion order, and its "first value" fallbacks depend on that order.
func orderedLocalizedEntries(value json.RawMessage) []localizedEntry {
	decoder := json.NewDecoder(bytes.NewReader(value))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return nil
	}
	var entries []localizedEntry
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return entries
		}
		var item json.RawMessage
		if err := decoder.Decode(&item); err != nil {
			return entries
		}
		name, _ := key.(string)
		entries = append(entries, localizedEntry{culture: name, value: item})
	}
	return entries
}

// runtimeLocalizedText mirrors clio's RuntimeEntitySchemaReader: a plain string as is, otherwise the en-US
// value, otherwise the first string value in server order.
func runtimeLocalizedText(value json.RawMessage) *string {
	var text string
	if json.Unmarshal(value, &text) == nil {
		return &text
	}
	entries := orderedLocalizedEntries(value)
	for _, entry := range entries {
		if strings.EqualFold(entry.culture, "en-US") {
			if json.Unmarshal(entry.value, &text) == nil {
				return &text
			}
			break
		}
	}
	for _, entry := range entries {
		if json.Unmarshal(entry.value, &text) == nil {
			return &text
		}
	}
	return nil
}
