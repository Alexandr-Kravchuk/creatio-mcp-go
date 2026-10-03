package main

import (
	"context"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func init() {
	registerTool(map[string]any{
		"name": "get-classic-page-sources",
		"description": "Collect the Classic page sources for a classic page schema on the single configured Creatio instance and WRITE them " +
			"to disk as a manifest the migration engine (migrate.mjs) folds: the whole replacing-schema layer chain (base->top), the " +
			"parent-template seed, and resolution inputs (entityColumns/columnTitles/resources/resourceStrings, detailSchemas with " +
			"each detail's entity, editPage and layer bodies, section, childPageSchemas, and the stand's own enumVocabulary). The " +
			"response returns the ABSOLUTE manifest path and a small summary; the bodies are written to the file, not returned. " +
			"ALWAYS read `warnings` before planning from the manifest: it is present only when the collected sources are incomplete. " +
			"The unit is collected whole, so a wide page can take minutes. Only reads from Creatio.",
		"inputSchema": map[string]any{"type": "object", "required": []string{"schema-name"}, "properties": map[string]any{
			"schema-name": map[string]string{"type": "string", "description": "Classic client-unit (page) schema name to collect the page sources for, e.g. 'ContactPageV2'"},
			"entity":      map[string]string{"type": "string", "description": "Entity schema name (optional; inferred from the page body when omitted). Drives entityColumns/columnTitles."},
			"output-file": map[string]string{"type": "string", "description": "Manifest output path (absolute path recommended). Must resolve inside the workspace or the OS " +
				"temp directory, and an existing file is never overwritten. Default: <workspace-root>/.clio-migration/<schema>/manifest.json."},
		}},
	}, func(ctx context.Context, client *creatio.Client, args map[string]any) (*mcp.CallToolResult, error) {
		// clio ignores unknown keys for this tool, so only a selector of another environment is refused.
		if refusal := refusesConnectionArgs(args); refusal != "" {
			return structuredToolResult(creatio.ClassicPageSourcesResult{Error: refusal}), nil
		}
		var input creatio.ClassicPageSourcesRequest
		var err error
		if input.SchemaName, err = optionalStringArg(args, "get-classic-page-sources", "schema-name"); err != nil {
			return nil, err
		}
		if input.Entity, err = optionalStringArg(args, "get-classic-page-sources", "entity"); err != nil {
			return nil, err
		}
		if input.OutputFile, err = optionalStringArg(args, "get-classic-page-sources", "output-file"); err != nil {
			return nil, err
		}
		return structuredToolResult(client.GetClassicPageSources(ctx, input)), nil
	})
}
