package creatio

import (
	"context"
	"encoding/json"
	"fmt"
)

const pkgWriteFSMRoute = "ServiceModel/WorkspaceExplorerService.svc/GetIsFileDesignMode"
const pkgWriteToDBRoute = "ServiceModel/AppInstallerService.svc/LoadPackagesToDB"
const pkgWriteToFSRoute = "ServiceModel/AppInstallerService.svc/LoadPackagesToFileSystem"

type pkgWriteSyncResponse struct {
	Success   bool `json:"success"`
	Value     bool `json:"value"`
	ErrorInfo *struct {
		Message   string `json:"message"`
		ErrorCode any    `json:"errorCode"`
	} `json:"errorInfo"`
	Errors []struct {
		WorkspaceItem *struct {
			Name string `json:"name"`
		} `json:"workspaceItem"`
		ErrorInfo *struct {
			Message   string `json:"message"`
			ErrorCode any    `json:"errorCode"`
		} `json:"errorInfo"`
	} `json:"errors"`
}

func pkgWriteSyncError(storage, reason string) LogMessage {
	return LogMessage{MessageType: "Error", Value: fmt.Sprintf("Load packages to %s on a web application ended with error: %s", storage, reason)}
}

func pkgWriteSyncDetail(info *struct {
	Message   string `json:"message"`
	ErrorCode any    `json:"errorCode"`
}) string {
	if info == nil {
		return "unknown error"
	}
	return fmt.Sprintf("%s (error code: %v)", info.Message, info.ErrorCode)
}

// SyncPackages follows FileDesignModeFileDesignModePackages: probe FSM first, then synchronize only when enabled.
func (c *Client) SyncPackages(ctx context.Context, toDB bool) CommandResult {
	storage, route := "file system", pkgWriteToFSRoute
	if toDB {
		storage, route = "database", pkgWriteToDBRoute
	}
	log := []LogMessage{}
	probe, err := c.callService(ctx, serviceCall{Route: pkgWriteFSMRoute, Body: []byte{}})
	if err != nil {
		return CommandResult{ExitCode: 1, Messages: []LogMessage{pkgWriteSyncError(storage, err.Error())}}
	}
	var state pkgWriteSyncResponse
	if err := json.Unmarshal(probe, &state); err != nil {
		return CommandResult{ExitCode: 1, Messages: []LogMessage{pkgWriteSyncError(storage, err.Error())}}
	}
	if !state.Success {
		log = append(log, LogMessage{MessageType: "Error", Value: "Get file design mode ended with error: " + pkgWriteSyncDetail(state.ErrorInfo)})
		log = append(log, pkgWriteSyncError(storage, "file design mode state is unknown"))
		return CommandResult{ExitCode: 1, Messages: log}
	}
	if !state.Value {
		return CommandResult{ExitCode: 1, Messages: []LogMessage{pkgWriteSyncError(storage, "disabled file design mode")}}
	}
	log = append(log, LogMessage{MessageType: "None", Value: "Start load packages to " + storage + " on a web application"})
	response, err := c.callService(ctx, serviceCall{Route: route, Body: []byte{}})
	if err != nil {
		return CommandResult{ExitCode: 1, Messages: append(log, pkgWriteSyncError(storage, err.Error()))}
	}
	var result pkgWriteSyncResponse
	if err := json.Unmarshal(response, &result); err != nil {
		return CommandResult{ExitCode: 1, Messages: append(log, pkgWriteSyncError(storage, err.Error()))}
	}
	if len(result.Errors) > 0 {
		if !result.Success && result.ErrorInfo != nil {
			log = append(log, pkgWriteSyncError(storage, pkgWriteSyncDetail(result.ErrorInfo)))
		}
		for _, item := range result.Errors {
			name := "unknown"
			if item.WorkspaceItem != nil && item.WorkspaceItem.Name != "" {
				name = item.WorkspaceItem.Name
			}
			log = append(log, pkgWriteSyncError(storage, fmt.Sprintf("Synchronization rejected item '%s': %s", name, pkgWriteSyncDetail(item.ErrorInfo))))
		}
		return CommandResult{ExitCode: 1, Messages: log}
	}
	if !result.Success {
		return CommandResult{ExitCode: 1, Messages: append(log, pkgWriteSyncError(storage, pkgWriteSyncDetail(result.ErrorInfo)))}
	}
	log = append(log, LogMessage{MessageType: "Info", Value: "Load packages to " + storage + " on a web application completed"})
	return CommandResult{ExitCode: 0, Messages: log}
}
