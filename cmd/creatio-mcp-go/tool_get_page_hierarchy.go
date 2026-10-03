package main

import (
	"context"
	"fmt"
	"math"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func init() {
	registerTool(map[string]any{
		"name": "get-page-hierarchy",
		"description": "Return the FULL Freedom UI page replacing-schema chain (root first, ordered by hierarchy level) with each " +
			"schema's raw body in ONE call, from the target Creatio environment. Use this instead of calling get-page / " +
			"get-client-unit-schema once per schema when you need to inspect a whole replacing chain. For a single schema's editable " +
			"body use get-page. Pass metadata-only for a lightweight chain listing, or offset/limit to page a very large chain; bodies " +
			"are left out when the selected window exceeds 200000 characters.",
		"inputSchema": map[string]any{"type": "object", "required": []string{"schema-name"}, "properties": map[string]any{
			"schema-name":   map[string]string{"type": "string", "description": "Freedom UI page schema name (any variant in the replacing chain), e.g. 'UsrApplicants_FormPage'"},
			"metadata-only": map[string]string{"type": "boolean", "description": "Optional. When true, omit each schema's raw body and return chain metadata only."},
			"offset":        map[string]string{"type": "integer", "description": "Optional. Zero-based index of the first chain entry to return (root first). Default 0."},
			"limit":         map[string]string{"type": "integer", "description": "Optional. Maximum number of chain entries to return; 0/omitted returns the whole chain from offset."},
		}},
	}, func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
		var input creatio.PageHierarchyRequest
		var err error
		if input.SchemaName, err = optionalStringArg(args, "get-page-hierarchy", "schema-name"); err != nil {
			return nil, err
		}
		if input.MetadataOnly, err = schemaGetBoolArg(args, "get-page-hierarchy", "metadata-only"); err != nil {
			return nil, err
		}
		if input.Offset, err = hierarchyIntArg(args, "get-page-hierarchy", "offset"); err != nil {
			return nil, err
		}
		if input.Limit, err = hierarchyIntArg(args, "get-page-hierarchy", "limit"); err != nil {
			return nil, err
		}
		// clio resolves the environment per call; a failure is reported inside the tool's own answer.
		client, failure, bindErr := envs.resolve("get-page-hierarchy", args, scopeDirect)
		if bindErr != nil {
			return nil, bindErr
		}
		if failure != nil {
			refusal := redacted(failure)
			return structuredToolResult(creatio.PageHierarchyResult{Error: refusal}), nil
		}
		return structuredToolResult(client.GetPageHierarchy(ctx, input)), nil
	})
}

// hierarchyIntArg reads an optional 32-bit integer the way clio's binder does: absent or null is 0; a
// fraction, an out-of-range number or any other JSON type is clio's invalid-parameter-type refusal.
func hierarchyIntArg(args map[string]any, tool, name string) (int, error) {
	switch value := args[name].(type) {
	case nil:
		return 0, nil
	case float64:
		if value == math.Trunc(value) && value >= math.MinInt32 && value <= math.MaxInt32 {
			return int(value), nil
		}
	}
	return 0, fmt.Errorf("invalid-parameter-type: argument '%s' for MCP tool '%s' must be a number. Received an incompatible JSON value.", name, tool)
}
