package main

import (
	"context"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func init() {
	registerTool(map[string]any{
		"name": "read-data-binding-db",
		"description": "Reports what a DB-first package data binding on the single configured Creatio instance ships: entity schema, row count, the " +
			"bound column set, and each row's values. Only the columns a binding was created with transfer, so check this rather than the live " +
			"record. Localizable columns appear inline here but in a Localization folder in a package export. Prints bound values — treat a " +
			"binding over a settings or credential schema as sensitive output. Returns clio's command envelope { exit-code, execution-log-messages }.",
		"inputSchema": map[string]any{"type": "object", "required": []string{"package-name", "binding-name"}, "properties": map[string]any{
			"package-name": map[string]string{"type": "string", "description": "Target package name on the remote environment"},
			"binding-name": map[string]string{"type": "string", "description": "Binding folder name, i.e. the SysPackageSchemaData.Name"},
		}},
	}, func(ctx context.Context, client *creatio.Client, args map[string]any) (*mcp.CallToolResult, error) {
		packageName, err := optionalStringArg(args, "read-data-binding-db", "package-name")
		if err != nil {
			return nil, err
		}
		bindingName, err := optionalStringArg(args, "read-data-binding-db", "binding-name")
		if err != nil {
			return nil, err
		}
		// clio ignores unknown keys for this tool, so only a selector of another environment is refused.
		if refusal := refusesConnectionArgs(args); refusal != "" {
			return structuredToolResult(creatio.NewUserTasksResult(1, "Error", refusal)), nil
		}
		return structuredToolResult(client.ReadDataBinding(ctx, packageName, bindingName)), nil
	})
}
