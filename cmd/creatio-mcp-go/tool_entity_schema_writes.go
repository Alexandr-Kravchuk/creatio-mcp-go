package main

// The entity-schema write tools (T8): create-entity-schema, create-lookup, update-entity-schema,
// modify-entity-schema-column, set-entity-schema-properties and sync-schemas. The Creatio side is in
// internal/creatio/schemawrite_entity_*.go; this file binds the arguments, resolves the environment and
// orders the steps the way clio's tools do (Data Forge enrichment first where clio enriches, then the
// tool's own validation, then the environment, then the command).

import (
	"context"
	"errors"
	"time"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/redact"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// schemaWriteEntResolve resolves the environment; failure is the resolver envelope's error and the text
// clio's enrichment would have warned with.
func schemaWriteEntResolve(envs *environments, tool string, args map[string]any) (*creatio.Client, error, string, error) {
	client, failure, err := envs.resolve(tool, args, scopeName)
	if err != nil {
		return nil, nil, "", err
	}
	if failure != nil {
		message := failure.Error()
		var target *environmentError
		if errors.As(failure, &target) {
			message = target.message
		}
		return nil, failure, message, nil
	}
	return client, nil, "", nil
}

// schemaWriteEntEnrich is the Data Forge enrichment of clio's schema tools: the aggregation when the
// environment resolved, otherwise the degraded warning naming why it could not be asked.
func schemaWriteEntEnrich(ctx context.Context, client *creatio.Client, failureText string, terms, hints []string) *creatio.SchemaWriteEntDataForge {
	if client == nil {
		return creatio.SchemaWriteEntDataForgeFailure(failureText)
	}
	return client.SchemaWriteEntEnrich(ctx, terms, hints)
}

// schemaWriteEntResolverResult is FromResolverError with the enrichment attached.
func schemaWriteEntResolverResult(failure error, dataForge *creatio.SchemaWriteEntDataForge) *mcp.CallToolResult {
	exitCode := 1
	var target *environmentError
	if errors.As(failure, &target) {
		exitCode = target.exitCode()
	}
	return structuredToolResult(creatio.SchemaWriteEntResult{ExitCode: exitCode,
		Messages: []creatio.LogMessage{{MessageType: "Error", Value: resolverErrorText(failure)}}, DataForge: dataForge})
}

func init() {
	// clio: [McpServerTool(ReadOnly = false, Destructive = true, Idempotent = false, OpenWorld = false)]
	for _, tool := range []string{"create-entity-schema", "create-lookup"} {
		tool := tool
		lookup := tool == "create-lookup"
		registerTool(map[string]any{"name": tool}, func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
			parsed, err := creatio.ParseSchemaWriteEntCreateArgs(tool, withoutEnvironmentArgs(args, scopeName))
			if err != nil {
				return nil, err
			}
			client, failure, failureText, err := schemaWriteEntResolve(envs, tool, args)
			if err != nil {
				return nil, err
			}
			terms := creatio.SchemaWriteEntCreateTerms(parsed)
			var hints []string
			if lookup {
				hints = terms
			}
			dataForge := schemaWriteEntEnrich(ctx, client, failureText, terms, hints)
			if message := creatio.SchemaWriteEntCreatePrecheck(parsed, lookup); message != "" {
				return structuredToolResult(creatio.SchemaWriteEntFailure(message, dataForge)), nil
			}
			if failure != nil {
				if lookup {
					// create-lookup resolves the environment inside its own try block, so the failure is
					// its caught, redacted message rather than the resolver envelope.
					return structuredToolResult(creatio.SchemaWriteEntFailure(redact.Text(failureText), dataForge)), nil
				}
				return schemaWriteEntResolverResult(failure, dataForge), nil
			}
			if lookup {
				return structuredToolResult(client.CreateLookup(ctx, parsed, dataForge)), nil
			}
			return structuredToolResult(client.CreateEntitySchema(ctx, parsed, dataForge)), nil
		}, withAnnotations(toolAnnotations{ReadOnly: false, Destructive: true, Idempotent: false, OpenWorld: false}))
	}

	// clio: [McpServerTool(ReadOnly = false, Destructive = true, Idempotent = false, OpenWorld = false)]
	registerTool(map[string]any{"name": "update-entity-schema"}, func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
		parsed, err := creatio.ParseUpdateEntitySchemaArgs(withoutEnvironmentArgs(args, scopeName))
		if err != nil {
			return nil, err
		}
		if len(parsed.Operations) == 0 {
			return structuredToolResult(creatio.SchemaWriteEntFailure(creatio.UpdateEntitySchemaMissingOperations, nil)), nil
		}
		client, failure, failureText, err := schemaWriteEntResolve(envs, "update-entity-schema", args)
		if err != nil {
			return nil, err
		}
		terms, hints := creatio.UpdateEntitySchemaTerms(parsed)
		dataForge := schemaWriteEntEnrich(ctx, client, failureText, terms, hints)
		if message := creatio.UpdateEntitySchemaPrecheck(parsed); message != "" {
			return structuredToolResult(creatio.SchemaWriteEntFailure(message, dataForge)), nil
		}
		if failure != nil {
			return schemaWriteEntResolverResult(failure, dataForge), nil
		}
		return structuredToolResult(client.UpdateEntitySchema(ctx, parsed, dataForge)), nil
	}, withAnnotations(toolAnnotations{ReadOnly: false, Destructive: true, Idempotent: false, OpenWorld: false}))

	// clio: [McpServerTool(ReadOnly = false, Destructive = true, Idempotent = false, OpenWorld = false)]
	registerTool(map[string]any{"name": "modify-entity-schema-column"}, func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
		parsed, err := creatio.ParseModifyEntitySchemaColumnArgs(withoutEnvironmentArgs(args, scopeName))
		if err != nil {
			return nil, err
		}
		if message := creatio.ModifyEntitySchemaColumnPrecheck(parsed); message != "" {
			return structuredToolResult(creatio.SchemaWriteEntFailure(message, nil)), nil
		}
		client, failure, _, err := schemaWriteEntResolve(envs, "modify-entity-schema-column", args)
		if err != nil {
			return nil, err
		}
		if failure != nil {
			return resolverFailureEnvelope(failure), nil
		}
		return structuredToolResult(client.ModifyEntitySchemaColumn(ctx, parsed)), nil
	}, withAnnotations(toolAnnotations{ReadOnly: false, Destructive: true, Idempotent: false, OpenWorld: false}))

	// clio: [McpServerTool(ReadOnly = false, Destructive = true, Idempotent = true, OpenWorld = false)]
	registerTool(map[string]any{"name": "set-entity-schema-properties"}, func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
		parsed, err := creatio.ParseSetEntitySchemaPropertiesArgs(withoutEnvironmentArgs(args, scopeName))
		if err != nil {
			return nil, err
		}
		client, failure, _, err := schemaWriteEntResolve(envs, "set-entity-schema-properties", args)
		if err != nil {
			return nil, err
		}
		if failure != nil {
			return resolverFailureEnvelope(failure), nil
		}
		return structuredToolResult(client.SetEntitySchemaProperties(ctx, parsed)), nil
	}, withAnnotations(toolAnnotations{ReadOnly: false, Destructive: true, Idempotent: true, OpenWorld: false}))

	// clio: [McpServerTool(ReadOnly = false, Destructive = true, Idempotent = false, OpenWorld = false)],
	// RequiresClientRequests = Progress: a stage line per operation plus clio's heartbeat, no deadline.
	registerTool(map[string]any{"name": "sync-schemas"}, func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
		parsed, err := creatio.ParseSchemaSyncArgs(withoutEnvironmentArgs(args, scopeName))
		if err != nil {
			return nil, err
		}
		if rejection := creatio.SchemaSyncValidateTopLevel(parsed); rejection != nil {
			return structuredToolResult(*rejection), nil
		}
		client, _, failureText, err := schemaWriteEntResolve(envs, "sync-schemas", args)
		if err != nil {
			return nil, err
		}
		terms, hints := creatio.SchemaSyncTerms(parsed)
		dataForge := schemaWriteEntEnrich(ctx, client, failureText, terms, hints)
		// Without an environment clio still runs the batch: each operation fails where it first needs the
		// environment, with the resolution failure as its error.
		result, _, err := runLongOperation(ctx, "sync-schemas", 24*time.Hour,
			func(workCtx context.Context, stage func(string)) creatio.SchemaSyncResponse {
				return creatio.SchemaSync(workCtx, client, failureText, parsed, dataForge, stage)
			})
		if err != nil {
			return nil, err
		}
		return structuredToolResult(result), nil
	}, withAnnotations(toolAnnotations{ReadOnly: false, Destructive: true, Idempotent: false, OpenWorld: false}))
}
