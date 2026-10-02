package main

import (
	"context"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func init() {
	registerTool(map[string]any{
		"name": "get-schema-name-prefix",
		"description": "Returns the active SchemaNamePrefix system setting of the single configured Creatio instance. " +
			"Returns an empty string with success:true when no prefix is configured (use no prefix then). Default Creatio environments return 'Usr'.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
	}, func(ctx context.Context, client *creatio.Client, args map[string]any) (*mcp.CallToolResult, error) {
		// clio ignores unknown keys for this tool, so only a selector of another environment is refused.
		if refusal := refusesConnectionArgs(args); refusal != "" {
			return structuredToolResult(creatio.SchemaNamePrefixResult{Error: refusal}), nil
		}
		return structuredToolResult(client.GetSchemaNamePrefix(ctx)), nil
	})
}
