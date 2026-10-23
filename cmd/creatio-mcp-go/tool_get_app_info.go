package main

import (
	"context"
	"strings"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/redact"
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
		// clio checks the identifier before it resolves the environment.
		if (strings.TrimSpace(id) == "") == (strings.TrimSpace(code) == "") {
			return structuredToolResult(creatio.AppInfoResponse{Error: "Provide exactly one identifier: id or code."}), nil
		}
		client, failure, err := envs.resolve("get-app-info", args, scopeName)
		if err != nil {
			return nil, err
		}
		if failure != nil {
			return structuredToolResult(creatio.AppInfoResponse{Error: redacted(failure)}), nil
		}
		// clio's ApplicationGetInfoTool redacts every failure it reports.
		result := client.GetAppInfo(ctx, creatio.AppInfoRequest{ID: id, Code: code})
		result.Error = redact.Text(result.Error)
		return structuredToolResult(result), nil
	})
}
