package workspace

// create-workspace: clio's CreateWorkspaceCommand with Empty = true, as CreateWorkspaceTool runs it.

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
)

// CreateWorkspaceRequest is CreateWorkspaceArgs plus appsettings' workspaces-root.
type CreateWorkspaceRequest struct {
	WorkspaceName  string
	Directory      string
	WorkspacesRoot string
}

// CreateWorkspace creates an empty workspace under Directory (or WorkspacesRoot) / WorkspaceName.
func CreateWorkspace(ctx context.Context, request CreateWorkspaceRequest) creatio.CommandResult {
	log := &Log{}
	destination, err := createWorkspace(ctx, log, request)
	if err != nil {
		log.Error(err.Error())
		return log.Result(1)
	}
	log.Info("Workspace created at: " + destination)
	log.Info("Done")
	return log.Result(0)
}

func createWorkspace(ctx context.Context, log *Log, request CreateWorkspaceRequest) (string, error) {
	if strings.TrimSpace(request.WorkspaceName) == "" {
		return "", errorf("WorkspaceName is required when using --empty. (Parameter 'options')")
	}
	if isPathRooted(request.WorkspaceName) {
		return "", errorf("WorkspaceName must be a relative folder name.")
	}
	base := request.Directory
	if strings.TrimSpace(base) == "" {
		base = request.WorkspacesRoot
	}
	if strings.TrimSpace(base) == "" {
		return "", errorf("Provide --directory for create-workspace --empty or configure 'workspaces-root' in appsettings.json.")
	}
	if !isPathRooted(base) {
		return "", errorf("--directory and appsettings 'workspaces-root' must be absolute paths.")
	}
	base = fullPath(base)
	if !directoryExists(base) {
		return "", errorf("Workspace root directory does not exist: %s", base)
	}
	destination := fullPath(combinePath(base, request.WorkspaceName))
	if !strings.HasPrefix(destination, strings.TrimRight(fullPath(base), string(filepath.Separator))+string(filepath.Separator)) {
		return "", errorf("WorkspaceName must be located under the resolved base directory.")
	}
	if directoryExists(destination) && !isEmptyDirectory(destination) {
		return "", errorf("Destination folder already exists and is not empty: %s", destination)
	}
	if err := os.MkdirAll(destination, 0o777); err != nil {
		return "", err
	}
	if err := (Layout{Root: destination}).create(log, NewSDKVersions(ctx, log)); err != nil {
		return "", err
	}
	return destination, nil
}
