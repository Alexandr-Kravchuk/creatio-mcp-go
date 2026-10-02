package main

import (
	"fmt"
	"sort"
	"strings"
)

// environmentNameAliases are the legacy spellings clio answers with a rename hint.
var environmentNameAliases = map[string]bool{"environmentname": true, "environment_name": true, "environment": true}

// environmentNameRefusal answers a call that names an environment. This server targets the one environment
// configured by CREATIO_* variables, so following clio's "use environment-name" advice would only loop.
const environmentNameRefusal = "environment-name is not accepted; this server targets the environment configured by the CREATIO_* variables."

// directConnectionRefusal answers clio's emergency uri/login/password arguments. Ignoring them would return
// data from a different environment than the caller asked for.
const directConnectionRefusal = "uri, login and password are not accepted; this server targets the environment configured by the CREATIO_* variables."

// unknownArgumentError mirrors clio's McpToolArgumentSupport.BuildLegacyAliasError for tools that refuse
// unknown keys. Keys are reported in sorted order because Go maps carry no request order.
func unknownArgumentError(args map[string]any, known map[string]bool) string {
	if _, ok := args["environment-name"]; ok {
		return environmentNameRefusal
	}
	keys := make([]string, 0, len(args))
	for key := range args {
		if !known[key] {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return ""
	}
	sort.Strings(keys)
	var renamed, unknown []string
	for _, key := range keys {
		shown := strings.NewReplacer("'", "", "\"", "").Replace(key)
		if environmentNameAliases[strings.ToLower(key)] {
			renamed = append(renamed, fmt.Sprintf("'%s' -> 'environment-name'", shown))
		} else {
			unknown = append(unknown, fmt.Sprintf("'%s'", shown))
		}
	}
	parts := make([]string, 0, 2)
	if len(renamed) > 0 {
		parts = append(parts, "Rename: "+joinCallerKeys(renamed)+".")
	}
	if len(unknown) > 0 {
		parts = append(parts, "Unknown args: "+joinCallerKeys(unknown)+". Valid: environment-name.")
	}
	return strings.Join(parts, " ")
}

func joinCallerKeys(keys []string) string {
	const maxEchoedKeys = 10
	if len(keys) <= maxEchoedKeys {
		return strings.Join(keys, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(keys[:maxEchoedKeys], ", "), len(keys)-maxEchoedKeys)
}

// optionalStringArg reads a string argument the way clio's binder does: absent or null is empty, any other
// JSON type is clio's invalid-parameter-type refusal.
func optionalStringArg(args map[string]any, tool, name string) (string, error) {
	value, ok := args[name]
	if !ok || value == nil {
		return "", nil
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("invalid-parameter-type: argument '%s' for MCP tool '%s' must be a string. Received an incompatible JSON value.", name, tool)
	}
	return text, nil
}

// refusesConnectionArgs reports whether a lenient (clio ignores unknown keys) tool was given an environment
// selector this server does not honor.
func refusesConnectionArgs(args map[string]any) string {
	if _, ok := args["environment-name"]; ok {
		return environmentNameRefusal
	}
	for _, key := range []string{"uri", "login", "password"} {
		if _, ok := args[key]; ok {
			return directConnectionRefusal
		}
	}
	return ""
}
