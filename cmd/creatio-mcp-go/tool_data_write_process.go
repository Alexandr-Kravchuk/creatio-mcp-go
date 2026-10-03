package main

import (
	"context"
	"encoding/json"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func init() {
	registerTool(map[string]any{"name": "run-process"}, func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
		const tool = "run-process"
		valid := []string{"environment-name", "process-name", "parameters", "result-parameters", "timeout", "uri", "login", "password"}
		if message := unknownArgumentError(args, valid...); message != "" {
			return structuredToolResult(creatio.DataWriteProcessResult{Warnings: []string{}, Error: message}), nil
		}
		if _, present := args["process-name"]; !present {
			return nil, dataWriteBindingError(tool)
		}
		name, err := optionalStringArg(args, tool, "process-name")
		if err != nil {
			return nil, err
		}
		var parameters map[string]json.RawMessage
		var results []string
		if value, ok := args["parameters"]; ok && value != nil {
			data, _ := json.Marshal(value)
			if err := json.Unmarshal(data, &parameters); err != nil {
				return nil, dataWriteNestedBindingError(tool, "parameters")
			}
		}
		if value, ok := args["result-parameters"]; ok && value != nil {
			data, _ := json.Marshal(value)
			if err := json.Unmarshal(data, &results); err != nil {
				return nil, dataWriteNestedBindingError(tool, "result-parameters")
			}
		}
		timeout := 0
		if value, ok := args["timeout"]; ok && value != nil {
			number, ok := value.(float64)
			if !ok {
				if integer, yes := value.(int); yes {
					number = float64(integer)
					ok = true
				}
			}
			if !ok || float64(int(number)) != number {
				return nil, dataWriteNestedBindingError(tool, "timeout")
			}
			timeout = int(number)
		}
		client, failure, err := envs.resolve(tool, args, scopeDirect)
		if err != nil {
			return nil, err
		}
		if failure != nil {
			return structuredToolResult(creatio.DataWriteProcessResult{Warnings: []string{}, Error: redacted(failure)}), nil
		}
		return structuredToolResult(client.DataWriteRunProcess(ctx, name, parameters, results, timeout)), nil
	}, withAnnotations(toolAnnotations{ReadOnly: false, Destructive: true, Idempotent: false, OpenWorld: false}))
}
