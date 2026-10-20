package main

// localize-page: clio 8.1.0.142's LocalizePageTool over LocalizePageCommand.

import (
	"context"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var localizePageArguments = []string{"schema-name", "culture", "resources", "caption", "output-directory", "environment-name", "uri",
	"login", "password"}

// localizePageAliases is LocalizePageTool.LegacyAliases (Ordinal, no bare 'environment').
var localizePageAliases = map[string]string{"schemaName": "schema-name", "schema_name": "schema-name",
	"environmentName": "environment-name", "environment_name": "environment-name"}

func init() {
	registerTool(map[string]any{"name": "localize-page"}, invokeLocalizePage,
		withAnnotations(toolAnnotations{Destructive: true, Idempotent: true}))
}

func invokeLocalizePage(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
	bind := t21aBinder{tool: "localize-page", args: args}
	values := map[string]*string{}
	for _, name := range localizePageArguments {
		value, err := bind.str(name)
		if err != nil {
			return nil, err
		}
		values[name] = value
	}
	if message := t21aAliasError(args, localizePageArguments, localizePageAliases, false, ".",
		"Valid: schema-name, culture, resources, caption, output-directory, environment-name, uri, login, password."); message != "" {
		return t21aResult(creatio.LocalizePageFailure(values["schema-name"], message)), nil
	}
	client, failure, err := t21aResolve(envs, bind.tool, args, scopeDirect)
	if err != nil {
		return nil, err
	}
	if failure != "" {
		return t21aResult(creatio.LocalizePageFailure(values["schema-name"], failure)), nil
	}
	text := func(name string) string {
		if values[name] == nil {
			return ""
		}
		return *values[name]
	}
	response := client.LocalizePage(ctx, creatio.LocalizePageRequest{SchemaName: values["schema-name"], Culture: values["culture"],
		Resources: values["resources"], Caption: values["caption"], OutputDirectory: text("output-directory"),
		EnvironmentName: text("environment-name"), EnvironmentURI: text("uri")})
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return t21aResult(response), nil
}
