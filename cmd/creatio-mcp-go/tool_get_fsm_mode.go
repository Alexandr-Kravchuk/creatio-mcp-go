package main

import (
	"context"
	"errors"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func init() {
	registerTool(map[string]any{
		"name": "get-fsm-mode",
		"description": "Detect whether the single CREATIO_URL configured at process start is in file system mode (FSM) on or off, " +
			"from ApplicationInfoService.svc/GetApplicationInfo. Returns mode, useStaticFileContent and staticFileContent. " +
			"Read-only; this server does not offer set-fsm-mode.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
	}, invokeGetFsmMode)
}

// clio has no envelope for this tool: every failure, including an environment selector this server refuses,
// is a tool error. Unknown keys are ignored, as clio's method binder does.
func invokeGetFsmMode(ctx context.Context, client *creatio.Client, args map[string]any) (*mcp.CallToolResult, error) {
	if refusal := runtimeSelectorRefusal(args); refusal != "" {
		return nil, errors.New(refusal)
	}
	result, err := client.GetFsmMode(ctx)
	if err != nil {
		return nil, err
	}
	return structuredToolResult(result), nil
}
