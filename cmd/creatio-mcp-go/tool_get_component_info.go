package main

import (
	"context"
	"fmt"
	"sort"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// componentInfoArgNames are the arguments of clio's ComponentInfoArgs, in the order its hint names them.
var componentInfoArgNames = []string{"component-type", "composite", "search", "schema-type", "environment-name", "version", "uri", "login", "password"}

// componentInfoAliases are the misspellings clio answers with a rename (ComponentInfoTool.LegacyAliases,
// matched case-sensitively).
var componentInfoAliases = map[string]string{
	"componentType": "component-type", "component_type": "component-type", "component-name": "component-type",
	"componentName": "component-type", "component_name": "component-type",
	"schemaType": "schema-type", "schema_type": "schema-type",
	"environmentName": "environment-name", "environment_name": "environment-name",
}

// get-component-info: clio's resident, read-only ComponentInfoTool. A value of the wrong type for one of its
// arguments fails binding (raised); an alias or unknown key is answered in the tool's own envelope.
func init() {
	registerTool(map[string]any{"name": "get-component-info"}, func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
		values := make(map[string]string, len(componentInfoArgNames))
		for _, name := range componentInfoArgNames {
			value, err := optionalStringArg(args, "get-component-info", name)
			if err != nil {
				return nil, err
			}
			values[name] = value
		}
		if message := componentInfoAliasError(args); message != "" {
			return structuredToolResult(creatio.ComponentInfoArgumentFailure(message)), nil
		}
		request := creatio.ComponentInfoRequest{
			ComponentType:   values["component-type"],
			Composite:       values["composite"],
			Search:          values["search"],
			SchemaType:      values["schema-type"],
			Version:         values["version"],
			EnvironmentName: values["environment-name"],
			URI:             values["uri"],
			Resolve: func() (*creatio.Client, error) {
				client, failure, err := envs.resolve("get-component-info", args, scopeDirect)
				if failure != nil {
					return nil, failure
				}
				return client, err
			},
		}
		return structuredToolResult(creatio.ComponentInfoGet(ctx, request)), nil
	})
}

// componentInfoAliasError is clio's BuildLegacyAliasError over the keys ComponentInfoArgs does not bind.
// Go maps carry no request order, so the keys are reported sorted.
func componentInfoAliasError(args map[string]any) string {
	known := make(map[string]bool, len(componentInfoArgNames))
	for _, name := range componentInfoArgNames {
		known[name] = true
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
		if canonical, ok := componentInfoAliases[key]; ok {
			renamed = append(renamed, fmt.Sprintf("'%s' -> '%s'", describeCallerKey(key), canonical))
		} else {
			unknown = append(unknown, fmt.Sprintf("'%s'", describeCallerKey(key)))
		}
	}
	message := ""
	if len(renamed) > 0 {
		message = "Rename: " + joinCallerKeys(renamed) + "."
	}
	if len(unknown) > 0 {
		if message != "" {
			message += " "
		}
		message += "Unknown args: " + joinCallerKeys(unknown) + ". Valid: component-type, composite, search, schema-type, environment-name, version, uri, login, password."
	}
	return message
}
