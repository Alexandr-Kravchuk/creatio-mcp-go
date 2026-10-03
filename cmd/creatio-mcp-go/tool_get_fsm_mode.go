package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func init() {
	registerTool(map[string]any{
		"name": "get-fsm-mode",
		"description": "Detect whether a registered Creatio environment is in file system mode (FSM) on or off, " +
			"from ApplicationInfoService.svc/GetApplicationInfo. Returns environmentName, mode, useStaticFileContent and staticFileContent. " +
			"Read-only; this server does not offer set-fsm-mode.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
			"environmentName": map[string]string{"type": "string", "description": "Registered clio environment name."},
		}},
	}, invokeGetFsmMode)
}

// fsmModeAnswer is clio's FsmModeStatusResult: the environment name exactly as the caller passed it, then
// the mode. Without a name the call goes to the default target and no name is echoed.
type fsmModeAnswer struct {
	EnvironmentName string `json:"environmentName,omitempty"`
	creatio.FsmModeResult
}

// clio has no envelope for this tool: every failure is a tool error. clio binds one scalar parameter,
// environmentName; environment-name is honoured too, so a caller using the common spelling is never served
// by another environment. Other keys are ignored, as clio's method binder does.
func invokeGetFsmMode(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
	name, err := optionalStringArg(args, "get-fsm-mode", "environmentName")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(name) == "" {
		if name, err = optionalStringArg(args, "get-fsm-mode", "environment-name"); err != nil {
			return nil, err
		}
	}
	client, err := envs.client(name, creatio.ConnectionOverrides{})
	if err != nil {
		if strings.HasPrefix(err.Error(), "Environment with key '") {
			// get-fsm-mode looks the name up without clio's resolver, so its text is the older one.
			return nil, fmt.Errorf("Environment with key '%s' not found. Check your clio configuration.", name)
		}
		return nil, errors.New(redacted(err))
	}
	result, err := client.GetFsmMode(ctx)
	if err != nil {
		return nil, err
	}
	return structuredToolResult(fsmModeAnswer{EnvironmentName: name, FsmModeResult: result}), nil
}
