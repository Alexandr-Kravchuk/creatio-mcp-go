package main

import (
	"context"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func init() {
	registerTool(map[string]any{
		"name": "validate-page",
		"description": "Validates a Freedom UI page body without saving and without calling Creatio. Accepts an inline body or a local " +
			"get-page files.bodyFile via body-file; inline wins. This server runs a SUBSET of clio's validate-page: the JavaScript " +
			"syntax gate (structural: brackets, strings, comments, templates), the section marker pairs, the JSON/object content of " +
			"each section and the resources argument. It does not run clio's binding, handler, converter, validator, AST lint, chart, " +
			"run-process or parent-container checks, and it does not validate mobile (JSON) bodies; every verdict says so in warnings.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
			"body":      map[string]string{"type": "string", "description": "Inline page body; takes precedence over body-file."},
			"body-file": map[string]string{"type": "string", "description": "Absolute local path, normally get-page files.bodyFile."},
			"resources": map[string]string{"type": "string", "description": "Optional JSON object string of resource key to text; it must be an object of strings."},
			"version":   map[string]string{"type": "string", "description": "Accepted for clio compatibility; chart validation is not run by this server."},
			"known-containers": map[string]any{"type": "array", "items": map[string]string{"type": "string"},
				"description": "Accepted for clio compatibility; the parent-container check is not run by this server."},
		}},
	}, func(_ context.Context, _ *environments, args map[string]any) (*mcp.CallToolResult, error) {
		// clio ignores unknown keys for this tool. It never calls Creatio, so an environment selector is
		// ignored too, as in clio.
		var input creatio.PageValidateRequest
		var err error
		if input.Body, err = optionalStringArg(args, "validate-page", "body"); err != nil {
			return nil, err
		}
		if input.BodyFile, err = optionalStringArg(args, "validate-page", "body-file"); err != nil {
			return nil, err
		}
		if input.Resources, err = optionalStringArg(args, "validate-page", "resources"); err != nil {
			return nil, err
		}
		if _, err = optionalStringArg(args, "validate-page", "version"); err != nil {
			return nil, err
		}
		return structuredToolResult(creatio.ValidatePage(input)), nil
	})
}
