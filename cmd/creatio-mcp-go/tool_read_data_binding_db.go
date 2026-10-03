package main

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func init() {
	registerTool(map[string]any{
		"name": "read-data-binding-db",
		"description": "Reports what a DB-first package data binding on the target Creatio environment ships: entity schema, row count, the " +
			"bound column set, and each row's values. Only the columns a binding was created with transfer, so check this rather than the live " +
			"record. Localizable columns appear inline here but in a Localization folder in a package export. Prints bound values — treat a " +
			"binding over a settings or credential schema as sensitive output. Returns clio's command envelope { exit-code, execution-log-messages }.",
		"inputSchema": map[string]any{"type": "object", "required": []string{"package-name", "binding-name"}, "properties": map[string]any{
			"package-name": map[string]string{"type": "string", "description": "Target package name on the remote environment"},
			"binding-name": map[string]string{"type": "string", "description": "Binding folder name, i.e. the SysPackageSchemaData.Name"},
		}},
	}, func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
		packageName, err := optionalStringArg(args, "read-data-binding-db", "package-name")
		if err != nil {
			return nil, err
		}
		bindingName, err := optionalStringArg(args, "read-data-binding-db", "binding-name")
		if err != nil {
			return nil, err
		}
		// clio resolves the environment per call; a failure is reported inside the tool's own answer.
		client, failure, bindErr := envs.resolve("read-data-binding-db", args, scopeName)
		if bindErr != nil {
			return nil, bindErr
		}
		if failure != nil {
			return resolverFailureEnvelope(failure), nil
		}
		return structuredToolResult(client.ReadDataBinding(ctx, packageName, bindingName)), nil
	})
}
