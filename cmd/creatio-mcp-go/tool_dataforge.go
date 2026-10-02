package main

// The six read-only Data Forge tools from clio's DataForgeTool. clio binds them leniently (unknown keys are
// ignored), reports every failure inside the envelope with a per-tool error code, and answers a value of the
// wrong JSON type with an invalid-parameter-type MCP error. dataforge-initialize and dataforge-update
// schedule index writes and are not offered.

import (
	"context"
	"fmt"
	"math"
	"strconv"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const dataForgePlatformRequirement = "Requires Creatio platform version 10.0.0 or later; CrtDataForge is included in supported platform versions."

func dataForgeContract(name, description string, properties map[string]any, required ...string) map[string]any {
	schema := map[string]any{"type": "object", "properties": properties}
	if len(required) > 0 {
		schema["required"] = required
	}
	return map[string]any{"name": name, "description": description + " " + dataForgePlatformRequirement, "inputSchema": schema}
}

var dataForgeLimitProperty = map[string]any{"type": "integer", "description": "Optional maximum number of results; the Data Forge service default applies when omitted."}

func init() {
	registerTool(dataForgeContract("dataforge-status",
		"Checks whether Data Forge is ready to provide schema, lookup, relation, and maintenance context for the configured Creatio environment.",
		map[string]any{}),
		func(ctx context.Context, client *creatio.Client, args map[string]any) (*mcp.CallToolResult, error) {
			if message := refusesConnectionArgs(args); message != "" {
				return structuredToolResult(creatio.DataForgeFailure("dataforge-status", message)), nil
			}
			return structuredToolResult(client.DataForgeStatus(ctx)), nil
		})

	registerTool(dataForgeContract("dataforge-find-tables",
		"Finds existing Creatio tables that semantically match a business concept, so callers can reuse or compare schemas before creating new ones.",
		map[string]any{"query": map[string]string{"type": "string", "description": "Business concept to search for."}, "limit": dataForgeLimitProperty},
		"query"),
		func(ctx context.Context, client *creatio.Client, args map[string]any) (*mcp.CallToolResult, error) {
			const tool = "dataforge-find-tables"
			if message := refusesConnectionArgs(args); message != "" {
				return structuredToolResult(creatio.DataForgeFailure(tool, message)), nil
			}
			query, err := optionalStringArg(args, tool, "query")
			if err != nil {
				return nil, err
			}
			limit, err := dataForgeLimitArg(args, tool)
			if err != nil {
				return nil, err
			}
			return structuredToolResult(client.DataForgeFindTables(ctx, query, limit)), nil
		})

	registerTool(dataForgeContract("dataforge-find-lookups",
		"Finds lookup values and lookup schemas that match a requested business value, useful for resolving lookup references before writing data bindings.",
		map[string]any{
			"query":       map[string]string{"type": "string", "description": "Lookup value to search for."},
			"schema-name": map[string]string{"type": "string", "description": "Optional lookup schema to search in."},
			"limit":       dataForgeLimitProperty,
		}, "query"),
		func(ctx context.Context, client *creatio.Client, args map[string]any) (*mcp.CallToolResult, error) {
			const tool = "dataforge-find-lookups"
			if message := refusesConnectionArgs(args); message != "" {
				return structuredToolResult(creatio.DataForgeFailure(tool, message)), nil
			}
			query, err := optionalStringArg(args, tool, "query")
			if err != nil {
				return nil, err
			}
			schemaName, err := dataForgeNullableString(args, tool, "schema-name")
			if err != nil {
				return nil, err
			}
			limit, err := dataForgeLimitArg(args, tool)
			if err != nil {
				return nil, err
			}
			return structuredToolResult(client.DataForgeFindLookups(ctx, query, schemaName, limit)), nil
		})

	registerTool(dataForgeContract("dataforge-get-relations",
		"Finds known relationship paths between two Creatio tables to help model references or understand existing entity links.",
		map[string]any{
			"source-table": map[string]string{"type": "string", "description": "Table the path starts from."},
			"target-table": map[string]string{"type": "string", "description": "Table the path ends at."},
			"limit":        dataForgeLimitProperty,
		}, "source-table", "target-table"),
		func(ctx context.Context, client *creatio.Client, args map[string]any) (*mcp.CallToolResult, error) {
			const tool = "dataforge-get-relations"
			if message := refusesConnectionArgs(args); message != "" {
				return structuredToolResult(creatio.DataForgeFailure(tool, message)), nil
			}
			source, err := optionalStringArg(args, tool, "source-table")
			if err != nil {
				return nil, err
			}
			target, err := optionalStringArg(args, tool, "target-table")
			if err != nil {
				return nil, err
			}
			limit, err := dataForgeLimitArg(args, tool)
			if err != nil {
				return nil, err
			}
			return structuredToolResult(client.DataForgeGetRelations(ctx, source, target, limit)), nil
		})

	registerTool(dataForgeContract("dataforge-get-table-columns",
		"Returns the logical columns of a Creatio table, including captions, data types, required flags, and lookup targets.",
		map[string]any{"table-name": map[string]string{"type": "string", "description": "Entity schema name, sent as given."}},
		"table-name"),
		func(ctx context.Context, client *creatio.Client, args map[string]any) (*mcp.CallToolResult, error) {
			const tool = "dataforge-get-table-columns"
			if message := refusesConnectionArgs(args); message != "" {
				return structuredToolResult(creatio.DataForgeFailure(tool, message)), nil
			}
			tableName, err := optionalStringArg(args, tool, "table-name")
			if err != nil {
				return nil, err
			}
			return structuredToolResult(client.DataForgeGetTableColumns(ctx, tableName)), nil
		})

	registerTool(dataForgeContract("dataforge-context",
		"Builds a compact Data Forge context package for planning schema work: similar tables, lookup matches, relation paths, table columns, and readiness status. "+
			"Per-term read failures are reported in warnings, and repeats of one cause are collapsed into one line.",
		map[string]any{
			"requirement-summary": map[string]string{"type": "string", "description": "Table search term used when candidate-terms is empty."},
			"candidate-terms":     map[string]any{"type": "array", "items": map[string]string{"type": "string"}, "description": "Table search terms."},
			"lookup-hints":        map[string]any{"type": "array", "items": map[string]string{"type": "string"}, "description": "Lookup search terms."},
			"relation-pairs": map[string]any{"type": "array", "description": "Table pairs to resolve relation paths for.", "items": map[string]any{
				"type": "object", "properties": map[string]any{
					"source-table": map[string]string{"type": "string"},
					"target-table": map[string]string{"type": "string"},
				}}},
		}),
		func(ctx context.Context, client *creatio.Client, args map[string]any) (*mcp.CallToolResult, error) {
			const tool = "dataforge-context"
			if message := refusesConnectionArgs(args); message != "" {
				return structuredToolResult(creatio.DataForgeFailure(tool, message)), nil
			}
			summary, err := optionalStringArg(args, tool, "requirement-summary")
			if err != nil {
				return nil, err
			}
			terms, err := dataForgeStringListArg(args, tool, "candidate-terms")
			if err != nil {
				return nil, err
			}
			hints, err := dataForgeStringListArg(args, tool, "lookup-hints")
			if err != nil {
				return nil, err
			}
			pairs, err := dataForgeRelationPairsArg(args, tool)
			if err != nil {
				return nil, err
			}
			return structuredToolResult(client.DataForgeContext(ctx, creatio.DataForgeContextRequest{
				RequirementSummary: summary, CandidateTerms: terms, LookupHints: hints, RelationPairs: pairs})), nil
		})
}

func dataForgeTypeError(tool, name, expected string) error {
	return fmt.Errorf("invalid-parameter-type: argument '%s' for MCP tool '%s' must be %s. Received an incompatible JSON value.", name, tool, expected)
}

func dataForgeNestedTypeError(tool, name string) error {
	return fmt.Errorf("invalid-parameter-type: argument '%s' for MCP tool '%s' contains a value that does not match the documented shape. Received an incompatible JSON value.", name, tool)
}

// dataForgeNullableString keeps absent and null apart from an empty string: clio forwards null to Creatio.
func dataForgeNullableString(args map[string]any, tool, name string) (*string, error) {
	value, ok := args[name]
	if !ok || value == nil {
		return nil, nil
	}
	text, ok := value.(string)
	if !ok {
		return nil, dataForgeTypeError(tool, name, "a string")
	}
	return &text, nil
}

// dataForgeLimitArg binds clio's int? limit: a whole JSON number in Int32 range, or a string holding one
// (clio's serializer reads numbers from strings); anything else is refused.
func dataForgeLimitArg(args map[string]any, tool string) (*int, error) {
	value, ok := args["limit"]
	if !ok || value == nil {
		return nil, nil
	}
	var number float64
	switch typed := value.(type) {
	case float64:
		number = typed
	case string:
		parsed, err := strconv.ParseInt(typed, 10, 32)
		if err != nil {
			return nil, dataForgeTypeError(tool, "limit", "a number")
		}
		number = float64(parsed)
	default:
		return nil, dataForgeTypeError(tool, "limit", "a number")
	}
	if number != math.Trunc(number) || number < math.MinInt32 || number > math.MaxInt32 {
		return nil, dataForgeTypeError(tool, "limit", "a number")
	}
	limit := int(number)
	return &limit, nil
}

// dataForgeStringListArg binds an IReadOnlyList<string>: null items are kept as nil, other non-strings refused.
func dataForgeStringListArg(args map[string]any, tool, name string) ([]*string, error) {
	value, ok := args[name]
	if !ok || value == nil {
		return nil, nil
	}
	items, ok := value.([]any)
	if !ok {
		return nil, dataForgeTypeError(tool, name, "an array")
	}
	list := make([]*string, 0, len(items))
	for _, item := range items {
		if item == nil {
			list = append(list, nil)
			continue
		}
		text, ok := item.(string)
		if !ok {
			return nil, dataForgeNestedTypeError(tool, name)
		}
		list = append(list, &text)
	}
	return list, nil
}

// dataForgeRelationPairsArg binds relation-pairs. Unknown keys inside a pair are ignored, as clio ignores them.
func dataForgeRelationPairsArg(args map[string]any, tool string) ([]*creatio.DataForgeRelationPair, error) {
	const name = "relation-pairs"
	value, ok := args[name]
	if !ok || value == nil {
		return nil, nil
	}
	items, ok := value.([]any)
	if !ok {
		return nil, dataForgeTypeError(tool, name, "an array")
	}
	pairs := make([]*creatio.DataForgeRelationPair, 0, len(items))
	for _, item := range items {
		if item == nil {
			pairs = append(pairs, nil)
			continue
		}
		object, ok := item.(map[string]any)
		if !ok {
			return nil, dataForgeNestedTypeError(tool, name)
		}
		pair := &creatio.DataForgeRelationPair{}
		for key, target := range map[string]**string{"source-table": &pair.SourceTable, "target-table": &pair.TargetTable} {
			side, ok := object[key]
			if !ok || side == nil {
				continue
			}
			text, ok := side.(string)
			if !ok {
				return nil, dataForgeNestedTypeError(tool, name)
			}
			*target = &text
		}
		pairs = append(pairs, pair)
	}
	return pairs, nil
}
