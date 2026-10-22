package main

import (
	"context"
	"errors"
	"strings"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func init() {
	// clio's ExecuteSqlScriptTool: the tool's own validation (command envelope, exit 1), then the environment,
	// then the [RequiresPackage("cliogate", "2.0.0.41")] gate, then SqlScriptCommand.
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
				return nil, errors.New("invalid-parameter-type: argument 'silent' for MCP tool 'execute-sql-script' must be a boolean. Received an incompatible JSON value.")
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
		view := "table"
		if raw, present := args["view"]; present && raw != nil {
			view = strings.ToLower(fields["view"])
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
		refusal, err := client.DataWriteSQLRequirement(ctx)
		if err != nil {
			return structuredToolResult(creatio.NewCommandResult(-1, "Error", creatio.CommandExceptionText(err))), nil
		}
		if refusal != "" {
			return failure(refusal)
		}
		request := creatio.DataWriteSQLRequest{View: view, Silent: silent}
		if strings.TrimSpace(fields["script"]) != "" {
			request.Script = fields["script"]
		} else {
			request.File = fields["file"]
		}
		if raw, present := args["destination-path"]; present && raw != nil {
			destination := fields["destination-path"]
			request.Destination = &destination
		}
		return structuredToolResult(client.ExecuteSQLScript(ctx, request)), nil
	}, withAnnotations(toolAnnotations{ReadOnly: false, Destructive: true, Idempotent: false, OpenWorld: false}))
}
