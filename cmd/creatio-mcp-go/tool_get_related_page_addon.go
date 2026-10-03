package main

import (
	"context"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func init() {
	registerTool(map[string]any{
		"name": "get-related-page-addon",
		"description": "Read an object's current RelatedPage configuration on the single configured Creatio instance: which Freedom UI pages are bound " +
			"as the default and the add page, per audience (role) and per record type. Returns entitySchemaUId as the base/root entity identity " +
			"resolved by Creatio, each entry's page-schema-uid + resolved page-schema-name, the role uid + resolved role-name (for the standard " +
			"'All employees' / 'All external users' audiences), the is-default / is-add / is-ssp-default flags, any type-column-value, and the " +
			"top-level type-column-uid. Read-only. Use this BEFORE create-related-page-addon: create REPLACES the whole configuration, so read the " +
			"current pages first, modify, then send the full set back.",
		"inputSchema": map[string]any{"type": "object", "required": []string{"entity-schema-name", "package-name"}, "properties": map[string]any{
			"entity-schema-name": map[string]string{"type": "string", "description": "Object (entity schema) name whose related pages to read, e.g. 'UsrDeliveryItem'."},
			"package-name":       map[string]string{"type": "string", "description": "Package that owns the add-on configuration."},
			"schema-type":        map[string]string{"type": "string", "description": "Which add-on to read: 'web' (RelatedPage, default) or 'mobile' (MobileRelatedPage — the object's default mobile edit page)."},
		}},
	}, func(ctx context.Context, client *creatio.Client, args map[string]any) (*mcp.CallToolResult, error) {
		values := map[string]string{}
		for _, name := range []string{"entity-schema-name", "package-name", "schema-type"} {
			value, err := optionalStringArg(args, "get-related-page-addon", name)
			if err != nil {
				return nil, err
			}
			values[name] = value
		}
		// clio ignores unknown keys for this tool; its uri/login/password fallback would target another environment.
		if refusal := refusesConnectionArgs(args); refusal != "" {
			return structuredToolResult(creatio.RelatedPageAddonFailure(refusal)), nil
		}
		return structuredToolResult(client.GetRelatedPageAddon(ctx, values["entity-schema-name"], values["package-name"], values["schema-type"])), nil
	})
}
