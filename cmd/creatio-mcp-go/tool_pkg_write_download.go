package main

// download-configuration-by-build and download-configuration-by-environment: clio's DownloadConfigurationTool.
// Record arguments, unknown keys ignored. The workspace path is checked first (required, absolute, existing);
// clio then runs the command with the process working directory set to it, which this server does not
// change: the path is passed to the command instead.
// clio: [McpServerTool(ReadOnly = false, Destructive = false, Idempotent = false, OpenWorld = false)] for both.

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// pkgWriteAbsolutePath is DownloadConfigurationTool.IsAbsolutePath: rooted, with a root that ends in a
// directory separator, so "C:file" is not absolute on Windows. (.NET also accepts a drive-less "\\dir" there.)
func pkgWriteAbsolutePath(path string) bool {
	return strings.TrimSpace(path) != "" && filepath.IsAbs(path)
}

// pkgWriteWorkspaceRefusal is ExecuteInWorkspace's checks.
func pkgWriteWorkspaceRefusal(path string) *mcp.CallToolResult {
	switch {
	case strings.TrimSpace(path) == "":
		return structuredToolResult(creatio.CommandFailure("Workspace path is required."))
	case !pkgWriteAbsolutePath(path):
		return structuredToolResult(creatio.CommandFailure("Workspace path must be absolute: " + path))
	}
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		return structuredToolResult(creatio.CommandFailure("Workspace path not found: " + path))
	}
	return nil
}

func init() {
	registerTool(map[string]any{"name": "download-configuration-by-build"}, func(_ context.Context, _ *environments, args map[string]any) (*mcp.CallToolResult, error) {
		const tool = "download-configuration-by-build"
		buildPath, err := optionalStringArg(args, tool, "build-path")
		if err != nil {
			return nil, err
		}
		workspacePath, err := optionalStringArg(args, tool, "workspace-path")
		if err != nil {
			return nil, err
		}
		if !pkgWriteAbsolutePath(buildPath) {
			return structuredToolResult(creatio.CommandFailure("Build path must be absolute: " + buildPath)), nil
		}
		if refusal := pkgWriteWorkspaceRefusal(workspacePath); refusal != nil {
			return refusal, nil
		}
		return structuredToolResult(creatio.DownloadConfigurationFromBuild(workspacePath, buildPath)), nil
	}, withAnnotations(toolAnnotations{}))

	registerTool(map[string]any{"name": "download-configuration-by-environment"}, func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
		const tool = "download-configuration-by-environment"
		name, err := optionalStringArg(args, tool, "environment-name")
		if err != nil {
			return nil, err
		}
		workspacePath, err := optionalStringArg(args, tool, "workspace-path")
		if err != nil {
			return nil, err
		}
		if refusal := pkgWriteWorkspaceRefusal(workspacePath); refusal != nil {
			return refusal, nil
		}
		// clio downloads from the active environment when the name is blank; this server asks for it.
		if refusal := pkgWriteBlankEnvironment("environment-name", name); refusal != nil {
			return refusal, nil
		}
		client, failure, err := pkgWriteNamedEnvironment(envs, tool, name)
		if err != nil {
			return nil, err
		}
		if failure != nil {
			return resolverFailureEnvelope(failure), nil
		}
		environmentPath := ""
		settings, _ := envs.snapshot()
		if environment, ok := settings.Find(name); ok {
			environmentPath = environment.EnvironmentPath
		}
		return structuredToolResult(client.DownloadConfigurationFromEnvironment(ctx, workspacePath, name, environmentPath)), nil
	}, withAnnotations(toolAnnotations{}))
}
