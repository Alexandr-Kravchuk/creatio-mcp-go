package main

import (
	"context"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func init() {
	registerTool(map[string]any{
		"name": "get-client-unit-schema",
		"description": "Read the JavaScript body and metadata of a client unit schema from the target Creatio environment. " +
			"Use before update-client-unit-schema to inspect current content. A schema name that exists in several packages resolves " +
			"deterministically to the top (most-derived) layer. Pass full-hierarchy=true to ALSO return the localizable strings merged " +
			"across the whole inheritance/package chain (with parentSchemaUId provenance); the body still reflects this schema's own top layer. " +
			"An explicit output-file must resolve inside the workspace or the OS temp directory.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
			"schema-name": map[string]string{"type": "string", "description": "Client unit schema name, e.g. 'NetworkUtilities'. Required unless schema-uid is provided."},
			"full-hierarchy": map[string]string{"type": "boolean", "description": "When true, also return the localizable strings merged across the full inheritance/" +
				"package hierarchy (with parentSchemaUId provenance). The body stays this schema's own top layer. Default false."},
			"schema-uid":  map[string]string{"type": "string", "description": "Fetch this exact schema UId directly, bypassing name resolution. Default null."},
			"output-file": map[string]string{"type": "string", "description": schemaGetOutputFileDescription},
		}},
	}, func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
		var input creatio.ClientUnitSchemaRequest
		var err error
		if input.SchemaName, err = optionalStringArg(args, "get-client-unit-schema", "schema-name"); err != nil {
			return nil, err
		}
		if input.FullHierarchy, err = schemaGetBoolArg(args, "get-client-unit-schema", "full-hierarchy"); err != nil {
			return nil, err
		}
		if input.SchemaUID, err = optionalStringArg(args, "get-client-unit-schema", "schema-uid"); err != nil {
			return nil, err
		}
		if input.OutputFile, err = optionalStringArg(args, "get-client-unit-schema", "output-file"); err != nil {
			return nil, err
		}
		// clio resolves the environment per call; a failure is reported inside the tool's own answer.
		client, failure, bindErr := envs.resolve("get-client-unit-schema", args, scopeDirect)
		if bindErr != nil {
			return nil, bindErr
		}
		if failure != nil {
			refusal := redacted(failure)
			return structuredToolResult(creatio.ClientUnitSchemaResult{Error: refusal}), nil
		}
		return structuredToolResult(client.GetClientUnitSchema(ctx, input)), nil
	}, withAnnotations(localWriteAnnotations))
}
