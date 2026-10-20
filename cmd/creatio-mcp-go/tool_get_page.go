package main

import (
	"context"
	"fmt"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func init() {
	registerTool(map[string]any{
		"name": "get-page",
		"description": "Get a Freedom UI page from the target Creatio environment. Writes body.js (editable body), bundle.json (merged view) " +
			"and meta.json to .clio-pages/{schema-name}/, returning paths. REPLACES that directory every call, so in-place edits are lost; " +
			"save edits with a page-writing tool (clio update-page/sync-pages read meta.json as the baseline). Paths are on the MCP SERVER host. " +
			"output-directory anchors it at your project root.",
		"inputSchema": map[string]any{"type": "object", "required": []string{"schema-name"}, "properties": map[string]any{
			"schema-name":        map[string]string{"type": "string", "description": "Freedom UI page schema name, e.g. 'UsrMyApp_FormPage'"},
			"output-directory":   map[string]string{"type": "string", "description": "Optional. Directory to anchor .clio-pages output under (typically your project root). Defaults to the auto-detected workspace root."},
			"include-operations": map[string]string{"type": "boolean", "description": "true lists page.ownBodySummary.viewConfigDiffOps; default: viewConfigDiffOpCounts (count per operation type). meta.json keeps the full list."},
		}},
	}, func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
		schemaName, err := optionalStringArg(args, "get-page", "schema-name")
		if err != nil {
			return nil, err
		}
		outputDirectory, err := optionalStringArg(args, "get-page", "output-directory")
		if err != nil {
			return nil, err
		}
		includeOperations, err := getPageIncludeOperations(args)
		if err != nil {
			return nil, err
		}
		// clio resolves the environment per call; a failure is reported inside the tool's own answer.
		client, failure, bindErr := envs.resolve("get-page", args, scopeDirect)
		if bindErr != nil {
			return nil, bindErr
		}
		if failure != nil {
			refusal := redacted(failure)
			return structuredToolResult(creatio.PageGetResult{Error: refusal}), nil
		}
		result := client.GetPage(ctx, creatio.PageGetRequest{SchemaName: schemaName, IncludeOperations: includeOperations})
		if !result.Success {
			return structuredToolResult(result), nil
		}
		return structuredToolResult(client.WritePageFilesFor(result, schemaName, outputDirectory,
			pageWriteArgText(args, "environment-name"), pageWriteArgText(args, "uri"))), nil
	}, withAnnotations(localWriteAnnotations))
}

// getPageIncludeOperations reads get-page's include-operations. clio 8.1.0.142 (ENG-103023) makes the
// operation list opt-in: an absent value answers with viewConfigDiffOpCounts, and meta.json keeps the
// full list. update-page keeps nil (the full list) because clio passes its argument through unchanged there.
func getPageIncludeOperations(args map[string]any) (*bool, error) {
	includeOperations := false
	switch value := args["include-operations"].(type) {
	case nil:
	case bool:
		includeOperations = value
	default:
		return nil, fmt.Errorf("invalid-parameter-type: argument 'include-operations' for MCP tool 'get-page' must be a boolean. Received an incompatible JSON value.")
	}
	return &includeOperations, nil
}
