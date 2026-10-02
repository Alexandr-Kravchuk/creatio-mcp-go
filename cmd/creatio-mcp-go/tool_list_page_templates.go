package main

import (
	"context"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func init() {
	registerTool(map[string]any{
		"name": "list-page-templates",
		"description": "List the Freedom UI page templates advertised by the single configured environment (schema.template.api), web first, " +
			"plus the dashboard and desktop templates the endpoint omits. Call this before create-page to discover valid template values.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
			"schema-type": map[string]string{"type": "string", "description": "Optional schema-type filter: 'web' (Freedom UI page) or 'mobile' (mobile page). Defaults to all."},
		}},
	}, func(ctx context.Context, client *creatio.Client, args map[string]any) (*mcp.CallToolResult, error) {
		// clio ignores unknown keys for this tool, so only a selector of another environment is refused.
		if refusal := refusesConnectionArgs(args); refusal != "" {
			return structuredToolResult(creatio.PageTemplateListResult{Error: refusal}), nil
		}
		schemaType, err := optionalStringArg(args, "list-page-templates", "schema-type")
		if err != nil {
			return nil, err
		}
		return structuredToolResult(client.ListPageTemplates(ctx, schemaType)), nil
	})
}
