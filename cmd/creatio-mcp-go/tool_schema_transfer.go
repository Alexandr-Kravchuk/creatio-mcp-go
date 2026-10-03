package main

import (
	"context"
	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func init() {
	for _, name := range []string{"export-schema", "import-schema"} {
		tool := name
		registerTool(map[string]any{"name": tool}, func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
			values := map[string]string{}
			for _, key := range []string{"schema-name", "package-name", "manager-name", "destination", "path"} {
				value, err := optionalStringArg(args, tool, key)
				if err != nil {
					return nil, err
				}
				values[key] = value
			}
			dry, err := schemaGetBoolArg(args, tool, "dry-run")
			if err != nil {
				return nil, err
			}
			layer, err := schemaGetBoolArg(args, tool, "allow-new-layer")
			if err != nil {
				return nil, err
			}
			client, failure, err := envs.resolve(tool, args, scopeName)
			if err != nil {
				return nil, err
			}
			if failure != nil {
				return resolverFailureEnvelope(failure), nil
			}
			if requirement := client.SchemaTransferRequirement(ctx); requirement != "" {
				return structuredToolResult(creatio.CommandFailure(requirement)), nil
			}
			input := creatio.SchemaTransferRequest{SchemaName: values["schema-name"], PackageName: values["package-name"], ManagerName: values["manager-name"], Destination: values["destination"], Path: values["path"], DryRun: dry, AllowNewLayer: layer}
			if tool == "export-schema" {
				return structuredToolResult(client.ExportSchema(ctx, input)), nil
			}
			return structuredToolResult(client.ImportSchema(ctx, input)), nil
		}, withAnnotations(toolAnnotations{ReadOnly: false, Destructive: tool == "import-schema", Idempotent: tool == "export-schema", OpenWorld: false}))
	}
}
