package main

import (
	"context"
	"fmt"
	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func init() {
	registerTool(map[string]any{"name": "create-client-unit-schema"}, func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
		var input struct {
			SchemaName     string `json:"schema-name"`
			PackageName    string `json:"package-name"`
			Caption        string `json:"caption"`
			Description    string `json:"description"`
			CaptionCulture string `json:"caption-culture"`
		}
		if err := decodeStrictArgs(withoutEnvironmentArgs(args, scopeDirect), &input); err != nil {
			return nil, fmt.Errorf("decode create-client-unit-schema arguments: %w", err)
		}
		client, failure, err := envs.resolve("create-client-unit-schema", args, scopeDirect)
		if err != nil {
			return nil, err
		}
		if failure != nil {
			return structuredToolResult(creatio.SourceCodeSchemaCreateResponse{Error: redacted(failure)}), nil
		}
		return structuredToolResult(client.CreateClientUnitSchema(ctx, creatio.ClientUnitCreateRequest{SchemaName: input.SchemaName, PackageName: input.PackageName, Caption: input.Caption, Description: input.Description, CaptionCulture: input.CaptionCulture})), nil
	}, withAnnotations(toolAnnotations{ReadOnly: false, Destructive: false, Idempotent: false, OpenWorld: false}))
	registerTool(map[string]any{"name": "update-client-unit-schema"}, func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
		var input struct {
			SchemaName string  `json:"schema-name"`
			Body       *string `json:"body"`
			BodyFile   string  `json:"body-file"`
			DryRun     bool    `json:"dry-run"`
		}
		if err := decodeStrictArgs(withoutEnvironmentArgs(args, scopeDirect), &input); err != nil {
			return nil, fmt.Errorf("decode update-client-unit-schema arguments: %w", err)
		}
		client, failure, err := envs.resolve("update-client-unit-schema", args, scopeDirect)
		if err != nil {
			return nil, err
		}
		if failure != nil {
			return structuredToolResult(creatio.SchemaBodyUpdateResponse{Error: redacted(failure)}), nil
		}
		return structuredToolResult(client.UpdateClientUnitSchema(ctx, creatio.SchemaBodyUpdateRequest{SchemaName: input.SchemaName, Body: input.Body, BodyFile: input.BodyFile, DryRun: input.DryRun})), nil
	}, withAnnotations(toolAnnotations{ReadOnly: false, Destructive: true, Idempotent: false, OpenWorld: false}))
}
