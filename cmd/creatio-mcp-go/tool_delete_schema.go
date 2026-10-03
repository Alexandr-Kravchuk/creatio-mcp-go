package main

import (
	"context"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func init() {
	registerTool(map[string]any{"name": "delete-schema"}, func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
		name, err := optionalStringArg(args, "delete-schema", "schema-name")
		if err != nil {
			return nil, err
		}
		workspace, err := optionalStringArg(args, "delete-schema", "workspace-path")
		if err != nil {
			return nil, err
		}
		remote, err := schemaGetBoolArg(args, "delete-schema", "remote")
		if err != nil {
			return nil, err
		}
		client, failure, err := envs.resolve("delete-schema", args, scopeName)
		if err != nil {
			return nil, err
		}
		if failure != nil {
			return resolverFailureEnvelope(failure), nil
		}
		return structuredToolResult(client.DeleteSchema(ctx, name, workspace, remote)), nil
	}, withAnnotations(toolAnnotations{ReadOnly: false, Destructive: true, Idempotent: false, OpenWorld: false}))
}
