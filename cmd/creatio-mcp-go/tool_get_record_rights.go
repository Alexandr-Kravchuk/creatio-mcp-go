package main

import (
	"context"
	"fmt"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func init() {
	registerTool(map[string]any{
		"name": "get-record-rights",
		"description": "See who has access to a record or dashboard — read the record-level access rights of a single Creatio record. " +
			"Target it with entity + record-id (for a client-unit schema/dashboard, pass entity=SysSchemaAdminUnit and record-id=the schema UId). " +
			"Returns each grant as operation (read/edit/delete) / level (granted/delegated) -> grantee.",
		"inputSchema": map[string]any{
			"type":     "object",
			"required": []string{"entity", "record-id"},
			"properties": map[string]any{
				"entity":    map[string]string{"type": "string", "description": "Entity schema name of the target record. For a client-unit schema/dashboard, pass SysSchemaAdminUnit."},
				"record-id": map[string]string{"type": "string", "description": "Primary column value (record id) of the target record. For a client-unit schema/dashboard, the schema UId."},
			},
		},
	}, func(ctx context.Context, client *creatio.Client, args map[string]any) (*mcp.CallToolResult, error) {
		var input struct {
			Entity   string `json:"entity"`
			RecordID string `json:"record-id"`
		}
		if err := decodeStrictArgs(args, &input); err != nil {
			return nil, fmt.Errorf("decode get-record-rights arguments: %w", err)
		}
		return structuredToolResult(client.GetRecordRights(ctx, input.Entity, input.RecordID)), nil
	})
}
