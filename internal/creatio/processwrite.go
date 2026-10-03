package creatio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ProcessWriteRequest is the wire-facing subset shared by the four ProcessDesignService writes.
type ProcessWriteRequest struct {
	Descriptor, Package, Name, UID, Operations string
	ConfirmLayoutChange                        bool
}

type processWriteResult struct {
	Success                  bool     `json:"success"`
	ErrorMessage             string   `json:"errorMessage"`
	SchemaName               string   `json:"schemaName"`
	SchemaUID                string   `json:"schemaUId"`
	AppliedOperations        int      `json:"appliedOperations"`
	FailedOperationIndex     *int     `json:"failedOperationIndex"`
	VersionName              string   `json:"versionName"`
	VersionSchemaUID         string   `json:"versionSchemaUId"`
	Version                  *int     `json:"version"`
	IsActiveVersion          *bool    `json:"isActiveVersion"`
	VersionRootSchemaUID     string   `json:"versionRootSchemaUId"`
	ActiveVersionName        string   `json:"activeVersionName"`
	ActiveVersionSchemaUID   string   `json:"activeVersionSchemaUId"`
	DeactivationFailureCount int      `json:"deactivationFailureCount"`
	Warnings                 []string `json:"warnings"`
}

const processWriteCompileNote = "compile-creatio not required"

func processWriteResultWith(messages ...LogMessage) CommandResult {
	return CommandResult{ExitCode: 0, Messages: messages}
}

func processWriteMessages(info string, warnings []string) []LogMessage {
	messages := []LogMessage{{MessageType: "Info", Value: info}}
	for _, warning := range warnings {
		messages = append(messages, LogMessage{MessageType: "Warning", Value: warning})
	}
	return messages
}

func processWriteAddCompileNote(result CommandResult) CommandResult {
	if result.ExitCode != 0 {
		return result
	}
	for _, message := range result.Messages {
		if strings.Contains(strings.ToLower(message.Value), "until the configuration is compiled") {
			return result
		}
	}
	result.Note = processWriteCompileNote
	return result
}

func (c *Client) processWriteCall(ctx context.Context, method string, request map[string]any) (processWriteResult, error) {
	var result processWriteResult
	installed, err := c.userTasksPackageInstalled(ctx, processBuilderPackageName)
	if err != nil {
		return result, err
	}
	if !installed {
		return result, errors.New(processBuilderMissingMessage)
	}
	body, err := json.Marshal(map[string]any{"request": request})
	if err != nil {
		return result, err
	}
	payload, err := c.callService(ctx, serviceCall{Route: "rest/ProcessDesignService/" + method, Body: body, Timeout: 45 * time.Second, Limit: maxResponseBytes})
	if err != nil {
		return result, err
	}
	if strings.TrimSpace(string(payload)) == "" {
		return result, fmt.Errorf("%s returned an empty response.", method)
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return result, fmt.Errorf("%s returned a response clio could not read, so whether the write took effect is UNKNOWN — re-read with describe-business-process before retrying.", method)
	}
	raw := envelope[method+"Result"]
	if len(raw) == 0 || string(raw) == "null" {
		return result, fmt.Errorf("%s returned an unexpected response shape.", method)
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return result, fmt.Errorf("%s returned an unexpected response shape.", method)
	}
	return result, nil
}

func processWriteOperations(source string, required bool) ([]any, error) {
	if strings.TrimSpace(source) == "" {
		if required {
			return nil, errors.New("operations is required and cannot be empty.")
		}
		return []any{}, nil
	}
	var operations []any
	if err := json.Unmarshal([]byte(source), &operations); err != nil {
		return nil, fmt.Errorf("Operations content is not valid JSON: %v", err)
	}
	if operations == nil {
		return nil, errors.New("Operations content must be a JSON array.")
	}
	return operations, nil
}

