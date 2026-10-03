package main

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// sequenceContextGUID is the only GUID spelling clio's System.Text.Json binder accepts for a Guid argument.
var sequenceContextGUID = regexp.MustCompile(`^[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}$`)

func init() {
	registerTool(map[string]any{
		"name": "get-sequence-context",
		"description": "Discover effective sequence fields, live lookup IDs and ruleset/schedule choices of the single configured Creatio instance in one " +
			"read-only call. Uses DataService; no OData fallback. Optional sequence-id inspects an existing definition. Check each section state: " +
			"missing, failed or truncated sections are not complete context. Schema presence does not prove lifecycle-service availability or write " +
			"permissions. Read get-guidance name=sequences before sequence work.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
			"sequence-id": map[string]string{"type": "string", "description": "Optional sequence definition UUID."},
		}},
	}, func(ctx context.Context, client *creatio.Client, args map[string]any) (*mcp.CallToolResult, error) {
		// clio ignores unknown keys for this tool, so only a selector of another environment is refused.
		if refusal := refusesConnectionArgs(args); refusal != "" {
			return nil, errors.New(refusal)
		}
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
