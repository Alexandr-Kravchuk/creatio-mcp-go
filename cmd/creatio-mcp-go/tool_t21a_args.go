package main

// Argument binding shared by the T21a tools (create-package, place-section-in-workplace, localize-page,
// trigger-smoke): clio's binder refusals for a value of the wrong JSON type, BuildLegacyAliasError with each
// tool's own alias map and comparer, and the environment resolution of a typed-response BaseTool.

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/redact"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// t21aBinder reads one tool's arguments the way clio's System.Text.Json binder does.
type t21aBinder struct {
	tool string
	args map[string]any
}

func (b t21aBinder) typeError(name, expected string) error {
	return fmt.Errorf("invalid-parameter-type: argument '%s' for MCP tool '%s' must be %s. Received an incompatible JSON value.", name, b.tool, expected)
}

func (b t21aBinder) shapeError(name string) error {
	return fmt.Errorf("invalid-parameter-type: argument '%s' for MCP tool '%s' contains a value that does not match the documented shape. Received an incompatible JSON value.", name, b.tool)
}

// str is an optional string: nil when absent or null.
func (b t21aBinder) str(name string) (*string, error) {
	value, ok := b.args[name]
	if !ok || value == nil {
		return nil, nil
	}
	text, ok := value.(string)
	if !ok {
		return nil, b.typeError(name, "a string")
	}
	return &text, nil
}

// text is str with an absent value read as "".
func (b t21aBinder) text(name string) (string, error) {
	value, err := b.str(name)
	if value == nil {
		return "", err
	}
	return *value, err
}

func (b t21aBinder) boolean(name string) (*bool, error) {
	value, ok := b.args[name]
	if !ok || value == nil {
		return nil, nil
	}
	flag, ok := value.(bool)
	if !ok {
		return nil, b.typeError(name, "a boolean")
	}
	return &flag, nil
}

// integer is an Int32: a whole JSON number in range; clio's binder also reads one from a string.
func (b t21aBinder) integer(name string) (*int, error) {
	return rightsArgs{tool: b.tool, args: b.args}.intPtr(name)
}

// strings is a string array: nil when absent or null; a null item stays (nil), a non-string item breaks the shape.
func (b t21aBinder) strings(name string) ([]*string, bool, error) {
	value, ok := b.args[name]
	if !ok || value == nil {
		return nil, false, nil
	}
	items, ok := value.([]any)
	if !ok {
		return nil, false, b.typeError(name, "an array")
	}
	result := make([]*string, 0, len(items))
	for _, item := range items {
		if item == nil {
			result = append(result, nil)
			continue
		}
		text, ok := item.(string)
		if !ok {
			return nil, false, b.shapeError(name)
		}
		result = append(result, &text)
	}
	return result, true, nil
}

// object is a JSON object argument: nil when absent or null.
func (b t21aBinder) object(name string) (map[string]any, error) {
	value, ok := b.args[name]
	if !ok || value == nil {
		return nil, nil
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, b.typeError(name, "an object")
	}
	return object, nil
}

// sortedKeys lists an object's keys in a stable order (Go maps keep no request order).
func sortedKeys(object map[string]any) []string {
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// t21aEnvironmentAliases is McpToolArgumentSupport.EnvironmentNameAliases.
var t21aEnvironmentAliases = map[string]string{"environmentName": "environment-name", "environment_name": "environment-name",
	"environment": "environment-name"}

// t21aAliasError is BuildLegacyAliasError over the keys the argument record does not bind. ignoreCase is the
// alias dictionary's comparer (OrdinalIgnoreCase or Ordinal). Keys are listed sorted: Go maps keep no request order.
func t21aAliasError(args map[string]any, known []string, aliases map[string]string, ignoreCase bool, renameSuffix, hint string) string {
	bound := make(map[string]bool, len(known))
	for _, name := range known {
		bound[name] = true
	}
	var keys []string
	for key := range args {
		if !bound[key] {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return ""
	}
	sort.Strings(keys)
	var renamed, unknown []string
	for _, key := range keys {
		canonical, ok := aliases[key]
		if !ok && ignoreCase {
			for alias, target := range aliases {
				if strings.EqualFold(alias, key) {
					canonical, ok = target, true
					break
				}
			}
		}
		if ok {
			renamed = append(renamed, fmt.Sprintf("'%s' -> '%s'", describeCallerKey(key), canonical))
		} else {
			unknown = append(unknown, fmt.Sprintf("'%s'", describeCallerKey(key)))
		}
	}
	var parts []string
	if len(renamed) > 0 {
		parts = append(parts, "Rename: "+joinCallerKeys(renamed)+renameSuffix)
	}
	if len(unknown) > 0 {
		parts = append(parts, "Unknown args: "+joinCallerKeys(unknown)+". "+hint)
	}
	return strings.Join(parts, " ")
}

// t21aWithAliases extends EnvironmentNameAliases with a tool's own entries.
func t21aWithAliases(extra map[string]string) map[string]string {
	merged := make(map[string]string, len(t21aEnvironmentAliases)+len(extra))
	for key, value := range t21aEnvironmentAliases {
		merged[key] = value
	}
	for key, value := range extra {
		merged[key] = value
	}
	return merged
}

// t21aResult is structuredToolResult with clio's System.Text.Json text, so the failure redaction of the
// dispatcher sees the same characters clio's does.
func t21aResult(value any) *mcp.CallToolResult {
	text, err := creatio.ClioJSONText(value)
	if err != nil {
		return structuredToolResult(value)
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}, StructuredContent: value}
}

// t21aResolve resolves the environment the way a typed-response clio tool sees it: a binding failure and the
// Safe-environment refusal are raised (err), any other resolution failure is the redacted message the tool
// puts into its own envelope.
func t21aResolve(envs *environments, tool string, args map[string]any, scope connectionScope) (*creatio.Client, string, error) {
	client, failure, err := envs.resolve(tool, args, scope)
	if err != nil {
		return nil, "", err
	}
	if failure != nil {
		var target *environmentError
		if errors.As(failure, &target) && target.safe {
			return nil, "", failure
		}
		return nil, redact.Text(failure.Error()), nil
	}
	return client, "", nil
}
