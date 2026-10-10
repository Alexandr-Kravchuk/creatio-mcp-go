package main

// restart-by-environment-name and restart-by-credentials: clio's RestartTool (master). Flat arguments,
// unknown keys ignored. With waitReady=false only the restart request runs. With waitReady (the default) the
// operation is recorded in the restart registry BEFORE the request, the request and the readiness wait run
// inside the response-deadline race, and the answer is the request's own result once the application serves
// again; restart-status reads the record (ready, timedout, or requestfailed when the request itself failed).
// clio: [McpServerTool(ReadOnly = false, Destructive = true, Idempotent = false, OpenWorld = false)].

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	// pkgWriteMaxReadyTimeoutSeconds is RestartOptions.MaxReadyTimeoutSeconds.
	pkgWriteMaxReadyTimeoutSeconds = 3600
	// pkgWriteDefaultResponseDeadline is clio master's McpProgressHeartbeat.DefaultResponseDeadline (45 s;
	// 8.1.0.134 waited 150 s). CLIO_MCP_RESPONSE_DEADLINE_SECONDS overrides it as in clio.
	pkgWriteDefaultResponseDeadline = 45 * time.Second
)

// pkgWriteResponseDeadline is the deadline compile-creatio and the restart tools answer in-progress after.
func pkgWriteResponseDeadline() time.Duration {
	return durationFromEnv("CLIO_MCP_RESPONSE_DEADLINE_SECONDS", pkgWriteDefaultResponseDeadline)
}

// pkgWriteBoolArg reads an optional flat boolean; absent or null is the default.
func pkgWriteBoolArg(args map[string]any, tool, name string, fallback bool) (bool, error) {
	value, ok := args[name]
	if !ok || value == nil {
		return fallback, nil
	}
	flag, ok := value.(bool)
	if !ok {
		return false, fmt.Errorf("invalid-parameter-type: argument '%s' for MCP tool '%s' must be a boolean. Received an incompatible JSON value.", name, tool)
	}
	return flag, nil
}

// pkgWriteIntArg reads an optional flat integer; absent or null is the default.
func pkgWriteIntArg(args map[string]any, tool, name string, fallback int) (int, error) {
	value, ok := args[name]
	if !ok || value == nil {
		return fallback, nil
	}
	number, ok := value.(float64)
	if !ok || number != math.Trunc(number) || number < math.MinInt32 || number > math.MaxInt32 {
		return 0, fmt.Errorf("invalid-parameter-type: argument '%s' for MCP tool '%s' must be an integer. Received an incompatible JSON value.", name, tool)
	}
	return int(number), nil
}

// pkgWriteResolverResult is CommandExecutionResult.FromResolverError for a failure the tool reports itself.
func pkgWriteResolverResult(err error) creatio.CommandResult {
	exitCode := 1
	var target *environmentError
	if errors.As(err, &target) {
		exitCode = target.exitCode()
	}
	return creatio.NewCommandResult(exitCode, "Error", resolverErrorText(err))
}

// pkgWriteRestartTarget is one restart call: how to reach the environment and how messages name it.
type pkgWriteRestartTarget struct {
	tool        string
	description string
	// environmentName is empty for a credentials restart, which restart-status cannot report.
	environmentName string
	tenantKey       string
	waitTimeout     int
	resolve         func() (*creatio.Client, *creatio.CommandResult)
}

