package main

import (
	"context"
	"errors"
	"strings"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// get-identity-assertion, get-identity-public-jwk and regenerate-identity-signing-key: clio's identity
// assertion tools. clio binds two flat parameters, environmentName and format; environment-name is honoured
// too, as get-fsm-mode does, so a caller using the common spelling is never served by another environment.
func init() {
	for _, entry := range []struct {
		tool        creatio.AdminWriteIdentityTool
		annotations toolAnnotations
	}{
		{creatio.AdminWriteIdentityAssertion, toolAnnotations{ReadOnly: false, Destructive: false, Idempotent: false, OpenWorld: false}},
		{creatio.AdminWriteIdentityPublicJwk, toolAnnotations{ReadOnly: true, Destructive: false, Idempotent: true, OpenWorld: false}},
		{creatio.AdminWriteIdentityRegenerate, toolAnnotations{ReadOnly: false, Destructive: true, Idempotent: false, OpenWorld: false}},
	} {
		tool := entry.tool
		registerTool(map[string]any{"name": tool.Name}, func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
			return invokeAdminWriteIdentity(ctx, envs, args, tool)
		}, withAnnotations(entry.annotations))
	}
}

func invokeAdminWriteIdentity(ctx context.Context, envs *environments, args map[string]any, tool creatio.AdminWriteIdentityTool) (*mcp.CallToolResult, error) {
	name, present, err := adminWriteIdentityEnvironment(args, tool.Name)
	if err != nil {
		return nil, err
	}
	// clio binds every parameter before it reports a missing one, so a format of the wrong type is refused first.
	format, err := optionalStringArg(args, tool.Name, "format")
	if err != nil {
		return nil, err
	}
	if !present {
		return nil, errors.New("The arguments dictionary is missing a value for the required parameter 'environmentName'. (Parameter 'arguments')")
	}
	if strings.TrimSpace(name) == "" {
		return structuredToolResult(creatio.CommandFailure("environment-name is required and cannot be empty.")), nil
	}
	client, failure, err := envs.resolve(tool.Name, map[string]any{"environment-name": name}, scopeName)
	if err != nil {
		return nil, err
	}
	if failure != nil {
		return resolverFailureEnvelope(failure), nil
	}
	return structuredToolResult(client.IdentityCommand(ctx, tool, format)), nil
}

// adminWriteIdentityEnvironment reads environmentName, else environment-name; present is false when neither
// was sent.
func adminWriteIdentityEnvironment(args map[string]any, tool string) (string, bool, error) {
	for _, key := range []string{"environmentName", "environment-name"} {
		if value, ok := args[key]; ok && value != nil {
			text, err := optionalStringArg(args, tool, key)
			if err == nil && key == "environment-name" && strings.TrimSpace(text) == "" {
				// clio binds only environmentName: a blank environment-name is no value for it.
				break
			}
			return text, true, err
		}
	}
	return "", false, nil
}
