package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// dataWriteBindingError is clio's answer when the MCP binder cannot bind a tool's args record.
func dataWriteBindingError(tool string) error {
	return errors.New("invalid-parameter-type: argument 'args' for MCP tool '" + tool + "' must be an object. Received an incompatible JSON value.")
}

// dataWriteNestedBindingError is clio's answer for a value inside an argument that does not bind.
func dataWriteNestedBindingError(tool, argument string) error {
	return errors.New("invalid-parameter-type: argument '" + argument + "' for MCP tool '" + tool + "' contains a value that does not match the documented shape. Received an incompatible JSON value.")
}

func init() {
	// clio's DataServiceBatchTool: the environment check, the environment resolution, then the service's
	// own validation, each raised as the tool's failure.
	registerTool(map[string]any{"name": "execute-dataservice-batch"}, func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
		const tool = "execute-dataservice-batch"
		name, err := optionalStringArg(args, tool, "environment-name")
		if err != nil {
			return nil, err
		}
		var operations []*creatio.DataWriteBatchOperation
		if raw, present := args["operations"]; present && raw != nil {
			encoded, _ := json.Marshal(raw)
			if operations, err = creatio.DataWriteBatchDecode(encoded); err != nil {
				if errors.Is(err, creatio.ErrDataWriteBatchNotArray) {
					return nil, errors.New("invalid-parameter-type: argument 'operations' for MCP tool '" + tool + "' must be an array. Received an incompatible JSON value.")
				}
				return nil, dataWriteNestedBindingError(tool, "operations")
			}
		}
		if strings.TrimSpace(name) == "" {
			return nil, errors.New("environment-name is required.")
		}
		client, err := envs.target(tool, map[string]any{"environment-name": name}, scopeName)
		if err != nil {
			return nil, err
		}
		result, err := client.ExecuteDataServiceBatch(ctx, operations)
		if err != nil {
			return nil, err
		}
		return structuredToolResult(result), nil
	}, withAnnotations(toolAnnotations{ReadOnly: false, Destructive: true, Idempotent: false, OpenWorld: false}))
}