func processWriteTarget(request ProcessWriteRequest) (map[string]any, error) {
	hasName, hasUID := strings.TrimSpace(request.Name) != "", strings.TrimSpace(request.UID) != ""
	if hasName == hasUID {
		if hasName {
			return nil, errors.New("Provide only one of process-name or process-uid, not both.")
		}
		return nil, errors.New("one of process-name or process-uid is required.")
	}
	result := map[string]any{}
	if hasName {
		result["name"] = request.Name
	} else {
		result["uid"] = request.UID
	}
	return result, nil
}

func (c *Client) CreateBusinessProcess(ctx context.Context, environment string, request ProcessWriteRequest) CommandResult {
	if strings.TrimSpace(environment) == "" {
		return CommandFailure("environment-name is required and cannot be empty.")
	}
	if strings.TrimSpace(request.Descriptor) == "" {
		return CommandFailure("descriptor is required and cannot be empty.")
	}
	var descriptor map[string]any
	if err := json.Unmarshal([]byte(request.Descriptor), &descriptor); err != nil {
		return CommandFailure("Process descriptor is not valid JSON: " + err.Error())
	}
	if descriptor == nil {
		return CommandFailure("Process descriptor must be a JSON object.")
	}
	if strings.TrimSpace(request.Package) != "" {
		descriptor["packageName"] = request.Package
	}
	result, err := c.processWriteCall(ctx, "BuildProcess", descriptor)
	if err != nil {
		return CommandFailure(err.Error())
	}
	if !result.Success {
		if result.ErrorMessage == "" {
			result.ErrorMessage = "BuildProcess failed."
		}
		return CommandFailure(result.ErrorMessage)
	}
	return processWriteAddCompileNote(processWriteResultWith(processWriteMessages(fmt.Sprintf("Process '%s' created (UId: %s).", result.SchemaName, result.SchemaUID), result.Warnings)...))
}

func (c *Client) ModifyBusinessProcess(ctx context.Context, environment string, request ProcessWriteRequest) CommandResult {
	if strings.TrimSpace(environment) == "" {
		return CommandFailure("environment-name is required and cannot be empty.")
	}
	target, err := processWriteTarget(request)
	if err != nil {
		return CommandFailure(err.Error())
	}
	operations, err := processWriteOperations(request.Operations, true)
	if err != nil {
		return CommandFailure(err.Error())
	}
	target["operations"] = operations
	target["confirmLayoutChange"] = request.ConfirmLayoutChange
	result, err := c.processWriteCall(ctx, "ModifyProcess", target)
	if err != nil {
		return CommandFailure(err.Error())
	}
	if !result.Success {
		message := result.ErrorMessage
		if message == "" {
			message = "ModifyProcess failed."
		}
		if result.FailedOperationIndex != nil {
			message += fmt.Sprintf(" The operation at index %d is the one that refused.", *result.FailedOperationIndex)
		}
		return CommandFailure(message)
	}
	return processWriteAddCompileNote(processWriteResultWith(processWriteMessages(fmt.Sprintf("Process '%s' edited (%d operation(s) applied; UId: %s).", result.SchemaName, result.AppliedOperations, result.SchemaUID), result.Warnings)...))
}

