package main

import (
	"context"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func init() {
	registerTool(map[string]any{
		"name": "get-app-info",
		"description": "Gets installed app identity, primary package, entities (own columns, each entity includes virtual), " +
			"primary-package Freedom UI pages and the SchemaNamePrefix setting from the target Creatio environment. " +
			"Each entity column uses the sync-schemas write vocabulary (type, reference-schema-name, required, default-value-config).",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
			"id":   map[string]string{"type": "string", "description": "Application ID (GUID). Provide exactly one of id or code."},
			"code": map[string]string{"type": "string", "description": "Application code, e.g. 'UsrMyApp'. Provide exactly one of id or code."},
		}},
	}, func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
		// clio reports every get-app-info failure inside its envelope, argument problems included.
		if refusal := unknownArgumentError(args, "environment-name", "id", "code"); refusal != "" {
			return structuredToolResult(creatio.AppInfoResponse{Error: refusal}), nil
		}
		id, err := optionalStringArg(args, "get-app-info", "id")
		if err != nil {
			return structuredToolResult(creatio.AppInfoResponse{Error: err.Error()}), nil
		}
		code, err := optionalStringArg(args, "get-app-info", "code")
		if err != nil {
			return structuredToolResult(creatio.AppInfoResponse{Error: err.Error()}), nil
		}
		client, failure, err := envs.resolve("get-app-info", args, scopeName)
		if err != nil {
			return nil, err
		}
		if failure != nil {
			return structuredToolResult(creatio.AppInfoResponse{Error: redacted(failure)}), nil
		}
		return structuredToolResult(client.GetAppInfo(ctx, creatio.AppInfoRequest{ID: id, Code: code})), nil
	})
}
