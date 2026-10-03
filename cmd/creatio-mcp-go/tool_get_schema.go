package main

import (
	"context"
	"fmt"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// schemaGetOutputFileDescription is clio's SchemaGetBaseArgs output-file text plus the confinement rule.
const schemaGetOutputFileDescription = "Optional absolute path to write the schema body to. When set, body is omitted from the response. " +
	"It must resolve inside the workspace or the OS temp directory, and an existing file is never overwritten."

func init() {
	registerTool(map[string]any{
		"name": "get-schema",
		"description": "Read the C# body and metadata of a source-code schema from the target Creatio environment. " +
			"Use before update-schema to inspect current content. output-file writes the body to a local file instead of returning it.",
		"inputSchema": map[string]any{"type": "object", "required": []string{"schema-name"}, "properties": map[string]any{
			"schema-name": map[string]string{"type": "string", "description": "C# source-code schema name, e.g. 'UsrMyHelper'"},
			"output-file": map[string]string{"type": "string", "description": schemaGetOutputFileDescription},
		}},
	}, func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
		schemaName, err := optionalStringArg(args, "get-schema", "schema-name")
		if err != nil {
			return nil, err
		}
		outputFile, err := optionalStringArg(args, "get-schema", "output-file")
		if err != nil {
			return nil, err
		}
		// clio resolves the environment per call; a failure is reported inside the tool's own answer.
		client, failure, bindErr := envs.resolve("get-schema", args, scopeDirect)
		if bindErr != nil {
			return nil, bindErr
		}
		if failure != nil {
			refusal := redacted(failure)
			return structuredToolResult(creatio.SourceCodeSchemaResult{Error: refusal}), nil
		}
		return structuredToolResult(client.GetSourceCodeSchema(ctx, creatio.SourceCodeSchemaRequest{
			SchemaName: schemaName, OutputFile: outputFile,
		})), nil
	}, withAnnotations(localWriteAnnotations))
}

// schemaGetBoolArg reads an optional boolean the way clio's binder does: absent or null is false, any other
// JSON type is clio's invalid-parameter-type refusal.
func schemaGetBoolArg(args map[string]any, tool, name string) (bool, error) {
	switch value := args[name].(type) {
	case nil:
		return false, nil
	case bool:
		return value, nil
	default:
		return false, fmt.Errorf("invalid-parameter-type: argument '%s' for MCP tool '%s' must be a boolean. Received an incompatible JSON value.", name, tool)
	}
}
