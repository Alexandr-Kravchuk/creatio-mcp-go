package main

import (
	"context"
	"fmt"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var processSignatureKnownArgs = map[string]bool{"process-name": true, "culture": true}

func init() {
	registerTool(map[string]any{
		"name": "get-process-signature",
		"description": "Resolve a Creatio business process by its code (schema Name) OR its display caption; a caption resolves to the ACTIVE version. " +
			"Returns its parameter signature: per parameter the CODE (name), caption, CLR type, dataValueTypeId, direction, and lookup reference schema. " +
			"Use the parameter CODE, not the caption, as the key in run-process button mappings. Targets the single configured Creatio instance.",
		"inputSchema": map[string]any{
			"type":     "object",
			"required": []string{"process-name"},
			"properties": map[string]any{
				"process-name": map[string]string{"type": "string", "description": "Process code (schema Name) or the display caption shown in the process designer."},
				"culture":      map[string]any{"type": "string", "description": "Culture used to resolve localized parameter captions.", "default": "en-US"},
			},
		},
	}, func(ctx context.Context, client *creatio.Client, args map[string]any) (*mcp.CallToolResult, error) {
		// clio answers unknown keys inside its envelope rather than as a protocol error; so does this tool.
		// Its uri/login/password fallback would target another environment, so it is refused.
		message := refusesConnectionArgs(args)
		if message == "" {
			message = unknownArgumentError(args, processSignatureKnownArgs)
		}
		if message != "" {
			return structuredToolResult(creatio.ProcessSignatureResponse{
				Parameters: []creatio.ProcessSignatureParameter{}, Error: message,
			}), nil
		}
		var input struct {
			ProcessName string `json:"process-name"`
			Culture     string `json:"culture"`
		}
		if err := decodeStrictArgs(args, &input); err != nil {
			return nil, fmt.Errorf("decode get-process-signature arguments: %w", err)
		}
		return structuredToolResult(client.GetProcessSignature(ctx, input.ProcessName, input.Culture)), nil
	})
}