func init() {
	// clio: RestartInstanceByName(environmentName [required], waitReady = true, waitTimeoutSeconds = 600).
	registerTool(map[string]any{"name": "restart-by-environment-name"}, func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
		const tool = "restart-by-environment-name"
		name, err := pkgWriteRequiredFlatString(args, tool, "environmentName")
		if err != nil {
			return nil, err
		}
		waitReady, waitTimeout, err := pkgWriteRestartOptions(args, tool)
		if err != nil {
			return nil, err
		}
		if pkgWriteBlank(name) {
			return structuredToolResult(creatio.CommandFailure("environment-name is required and cannot be empty.")), nil
		}
		return pkgWriteRestart(ctx, pkgWriteRestartTarget{
			tool: tool, description: fmt.Sprintf("environment '%s'", name), environmentName: name,
			tenantKey: envs.tenantKey(name), waitTimeout: waitTimeout,
			resolve: func() (*creatio.Client, *creatio.CommandResult) {
				client, failure, err := pkgWriteNamedEnvironment(envs, tool, name)
				if err == nil && failure != nil {
					err = failure
				}
				if err != nil {
					result := pkgWriteResolverResult(err)
					return nil, &result
				}
				return client, nil
			},
		}, waitReady)
	}, withAnnotations(toolAnnotations{Destructive: true}))

	// clio: RestartInstanceByCredentials(url, userName, password [required], isNetCore = false, waitReady = true,
	// waitTimeoutSeconds = 600).
	registerTool(map[string]any{"name": "restart-by-credentials"}, func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
		const tool = "restart-by-credentials"
		values := map[string]string{}
		for _, name := range []string{"url", "userName", "password"} {
			value, err := pkgWriteRequiredFlatString(args, tool, name)
			if err != nil {
				return nil, err
			}
			values[name] = value
		}
		isNetCore, err := pkgWriteBoolArg(args, tool, "isNetCore", false)
		if err != nil {
			return nil, err
		}
		waitReady, waitTimeout, err := pkgWriteRestartOptions(args, tool)
		if err != nil {
			return nil, err
		}
		for _, name := range []string{"url", "userName", "password"} {
			if pkgWriteBlank(values[name]) {
				return structuredToolResult(creatio.CommandFailure(name + " is required and cannot be empty.")), nil
			}
		}
		client, clientErr := pkgWriteCredentialsClient(values["url"], values["userName"], values["password"], isNetCore)
		tenantKey := "unresolved:" + strings.TrimSpace(values["url"])
		if clientErr == nil {
			tenantKey = client.TargetKey()
		}
		return pkgWriteRestart(ctx, pkgWriteRestartTarget{
			tool: tool, description: fmt.Sprintf("'%s'", values["url"]), tenantKey: tenantKey, waitTimeout: waitTimeout,
			resolve: func() (*creatio.Client, *creatio.CommandResult) {
				if clientErr != nil {
					result := creatio.CommandFailure(clientErr.Error())
					return nil, &result
				}
				return client, nil
			},
		}, waitReady)
	}, withAnnotations(toolAnnotations{Destructive: true}))
}

// pkgWriteRestartOptions reads waitReady and waitTimeoutSeconds, the latter clamped to 1..3600.
func pkgWriteRestartOptions(args map[string]any, tool string) (bool, int, error) {
	waitReady, err := pkgWriteBoolArg(args, tool, "waitReady", true)
	if err != nil {
		return false, 0, err
	}
	waitTimeout, err := pkgWriteIntArg(args, tool, "waitTimeoutSeconds", 600)
	if err != nil {
		return false, 0, err
	}
	return waitReady, min(max(waitTimeout, 1), pkgWriteMaxReadyTimeoutSeconds), nil
}

// pkgWriteCredentialsClient builds the direct connection restart-by-credentials uses. A url that is not an
// absolute http(s) address fails as .NET's Uri does when clio builds the service address from it.
func pkgWriteCredentialsClient(rawURL, login, password string, isNetCore bool) (*creatio.Client, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, errors.New("Invalid URI: The format of the URI could not be determined.")
	}
	config, err := creatio.ClioEnvironment{URI: strings.TrimSpace(rawURL), Login: login, Password: password, IsNetCore: isNetCore}.Config()
	if err != nil {
		return nil, err
	}
	return creatio.NewClient(config)
}

