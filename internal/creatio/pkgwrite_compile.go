package creatio

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const pkgWriteCompileAllRoute = "ServiceModel/WorkspaceExplorerService.svc/Rebuild"
const pkgWriteCompilePackageRoute = "ServiceModel/WorkspaceExplorerService.svc/BuildPackage"
const pkgWriteCompileProcessRoute = "rest/ProcessDesignService/CompileProcess"

// Compile sends clio's scoped request. A missing build verdict is a failure, since it cannot prove the build finished.
func (c *Client) Compile(ctx context.Context, environment, packageName, processName string) CommandResult {
	route := pkgWriteCompileAllRoute
	body := []byte("{}")
	log := []LogMessage{}
	if packageName != "" {
		route = pkgWriteCompilePackageRoute
		body, _ = json.Marshal(map[string]string{"packageName": packageName})
		log = append(log, LogMessage{MessageType: "None", Value: "Start build packages (" + packageName + ")."})
	} else if processName != "" {
		route = pkgWriteCompileProcessRoute
		body, _ = json.Marshal(map[string]any{"request": map[string]string{"name": processName}})
		log = append(log, LogMessage{MessageType: "Info", Value: fmt.Sprintf("Compiling the package of process '%s' on '%s'. A compile reloads the runtime for every user of the environment and usually takes a few minutes...", processName, environment)})
	}
	response, err := c.callService(ctx, serviceCall{Route: route, Body: body, Timeout: 60 * time.Minute})
	if err != nil {
		return CommandResult{ExitCode: 1, Messages: append(log, LogMessage{MessageType: "Error", Value: err.Error()})}
	}
	var result struct {
		Success     *bool `json:"success"`
		BuildResult *int  `json:"buildResult"`
		ErrorInfo   *struct {
			Message   string `json:"message"`
			ErrorCode string `json:"errorCode"`
		} `json:"errorInfo"`
		CompileProcessResult *struct {
			Success         bool   `json:"success"`
			CompileRequired bool   `json:"compileRequired"`
			ProcessName     string `json:"processName"`
			PackageName     string `json:"packageName"`
			ErrorMessage    string `json:"errorMessage"`
		} `json:"CompileProcessResult"`
	}
	if json.Unmarshal(response, &result) != nil {
		return CommandResult{ExitCode: 1, Messages: append(log, LogMessage{MessageType: "Error", Value: "The compilation response is not valid JSON; it may have been cut off."})}
	}
	if processName != "" {
		if result.CompileProcessResult == nil {
			return CommandResult{ExitCode: 1, Messages: append(log, LogMessage{MessageType: "Error", Value: "CompileProcess returned a response clio could not read, so whether the package was compiled is UNKNOWN. Read last-compilation-log for this environment before compiling again."})}
		}
		process := result.CompileProcessResult
		if !process.Success {
			return CommandResult{ExitCode: 1, Messages: append(log, LogMessage{MessageType: "Error", Value: process.ErrorMessage})}
		}
		if !process.CompileRequired {
			log = append(log, LogMessage{MessageType: "Info", Value: fmt.Sprintf("Process '%s' carries no C# (no script task and no process methods), so nothing was compiled and nothing needs to be.", process.ProcessName)})
		}
		return CommandResult{ExitCode: 0, Messages: log}
	}
	if result.Success == nil {
		return CommandResult{ExitCode: 1, Messages: append(log, LogMessage{MessageType: "Error", Value: "The compilation response carried no build result."})}
	}
	if !*result.Success {
		message := "Compilation failed on the environment."
		if result.BuildResult != nil {
			message += fmt.Sprintf(" Build result: %d.", *result.BuildResult)
		}
		log = append(log, LogMessage{MessageType: "Error", Value: message})
		if result.ErrorInfo != nil {
			log = append(log, LogMessage{MessageType: "Error", Value: strings.Trim(strings.Join([]string{result.ErrorInfo.ErrorCode, result.ErrorInfo.Message}, ": "), ": ")})
		}
		return CommandResult{ExitCode: 1, Messages: log}
	}
	if packageName != "" {
		log = append(log, LogMessage{MessageType: "Info", Value: "Done"})
	}
	return CommandResult{ExitCode: 0, Messages: log}
}
