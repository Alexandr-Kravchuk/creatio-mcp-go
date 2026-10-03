package main

import (
	"context"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func init() {
	registerTool(map[string]any{
		"name": "list-user-tasks",
		"description": "List the user-facing user tasks (process designer palette, custom ones included) of the target environment. " +
			"Returns clio's command envelope { exit-code, execution-log-messages }: one Info line \"name<TAB>uid\" per task, then the total. " +
			"Pass a name as userTaskName on a userTask element of create-business-process; prefer the dedicated sendEmail, approval, addData " +
			"and changeAccessRights element types for EmailTemplateUserTask, ApprovalUserTask, AddDataUserTask and ChangeAdminRightsUserTask. " +
			"Requires the CrtProcessBuilder package (ProcessDesignService) on the target environment.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
	}, func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
		// clio reports argument refusals as exit code 1 inside its command envelope.
		if refusal := unknownArgumentError(args, "environment-name"); refusal != "" {
			return structuredToolResult(creatio.NewUserTasksResult(1, "Error", refusal)), nil
		}
		client, failure, err := envs.resolve("list-user-tasks", args, scopeName)
		if err != nil {
			return nil, err
		}
		if failure != nil {
			return resolverFailureEnvelope(failure), nil
		}
		return structuredToolResult(client.ListUserTasks(ctx)), nil
	})
}
