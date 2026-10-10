package main

import (
	"context"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// set-fsm-mode: clio's FsmModeTool.SetFsmMode (TurnFsmCommand). The environment's registered EnvironmentPath
// and IsNetCore decide how the configuration is written, as clio's EnvironmentSettings do.
func init() {
	registerTool(map[string]any{"name": "set-fsm-mode"}, invokeAdminWriteSetFsmMode,
		withAnnotations(toolAnnotations{ReadOnly: false, Destructive: true, Idempotent: false, OpenWorld: false}))
}

func invokeAdminWriteSetFsmMode(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
	const tool = "set-fsm-mode"
	text, err := adminWriteStrings(args, tool, "environment-name", "mode")
	if err != nil {
		return nil, err
	}
	client, failure, err := envs.resolve(tool, args, scopeName)
	if err != nil {
		return nil, err
	}
	if failure != nil {
		return resolverFailureEnvelope(failure), nil
	}
	name := adminWriteDeref(text["environment-name"])
	target := creatio.AdminWriteFsmTarget{Name: name, IsNetCore: client.AdminWriteIsNetCore()}
	if settings, _ := envs.snapshot(); name != "" {
		if registered, ok := settings.Find(name); ok {
			target.EnvironmentPath = registered.EnvironmentPath
		}
	}
	return structuredToolResult(client.SetFsmMode(ctx, target, adminWriteDeref(text["mode"]))), nil
}
