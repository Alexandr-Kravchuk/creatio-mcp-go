package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// create-page: clio's PageCreateTool. The schema name is checked before the environment is resolved; a
// successful create carries clio's "compile-creatio not required" note after any warning of its own.
func init() {
	registerTool(map[string]any{"name": "create-page"}, func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
		var input struct {
			SchemaName         string `json:"schema-name"`
			Template           string `json:"template"`
			PackageName        string `json:"package-name"`
			Caption            string `json:"caption"`
			Description        string `json:"description"`
			EntitySchemaName   string `json:"entity-schema-name"`
			CaptionCulture     string `json:"caption-culture"`
			OptionalProperties string `json:"optional-properties"`
		}
		if err := decodeStrictArgs(withoutEnvironmentArgs(args, scopeDirect), &input); err != nil {
			return nil, fmt.Errorf("decode create-page arguments: %w", err)
		}
		if strings.TrimSpace(input.SchemaName) == "" {
			return structuredToolResult(creatio.PageCreateResponse{Error: "schema-name is required"}), nil
		}
		if !creatio.PageWriteValidSchemaName(input.SchemaName) {
			return structuredToolResult(creatio.PageCreateResponse{Error: creatio.PageWriteSchemaNameError()}), nil
		}
		client, failure, err := envs.resolve("create-page", args, scopeDirect)
		if err != nil {
			return nil, err
		}
		if failure != nil {
			return structuredToolResult(creatio.PageCreateResponse{Error: redacted(failure)}), nil
		}
		response := client.CreatePage(ctx, creatio.PageCreateRequest{SchemaName: input.SchemaName, Template: input.Template,
			PackageName: input.PackageName, Caption: input.Caption, Description: input.Description,
			EntitySchemaName: input.EntitySchemaName, CaptionCulture: input.CaptionCulture, OptionalProperties: input.OptionalProperties})
		if response.Success {
			if strings.TrimSpace(response.Note) == "" {
				response.Note = creatio.PageWriteCompileNotRequiredNote
			} else {
				response.Note += " " + creatio.PageWriteCompileNotRequiredNote
			}
		}
		return structuredToolResult(response), nil
	}, withAnnotations(toolAnnotations{ReadOnly: false, Destructive: true, Idempotent: false, OpenWorld: false}))
}
