package main

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// compile-status and restart-status read the operation registry (operations.go) that compile-creatio and
// the restart tools record into, scoped to the caller's environment as clio scopes them. Nothing is sent to
// Creatio.
const (
	runtimeCompileNotFoundNote = "No compile-creatio operation has been recorded for this environment in the current MCP server session."
	runtimeRestartNotFoundNote = "No restart operation has been recorded for this environment in the current MCP server session."
)

// compileStatusResponse is clio's CompileStatusResponse; restart-status leaves the compile-only fields out.
type compileStatusResponse struct {
	Success         bool     `json:"success"`
	Status          string   `json:"status"`
	OperationID     string   `json:"operation-id,omitempty"`
	EnvironmentName string   `json:"environment-name,omitempty"`
	PackageName     string   `json:"package-name,omitempty"`
	StartedUTC      *utcTime `json:"started-utc,omitempty"`
	FinishedUTC     *utcTime `json:"finished-utc,omitempty"`
	ExitCode        *int     `json:"exit-code,omitempty"`
	MessageTail     []string `json:"message-tail,omitzero"`
	Note            string   `json:"note,omitempty"`
	ProcessName     string   `json:"process-name,omitempty"`
}

func init() {
	operationID := map[string]any{"type": "string", "description": "Optional operation id from an in-progress response. When omitted, returns the most recently started operation for this environment."}
	registerTool(map[string]any{
		"name": "compile-status",
		"description": "Returns the status of the most recent compile-creatio operation tracked for an environment, or of a specific " +
			"operation-id from a compile-creatio in-progress response. Use this after compile-creatio returns an in-progress note " +
			"to check whether the compile finished; do not re-run compile-creatio just to check. Read-only.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"operation-id": operationID}},
	}, func(_ context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
		return operationStatusResult("compile-status", envs, args, compileOperations, runtimeCompileNotFoundNote)
	})
	registerTool(map[string]any{
		"name": "restart-status",
		"description": "Returns the readiness status of the most recent restart tracked for an environment, or of a specific " +
			"operation-id from a restart-by-environment-name in-progress response. Use this after that tool returns an " +
			"in-progress note to check whether the instance finished warming up; do not re-run the restart just to check. Read-only.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"operation-id": operationID}},
	}, func(_ context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
		return operationStatusResult("restart-status", envs, args, restartOperations, runtimeRestartNotFoundNote)
	})
}

// operationStatusResult is lenient like clio (unknown keys are ignored). environment-name is echoed exactly
// as passed; an unknown name answers not-found, as in clio.
func operationStatusResult(tool string, envs *environments, args map[string]any, store *operationStore, note string) (*mcp.CallToolResult, error) {
	operationID, err := optionalStringArg(args, tool, "operation-id")
	if err != nil {
		return nil, err
	}
	name, err := optionalStringArg(args, tool, "environment-name")
	if err != nil {
		return nil, err
	}
	record, ok := store.lookup(envs.tenantKey(name), operationID)
	if !ok {
		return structuredToolResult(compileStatusResponse{Success: true, Status: "not-found", EnvironmentName: name, Note: note}), nil
	}
	environmentName := record.EnvironmentName
	if environmentName == "" {
		environmentName = name
	}
	return structuredToolResult(compileStatusResponse{
		Success: true, Status: record.Status, OperationID: record.ID, EnvironmentName: environmentName,
		PackageName: record.Details.PackageName, ProcessName: record.Details.ProcessName,
		StartedUTC: utcTimePointer(&record.StartedUTC), FinishedUTC: utcTimePointer(record.FinishedUTC),
		ExitCode: record.ExitCode, MessageTail: record.MessageTail,
	}), nil
}
