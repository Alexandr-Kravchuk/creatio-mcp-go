package main

import (
	"context"
	"errors"
	"time"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// describeEnvironmentKnownArgs is clio's accepted list, in the order its unknown-argument hint names it.
// describe-environment is the one tool that also takes OAuth client credentials for a direct connection.
var describeEnvironmentKnownArgs = []string{"environment-name", "uri", "login", "password", "client-id", "client-secret", "auth-app-uri", "timeout"}

func init() {
	registerTool(map[string]any{
		"name": "describe-environment",
		"description": "Describe the target Creatio environment as one JSON report, returned as the None message of " +
			"execution-log-messages. Always: coreVersion plus user, culture, workspace, maintainer and environmentType metadata from " +
			"ApplicationInfoService. With CanManageSolution: dbEngineType, frameworkKind and frameworkDescription. With cliogate " +
			"2.0.0.32+: productName and licenseInfo; without it a Warning message says so and the rest is still reported. " +
			"An unreachable, non-Creatio or rejecting target answers exit-code 1 with one Error message. Read-only.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
			"timeout": map[string]any{"type": "integer", "minimum": 1, "default": 100000, "description": "Per-request timeout in milliseconds."},
		}},
	}, invokeDescribeEnvironment)
}

func invokeDescribeEnvironment(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
	// clio reports argument refusals as an exit-code-1 envelope, not as a protocol error.
	if refusal := unknownArgumentError(args, describeEnvironmentKnownArgs...); refusal != "" {
		return structuredToolResult(creatio.CommandFailure(refusal)), nil
	}
	var input struct {
		Timeout *int `json:"timeout,omitempty"`
	}
	if err := decodeStrictArgs(withoutEnvironmentArgs(args, scopeDirectOAuth), &input); err != nil {
		return nil, errors.New("invalid-parameter-type: argument 'timeout' for MCP tool 'describe-environment' must be an integer. Received an incompatible JSON value.")
	}
	client, refusal, err := envs.resolve("describe-environment", args, scopeDirectOAuth)
	if err != nil {
		return nil, err
	}
	if refusal != nil {
		return resolverFailureEnvelope(refusal), nil
	}
	timeout := time.Duration(0)
	// clio ignores a zero or negative timeout and keeps its default; so does this server.
	if input.Timeout != nil && *input.Timeout > 0 {
		timeout = time.Duration(*input.Timeout) * time.Millisecond
	}
	return structuredToolResult(client.DescribeEnvironment(ctx, timeout)), nil
}
