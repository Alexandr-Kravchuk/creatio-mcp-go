package main

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// dataWriteRunProcessDeadline is clio master's run-process response deadline: McpProgressHeartbeat's 45 s
// default, overridden by CLIO_MCP_RESPONSE_DEADLINE_SECONDS.
func dataWriteRunProcessDeadline() time.Duration {
	return durationFromEnv("CLIO_MCP_RESPONSE_DEADLINE_SECONDS", 45*time.Second)
}

// dataWriteKindBindingError is clio's binder refusal for an argument of the wrong JSON kind.
func dataWriteKindBindingError(tool, argument, kind string) error {
	return fmt.Errorf("invalid-parameter-type: argument '%s' for MCP tool '%s' must be %s. Received an incompatible JSON value.", argument, tool, kind)
}

func init() {
	// clio's RunProcessTool: unknown keys answered in the response, the launch raced against the response
	// deadline with a launch gate, the platform verdict mapped by RunProcessCommand.
	registerTool(map[string]any{"name": "run-process"}, func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
		const tool = "run-process"
		valid := []string{"environment-name", "process-name", "parameters", "result-parameters", "timeout", "wait", "wait-timeout", "watch", "cleanup", "uri", "login", "password"}
		if message := unknownArgumentError(args, valid...); message != "" {
			return structuredToolResult(creatio.DataWriteProcessResult{Warnings: []string{}, Error: message}), nil
		}
		if _, present := args["process-name"]; !present {
			return nil, dataWriteBindingError(tool)
		}
		name, err := optionalStringArg(args, tool, "process-name")
		if err != nil {
			return nil, err
		}
		request := creatio.DataWriteProcessRequest{ProcessName: name, Gate: &creatio.DataWriteLaunchGate{}}
		if value, ok := args["parameters"]; ok && value != nil {
			object, isObject := value.(map[string]any)
			if !isObject {
				return nil, dataWriteKindBindingError(tool, "parameters", "an object")
			}
			// Go's decoder keeps no key order, so the values go out in ordinal key order.
			codes := make([]string, 0, len(object))
			for code := range object {
				codes = append(codes, code)
			}
			sort.Strings(codes)
			for _, code := range codes {
				request.Parameters = append(request.Parameters, creatio.DataWriteProcessParameter{Code: code, Value: object[code]})
			}
		}
		if value, ok := args["result-parameters"]; ok && value != nil {
			items, isArray := value.([]any)
			if !isArray {
				return nil, dataWriteKindBindingError(tool, "result-parameters", "an array")
			}
			request.ResultParameters = []string{}
			for _, item := range items {
				text, isString := item.(string)
				if !isString && item != nil {
					return nil, dataWriteNestedBindingError(tool, "result-parameters")
				}
				request.ResultParameters = append(request.ResultParameters, text)
			}
		}
		integer := func(name string, fallback int) (int, error) {
			value, ok := args[name]
			if !ok || value == nil {
				return fallback, nil
			}
			number, isNumber := value.(float64)
			if text, isText := value.(string); isText {
				parsed, parseErr := strconv.ParseFloat(strings.TrimSpace(text), 64)
				number, isNumber = parsed, parseErr == nil
			}
			if !isNumber || number != math.Trunc(number) || number > math.MaxInt32 || number < math.MinInt32 {
				return 0, dataWriteKindBindingError(tool, name, "a number")
			}
			return int(number), nil
		}
		flag := func(name string, fallback bool) (bool, error) {
			value, ok := args[name]
			if !ok || value == nil {
				return fallback, nil
			}
			typed, isBool := value.(bool)
			if !isBool {
				return false, dataWriteKindBindingError(tool, name, "a boolean")
			}
			return typed, nil
		}
		if request.TimeoutSeconds, err = integer("timeout", 0); err != nil {
			return nil, err
		}
		// clio 8.1.0.142 (ENG-103024): wait defaults to true and wait-timeout to 20 s.
		if request.Wait, err = flag("wait", true); err != nil {
			return nil, err
		}
		if request.WaitTimeoutSeconds, err = integer("wait-timeout", creatio.DataWriteDefaultWaitSeconds); err != nil {
			return nil, err
		}
		if value, ok := args["watch"]; ok && value != nil {
			items, isArray := value.([]any)
			if !isArray {
				return nil, dataWriteKindBindingError(tool, "watch", "an array")
			}
			request.Watch = []string{}
			for _, item := range items {
				text, isString := item.(string)
				if !isString && item != nil {
					return nil, dataWriteNestedBindingError(tool, "watch")
				}
				request.Watch = append(request.Watch, text)
			}
		}
		if request.Cleanup, err = flag("cleanup", false); err != nil {
			return nil, err
		}
		// The work runs detached from this request, so the caller's own context is what says it stopped waiting:
		// once it is done, no cancel or delete starts.
		request.CallerGone = func() bool { return ctx.Err() != nil }
		request.Version = creatio.NewDataWriteVersionRead()
		deadline := dataWriteRunProcessDeadline()
		request.Deadline = time.Now().Add(deadline)
		result, finished, err := runLongOperation(ctx, tool, deadline, func(workCtx context.Context, _ func(string)) creatio.DataWriteProcessResult {
			client, failure, bindErr := envs.resolve(tool, args, scopeDirect)
			if bindErr != nil {
				return creatio.DataWriteProcessResult{Warnings: []string{}, Error: redacted(bindErr)}
			}
			if failure != nil {
				return creatio.DataWriteProcessResult{Warnings: []string{}, Error: redacted(failure)}
			}
			return client.RunProcess(workCtx, request)
		})
		if err != nil {
			// The caller stopped waiting: a launch nobody waits for must not start afterwards.
			request.Gate.TryWithdraw()
			return nil, err
		}
		if !finished {
			if request.Gate.TryWithdraw() {
				return structuredToolResult(creatio.DataWriteProcessResult{Status: creatio.DataWriteNotStartedStatus, Warnings: []string{},
					Error: creatio.DataWriteNotLaunchedError(name)}), nil
			}
			sent, _ := request.Gate.SentAt()
			stillRunning := creatio.DataWriteProcessResult{Status: creatio.DataWriteStillRunningStatus,
				Warnings: []string{creatio.DataWriteStillRunningNote(name, sent)}}
			// The launched code is known whatever the version read did; its standing only when that read has answered.
			creatio.DataWriteAddVersion(&stillRunning, name, request.Version.CompletedFacts())
			return structuredToolResult(stillRunning), nil
		}
		return structuredToolResult(result), nil
	}, withAnnotations(toolAnnotations{ReadOnly: false, Destructive: true, Idempotent: false, OpenWorld: false}))
}
