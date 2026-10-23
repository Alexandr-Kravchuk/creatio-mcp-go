package main

// check-settings-health: clio 8.1.0.142's SettingsHealthTool, the bootstrap report for appsettings.json. Like
// clio's MCP server it applies the bootstrap's repairs: a missing file is created and a pending migration is
// written (internal/clioconfig). It takes no arguments and ignores any it is given.

import (
	"context"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/clioconfig"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func init() {
	registerTool(map[string]any{"name": "check-settings-health"}, func(_ context.Context, envs *environments, _ map[string]any) (*mcp.CallToolResult, error) {
		report, err := clioconfig.Check(envs.settingsPath(), true)
		if err != nil {
			return nil, err
		}
		return structuredToolResult(report), nil
	}, withAnnotations(toolAnnotations{ReadOnly: true, Destructive: false, Idempotent: true, OpenWorld: false}))
}