// pkgWriteRestart is RestartTool.ExecuteWithReadinessWait.
func pkgWriteRestart(ctx context.Context, target pkgWriteRestartTarget, waitReady bool) (*mcp.CallToolResult, error) {
	if !waitReady {
		client, failure := target.resolve()
		if failure != nil {
			return structuredToolResult(*failure), nil
		}
		return structuredToolResult(client.RestartApplication(ctx)), nil
	}
	operation := restartOperations.begin(target.tenantKey, target.environmentName, operationDetails{})
	var accepted atomic.Bool
	result, finished, err := runLongOperation(ctx, target.tool, pkgWriteResponseDeadline(),
		func(ctx context.Context, _ func(string)) creatio.CommandResult {
			client, failure := target.resolve()
			if failure != nil {
				restartOperations.finishAs(operation.ID, failure.ExitCode, "requestfailed", nil)
				return *failure
			}
			request := client.RestartApplication(ctx)
			if request.ExitCode != 0 {
				restartOperations.finishAs(operation.ID, request.ExitCode, "requestfailed", nil)
				return request
			}
			accepted.Store(true)
			if client.WaitForReadiness(ctx, time.Duration(target.waitTimeout)*time.Second) {
				restartOperations.finish(operation.ID, 0, nil)
				return request
			}
			restartOperations.finish(operation.ID, 1, nil)
			return creatio.CommandFailure(fmt.Sprintf("Restart of %s was requested successfully, but the application "+
				"did not answer its health-check within %ds.", target.description, target.waitTimeout))
		})
	if err != nil {
		return nil, err
	}
	if finished {
		return structuredToolResult(result), nil
	}
	if accepted.Load() {
		return structuredToolResult(creatio.CommandInfo(pkgWriteRestartInProgress(target, operation.ID))), nil
	}
	return structuredToolResult(creatio.CommandInfo(pkgWriteRestartPending(target, operation.ID))), nil
}

// pkgWriteRestartInProgress is RestartTool.BuildInProgressMessage.
func pkgWriteRestartInProgress(target pkgWriteRestartTarget, operationID string) string {
	return fmt.Sprintf("Restart of %s was accepted and the restart request itself already succeeded; the application is "+
		"still warming up (MCP response deadline reached, the readiness wait continues server-side for up to %ds). ",
		target.description, target.waitTimeout) +
		pkgWriteRestartPollGuidance(target, operationID) +
		fmt.Sprintf(" Typical warm-up is 1-10 minutes; do NOT retry %s.", target.tool)
}

// pkgWriteRestartPending is RestartTool.BuildRequestPendingMessage.
func pkgWriteRestartPending(target pkgWriteRestartTarget, operationID string) string {
	text := fmt.Sprintf("The restart request for %s has not been answered yet (MCP response deadline reached); it "+
		"keeps going server-side, so it is not yet known whether the restart was accepted. ", target.description) +
		pkgWriteRestartPollGuidance(target, operationID)
	if pkgWriteBlank(target.environmentName) {
		text += " Whether this restart happened is unknown, and nothing here will report it: do not assume new code is " +
			"loaded - an instance that answers proves nothing, because it also answers when the request failed and " +
			"it never went down. Ask the user before restarting again."
	} else {
		text += " A request that fails is reported there as requestfailed: no restart happened, so ask the user, then retry once."
	}
	return text + fmt.Sprintf(" Do NOT retry %s.", target.tool)
}

// pkgWriteRestartPollGuidance is RestartTool.BuildPollGuidance.
func pkgWriteRestartPollGuidance(target pkgWriteRestartTarget, operationID string) string {
	if pkgWriteBlank(target.environmentName) {
		return "This restart was started by credentials, not by environment name, so restart-status " +
			"(which is keyed by a registered environment name) cannot report it. The healthcheck " +
			"tool only confirms liveness — it is not an authenticated readiness signal — so to " +
			"confirm the instance is genuinely serving, use restart-by-environment-name, which waits " +
			"for an authenticated application-layer round-trip and is pollable via restart-status."
	}
	return fmt.Sprintf("Poll restart-status with environment-name '%s' (or operation-id '%s') — exit code 0 means the "+
		"application answered an authenticated application-layer round-trip and is genuinely serving.",
		target.environmentName, operationID)
}
