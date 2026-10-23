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
	"os"
	"strings"

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
