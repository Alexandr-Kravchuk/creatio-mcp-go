package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func init() {
	registerTool(map[string]any{
		"name": "read-entity-business-rules",
		"description": "Reads ALL entity-level Freedom UI business rules persisted for an entity schema (full package hierarchy, so inherited rules are included). " +
			"Each rule is returned in the create/update contract shape with 'name', 'enabled', and block 'uId's. " +
			"apply-static-filter rules read back with the same friendly 'filter' shape used to create them. Targets the single configured Creatio instance.",
		"inputSchema": map[string]any{
			"type":     "object",
			"required": []string{"package-name", "entity-schema-name"},
			"properties": map[string]any{
				"package-name":       map[string]string{"type": "string", "description": "Target package name on the Creatio environment."},
				"entity-schema-name": map[string]string{"type": "string", "description": "Target entity schema name."},
			},
		},
	}, func(ctx context.Context, client *creatio.Client, args map[string]any) (*mcp.CallToolResult, error) {
		var input struct {
			PackageName      string `json:"package-name"`
			EntitySchemaName string `json:"entity-schema-name"`
		}
		if err := decodeStrictArgs(args, &input); err != nil {
			return nil, fmt.Errorf("decode read-entity-business-rules arguments: %w", err)
		}
		return structuredToolResult(client.ReadEntityBusinessRules(ctx, creatio.BusinessRulesReadRequest{
			PackageName: input.PackageName, SchemaName: input.EntitySchemaName,
		})), nil
	})

	registerTool(map[string]any{
		"name": "read-page-business-rules",
		"description": "Reads ALL page-level Freedom UI business rules persisted for a page schema (full package hierarchy, so inherited rules are included). " +
			"Each rule is returned in the create/update contract shape with 'name', 'enabled', and block 'uId's. Targets the single configured Creatio instance.",
		"inputSchema": map[string]any{
			"type":     "object",
			"required": []string{"package-name"},
			"properties": map[string]any{
				"package-name":     map[string]string{"type": "string", "description": "Target package name on the Creatio environment."},
				"page-schema-name": map[string]string{"type": "string", "description": "Target Freedom UI page schema name. Alias: 'schema-name'."},
				"schema-name":      map[string]string{"type": "string", "description": "Alias for page-schema-name; page-schema-name wins when both are supplied."},
			},
		},
	}, func(ctx context.Context, client *creatio.Client, args map[string]any) (*mcp.CallToolResult, error) {
		var input struct {
			PackageName    string `json:"package-name"`
			PageSchemaName string `json:"page-schema-name"`
			SchemaName     string `json:"schema-name"`
		}
		if err := decodeStrictArgs(args, &input); err != nil {
			return nil, fmt.Errorf("decode read-page-business-rules arguments: %w", err)
		}
		schemaName := input.PageSchemaName
		if strings.TrimSpace(schemaName) == "" {
			schemaName = input.SchemaName
		}
		return structuredToolResult(client.ReadPageBusinessRules(ctx, creatio.BusinessRulesReadRequest{
			PackageName: input.PackageName, SchemaName: schemaName,
		})), nil
	})
}
