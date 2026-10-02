package main

import (
	"context"
	"errors"
	"time"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// oauthConnectionRefusal answers clio's emergency OAuth target arguments, which describe-environment takes
// in addition to uri/login/password. Honoring them would describe a different environment.
const oauthConnectionRefusal = "client-id, client-secret and auth-app-uri are not accepted; this server targets the environment configured by the CREATIO_* variables."

func init() {
	registerTool(map[string]any{
		"name": "describe-environment",
		"description": "Describe the single CREATIO_URL configured at process start as one JSON report, returned as the None message of " +
			"execution-log-messages. Always: coreVersion plus user, culture, workspace, maintainer and environmentType metadata from " +
			"ApplicationInfoService. With CanManageSolution: dbEngineType, frameworkKind and frameworkDescription. With cliogate " +
			"2.0.0.32+: productName and licenseInfo; without it a Warning message says so and the rest is still reported. " +
			"An unreachable, non-Creatio or rejecting target answers exit-code 1 with one Error message. Read-only.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
			"timeout": map[string]any{"type": "integer", "minimum": 1, "default": 100000, "description": "Per-request timeout in milliseconds."},
		}},
	}, invokeDescribeEnvironment)
}

func invokeDescribeEnvironment(ctx context.Context, client *creatio.Client, args map[string]any) (*mcp.CallToolResult, error) {
	// clio reports argument refusals as an exit-code-1 envelope, not as a protocol error.
	if refusal := refusesConnectionArgs(args); refusal != "" {
		return structuredToolResult(creatio.DescribeFailure(refusal)), nil
	}
	for _, key := range []string{"client-id", "client-secret", "auth-app-uri"} {
		if _, ok := args[key]; ok {
			return structuredToolResult(creatio.DescribeFailure(oauthConnectionRefusal)), nil
		}
	}
	if refusal := unknownArgumentError(args, map[string]bool{"timeout": true}); refusal != "" {
		return structuredToolResult(creatio.DescribeFailure(refusal)), nil
	}
	var input struct {
		Timeout *int `json:"timeout,omitempty"`
	}
	if err := decodeStrictArgs(args, &input); err != nil {
		return nil, errors.New("invalid-parameter-type: argument 'timeout' for MCP tool 'describe-environment' must be an integer. Received an incompatible JSON value.")
	}
	timeout := time.Duration(0)
	// clio ignores a zero or negative timeout and keeps its default; so does this server.
	if input.Timeout != nil && *input.Timeout > 0 {
		timeout = time.Duration(*input.Timeout) * time.Millisecond
	}
	return structuredToolResult(client.DescribeEnvironment(ctx, timeout)), nil
}
