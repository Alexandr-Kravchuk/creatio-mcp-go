package main

// dataforge-initialize and dataforge-update: clio's DataForgeTool.Initialize / Update. Both schedule index
// work through Creatio's DataForgeMaintenanceService and answer clio's DataForgeMaintenanceResponse; every
// failure, a missing or unknown environment included, is reported inside that envelope with the tool's own
// error code. Unknown keys are ignored (the legacy environmentName spelling therefore reads as "missing").
// clio: [McpServerTool(ReadOnly = false, Destructive = true, Idempotent = false, OpenWorld = false)].

import (
	"context"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func init() {
	for _, item := range []struct {
		name       string
		initialize bool
	}{{"dataforge-initialize", true}, {"dataforge-update", false}} {
		item := item
		registerTool(map[string]any{"name": item.name}, func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
			name, err := optionalStringArg(args, item.name, "environment-name")
			if err != nil {
				return nil, err
			}
			if pkgWriteBlank(name) {
				// DataForgeTool.EnsureRequired, raised inside the tool's catch.
				return structuredToolResult(creatio.DataForgeFailure(item.name, "environment-name is required.")), nil
			}
			client, failure, err := dataForgeTarget(envs, item.name, args)
			if client == nil {
				return failure, err
			}
			return structuredToolResult(client.DataForgeMaintain(ctx, item.initialize)), nil
		}, withAnnotations(toolAnnotations{Destructive: true}))
	}
}
