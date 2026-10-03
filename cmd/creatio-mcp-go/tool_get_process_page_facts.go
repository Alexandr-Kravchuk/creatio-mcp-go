package main

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// processPageAliases are the spellings clio answers with a rename to schema-name.
var processPageAliases = map[string]string{"schemaName": "schema-name", "pageName": "schema-name", "page-name": "schema-name", "page": "schema-name", "name": "schema-name"}

func init() {
	registerTool(map[string]any{
		"name": "get-process-page-facts",
		"description": "Read the facts a Pre-configured page process element needs about a Freedom UI page on the single configured Creatio instance: " +
			"the buttons that can complete the page (with the caption the process designer records) and the page-scoped entity data sources. " +
			"Pass these verbatim into the process descriptor's preconfiguredPage.buttons / .dataSources — they are page FACTS, not choices, and " +
			"cannot be derived server-side because a page inherits buttons from its template chain. Choosing WHICH candidates complete the page " +
			"is still yours. Fails for a Classic UI page, which completes through its own page-designer buttons instead. Writes no files.",
		"inputSchema": map[string]any{"type": "object", "required": []string{"schema-name"}, "properties": map[string]any{
			"schema-name": map[string]string{"type": "string", "description": "Freedom UI page schema name, e.g. 'UsrMyApp_FormPage'."},
			"culture":     map[string]string{"type": "string", "description": "Culture used to resolve resource-backed button captions. Default en-US."},
		}},
	}, func(ctx context.Context, client *creatio.Client, args map[string]any) (*mcp.CallToolResult, error) {
		schemaName, err := optionalStringArg(args, "get-process-page-facts", "schema-name")
		if err != nil {
			return nil, err
		}
		culture, err := optionalStringArg(args, "get-process-page-facts", "culture")
		if err != nil {
			return nil, err
		}
		if refusal := processPageArgumentError(args); refusal != "" {
			return structuredToolResult(creatio.ProcessPageFactsResponse{SchemaName: schemaName, Error: refusal}), nil
		}
		return structuredToolResult(client.GetProcessPageFacts(ctx, schemaName, culture)), nil
	})
}

// processPageArgumentError reproduces clio's legacy-alias refusal: known misspellings get a rename hint,
// anything else is unknown. Environment selectors are refused, since this server targets one environment.
func processPageArgumentError(args map[string]any) string {
	if refusal := refusesConnectionArgs(args); refusal != "" {
		return refusal
	}
	keys := make([]string, 0, len(args))
	for key := range args {
		if environmentNameAliases[strings.ToLower(key)] {
			return environmentNameRefusal
		}
		if key != "schema-name" && key != "culture" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	var renames, unknown []string
	for _, key := range keys {
		shown := strings.NewReplacer("'", "", `"`, "").Replace(key)
		if canonical, ok := processPageAliases[key]; ok {
			renames = append(renames, fmt.Sprintf("'%s' -> '%s'", shown, canonical))
		} else {
			unknown = append(unknown, fmt.Sprintf("'%s'", shown))
		}
	}
	parts := []string{}
	if len(renames) > 0 {
		parts = append(parts, "Rename: "+joinCallerKeys(renames)+".")
	}
	if len(unknown) > 0 {
		parts = append(parts, "Unknown args: "+joinCallerKeys(unknown)+". Valid: culture, schema-name.")
	}
	return strings.Join(parts, " ")
}
