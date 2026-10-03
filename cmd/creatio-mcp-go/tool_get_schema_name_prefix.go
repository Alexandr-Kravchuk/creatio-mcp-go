package main

import (
	"context"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func init() {
	registerTool(map[string]any{
		"name": "get-schema-name-prefix",
		"description": "Returns the active SchemaNamePrefix system setting of the target Creatio environment." +
			"Returns an empty string with success:true when no prefix is configured (use no prefix then). Default Creatio environments return 'Usr'.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
	}, func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
		// clio ignores unknown keys for this tool and classifies a resolution failure as Configuration.
		client, failure, err := envs.resolve("get-schema-name-prefix", args, scopeName)
		if err != nil {
			return nil, err
		}
		if failure != nil {
			return structuredToolResult(creatio.SchemaNamePrefixConfigurationFailure(redacted(failure))), nil
		}
		return structuredToolResult(client.GetSchemaNamePrefix(ctx)), nil
	})
}
