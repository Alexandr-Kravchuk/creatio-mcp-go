package main

import (
	"context"
	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func init() {
	registerTool(map[string]any{"name": "delete-app"}, func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
		name, err := optionalStringArg(args, "delete-app", "app-name")
		if err != nil {
			return nil, err
		}
		if name == "" {
			return structuredToolResult(creatio.AppDeleteResponse{Error: "app-name is required. Provide the application name or code."}), nil
		}
		client, failure, err := envs.resolve("delete-app", args, scopeDirect)
		if err != nil {
			return nil, err
		}
		if failure != nil {
			return structuredToolResult(creatio.AppDeleteResponse{Error: redacted(failure)}), nil
		}
		return structuredToolResult(client.DeleteApp(ctx, name)), nil
	}, withAnnotations(toolAnnotations{ReadOnly: false, Destructive: true, Idempotent: false, OpenWorld: false}))
}
