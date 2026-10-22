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
	"Install the package in the target environment and retry.\n" + processWriteInstallHint

// ListUserTasks reads the process designer palette through the CrtProcessBuilder ProcessDesignService. Like
// clio it first refuses when that package is not installed. Past that gate the log opens with clio's
// "Reading user task catalog from '<environment>'..." Info line, then lists one "name<TAB>uid" Info line per
// task and a closing total, or the Error of a failed read, exactly as clio's command log does.
func (c *Client) ListUserTasks(ctx context.Context, environmentName string) CommandResult {
	installed, err := c.userTasksPackageInstalled(ctx, processBuilderPackageName)
	if err != nil {
		return NewCommandResult(-1, "Error", CommandExceptionText(err))
	}
	if !installed {
		return NewCommandResult(1, "Error", processBuilderMissingMessage)
	}
	reading := LogMessage{MessageType: "Info", Value: fmt.Sprintf("Reading user task catalog from '%s'...", environmentName)}
	tasks, err := c.readUserTasks(ctx)
	if err != nil {
		return CommandResult{ExitCode: 1, Messages: []LogMessage{reading, {MessageType: "Error", Value: err.Error()}}}
	}
	messages := make([]LogMessage, 0, len(tasks)+2)
	messages = append(messages, reading)
	for _, task := range tasks {
		messages = append(messages, LogMessage{MessageType: "Info", Value: task.Name + "\t" + task.UID})
	}
	messages = append(messages, LogMessage{MessageType: "Info", Value: fmt.Sprintf("Total user tasks: %d", len(tasks))})
	return CommandResult{ExitCode: 0, Messages: messages}
}

type userTaskInfo struct {
	Name string `json:"name"`
	UID  string `json:"uid"`
}

func (c *Client) readUserTasks(ctx context.Context) ([]userTaskInfo, error) {
	// ProcessDesignService uses BodyStyle=Wrapped: a parameterless operation accepts an empty JSON object.
	response, err := c.callService(ctx, serviceCall{Route: "rest/ProcessDesignService/ListUserTasks", Body: []byte("{}"), Timeout: 45 * time.Second, Limit: maxResponseBytes})
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
