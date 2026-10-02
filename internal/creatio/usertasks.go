package creatio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const processBuilderPackageName = "CrtProcessBuilder"

// processBuilderMissingMessage is clio's [RequiresPackage] refusal for CrtProcessBuilder, hint line included.
const processBuilderMissingMessage = "To use this command, you need to install the CrtProcessBuilder package. " +
	"Install the package in the target environment and retry.\n" +
	"Run 'clio install-process-builder -e <environment>' (or call the install-process-builder MCP tool) to install or update CrtProcessBuilder."

// UserTasksResult reproduces clio's log-based CommandExecutionResult envelope for list-user-tasks.
type UserTaskLogMessage struct {
	MessageType string `json:"message-type"`
	Value       string `json:"value"`
}

type UserTasksResult struct {
	ExitCode int                  `json:"exit-code"`
	Messages []UserTaskLogMessage `json:"execution-log-messages"`
}

// NewUserTasksResult builds clio's one-message envelope; exit code 1 is a caller-actionable refusal,
// -1 an unexpected failure.
func NewUserTasksResult(exitCode int, messageType, value string) UserTasksResult {
	return UserTasksResult{ExitCode: exitCode, Messages: []UserTaskLogMessage{{MessageType: messageType, Value: value}}}
}

// ListUserTasks reads the process designer palette through the CrtProcessBuilder ProcessDesignService. Like
// clio it first refuses when that package is not installed. The result lists one "name<TAB>uid" Info line per
// task and a closing total, exactly as clio's command log does.
func (c *Client) ListUserTasks(ctx context.Context) UserTasksResult {
	installed, err := c.userTasksPackageInstalled(ctx, processBuilderPackageName)
	if err != nil {
		return NewUserTasksResult(-1, "Error", err.Error())
	}
	if !installed {
		return NewUserTasksResult(1, "Error", processBuilderMissingMessage)
	}
	tasks, err := c.readUserTasks(ctx)
	if err != nil {
		return NewUserTasksResult(1, "Error", err.Error())
	}
	messages := make([]UserTaskLogMessage, 0, len(tasks)+1)
	for _, task := range tasks {
		messages = append(messages, UserTaskLogMessage{MessageType: "Info", Value: task.Name + "\t" + task.UID})
	}
	messages = append(messages, UserTaskLogMessage{MessageType: "Info", Value: fmt.Sprintf("Total user tasks: %d", len(tasks))})
	return UserTasksResult{ExitCode: 0, Messages: messages}
}

type userTaskInfo struct {
	Name string `json:"name"`
	UID  string `json:"uid"`
}

func (c *Client) readUserTasks(ctx context.Context) ([]userTaskInfo, error) {
	// ProcessDesignService uses BodyStyle=Wrapped: a parameterless operation accepts an empty JSON object.
	response, err := c.postCreatioJSON(ctx, "rest/ProcessDesignService/ListUserTasks", []byte("{}"), 45*time.Second, maxResponseBytes)
	if err != nil {
		return nil, err
	}
	var envelope *struct {
		Result *struct {
			Success      bool           `json:"success"`
			UserTasks    []userTaskInfo `json:"userTasks"`
			ErrorMessage *string        `json:"errorMessage"`
		} `json:"ListUserTasksResult"`
	}
	if err := json.Unmarshal(response, &envelope); err != nil {
		return nil, fmt.Errorf("ListUserTasks returned invalid JSON: %w", err)
	}
	if envelope == nil {
		return nil, errors.New("ListUserTasks returned an empty response.")
	}
	if envelope.Result == nil {
		return nil, errors.New("ListUserTasks returned an unexpected response shape.")
	}
	if !envelope.Result.Success {
		if envelope.Result.ErrorMessage != nil {
			return nil, errors.New(*envelope.Result.ErrorMessage)
		}
		return nil, errors.New("ListUserTasks failed.")
	}
	return envelope.Result.UserTasks, nil
}

// packageInstalled checks presence the way clio's package gate does: a case-insensitive name match against the
// installed package list.
func (c *Client) userTasksPackageInstalled(ctx context.Context, name string) (bool, error) {
	rows, err := c.selectRows(ctx, buildSelectQuery("SysPackage", map[string]string{"Name": "Name"}, nil, 10000))
	if err != nil {
		return false, err
	}
	for _, row := range rows {
		if strings.EqualFold(rowString(row, "Name"), name) {
			return true, nil
		}
	}
	return false, nil
}
