package main

// The code-generation tools of clio 8.1.0.142 (T16c): generate-source-code (GenerateSourceCodeTool over
// GenerateSourceCodeCommand) and add-item-model (AddItemModelTool over AddItemCommand and ModelBuilder). Both
// bind their argument record as System.Text.Json does and answer with clio's command envelope. clio runs them
// to the end however long they take (neither is in the worker cohort that the parent kills at 120 s), so the
// call waits for the work and reports progress meanwhile.
// clio: generate-source-code [McpServerTool(ReadOnly = false, Destructive = false, Idempotent = true,
// OpenWorld = false)]; add-item-model [McpServerTool(ReadOnly = false, Destructive = true, Idempotent = false,
// OpenWorld = false)].

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/workspace"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// codegenNoDeadline lets the call wait for the work, as clio's in-process tool call does.
const codegenNoDeadline = 100 * 365 * 24 * time.Hour

// generateSourceCodeAliases is GenerateSourceCodeTool.ArgsFieldAliases (Ordinal).
var generateSourceCodeAliases = t21aWithAliases(map[string]string{
	"timeOut": "timeout", "time_out": "timeout", "timeoutMs": "timeout", "timeout-ms": "timeout",
})

func init() {
	registerTool(map[string]any{"name": "generate-source-code"}, invokeGenerateSourceCode,
		withAnnotations(toolAnnotations{ReadOnly: false, Destructive: false, Idempotent: true, OpenWorld: false}))
	registerTool(map[string]any{"name": "add-item-model"}, invokeAddItemModel,
		withAnnotations(toolAnnotations{ReadOnly: false, Destructive: true, Idempotent: false, OpenWorld: false}))
}

// codegenResolve is the command resolver both tools go through: a blank name is clio's missing-target refusal
// (the active environment is no fallback for an MCP command), and the bound name is resolved under its
// canonical key, so a case variant the binder accepted ("Environment-Name") resolves as clio resolves it.
func codegenResolve(envs *environments, tool string, bound clioBound) (client *creatio.Client, refusal, err error) {
	name, _ := bound.str("environment-name")
	if strings.TrimSpace(name) == "" {
		return nil, &environmentError{message: missingTargetMessage}, nil
	}
	return envs.resolve(tool, map[string]any{"environment-name": name}, scopeName)
}

// codegenResult is the answer with the text clio's serializer writes ('<' and the apostrophe as they are).
func codegenResult(result creatio.CommandResult) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: creatio.CommandResultText(result)}},
		StructuredContent: result}
}

// codegenFlag is a bool? member: absent or null is false; any other kind is the binder's refusal.
func codegenFlag(tool string, bound clioBound, name string) (bool, error) {
	value, present := bound.values[name]
	if !present || value == nil {
		return false, nil
	}
	flag, ok := value.(bool)
	if !ok {
		return false, dataWriteKindBindingError(tool, name, "a boolean")
	}
	return flag, nil
}

func invokeGenerateSourceCode(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
	const tool = "generate-source-code"
	bound, err := clioBind(tool, args, []clioMember{
		{name: "environment-name", kind: clioString},
		{name: "modified", kind: clioAny}, {name: "required", kind: clioAny}, {name: "background", kind: clioAny},
		{name: "timeout", kind: clioInt},
	})
	if err != nil {
		return nil, err
	}
	var request creatio.GenerateSourceCodeRequest
	if request.Modified, err = codegenFlag(tool, bound, "modified"); err != nil {
		return nil, err
	}
	if request.Required, err = codegenFlag(tool, bound, "required"); err != nil {
		return nil, err
	}
	if request.Background, err = codegenFlag(tool, bound, "background"); err != nil {
		return nil, err
	}
	extension := make(map[string]any, len(bound.extension))
	for key, value := range bound.extension {
		extension[key] = value
	}
	if message := t21aAliasError(extension, nil, generateSourceCodeAliases, false, ".",
		"Valid fields: environment-name, modified, required, background, timeout."); message != "" {
		return codegenResult(creatio.CommandFailure(tool + " arguments are invalid: " + message + " Nothing was generated.")), nil
	}
	request.TimeoutMilliseconds = creatio.GenerateSourceCodeDefaultTimeout
	if timeout := bound.integer("timeout"); timeout != nil {
		if *timeout <= 0 || *timeout > creatio.GenerateSourceCodeMaxTimeout {
			return codegenResult(creatio.CommandFailure(fmt.Sprintf(
				"%s 'timeout' must be between 1 and %d milliseconds (%d minutes).", tool,
				creatio.GenerateSourceCodeMaxTimeout, creatio.GenerateSourceCodeMaxTimeout/60_000))), nil
		}
		request.TimeoutMilliseconds = *timeout
	}
	client, refusal, err := codegenResolve(envs, tool, bound)
	if err != nil {
		return nil, err
	}
	if refusal != nil {
		// resolverFailureEnvelope's envelope, written with clio's serializer.
		exitCode := 1
		var target *environmentError
		if errors.As(refusal, &target) {
			exitCode = target.exitCode()
		}
		return codegenResult(creatio.NewCommandResult(exitCode, "Error", resolverErrorText(refusal))), nil
	}
	result, _, err := runLongOperation(ctx, tool, codegenNoDeadline,
		func(workCtx context.Context, stage func(string)) creatio.CommandResult {
			stage("Generating source code")
			return client.GenerateSourceCode(workCtx, request)
		})
	if err != nil {
		return nil, err
	}
	return codegenResult(result), nil
}

func invokeAddItemModel(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
	const tool = "add-item-model"
	// AddItemModelArgs has no overflow bag: an unknown key is dropped by the binder.
	bound, err := clioBind(tool, args, []clioMember{
		{name: "namespace", kind: clioString}, {name: "folder", kind: clioString}, {name: "environment-name", kind: clioString},
	})
	if err != nil {
		return nil, err
	}
	folder, _ := bound.str("folder")
	if message := workspace.ValidateModelFolder(folder); message != "" {
		return codegenResult(creatio.CommandFailure(message)), nil
	}
	destination, message := workspace.PrepareModelFolder(folder)
	if message != "" {
		return codegenResult(creatio.CommandFailure(message)), nil
	}
	// The command resolver raises a resolution failure out of the tool.
	client, refusal, err := codegenResolve(envs, tool, bound)
	if err != nil {
		return nil, err
	}
	if refusal != nil {
		return nil, refusal
	}
	namespace, _ := bound.str("namespace")
	result, _, err := runLongOperation(ctx, tool, codegenNoDeadline,
		func(workCtx context.Context, stage func(string)) creatio.CommandResult {
			stage("Generating models")
			return workspace.GenerateModels(workCtx, client, namespace, destination)
		})
	if err != nil {
		return nil, err
	}
	return codegenResult(result), nil
}
