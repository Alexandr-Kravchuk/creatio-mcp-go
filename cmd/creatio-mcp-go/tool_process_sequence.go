package main

// enroll-sequence-participants: clio's SequenceEnrollmentTool. It answers SequenceEnrollmentResult itself
// (no command envelope); a blank environment, an unknown one and an unusable selection are raised failures,
// as clio's tool throws them. Unknown keys are ignored.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const processSequenceTool = "enroll-sequence-participants"

// processSequenceGUIDs binds Guid[]: absent or null is nil, an array of 36-character GUID strings is
// parsed, anything else is the binder's refusal.
func processSequenceGUIDs(args map[string]any, name string) ([]string, error) {
	value, present := args[name]
	if !present || value == nil {
		return nil, nil
	}
	items, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("invalid-parameter-type: argument '%s' for MCP tool '%s' must be an array. Received an incompatible JSON value.", name, processSequenceTool)
	}
	ids := make([]string, 0, len(items))
	for _, item := range items {
		text, isText := item.(string)
		parsed := ""
		if isText && len(text) == 36 {
			parsed = creatio.ProcessWriteParseGUID(text)
		}
		if parsed == "" {
			return nil, fmt.Errorf("invalid-parameter-type: argument '%s' for MCP tool '%s' contains a value that does not match the documented shape. Received an incompatible JSON value.", name, processSequenceTool)
		}
		ids = append(ids, parsed)
	}
	return ids, nil
}

func init() {
	registerTool(map[string]any{"name": processSequenceTool},
		func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
			environment, err := optionalStringArg(args, processSequenceTool, "environment-name")
			if err != nil {
				return nil, err
			}
			sequence, err := processSequenceGUID(args, "sequence-id")
			if err != nil {
				return nil, err
			}
			contacts, err := processSequenceGUIDs(args, "contact-ids")
			if err != nil {
				return nil, err
			}
			if _, present := args["environment-name"]; present && strings.TrimSpace(environment) == "" {
				return nil, errors.New("environment-name is required.")
			}
			client, err := envs.target(processSequenceTool, args, scopeName)
			if err != nil {
				return nil, err
			}
			result, err := client.EnrollSequenceParticipants(ctx, sequence, contacts)
			if err != nil {
				return nil, err
			}
			return structuredToolResult(result), nil
		}, withAnnotations(toolAnnotations{ReadOnly: false, Destructive: true, Idempotent: false, OpenWorld: false}))
}

// processSequenceGUID binds a Guid the way register-process-element's uid binds.
func processSequenceGUID(args map[string]any, name string) (string, error) {
	value, present := args[name]
	if !present {
		return "", nil
	}
	if text, ok := value.(string); ok && len(text) == 36 {
		if parsed := creatio.ProcessWriteParseGUID(text); parsed != "" {
			return parsed, nil
		}
	}
	return "", fmt.Errorf("invalid-parameter-type: argument '%s' for MCP tool '%s' must be an object. Received an incompatible JSON value.", name, processSequenceTool)
}