func (c *Client) ModifyBusinessProcessAsNewVersion(ctx context.Context, environment string, request ProcessWriteRequest) CommandResult {
	if strings.TrimSpace(environment) == "" {
		return CommandFailure("environment-name is required and cannot be empty.")
	}
	target, err := processWriteTarget(request)
	if err != nil {
		return CommandFailure(err.Error())
	}
	operations, err := processWriteOperations(request.Operations, false)
	if err != nil {
		return CommandFailure(err.Error())
	}
	target["operations"] = operations
	if strings.TrimSpace(request.Package) != "" {
		target["packageName"] = request.Package
	}
	result, err := c.processWriteCall(ctx, "ModifyProcessAsNewVersion", target)
	if err != nil {
		return CommandFailure(err.Error())
	}
	if !result.Success {
		message := result.ErrorMessage
		if message == "" {
			message = "ModifyProcessAsNewVersion failed."
		}
		if result.FailedOperationIndex != nil {
			message += fmt.Sprintf(" The operation at index %d is the one that refused.", *result.FailedOperationIndex)
		}
		if result.AppliedOperations > 0 {
			message += fmt.Sprintf(" %d operation(s) had been applied to the version draft.", result.AppliedOperations)
		}
		if result.VersionName != "" || result.VersionSchemaUID != "" {
			message += fmt.Sprintf(" The version '%s' (UId: %s) WAS created and still exists — a version cannot be deleted.", result.VersionName, result.VersionSchemaUID)
		}
		return CommandFailure(message)
	}
	version := "A new version"
	if result.Version != nil {
		version = fmt.Sprintf("Version %d", *result.Version)
	}
	messages := processWriteMessages(fmt.Sprintf("%s '%s' created (%d operation(s) applied; UId: %s; family root: %s).", version, result.VersionName, result.AppliedOperations, result.VersionSchemaUID, result.VersionRootSchemaUID), nil)
	active := "The environment did not report which version is actual — verify with describe-business-process before reporting what runs."
	if result.IsActiveVersion != nil {
		if *result.IsActiveVersion {
			active = "This version is reported ACTIVE — unexpected for a create; verify with describe-business-process."
		} else {
			active = "The source version is still the actual one and keeps running. ASK THE USER to open this version and say whether to make it actual; call set-active-business-process-version only once they have. Do not chain the two - a version is created inactive precisely so they get to look first."
		}
	}
	messages = append(messages, LogMessage{MessageType: "Info", Value: active})
	for _, warning := range result.Warnings {
		messages = append(messages, LogMessage{MessageType: "Warning", Value: warning})
	}
	return processWriteAddCompileNote(processWriteResultWith(messages...))
}

func (c *Client) SetActiveBusinessProcessVersion(ctx context.Context, environment, name, uid string) CommandResult {
	if strings.TrimSpace(environment) == "" {
		return CommandFailure("environment-name is required and cannot be empty.")
	}
	hasName, hasUID := strings.TrimSpace(name) != "", strings.TrimSpace(uid) != ""
	if hasName == hasUID {
		if hasName {
			return CommandFailure("Provide only one of version-name or version-uid, not both.")
		}
		return CommandFailure("one of version-name or version-uid is required.")
	}
	target := map[string]any{}
	if hasName {
		target["name"] = name
	} else {
		target["uid"] = uid
	}
	result, err := c.processWriteCall(ctx, "SetActiveProcessVersion", target)
	if err != nil {
		return CommandFailure(err.Error())
	}
	if !result.Success {
		message := result.ErrorMessage
		if message == "" {
			message = "SetActiveProcessVersion failed."
		}
		return CommandFailure(message)
	}
	messages := []LogMessage{{MessageType: "Info", Value: fmt.Sprintf("Version '%s' is now the actual one (UId: %s).", result.ActiveVersionName, result.ActiveVersionSchemaUID)}, {MessageType: "Info", Value: "New process instances will start on this version. Instances already running stay on the version they started with — activation never migrates them."}}
	if result.DeactivationFailureCount > 0 {
		messages = append(messages, LogMessage{MessageType: "Warning", Value: fmt.Sprintf("%d sibling(s) are ALSO still flagged active, so which one runs is decided by package order rather than by this call. Read the family back with describe-business-process before reporting the rollback as done.", result.DeactivationFailureCount)})
	}
	if (hasName && strings.TrimSpace(result.ActiveVersionName) == "") || (hasUID && strings.TrimSpace(result.ActiveVersionSchemaUID) == "") {
		messages = append(messages, LogMessage{MessageType: "Warning", Value: "The activation was accepted, but the environment did not report which version is actual, so clio could NOT confirm the change took effect. Read the family back with describe-business-process and check isActiveVersion before reporting this as done."})
	}
	for _, warning := range result.Warnings {
		messages = append(messages, LogMessage{MessageType: "Warning", Value: warning})
	}
	return processWriteAddCompileNote(processWriteResultWith(messages...))
}
