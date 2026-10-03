package main

import (
	"context"
	"fmt"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// create-user-task-page: clio's CreateUserTaskPageTool. Offline: it changes only the local workspace and needs
// no environment.
func init() {
	registerTool(map[string]any{"name": "create-user-task-page"}, func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
		var input struct {
			WorkspacePath string `json:"workspace-path"`
			PackageName   string `json:"package-name"`
			UserTaskUID   string `json:"user-task-uid"`
			PageName      string `json:"page-name"`
			Caption       string `json:"caption"`
			Culture       string `json:"culture"`
			SmallIconPath string `json:"small-icon-path"`
			LargeIconPath string `json:"large-icon-path"`
			TitleIconPath string `json:"title-icon-path"`
		}
		// clio binds user-task-uid as a Guid: a value that is not one is an argument-binding failure.
		if value, present := args["user-task-uid"]; present && value != nil {
			if text, isText := value.(string); !isText || !creatio.IsGUIDText(text) {
				return nil, fmt.Errorf("invalid-parameter-type: argument 'user-task-uid' for MCP tool 'create-user-task-page' must be an object. Received an incompatible JSON value.")
			}
		}
		if err := decodeStrictArgs(withoutEnvironmentArgs(args, scopeName), &input); err != nil {
			return nil, fmt.Errorf("decode create-user-task-page arguments: %w", err)
		}
		return structuredToolResult(creatio.CreateUserTaskPage(creatio.UserTaskPageRequest{WorkspacePath: input.WorkspacePath,
			PackageName: input.PackageName, UserTaskUID: input.UserTaskUID, PageName: input.PageName, Caption: input.Caption,
			Culture: input.Culture, SmallIconPath: input.SmallIconPath, LargeIconPath: input.LargeIconPath,
			TitleIconPath: input.TitleIconPath})), nil
	}, withAnnotations(toolAnnotations{ReadOnly: false, Destructive: false, Idempotent: false, OpenWorld: false}))
}
