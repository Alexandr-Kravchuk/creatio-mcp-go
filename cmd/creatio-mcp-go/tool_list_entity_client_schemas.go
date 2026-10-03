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
	}, func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
		entityName, err := optionalStringArg(args, "list-entity-client-schemas", "entity-name")
		if err != nil {
			return nil, err
		}
		// clio resolves the environment per call; a failure is reported inside the tool's own answer.
		client, failure, bindErr := envs.resolve("list-entity-client-schemas", args, scopeDirect)
		if bindErr != nil {
			return nil, bindErr
		}
		if failure != nil {
			refusal := redacted(failure)
			return structuredToolResult(creatio.EntityClientSchemasResult{Error: refusal}), nil
		}
		return structuredToolResult(client.ListEntityClientSchemas(ctx, entityName)), nil
	})
}
