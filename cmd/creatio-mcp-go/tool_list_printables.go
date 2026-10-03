package main

import (
	"context"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func init() {
	registerTool(map[string]any{
		"name": "list-printables",
		"description": "List the MS Word printables (reports) of the target environment, optionally filtered by the entity they are " +
			"attached to directly or through their section module. Fill crt.PrintablesRequest templateId and printableCaption ONLY from " +
			"these results; never invent a template GUID. Each item carries templateId, printableCaption, convertInPDF, showInCard and showInSection.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
			"entity-name": map[string]string{"type": "string", "description": "Optional entity schema name (e.g. 'Contact'), matched case-insensitively. Omit to list every MS Word printable."},
		}},
	}, func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
		entityName, err := optionalStringArg(args, "list-printables", "entity-name")
		if err != nil {
			return nil, err
		}
		// clio resolves the environment per call; a failure is reported inside the tool's own answer.
		client, failure, bindErr := envs.resolve("list-printables", args, scopeDirect)
		if bindErr != nil {
			return nil, bindErr
		}
		if failure != nil {
			refusal := redacted(failure)
			return structuredToolResult(creatio.PrintableListResult{Error: refusal, Printables: []creatio.PrintableItem{}}), nil
		}
		return structuredToolResult(client.ListPrintables(ctx, entityName)), nil
	})
}
