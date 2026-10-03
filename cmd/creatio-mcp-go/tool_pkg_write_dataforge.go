package main

import (
	"context"
	"strings"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func init() {
	for _, name := range []string{"dataforge-initialize", "dataforge-update"} {
		tool := name
		registerTool(map[string]any{"name": tool}, func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
			name, err := optionalStringArg(args, tool, "environment-name")
			if err != nil {
				return nil, err
			}
			if strings.TrimSpace(name) == "" {
				return structuredToolResult(creatio.DataForgeFailure(tool, "environment-name is required.")), nil
			}
			client, failure, err := envs.resolve(tool, args, scopeName)
			if err != nil {
				return nil, err
			}
			if failure != nil {
				return structuredToolResult(creatio.DataForgeFailure(tool, redacted(failure))), nil
			}
			return structuredToolResult(client.DataForgeMaintain(ctx, tool == "dataforge-initialize")), nil
		}, withAnnotations(toolAnnotations{Destructive: true}))
	}
}
