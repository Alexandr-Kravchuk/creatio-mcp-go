package main

// install-toolkit, update-toolkit and delete-toolkit, ported from clio 8.1.0.142's SkillManagementTool over
// internal/toolkit. Like clio they act on the user's coding agents (their home directories and CLIs on
// PATH), never on Creatio.

import (
	"context"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/knowledge"
	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/toolkit"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func init() {
	registerTool(map[string]any{"name": "install-toolkit"}, func(_ context.Context, _ *environments, args map[string]any) (*mcp.CallToolResult, error) {
		return invokeToolkit(args, "install-toolkit", toolkit.Install, true)
	}, withAnnotations(toolAnnotations{Idempotent: true, OpenWorld: true}))
	registerTool(map[string]any{"name": "update-toolkit"}, func(_ context.Context, _ *environments, args map[string]any) (*mcp.CallToolResult, error) {
		return invokeToolkit(args, "update-toolkit", toolkit.Update, true)
	}, withAnnotations(toolAnnotations{Idempotent: true, OpenWorld: true}))
	registerTool(map[string]any{"name": "delete-toolkit"}, func(_ context.Context, _ *environments, args map[string]any) (*mcp.CallToolResult, error) {
		return invokeToolkit(args, "delete-toolkit", toolkit.Delete, false)
	}, withAnnotations(toolAnnotations{Destructive: true, Idempotent: true}))
}

// invokeToolkit is the command plus clio's SkillCommandReporting: one message per agent (Error for a
// failure, Info otherwise), then the summary (Error when nothing ran and the exit code is not 0).
func invokeToolkit(args map[string]any, tool string, operation toolkit.Operation, acceptsRepo bool) (*mcp.CallToolResult, error) {
	target, err := knowledgeStringArg(args, tool, "target")
	if err != nil {
		return nil, err
	}
	repo := ""
	if acceptsRepo {
		value, err := knowledgeStringArg(args, tool, "repo")
		if err != nil {
			return nil, err
		}
		repo = knowledgeText(value)
	}
	result := toolkit.Run(toolkit.CurrentEnvironment(knowledge.ClioHome()), operation, knowledgeText(target), repo)
	envelope := creatio.CommandResult{ExitCode: result.ExitCode, Messages: []creatio.LogMessage{}}
	for _, outcome := range result.Outcomes {
		kind := "Info"
		if outcome.Status == toolkit.Failed {
			kind = "Error"
		}
		envelope.Messages = append(envelope.Messages, creatio.LogMessage{MessageType: kind, Value: outcome.Message})
	}
	summaryKind := "Info"
	if result.ExitCode != 0 && len(result.Outcomes) == 0 {
		summaryKind = "Error"
	}
	envelope.Messages = append(envelope.Messages, creatio.LogMessage{MessageType: summaryKind, Value: result.Summary})
	return structuredToolResult(envelope), nil
}
