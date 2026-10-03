package main

import (
	"context"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func init() {
	registerTool(map[string]any{
		"name": "get-classic-list-columns",
		"description": "Resolve the effective default columns of a Classic section list on the single configured Creatio instance, through read-only APIs. " +
			"Returns source=profile for the saved grid profile the section actually renders (with view, viewType and profileScope), " +
			"source=schema-default for static getGridDataColumns/initColumnsConfig paths, source=entity-default for the entity primary " +
			"display column, or a successful source=none with no columns. Pass ignore-profile=true to get only the statically declared set. " +
			"Reads profile data, never writes it. The static branch reads the section bodies with a structural JavaScript reader rather than a full parser.",
		"inputSchema": map[string]any{"type": "object", "required": []string{"schema-name"}, "properties": map[string]any{
			"schema-name":    map[string]string{"type": "string", "description": "Classic section schema name, for example 'ContactSectionV2'"},
			"ignore-profile": map[string]string{"type": "boolean", "description": "Optional. True to skip the saved grid profile and resolve only statically declared columns."},
		}},
	}, func(ctx context.Context, client *creatio.Client, args map[string]any) (*mcp.CallToolResult, error) {
		// clio ignores unknown keys for this tool, so only a selector of another environment is refused.
		if refusal := refusesConnectionArgs(args); refusal != "" {
			return structuredToolResult(creatio.ClassicListColumnsResult{Columns: []creatio.ClassicListColumnInfo{}, Notes: []string{}, Error: refusal}), nil
		}
		schemaName, err := optionalStringArg(args, "get-classic-list-columns", "schema-name")
		if err != nil {
			return nil, err
		}
		ignoreProfile, err := schemaGetBoolArg(args, "get-classic-list-columns", "ignore-profile")
		if err != nil {
			return nil, err
		}
		return structuredToolResult(client.GetClassicListColumns(ctx, schemaName, ignoreProfile)), nil
	})
}
