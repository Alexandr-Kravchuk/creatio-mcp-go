package main

import (
	"context"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func init() {
	registerTool(map[string]any{
		"name": "list-entity-client-schemas",
		"description": "Resolve the page-role graph of an entity for a Classic->Freedom migration: its Classic sections, edit pages " +
			"(including per-type pages) and add mini pages, each classified classic, freedom, or unknown. Per-type edit pages also carry " +
			"the Type's display name (typeColumnDisplayValue) when it resolves. One level only; call it per detail entity to recurse.",
		"inputSchema": map[string]any{"type": "object", "required": []string{"entity-name"}, "properties": map[string]any{
			"entity-name": map[string]string{"type": "string", "description": "Entity schema name, e.g. 'Contract' or 'SupportUnit'"},
		}},
	}, func(ctx context.Context, client *creatio.Client, args map[string]any) (*mcp.CallToolResult, error) {
		// clio ignores unknown keys for this tool, so only a selector of another environment is refused.
		if refusal := refusesConnectionArgs(args); refusal != "" {
			return structuredToolResult(creatio.EntityClientSchemasResult{Error: refusal}), nil
		}
		entityName, err := optionalStringArg(args, "list-entity-client-schemas", "entity-name")
		if err != nil {
			return nil, err
		}
		return structuredToolResult(client.ListEntityClientSchemas(ctx, entityName)), nil
	})
}
