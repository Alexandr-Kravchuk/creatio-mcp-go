package creatio

// pkg-to-db and pkg-to-file-system: clio's LoadPackagesToDbCommand / LoadPackagesToFileSystemCommand over
// FileDesignModeFileDesignModePackages. The file design mode (FSM) is read first; only when it is on are the
// packages loaded. Every request is retried as clio's ExecutePostRequest(..., 30 attempts, 3 s delay) does,
// because an application that has just restarted accepts requests with a delay.

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

const (
	pkgWriteFSMRoute  = "ServiceModel/WorkspaceExplorerService.svc/GetIsFileDesignMode"
	pkgWriteToDBRoute = "ServiceModel/AppInstallerService.svc/LoadPackagesToDB"
	pkgWriteToFSRoute = "ServiceModel/AppInstallerService.svc/LoadPackagesToFileSystem"
	// pkgWriteSyncTimeout stands in for clio's Timeout.Infinite: a package load of a large configuration runs
	// for many minutes.
	pkgWriteSyncTimeout = 60 * time.Minute
)

// pkgWriteSyncAttempts and pkgWriteSyncDelay are clio's maxRequestAttempts and delayBetweenRetryAttemptsSec;
// tests shorten the delay.
var (
	pkgWriteSyncAttempts = 30
	pkgWriteSyncDelay    = 3 * time.Second
)

type pkgWriteErrorInfo struct {
	Message   string `json:"message"`
	ErrorCode any    `json:"errorCode"`
}

type pkgWriteSyncResponse struct {
	Success   bool               `json:"success"`
	Value     bool               `json:"value"`
	ErrorInfo *pkgWriteErrorInfo `json:"errorInfo"`
	Errors    []*struct {
		WorkspaceItem *struct {
			Name *string `json:"name"`
		} `json:"workspaceItem"`
		ErrorInfo *pkgWriteErrorInfo `json:"errorInfo"`
	} `json:"errors"`
}

// pkgWriteSyncError is FileDesignModeLoadMessage.Build.
func pkgWriteSyncError(storage, reason string) LogMessage {
	return LogMessage{MessageType: "Error", Value: fmt.Sprintf("Load packages to %s on a web application ended with error: %s", storage, reason)}
}

// pkgWriteSyncDetail is GetErrorDetails.
func pkgWriteSyncDetail(info *pkgWriteErrorInfo) string {
	if info == nil {
		return "unknown error"
	}
	code := ""
	if info.ErrorCode != nil {
		code = fmt.Sprint(info.ErrorCode)
	}
	return fmt.Sprintf("%s (error code: %s)", info.Message, code)
}

// pkgWriteRetryPost is ExecutePostRequest with clio's attempt count and delay.
func (c *Client) pkgWriteRetryPost(ctx context.Context, route string) ([]byte, error) {
	var lastErr error
	for attempt := 1; attempt <= pkgWriteSyncAttempts; attempt++ {
		payload, err := c.callService(ctx, serviceCall{Route: route, Body: []byte{}, Timeout: pkgWriteSyncTimeout})
		if err == nil {
			return payload, nil
		}
		lastErr = err
		if attempt == pkgWriteSyncAttempts {
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(pkgWriteSyncDelay):
		}
	}
	return nil, lastErr
}

// SyncPackages is LoadPackagesToDbCommand.Execute (toDB) or LoadPackagesToFileSystemCommand.Execute.
func (c *Client) SyncPackages(ctx context.Context, toDB bool) CommandResult {
	storage, route := "file system", pkgWriteToFSRoute
	if toDB {
		storage, route = "database", pkgWriteToDBRoute
	}
	log, outcome, err := c.pkgWriteLoad(ctx, storage, route)
	if err != nil {
		// Load's catch: the readable failure, and the load counts as refused.
		return CommandResult{ExitCode: 1, Messages: append(log, LogMessage{MessageType: "Error", Value: err.Error()})}
	}
	// Load writes an empty line after the loader returns.
	log = append(log, LogMessage{MessageType: "None", Value: ""})
	switch outcome {
	case "completed":
		return CommandResult{ExitCode: 0, Messages: log}
	case "disabled":
		// The loader stays silent on a disabled FSM; the standalone command reports it.
		return CommandResult{ExitCode: 1, Messages: append(log, pkgWriteSyncError(storage, "disabled file design mode"))}
	default:
		return CommandResult{ExitCode: 1, Messages: log}
	}
}

// pkgWriteLoad is LoadPackagesToStorage: the log it writes and its outcome (completed, disabled, unknown,
// refused).
func (c *Client) pkgWriteLoad(ctx context.Context, storage, route string) ([]LogMessage, string, error) {
	log := []LogMessage{}
	probe, err := c.pkgWriteRetryPost(ctx, pkgWriteFSMRoute)
	if err != nil {
		return log, "", err
	}
	var state pkgWriteSyncResponse
	if err := json.Unmarshal(probe, &state); err != nil {
		return log, "", fmt.Errorf("GetIsFileDesignMode returned a response that is not valid JSON: %w", err)
	}
	if !state.Success {
		log = append(log, LogMessage{MessageType: "Error", Value: "Get file design mode ended with error: " + pkgWriteSyncDetail(state.ErrorInfo)})
		log = append(log, pkgWriteSyncError(storage, "file design mode state is unknown"))
		return log, "unknown", nil
	}
	if !state.Value {
		return log, "disabled", nil
	}
	log = append(log, LogMessage{MessageType: "None", Value: "Start load packages to " + storage + " on a web application"})
	response, err := c.pkgWriteRetryPost(ctx, route)
	if err != nil {
		return log, "", err
	}
	var result pkgWriteSyncResponse
	if err := json.Unmarshal(response, &result); err != nil {
		return log, "", fmt.Errorf("%s returned a response that is not valid JSON: %w", route, err)
	}
	if len(result.Errors) > 0 {
		if !result.Success && result.ErrorInfo != nil {
			log = append(log, pkgWriteSyncError(storage, pkgWriteSyncDetail(result.ErrorInfo)))
		}
		for _, item := range result.Errors {
			name := "unknown"
			var info *pkgWriteErrorInfo
			if item != nil {
				if item.WorkspaceItem != nil && item.WorkspaceItem.Name != nil {
					name = *item.WorkspaceItem.Name
				}
				info = item.ErrorInfo
			}
			log = append(log, pkgWriteSyncError(storage, fmt.Sprintf("Synchronization rejected item '%s': %s", name, pkgWriteSyncDetail(info))))
		}
		return log, "refused", nil
	}
	if result.Success {
		log = append(log, LogMessage{MessageType: "Info", Value: "Load packages to " + storage + " on a web application completed"})
		return log, "completed", nil
	}
	log = append(log, pkgWriteSyncError(storage, pkgWriteSyncDetail(result.ErrorInfo)))
	return log, "refused", nil
}
