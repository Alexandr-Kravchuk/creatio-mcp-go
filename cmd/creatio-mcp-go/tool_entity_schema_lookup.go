package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// find-entity-schema and get-entity-schema-column-properties return clio's bare result, not an envelope:
// clio reports their failures as MCP isError results, so these handlers return errors instead.

func init() {
	registerTool(map[string]any{
		"name": "find-entity-schema",
		"description": "Finds Creatio entity schemas without a package name. Returns schema, package, maintainer, and parent, one item per " +
			"package layer. Empty pattern results get one broader check; capped checks fail. Use package-name in follow-ups. " +
			"Select by exact schema-name, substring search-pattern, or uid; supplied criteria are combined.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
			"schema-name":    map[string]string{"type": "string", "description": "Exact entity schema name to find (use instead of search-pattern or uid)"},
			"search-pattern": map[string]string{"type": "string", "description": "Case-insensitive substring to search in entity schema names (use instead of schema-name or uid)"},
			"uid":            map[string]string{"type": "string", "description": "Entity schema UId (Guid) for exact lookup (use instead of schema-name or search-pattern)"},
		}},
	}, func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
		if refusal := unknownArgumentError(args, "environment-name", "schema-name", "search-pattern", "uid"); refusal != "" {
			return nil, errors.New(refusal)
		}
		var input struct {
			SchemaName    string `json:"schema-name"`
			SearchPattern string `json:"search-pattern"`
			UID           string `json:"uid"`
		}
		if err := decodeStrictArgs(withoutEnvironmentArgs(args, scopeName), &input); err != nil {
			return nil, fmt.Errorf("decode find-entity-schema arguments: %w", err)
		}
		client, err := envs.target("find-entity-schema", args, scopeName)
		if err != nil {
			return nil, errors.New(redacted(err))
		}
		results, err := client.FindEntitySchemas(ctx, creatio.EntitySchemaSearchRequest{
			SchemaName: input.SchemaName, SearchPattern: input.SearchPattern, UID: input.UID,
		})
		if err != nil {
			return nil, err
		}
		return jsonTextToolResult(results)
	})
	registerTool(map[string]any{
		"name": "get-entity-schema-column-properties",
		"description": "Reads one column. Omit package-name for merged discovery; supply it for package-layer metadata. " +
			"Merged track-changes, localizable-text and do-not-control-integrity are null; source means inheritance. " +
			"default-value-config adds display-value for lookup Const records and native SystemValue source captions " +
			"on supported column types, preserving GUIDs. Unavailable captions carry record-resolution (Const) or " +
			"source-resolution (SystemValue). Captions identify sources, not evaluated defaults.",
		"inputSchema": map[string]any{"type": "object", "required": []string{"schema-name", "column-name"}, "properties": map[string]any{
			"package-name": map[string]string{"type": "string", "description": "Optional package. Omit for merged runtime discovery; supply for authoritative package-layer metadata."},
			"schema-name":  map[string]string{"type": "string", "description": "Entity schema name"},
			"column-name":  map[string]string{"type": "string", "description": "Column name"},
		}},
	}, func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
		if refusal := unknownArgumentError(args, "environment-name", "package-name", "schema-name", "column-name"); refusal != "" {
			return nil, errors.New(refusal)
		}
		var input struct {
			PackageName string `json:"package-name"`
			SchemaName  string `json:"schema-name"`
			ColumnName  string `json:"column-name"`
		}
		if err := decodeStrictArgs(withoutEnvironmentArgs(args, scopeName), &input); err != nil {
			return nil, fmt.Errorf("decode get-entity-schema-column-properties arguments: %w", err)
		}
		client, err := envs.target("get-entity-schema-column-properties", args, scopeName)
		if err != nil {
			return nil, errors.New(redacted(err))
		}
		result, err := client.GetEntitySchemaColumnProperties(ctx, creatio.EntitySchemaColumnPropertiesRequest{
			PackageName: input.PackageName, SchemaName: input.SchemaName, ColumnName: input.ColumnName,
		})
		if err != nil {
			return nil, err
		}
		return structuredToolResult(result), nil
	})
}

// jsonTextToolResult answers with JSON text only. find-entity-schema's result is an array, which MCP
// structuredContent (an object) cannot carry; clio sends the same text-only shape.
func jsonTextToolResult(value any) (*mcp.CallToolResult, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode tool result: %w", err)
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(encoded)}}}, nil
}
