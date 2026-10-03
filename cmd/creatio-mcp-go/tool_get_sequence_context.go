package main

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// sequenceContextGUID is the only GUID spelling clio's System.Text.Json binder accepts for a Guid argument.
var sequenceContextGUID = regexp.MustCompile(`^[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}$`)

func init() {
	registerTool(map[string]any{
		"name": "get-sequence-context",
		"description": "Discover effective sequence fields, live lookup IDs and ruleset/schedule choices of the target Creatio environment in one " +
			"read-only call. Uses DataService; no OData fallback. Optional sequence-id inspects an existing definition. Check each section state: " +
			"missing, failed or truncated sections are not complete context. Schema presence does not prove lifecycle-service availability or write " +
			"permissions. Read get-guidance name=sequences before sequence work.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
			"sequence-id": map[string]string{"type": "string", "description": "Optional sequence definition UUID."},
		}},
	}, func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
		var sequenceID *string
		switch value := args["sequence-id"].(type) {
		case nil:
		case string:
			if !sequenceContextGUID.MatchString(value) {
				return nil, sequenceContextTypeError()
			}
			lower := strings.ToLower(value)
			sequenceID = &lower
		default:
			return nil, sequenceContextTypeError()
		}
		// clio resolves the environment per call; a failure is reported inside the tool's own answer.
		client, failure, bindErr := envs.resolve("get-sequence-context", args, scopeName)
		if bindErr != nil {
			return nil, bindErr
		}
		if failure != nil {
			return nil, errors.New(redacted(failure))
		}
		result, err := client.GetSequenceContext(ctx, sequenceID)
		if err != nil {
			return nil, err
		}
		return structuredToolResult(result), nil
	})
}

// sequenceContextTypeError is clio's binder refusal of a sequence-id that is not a GUID, worded as clio words it.
func sequenceContextTypeError() error {
	return errors.New("invalid-parameter-type: argument 'sequence-id' for MCP tool 'get-sequence-context' must be an object. Received an incompatible JSON value.")
}
