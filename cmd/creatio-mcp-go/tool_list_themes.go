package main

import (
	"context"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func init() {
	registerTool(map[string]any{
		"name": "list-themes",
		"description": "List the custom Creatio themes available on the target environment through ThemeService (Creatio 10.0.0 or later). " +
			"Returns { success, themes:[{ id, caption, cssClassName, cssFilePath }], error? }. An empty themes array means the catalog " +
			"is empty or the caller lacks the CanCustomizeBranding license. The Creatio version floor is not pre-checked.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
	}, func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
		// clio refuses unknown keys inside its envelope, so these failures stay success:false results.
		if refusal := unknownArgumentError(args, "environment-name"); refusal != "" {
			return structuredToolResult(creatio.ThemeListResult{Error: refusal}), nil
		}
		client, failure, err := envs.resolve("list-themes", args, scopeName)
		if err != nil {
			return nil, err
		}
		if failure != nil {
			return structuredToolResult(creatio.ThemeListResult{Error: redacted(failure)}), nil
		}
		return structuredToolResult(client.ListThemes(ctx)), nil
	})
}
