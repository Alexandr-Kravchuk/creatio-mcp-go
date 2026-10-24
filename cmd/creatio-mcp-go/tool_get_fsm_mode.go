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
// the mode. A call without a name is served only by the CREATIO_* default (no clio settings file), and then
// no name is echoed.
type fsmModeAnswer struct {
	EnvironmentName string `json:"environmentName,omitempty"`
	creatio.FsmModeResult
}

// clio's texts for a missing or blank environmentName: its MCP binder's for a required parameter that is
// absent or null, and FsmModeStatusService's ArgumentException for an empty one.
const (
	fsmModeMissingName = "The arguments dictionary is missing a value for the required parameter 'environmentName'. (Parameter 'arguments')"
	fsmModeNullName    = "invalid-parameter-type: argument 'environmentName' for MCP tool 'get-fsm-mode' must be a string. " +
		"Received a JSON null, and the argument is required."
	fsmModeBlankName = "Environment name is required. (Parameter 'environmentName')"
)

// clio has no envelope for this tool: every failure is a tool error. clio binds one scalar parameter,
// environmentName, and ignores other keys (environment-name among them), as its method binder does.
func invokeGetFsmMode(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
	raw, present := args["environmentName"]
	if present && raw == nil {
		return nil, errors.New(fsmModeNullName)
	}
	name, err := optionalStringArg(args, "get-fsm-mode", "environmentName")
	if err != nil {
		return nil, err
	}
	client, err := envs.client(name, creatio.ConnectionOverrides{})
	if err != nil {
		switch {
		case strings.TrimSpace(name) == "" && err.Error() == missingTargetMessage && !present:
			return nil, errors.New(fsmModeMissingName)
		case strings.TrimSpace(name) == "" && err.Error() == missingTargetMessage:
			return nil, errors.New(fsmModeBlankName)
		case strings.HasPrefix(err.Error(), "Environment with key '"):
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
