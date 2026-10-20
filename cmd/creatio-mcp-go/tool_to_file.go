package main

// clio 8.1.0.142's *-to-file twins of five read tools. Each twin is a separate tool because clio's MCP safety
// annotations are static per tool: it creates a local file and refuses an existing one, so it is neither
// read-only nor idempotent (and so reachable only through clio-run). Every twin confines output-file BEFORE
// its remote work and reuses the inline tool's request code.

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// toFileAnnotations is the [McpServerTool] attribute of all five twins.
var toFileAnnotations = toolAnnotations{ReadOnly: false, Destructive: false, Idempotent: false, OpenWorld: false}

// toFileResult is structuredToolResult with the text escaped as System.Text.Json's default encoder escapes
// it (non-ASCII and "&'+<>` as \uXXXX). clio redacts a failed result over that text, so a path quoted with '
// loses its closing quote to the path placeholder exactly as in clio ("output-file '[redacted-path] resolves").
func toFileResult(value any) *mcp.CallToolResult {
	text, err := creatio.STJDefaultJSON(value)
	if err != nil {
		return structuredToolResult(value)
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(text)}}, StructuredContent: json.RawMessage(text)}
}

// toFileOutputArg binds output-file the way System.Text.Json binds the twin's argument record: the member is
// matched case-insensitively, null binds as empty, another JSON kind is clio's invalid-parameter-type
// refusal, and a record that declares the member `required` refuses the whole binding when it is absent. It
// returns the path and the arguments without the bound key, for the inline tool's own binding.
func toFileOutputArg(tool string, args map[string]any, required bool) (string, map[string]any, error) {
	key, found := "", false
	if _, ok := args["output-file"]; ok {
		key, found = "output-file", true
	} else {
		for candidate := range args {
			if strings.EqualFold(candidate, "output-file") {
				key, found = candidate, true
				break
			}
		}
	}
	if !found {
		if required {
			return "", nil, dataWriteBindingError(tool)
		}
		return "", args, nil
	}
	rest := make(map[string]any, len(args))
	for name, value := range args {
		if name != key {
			rest[name] = value
		}
	}
	value := args[key]
	if value == nil {
		return "", rest, nil
	}
	text, ok := value.(string)
	if !ok {
		return "", nil, dataWriteKindBindingError(tool, "output-file", "a string")
	}
	return text, rest, nil
}

func init() {
	// get-component-info-to-file: ComponentInfoToFileTool.
	registerTool(map[string]any{"name": "get-component-info-to-file"}, func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
		const tool = "get-component-info-to-file"
		outputFile, rest, err := toFileOutputArg(tool, args, true)
		if err != nil {
			return nil, err
		}
		for _, name := range componentInfoArgNames {
			if _, err := optionalStringArg(rest, tool, name); err != nil {
				return nil, err
			}
		}
		path, refusal := creatio.ToFileResolve(outputFile)
		if refusal != "" {
			return toFileResult(creatio.ToFileDocumentationFailure(refusal + " Use get-component-info when the documentation should be returned inline.")), nil
		}
		response, err := componentInfoAnswer(ctx, envs, tool, rest)
		if err != nil {
			return nil, err
		}
		return toFileResult(creatio.ToFileProjectDocumentation(response, response.Documentation, path)), nil
	}, withAnnotations(toFileAnnotations))

	// get-request-info-to-file: RequestInfoToFileTool.
	registerTool(map[string]any{"name": "get-request-info-to-file"}, func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
		const tool = "get-request-info-to-file"
		outputFile, rest, err := toFileOutputArg(tool, args, true)
		if err != nil {
			return nil, err
		}
		for _, name := range dataWriteRequestInfoArgNames {
			if _, err := optionalStringArg(rest, tool, name); err != nil {
				return nil, err
			}
		}
		path, refusal := creatio.ToFileResolve(outputFile)
		if refusal != "" {
			return toFileResult(creatio.ToFileDocumentationFailure(refusal + " Use get-request-info when the documentation should be returned inline.")), nil
		}
		response, err := requestInfoAnswer(ctx, envs, tool, rest)
		if err != nil {
			return nil, err
		}
		return toFileResult(creatio.ToFileProjectDocumentation(response, response.Documentation, path)), nil
	}, withAnnotations(toFileAnnotations))

	// list-entity-client-schemas-to-file: ListEntityClientSchemasToFileTool. Its record declares output-file
	// positionally, not `required`, so an absent one reaches the tool and is refused there.
	registerTool(map[string]any{"name": "list-entity-client-schemas-to-file"}, func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
		const tool = "list-entity-client-schemas-to-file"
		outputFile, rest, err := toFileOutputArg(tool, args, false)
		if err != nil {
			return nil, err
		}
		entityName, err := optionalStringArg(rest, tool, "entity-name")
		if err != nil {
			return nil, err
		}
		path, refusal := creatio.ToFileResolve(outputFile)
		if refusal != "" {
			return toFileResult(creatio.EntityClientSchemasToFileFailure(refusal + " Use list-entity-client-schemas when the list should be returned inline.")), nil
		}
		client, failure, bindErr := envs.resolve(tool, rest, scopeDirect)
		if bindErr != nil {
			return nil, bindErr
		}
		if failure != nil {
			return toFileResult(creatio.EntityClientSchemasToFileFailure(redacted(failure))), nil
		}
		return toFileResult(creatio.EntityClientSchemasToFile(client.ListEntityClientSchemas(ctx, entityName), path)), nil
	}, withAnnotations(toFileAnnotations))
}
