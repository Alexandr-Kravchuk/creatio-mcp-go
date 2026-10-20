package main

// clioBinder binds a call's arguments the way System.Text.Json binds clio's argument records under the MCP
// serializer options (web defaults): members match case-insensitively (an exact spelling wins), numbers may
// arrive as numeric strings, a member of the wrong JSON kind fails the whole binding with clio's
// invalid-parameter-type text, and every unmatched key lands in the record's overflow bag (JsonExtensionData).

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
)

type clioMemberKind int

const (
	clioString clioMemberKind = iota
	clioInt
	clioBool
	clioAny    // JsonElement: any JSON value
	clioObject // a nested record: a JSON object (or null)
)

type clioMember struct {
	name     string
	kind     clioMemberKind
	required bool // declared with C#'s `required`: an absent member fails the binding
}

// clioBound is one bound call: present members by canonical name, and the overflow bag.
type clioBound struct {
	values    map[string]any
	present   map[string]bool
	extension map[string]json.RawMessage
}

func (b clioBound) has(name string) bool { return b.present[name] }

func (b clioBound) str(name string) (string, bool) {
	text, ok := b.values[name].(string)
	return text, ok
}

func (b clioBound) raw(name string) json.RawMessage {
	if !b.present[name] {
		return nil
	}
	encoded, _ := json.Marshal(b.values[name])
	return encoded
}

func (b clioBound) integer(name string) *int {
	value, ok := b.values[name].(int)
	if !ok {
		return nil
	}
	return &value
}

func clioBind(tool string, args map[string]any, members []clioMember) (clioBound, error) {
	bound := clioBound{values: map[string]any{}, present: map[string]bool{}, extension: map[string]json.RawMessage{}}
	matched := map[string]string{}
	for key := range args {
		member := ""
		for _, candidate := range members {
			if key == candidate.name {
				member = candidate.name
				break
			}
		}
		if member == "" {
			for _, candidate := range members {
				if strings.EqualFold(key, candidate.name) {
					if _, exact := args[candidate.name]; !exact {
						member = candidate.name
					}
					break
				}
			}
		}
		if member == "" {
			raw, _ := json.Marshal(args[key])
			bound.extension[key] = raw
			continue
		}
		matched[member] = key
	}
	for _, member := range members {
		key, ok := matched[member.name]
		if !ok {
			if member.required {
				return bound, dataWriteBindingError(tool)
			}
			continue
		}
		value := args[key]
		bound.present[member.name] = true
		if value == nil {
			if member.kind == clioInt || member.kind == clioBool {
				// A null for a non-nullable value type does not bind; the records here use int?/bool, so
				// only the bool members refuse it.
				if member.kind == clioBool {
					return bound, dataWriteKindBindingError(tool, member.name, "a boolean")
				}
			}
			bound.values[member.name] = nil
			continue
		}
		switch member.kind {
		case clioString:
			text, ok := value.(string)
			if !ok {
				return bound, dataWriteKindBindingError(tool, member.name, "a string")
			}
			bound.values[member.name] = text
		case clioInt:
			number, ok := clioInteger(value)
			if !ok {
				return bound, dataWriteKindBindingError(tool, member.name, "a number")
			}
			bound.values[member.name] = number
		case clioBool:
			flag, ok := value.(bool)
			if !ok {
				return bound, dataWriteKindBindingError(tool, member.name, "a boolean")
			}
			bound.values[member.name] = flag
		case clioObject:
			if _, ok := value.(map[string]any); !ok {
				return bound, dataWriteKindBindingError(tool, member.name, "an object")
			}
			bound.values[member.name] = value
		default:
			bound.values[member.name] = value
		}
	}
	return bound, nil
}

// clioInteger reads an Int32 as System.Text.Json does with AllowReadingFromString.
func clioInteger(value any) (int, bool) {
	switch typed := value.(type) {
	case float64:
		if typed != math.Trunc(typed) || typed < math.MinInt32 || typed > math.MaxInt32 {
			return 0, false
		}
		return int(typed), true
	case string:
		parsed, err := strconv.ParseInt(typed, 10, 32)
		return int(parsed), err == nil
	}
	return 0, false
}
