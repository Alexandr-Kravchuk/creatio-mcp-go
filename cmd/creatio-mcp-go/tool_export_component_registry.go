package main

import (
	"context"
	"fmt"
	"sort"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// exportRegistryArgNames are ExportComponentRegistryArgs' members, in the order clio's hint names them.
var exportRegistryArgNames = []string{"version", "schema-type", "output-file", "environment-name", "uri", "login", "password"}

// exportRegistryAliases is ExportComponentRegistryTool.LegacyAliases.
var exportRegistryAliases = map[string]string{"schemaType": "schema-type", "schema_type": "schema-type",
	"outputFile": "output-file", "output_file": "output-file", "environmentName": "environment-name",
	"environment_name": "environment-name"}

// export-component-registry: clio's ExportComponentRegistryTool. A wrong-typed argument fails binding
// (raised); an alias or unknown key, like every other failure, is answered in the tool's own envelope.
func init() {
	registerTool(map[string]any{"name": "export-component-registry"}, func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
		values := make(map[string]string, len(exportRegistryArgNames))
		for _, name := range exportRegistryArgNames {
			value, err := optionalStringArg(args, "export-component-registry", name)
			if err != nil {
				return nil, err
			}
			values[name] = value
		}
		if message := exportRegistryAliasError(args); message != "" {
			return structuredToolResult(creatio.ComponentRegistryExportResponse{Error: &message}), nil
		}
		result, err := creatio.ExportComponentRegistry(ctx, creatio.ComponentRegistryExportRequest{
			Version: values["version"], SchemaType: values["schema-type"], OutputFile: values["output-file"],
			EnvironmentName: values["environment-name"], URI: values["uri"],
			Resolve: func() (*creatio.Client, error) {
				client, failure, err := envs.resolve("export-component-registry", args, scopeDirect)
				if failure != nil {
					return nil, failure
				}
				return client, err
			},
		})
		if err != nil {
			return nil, err
		}
		return structuredToolResult(result), nil
	}, withAnnotations(toolAnnotations{}))
}

// exportRegistryAliasError is clio's BuildLegacyAliasError over the keys the arguments do not bind.
func exportRegistryAliasError(args map[string]any) string {
	known := map[string]bool{}
	for _, name := range exportRegistryArgNames {
		known[name] = true
	}
	var keys []string
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
		if canonical, ok := exportRegistryAliases[key]; ok {
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
		message += "Unknown args: " + joinCallerKeys(unknown) + ". Valid: version, schema-type, output-file, environment-name, uri, login, password."
	}
	return message
}
