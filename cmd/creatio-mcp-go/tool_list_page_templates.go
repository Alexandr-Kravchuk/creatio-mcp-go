package main

import (
	"context"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func init() {
	registerTool(map[string]any{
		"name": "list-page-templates",
		"description": "List the Freedom UI page templates advertised by the target environment (schema.template.api), web first, " +
			"plus the dashboard and desktop templates the endpoint omits. Call this before create-page to discover valid template values.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
			"schema-type": map[string]string{"type": "string", "description": "Optional schema-type filter: 'web' (Freedom UI page) or 'mobile' (mobile page). Defaults to all."},
		}},
	}, func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
		schemaType, err := optionalStringArg(args, "list-page-templates", "schema-type")
		if err != nil {
			return nil, err
		}
		// clio resolves the environment per call; a failure is reported inside the tool's own answer.
		client, failure, bindErr := envs.resolve("list-page-templates", args, scopeDirect)
		if bindErr != nil {
			return nil, bindErr
		}
		if failure != nil {
			refusal := redacted(failure)
			return structuredToolResult(creatio.PageTemplateListResult{Error: refusal}), nil
		}
		return structuredToolResult(client.ListPageTemplates(ctx, schemaType)), nil
	})
}
