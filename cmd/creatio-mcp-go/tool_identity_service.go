package main

// check-auth-code-flow and get-identity-service-config: clio 8.1.0.142's CheckAuthCodeFlowTool (flat
// environmentName and format, as the identity assertion tools) and GetIdentityServiceConfigTool ({success,
// config, error}).

import (
	"context"
	"errors"
	"strings"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func init() {
	registerTool(map[string]any{"name": "check-auth-code-flow"}, invokeCheckAuthCodeFlow,
		withAnnotations(toolAnnotations{ReadOnly: true, Destructive: false, Idempotent: true, OpenWorld: false}))
	registerTool(map[string]any{"name": "get-identity-service-config"}, invokeGetIdentityServiceConfig,
		withAnnotations(toolAnnotations{ReadOnly: true, Destructive: false, Idempotent: true, OpenWorld: false}))
}

// invokeCheckAuthCodeFlow reads the arguments as the identity assertion tools do; unlike their port, the Safe
// refusal is raised, as clio's BaseTool raises it for this tool.
func invokeCheckAuthCodeFlow(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
	const tool = "check-auth-code-flow"
	name, present, err := adminWriteIdentityEnvironment(args, tool)
	if err != nil {
		return nil, err
	}
	// clio binds every parameter before it reports a missing one, so a format of the wrong type is refused first.
	format, err := optionalStringArg(args, tool, "format")
	if err != nil {
		return nil, err
	}
	if !present {
		return nil, errors.New("The arguments dictionary is missing a value for the required parameter 'environmentName'. (Parameter 'arguments')")
	}
	if strings.TrimSpace(name) == "" {
		return structuredToolResult(creatio.CommandFailure("environment-name is required and cannot be empty.")), nil
	}
	client, failure, err := envs.resolve(tool, map[string]any{"environment-name": name}, scopeName)
	if err != nil {
		return nil, err
	}
	if failure != nil {
		var target *environmentError
		if errors.As(failure, &target) && target.safe {
			return nil, failure
		}
		return resolverFailureEnvelope(failure), nil
	}
	return structuredToolResult(client.CheckAuthCodeFlow(ctx, format)), nil
}

func invokeGetIdentityServiceConfig(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
	name, err := optionalStringArg(args, "get-identity-service-config", "environment-name")
	if err != nil {
		return nil, err
	}
	// A blank name is refused by the resolver with clio's missing-target text (CREATIO_* serve it only while
	// clio's settings file does not exist).
	client, failure, err := envs.resolve("get-identity-service-config", map[string]any{"environment-name": name}, scopeName)
	if err != nil {
		return nil, err
	}
	if failure != nil {
		return structuredToolResult(creatio.IdentityServiceConfigResponse{Error: failure.Error()}), nil
	}
	return structuredToolResult(client.GetIdentityServiceConfig(ctx)), nil
}
