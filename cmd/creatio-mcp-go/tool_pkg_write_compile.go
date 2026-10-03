package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var pkgWriteCompileSpec = themeWriteArgSpec{tool: "compile-creatio",
	strings:              []string{"environment-name", "package-name", "process-name"},
	aliases:              map[string]string{"packageName": "package-name", "package_name": "package-name", "package": "package-name", "processName": "process-name", "process_name": "process-name", "process": "process-name"},
	envAliasesIgnoreCase: true,
	hint:                 "Valid: environment-name, package-name, process-name; omit both scope arguments for a full compile.",
}

func init() {
	registerTool(map[string]any{"name": "compile-creatio"}, invokePkgWriteCompile,
		withAnnotations(toolAnnotations{Destructive: true}))
}

func invokePkgWriteCompile(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
	bound, err := pkgWriteCompileSpec.bind(args)
	if err != nil {
		return nil, err
	}
	if refusal := pkgWriteCompileSpec.aliasError(bound); refusal != "" {
		return structuredToolResult(creatio.CommandFailure(refusal + " Nothing was compiled.")), nil
	}
	packageName, processName := bound.str("package-name"), bound.str("process-name")
	if strings.Contains(packageName, ",") {
		return structuredToolResult(creatio.CommandFailure("`package-name` must contain exactly one package name. Comma-separated package lists are not supported by `compile-creatio`.")), nil
	}
	if raw, ok := bound.raw["package-name"]; ok && raw != nil && strings.TrimSpace(packageName) == "" {
		return structuredToolResult(creatio.CommandFailure("`package-name` is empty. Pass the package name, or omit the argument for a full compilation.")), nil
	}
	if raw, ok := bound.raw["process-name"]; ok && raw != nil && strings.TrimSpace(processName) == "" {
		return structuredToolResult(creatio.CommandFailure("`process-name` is empty. Pass the business process code, or omit the argument for a full compilation.")), nil
	}
	packageName, processName = strings.TrimSpace(packageName), strings.TrimSpace(processName)
	if packageName == "" && processName == "" {
		for _, key := range []string{"package-name", "process-name"} {
			if value, ok := bound.raw[key]; ok && value == nil {
				return structuredToolResult(creatio.CommandFailure(fmt.Sprintf("`%s` is null. Pass a value, or omit both scope arguments for a full compilation. Nothing was compiled.", key))), nil
			}
		}
	}
	if packageName != "" && processName != "" {
		return structuredToolResult(creatio.CommandFailure("Provide only one of `package-name` or `process-name`: `process-name` already compiles the package the process is in.")), nil
	}
	name := bound.str("environment-name")
	if record, ok := compileOperations.lookup(envs.tenantKey(name), ""); ok && record.Status == "running" {
		return structuredToolResult(creatio.CommandFailure(fmt.Sprintf("THIS clio process is already running a compilation for '%s', so this request was not started. That is a local guard, not a server-wide one - a different clio process is not covered by it. What actually serializes concurrent builds is the Creatio platform, which rejects a second one on the node. Poll compile-status for the running operation and wait for it to finish before compiling again.", name))), nil
	}
	op := compileOperations.begin(envs.tenantKey(name), name, operationDetails{PackageName: packageName, ProcessName: processName})
	result, finished, err := runLongOperation(ctx, "compile-creatio", 0, func(ctx context.Context, stage func(string)) creatio.CommandResult {
		stage("Compiling")
		client, failure, resolveErr := envs.resolve("compile-creatio", bound.raw, scopeName)
		var answer creatio.CommandResult
		switch {
		case resolveErr != nil:
			answer = creatio.CommandFailure(resolveErr.Error())
		case failure != nil:
			answer = creatio.CommandFailure(redacted(failure))
		default:
			answer = client.Compile(ctx, name, packageName, processName)
		}
		compileOperations.finish(op.ID, answer.ExitCode, answer.Messages)
		return answer
	})
	if err != nil {
		return nil, err
	}
	if !finished {
		return structuredToolResult(creatio.CommandInfo(fmt.Sprintf("Compilation for '%s' (operation-id '%s') was accepted and is still running server-side (MCP response deadline reached). Poll compile-status with the same environment-name (or this operation-id) for its current state — do NOT retry compile-creatio: the Creatio core serializes compilation, so a second concurrent compile for the same environment is rejected, not queued. Do NOT issue other environment-bound calls (e.g. restart-by-environment-name) for this environment until compile-status reports completion. A full compilation typically takes 3-20 minutes; a process-name compile a few.", name, op.ID))), nil
	}
	return structuredToolResult(result), nil
}
