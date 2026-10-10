package main

import (
	"context"
	"strings"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func init() {
	registerTool(map[string]any{"name": "execute-sql-script"}, func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
		const tool = "execute-sql-script"
		fields := map[string]string{}
		for _, key := range []string{"environment-name", "script", "file", "view", "destination-path"} {
			value, err := optionalStringArg(args, tool, key)
			if err != nil {
				return nil, err
			}
			fields[key] = value
		}
		silent := false
		if raw, exists := args["silent"]; exists && raw != nil {
			value, ok := raw.(bool)
			if !ok {
				return nil, dataWriteNestedBindingError(tool, "silent")
			}
			silent = value
		}
		failure := func(message string) (*mcp.CallToolResult, error) {
			return structuredToolResult(creatio.CommandFailure(message)), nil
		}
		if strings.TrimSpace(fields["environment-name"]) == "" {
			return failure("environment-name is required.")
		}
		if (strings.TrimSpace(fields["script"]) == "") == (strings.TrimSpace(fields["file"]) == "") {
			return failure("Provide exactly one of script or file.")
		}
		view := strings.ToLower(fields["view"])
		if view == "" {
			view = "table"
		}
		if view != "table" && view != "json" && view != "csv" && view != "xlsx" {
			return failure("view must be table, json, csv, or xlsx.")
		}
		if (view == "csv" || view == "xlsx") && strings.TrimSpace(fields["destination-path"]) == "" {
			return failure("destination-path is required for csv and xlsx.")
		}
		client, resolution, err := envs.resolve(tool, args, scopeName)
		if err != nil {
			return nil, err
		}
		if resolution != nil {
			return resolverFailureEnvelope(resolution), nil
		}
		return structuredToolResult(client.DataWriteSQL(ctx, fields["script"], fields["file"], view, fields["destination-path"], silent)), nil
	}, withAnnotations(toolAnnotations{ReadOnly: false, Destructive: true, Idempotent: false, OpenWorld: false}))
}
