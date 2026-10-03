package main

import (
	"context"
	"fmt"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// install-application is clio's command-style InstallApplicationTool: the command envelope, and the
// resolver's envelope on an unusable environment. A missing name installs the current directory, as in clio.
// Window-only on the shared stand: it installs into the configuration and may restart the application.
func init() {
	registerTool(map[string]any{"name": "install-application"}, func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
		const tool = "install-application"
		values, err := appWriteStrings(tool, args, "name", "environment-name", "report-path")
		if err != nil {
			return nil, err
		}
		var check *bool
		switch value := args["check-compilation-errors"].(type) {
		case nil:
		case bool:
			check = &value
		default:
			return nil, fmt.Errorf("invalid-parameter-type: argument 'check-compilation-errors' for MCP tool '%s' must be a boolean. Received an incompatible JSON value.", tool)
		}
		client, refusal, err := envs.resolve(tool, args, scopeName)
		if err != nil {
			return nil, err
		}
		if refusal != nil {
			return resolverFailureEnvelope(refusal), nil
		}
		request := creatio.AppInstallRequest{Name: values["name"], ReportPath: values["report-path"], CheckCompilationErrors: check}
		if settings, _ := envs.snapshot(); values["environment-name"] != "" {
			if environment, found := settings.Find(values["environment-name"]); found {
				request.EnvironmentURI = environment.URI
				request.DeveloperMode = environment.DeveloperModeEnabled != nil && *environment.DeveloperModeEnabled
			}
		}
		return structuredToolResult(client.InstallApplication(ctx, request)), nil
	}, withAnnotations(appWriteAnnotations))
}
