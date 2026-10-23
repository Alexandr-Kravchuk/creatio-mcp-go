package main

import (
	"context"
	"fmt"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func init() {
	registerTool(map[string]any{
		"name": "validate-page",
		"description": "Validates a Freedom UI page body without saving and without calling Creatio. Accepts an inline body or a local " +
			"get-page files.bodyFile via body-file; inline wins. This server runs a SUBSET of clio's validate-page: the JavaScript " +
			"syntax gate (structural: brackets, strings, comments, templates), the section marker pairs, the JSON/object content of " +
			"each section, the resources argument and the web parent references (known-containers). It does not run clio's binding, " +
			"handler, converter, validator, AST lint, chart, run-process, placement-slot or data-source checks, and it does not validate " +
			"mobile (JSON) bodies; every verdict says so in warnings.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
			"body":      map[string]string{"type": "string", "description": "Inline page body; takes precedence over body-file."},
			"body-file": map[string]string{"type": "string", "description": "Absolute local path, normally get-page files.bodyFile."},
			"resources": map[string]string{"type": "string", "description": "Optional JSON object string of resource key to text; it must be an object of strings."},
			"version":   map[string]string{"type": "string", "description": "Accepted for clio compatibility; chart validation is not run by this server."},
			"known-containers": map[string]any{"type": "array", "items": map[string]string{"type": "string"},
				"description": "Inherited element names from the page bundle. Omitted: unresolved parents warn because template context is unknown. Supplied (including []): unresolved parents fail."},
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
		// clio 8.1.0.142's binder refusals for known-containers. JSON null is the argument left out; a null item
		// is dropped (clio keeps it in its set, where it can match no parent name).
		if value, ok := args["known-containers"]; ok && value != nil {
			items, isArray := value.([]any)
			if !isArray {
				return nil, fmt.Errorf("invalid-parameter-type: argument 'known-containers' for MCP tool 'validate-page' must be an array. Received an incompatible JSON value.")
			}
			input.KnownContainers = []string{}
			for _, item := range items {
				text, isText := item.(string)
				if !isText && item != nil {
					return nil, fmt.Errorf("invalid-parameter-type: argument 'known-containers' for MCP tool 'validate-page' contains a value that does not match the documented shape. Received an incompatible JSON value.")
				}
				if isText {
					input.KnownContainers = append(input.KnownContainers, text)
				}
			}
		}
		result, err := creatio.ValidatePage(input)
		if err != nil {
			return nil, err
		}
		return structuredToolResult(result), nil
	})
}
