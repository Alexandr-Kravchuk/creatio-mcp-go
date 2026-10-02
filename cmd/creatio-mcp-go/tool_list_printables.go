package main

import (
	"context"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func init() {
	registerTool(map[string]any{
		"name": "list-printables",
		"description": "List the MS Word printables (reports) of the single configured environment, optionally filtered by the entity they are " +
			"attached to directly or through their section module. Fill crt.PrintablesRequest templateId and printableCaption ONLY from " +
			"these results; never invent a template GUID. Each item carries templateId, printableCaption, convertInPDF, showInCard and showInSection.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
			"entity-name": map[string]string{"type": "string", "description": "Optional entity schema name (e.g. 'Contact'), matched case-insensitively. Omit to list every MS Word printable."},
		}},
	}, func(ctx context.Context, client *creatio.Client, args map[string]any) (*mcp.CallToolResult, error) {
		// clio ignores unknown keys for this tool, so only a selector of another environment is refused.
		if refusal := refusesConnectionArgs(args); refusal != "" {
			return structuredToolResult(creatio.PrintableListResult{Error: refusal, Printables: []creatio.PrintableItem{}}), nil
		}
		entityName, err := optionalStringArg(args, "list-printables", "entity-name")
		if err != nil {
			return nil, err
		}
		return structuredToolResult(client.ListPrintables(ctx, entityName)), nil
	})
}
