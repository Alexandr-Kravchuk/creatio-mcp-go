package main

// The local workspace tools of clio 8.1.0.142 (T16c): create-workspace (CreateWorkspaceTool), add-package
// (WorkspacePackageTool), new-test-project (CreateTestProjectTool), new-integration-test-project
// (CreateIntegrationTestProjectTool) and new-ui-project (CreateUiProjectTool). Each binds its argument record
// as System.Text.Json does (case-insensitive members, unknown keys ignored) and answers with clio's command
// envelope. The commands themselves are in internal/workspace.
// clio: [McpServerTool(ReadOnly = false, Destructive = false, Idempotent = false, OpenWorld = false)] for all.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/workspace"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var localWorkspaceAnnotations = toolAnnotations{}

func init() {
	registerTool(map[string]any{"name": "create-workspace"}, invokeCreateWorkspace, withAnnotations(localWorkspaceAnnotations))
}

// workspaceAppSetting reads one top-level string of clio's appsettings.json (for example workspaces-root),
// matched case-insensitively as Json.NET does; empty when the file or the member is missing.
func workspaceAppSetting(envs *environments, name string) string {
	path := creatio.ClioSettingsPath()
	if envs != nil && envs.path != "" {
		path = envs.path
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var settings map[string]json.RawMessage
	if json.Unmarshal([]byte(strings.TrimPrefix(string(data), "\uFEFF")), &settings) != nil {
		return ""
	}
	for key, raw := range settings {
		if strings.EqualFold(key, name) {
			var value string
			if json.Unmarshal(raw, &value) == nil {
				return value
			}
		}
	}
	return ""
}

func invokeCreateWorkspace(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
	bound, err := clioBind("create-workspace", args, []clioMember{
		{name: "workspaceName", kind: clioString}, {name: "directory", kind: clioString},
	})
	if err != nil {
		return nil, err
	}
	name, _ := bound.str("workspaceName")
	directory, _ := bound.str("directory")
	return structuredToolResult(workspace.CreateWorkspace(ctx, workspace.CreateWorkspaceRequest{
		WorkspaceName: name, Directory: directory, WorkspacesRoot: workspaceAppSetting(envs, "workspaces-root"),
	})), nil
}

func init() {
	registerTool(map[string]any{"name": "add-package"}, invokeAddPackage, withAnnotations(localWorkspaceAnnotations))
	registerTool(map[string]any{"name": "new-test-project"}, invokeNewTestProject, withAnnotations(localWorkspaceAnnotations))
	registerTool(map[string]any{"name": "new-integration-test-project"}, invokeNewIntegrationTestProject,
		withAnnotations(localWorkspaceAnnotations))
}

// optionalBound is a bound string member as clio's nullable record member: nil when absent or null.
func optionalBound(bound clioBound, name string) *string {
	if text, ok := bound.str(name); ok {
		return &text
	}
	return nil
}

func processDirectory() string {
	directory, _ := os.Getwd()
	return directory
}

func invokeAddPackage(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
	const tool = "add-package"
	bound, err := clioBind(tool, args, []clioMember{
		{name: "name", kind: clioString}, {name: "workspace-path", kind: clioString},
		{name: "environment-name", kind: clioString}, {name: "as-app", kind: clioBool},
		{name: "build-zip-path", kind: clioString}, {name: "schema-name-prefix", kind: clioString},
	})
	if err != nil {
		return nil, err
	}
	request := workspace.AddPackageRequest{
		SchemaNamePrefix: optionalBound(bound, "schema-name-prefix"),
		BuildZipPath:     optionalBound(bound, "build-zip-path"),
		EnvironmentName:  optionalBound(bound, "environment-name"),
		WorkingDirectory: processDirectory(),
	}
	request.Name, _ = bound.str("name")
	request.WorkspacePath, _ = bound.str("workspace-path")
	if flag, ok := bound.values["as-app"].(bool); ok {
		request.AsApp = flag
	}
	var client *creatio.Client
	if request.EnvironmentName != nil && strings.TrimSpace(*request.EnvironmentName) != "" {
		resolved, failure, err := envs.resolve(tool, map[string]any{"environment-name": *request.EnvironmentName}, scopeName)
		if err != nil {
			return nil, err
		}
		if failure != nil {
			// BaseTool resolves the command for the named environment before it runs: an unknown name ends
			// the call in the resolver-error envelope.
			return resolverFailureEnvelope(failure), nil
		}
		{
			client = resolved
			settings, _ := envs.snapshot()
			if environment, ok := settings.Find(*request.EnvironmentName); ok {
				request.EnvironmentURI = environment.URI
				if environment.Present("Maintainer") {
					maintainer := environment.Maintainer
					request.Maintainer = &maintainer
				}
			}
		}
	}
	if client != nil {
		request.ReadPrefix = func(ctx context.Context) (string, workspace.PrefixReadFailure, error) {
			ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			result := client.GetSchemaNamePrefix(ctx)
			if result.Success {
				return result.SchemaNamePrefix, workspace.PrefixReadOther, nil
			}
			if ctx.Err() == context.DeadlineExceeded {
				return "", workspace.PrefixReadTimeout, ctx.Err()
			}
			switch result.ErrorCategory {
			case "Network":
				return "", workspace.PrefixReadNetwork, errors.New(result.Error)
			case "Authentication":
				return "", workspace.PrefixReadAuthentication, errors.New(result.Error)
			}
			return "", workspace.PrefixReadOther, errors.New(result.Error)
		}
	}
	request.DownloadConfiguration = func(ctx context.Context, root, value string, isEnvironment bool) creatio.CommandResult {
		if !isEnvironment {
			return creatio.DownloadConfigurationFromBuild(root, value)
		}
		if client == nil {
			return creatio.CommandFailure("Environment '" + value + "' is not registered.")
		}
		environmentPath := ""
		settings, _ := envs.snapshot()
		if environment, ok := settings.Find(value); ok {
			environmentPath = environment.EnvironmentPath
		}
		return client.DownloadConfigurationFromEnvironment(ctx, root, value, environmentPath)
	}
	return structuredToolResult(workspace.AddPackage(ctx, request)), nil
}

func invokeNewTestProject(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
	const tool = "new-test-project"
	bound, err := clioBind(tool, args, []clioMember{
		{name: "package-name", kind: clioString}, {name: "workspace-path", kind: clioString},
		{name: "environment-name", kind: clioString},
	})
	if err != nil {
		return nil, err
	}
	if name, ok := bound.str("environment-name"); ok && strings.TrimSpace(name) != "" {
		if _, failure, err := envs.resolve(tool, map[string]any{"environment-name": name}, scopeName); err != nil {
			return nil, err
		} else if failure != nil {
			return resolverFailureEnvelope(failure), nil
		}
	}
	request := workspace.TestProjectRequest{PackageName: optionalBound(bound, "package-name"), WorkingDirectory: processDirectory()}
	request.WorkspacePath, _ = bound.str("workspace-path")
	return structuredToolResult(workspace.CreateTestProject(request)), nil
}

func invokeNewIntegrationTestProject(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
	bound, err := clioBind("new-integration-test-project", args, []clioMember{
		{name: "package-name", kind: clioString}, {name: "workspace-path", kind: clioString},
		{name: "target-framework", kind: clioString},
	})
	if err != nil {
		return nil, err
	}
	request := workspace.TestProjectRequest{PackageName: optionalBound(bound, "package-name"), WorkingDirectory: processDirectory(),
		TargetFramework: "net10.0"}
	request.WorkspacePath, _ = bound.str("workspace-path")
	if framework, ok := bound.str("target-framework"); ok {
		request.TargetFramework = framework
	}
	return structuredToolResult(workspace.CreateIntegrationTestProject(request)), nil
}
