package main

// push-workspace and restore-workspace: clio 8.1.0.142's PushWorkspaceTool and RestoreWorkspaceTool over
// WorkspaceCommandToolBase.ExecuteInWorkspace. The argument record binds as System.Text.Json does (members
// matched case-insensitively, unknown keys ignored, a wrong JSON kind raised as invalid-parameter-type); the
// workspace path is checked next (required, not a network path, absolute, an existing folder), then the
// environment resolves (clio's resolver envelope on failure), then the command runs under a per-environment
// lock with its answer in clio's command envelope. The commands are in internal/workspace.
// clio: [McpServerTool(ReadOnly = false, Destructive = true, Idempotent = false, OpenWorld = false)] for both.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"runtime"
	"strings"
	"sync"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/workspace"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var workspaceSyncAnnotations = toolAnnotations{Destructive: true}

func init() {
	registerTool(map[string]any{"name": "push-workspace"}, invokePushWorkspace, withAnnotations(workspaceSyncAnnotations))
	registerTool(map[string]any{"name": "restore-workspace"}, invokeRestoreWorkspace, withAnnotations(workspaceSyncAnnotations))
}

// workspaceSyncLocks is ExecuteUnderTenantLock: one push or restore at a time per environment.
var workspaceSyncLocks sync.Map

func workspaceSyncLock(key string) *sync.Mutex {
	lock, _ := workspaceSyncLocks.LoadOrStore(key, &sync.Mutex{})
	return lock.(*sync.Mutex)
}

// workspaceSyncAbsolute is WorkspaceCommandToolBase.IsAbsolutePath: rooted, with a root that ends in a directory
// separator ("C:\x" and "\x" on Windows, "/x" elsewhere).
func workspaceSyncAbsolute(path string) bool {
	if runtime.GOOS != "windows" {
		return strings.HasPrefix(path, "/")
	}
	if strings.HasPrefix(path, `\`) || strings.HasPrefix(path, "/") {
		return true
	}
	return len(path) >= 3 && path[1] == ':' && (path[2] == '\\' || path[2] == '/')
}

// workspaceSyncFolder is Directory.Exists.
func workspaceSyncFolder(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// workspaceSyncRefusal is ExecuteInWorkspace's path checks, in clio's order.
func workspaceSyncRefusal(path string) *mcp.CallToolResult {
	var message string
	switch {
	case strings.TrimSpace(path) == "":
		message = "Workspace path is required."
	case strings.HasPrefix(path, `\\`) || strings.HasPrefix(path, "//"):
		message = "Workspace path must be a local absolute path: " + path
	case !workspaceSyncAbsolute(path):
		message = "Workspace path must be absolute: " + path
	case !workspaceSyncFolder(path):
		message = "Workspace path not found: " + path
	default:
		return nil
	}
	return workspaceSyncResult(creatio.CommandFailure(message))
}

// workspaceSyncArgs binds the argument record; skip is nil for push's absent or null skip-backup.
func workspaceSyncArgs(tool string, args map[string]any, withSkip bool) (name, path string, skip *bool, err error) {
	members := []clioMember{{name: "environment-name", kind: clioString}, {name: "workspace-path", kind: clioString}}
	if withSkip {
		members = append(members, clioMember{name: "skip-backup", kind: clioAny})
	}
	bound, err := clioBind(tool, args, members)
	if err != nil {
		return "", "", nil, err
	}
	name, _ = bound.str("environment-name")
	path, _ = bound.str("workspace-path")
	if withSkip && bound.has("skip-backup") {
		switch value := bound.values["skip-backup"].(type) {
		case nil:
		case bool:
			skip = &value
		default:
			return "", "", nil, dataWriteKindBindingError(tool, "skip-backup", "a boolean")
		}
	}
	return name, path, skip, nil
}

// workspaceSyncResult is the command envelope as clio's serializer writes it: '<', '>' and '&' are not escaped
// (log lines carry dotnet output, paths and the resolver's "<url>" placeholders).
func workspaceSyncResult(value creatio.CommandResult) *mcp.CallToolResult {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return toolError(err)
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: strings.TrimSuffix(buffer.String(), "\n")}},
		StructuredContent: value}
}

// workspaceSyncResolverFailure is resolverFailureEnvelope written by workspaceSyncResult.
func workspaceSyncResolverFailure(err error) *mcp.CallToolResult {
	exitCode := 1
	var target *environmentError
	if errors.As(err, &target) {
		exitCode = target.exitCode()
	}
	return workspaceSyncResult(creatio.NewCommandResult(exitCode, "Error", resolverErrorText(err)))
}

// workspaceSyncRun resolves the environment and runs the command as a long operation under the tenant lock.
// A blank name does not fall back to the active environment: clio's MCP resolver requires a target.
func workspaceSyncRun(ctx context.Context, envs *environments, tool, name string,
	work func(ctx context.Context, client *creatio.Client, environment creatio.ClioEnvironment) creatio.CommandResult) (*mcp.CallToolResult, error) {
	if strings.TrimSpace(name) == "" {
		return workspaceSyncResolverFailure(&environmentError{message: missingTargetMessage}), nil
	}
	client, failure, err := envs.resolve(tool, map[string]any{"environment-name": name}, scopeName)
	if err != nil {
		return nil, err
	}
	if failure != nil {
		return workspaceSyncResolverFailure(failure), nil
	}
	settings, _ := envs.snapshot()
	environment, _ := settings.Find(name)
	lock := workspaceSyncLock(envs.tenantKey(name))
	result, _, err := runLongOperation(ctx, tool, appWriteNoDeadline, func(ctx context.Context, _ func(string)) creatio.CommandResult {
		lock.Lock()
		defer lock.Unlock()
		return work(ctx, client, environment)
	})
	if err != nil {
		return nil, err
	}
	return workspaceSyncResult(result), nil
}

func invokePushWorkspace(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
	const tool = "push-workspace"
	name, path, skip, err := workspaceSyncArgs(tool, args, true)
	if err != nil {
		return nil, err
	}
	if refusal := workspaceSyncRefusal(path); refusal != nil {
		return refusal, nil
	}
	return workspaceSyncRun(ctx, envs, tool, name, func(ctx context.Context, client *creatio.Client, environment creatio.ClioEnvironment) creatio.CommandResult {
		return workspace.PushWorkspace(ctx, client, workspace.PushRequest{
			Root: path, SkipBackup: skip, EnvironmentURI: environment.URI, IsNetCore: environment.IsNetCore,
			DeveloperMode: environment.DeveloperModeEnabled != nil && *environment.DeveloperModeEnabled,
		})
	})
}

func invokeRestoreWorkspace(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
	const tool = "restore-workspace"
	name, path, _, err := workspaceSyncArgs(tool, args, false)
	if err != nil {
		return nil, err
	}
	if refusal := workspaceSyncRefusal(path); refusal != nil {
		return refusal, nil
	}
	return workspaceSyncRun(ctx, envs, tool, name, func(ctx context.Context, client *creatio.Client, _ creatio.ClioEnvironment) creatio.CommandResult {
		return workspace.RestoreWorkspace(ctx, client, workspace.RestoreRequest{Root: path})
	})
}
