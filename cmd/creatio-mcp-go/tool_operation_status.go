package main

import (
	"context"
	"strings"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// compile-status and restart-status read clio's in-process registry of compile-creatio and restart
// operations that clio itself started. This server starts neither, so its registry is always empty and the
// only truthful answer is clio's not-found envelope. Nothing is sent to Creatio.
const (
	runtimeCompileNotFoundNote = "No compile-creatio operation has been recorded for this environment in the current MCP server session."
	runtimeRestartNotFoundNote = "No restart operation has been recorded for this environment in the current MCP server session."
)

// runtimeOperationStatus is the shared compile-status / restart-status envelope. clio also echoes
// environment-name; this server has no registered name, its target is CREATIO_URL.
type runtimeOperationStatus struct {
	Success bool   `json:"success"`
	Status  string `json:"status"`
	Note    string `json:"note,omitempty"`
}

func init() {
	operationID := map[string]any{"type": "string", "description": "Optional operation id from an in-progress response. Accepted for compatibility; this server records no operations."}
	registerTool(map[string]any{
		"name": "compile-status",
		"description": "Return the status of a compile-creatio operation tracked by this MCP server session. This server starts no " +
			"compilations, so it always answers success:true, status:not-found. Read the persisted result of the last build with " +
			"last-compilation-log instead. Read-only; never starts a compilation.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"operation-id": operationID}},
	}, func(_ context.Context, _ *creatio.Client, args map[string]any) (*mcp.CallToolResult, error) {
		return runtimeOperationStatusResult("compile-status", args, runtimeCompileNotFoundNote)
	})
	registerTool(map[string]any{
		"name": "restart-status",
		"description": "Return the readiness status of a restart tracked by this MCP server session. This server starts no " +
			"restarts, so it always answers success:true, status:not-found. Read-only; never restarts anything.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"operation-id": operationID}},
	}, func(_ context.Context, _ *creatio.Client, args map[string]any) (*mcp.CallToolResult, error) {
		return runtimeOperationStatusResult("restart-status", args, runtimeRestartNotFoundNote)
	})
}

// runtimeOperationStatusResult is lenient like clio (unknown keys are ignored); an environment selector is
// answered with clio's invalid-request envelope, because ignoring it would answer for another environment.
func runtimeOperationStatusResult(tool string, args map[string]any, note string) (*mcp.CallToolResult, error) {
	if _, err := optionalStringArg(args, tool, "operation-id"); err != nil {
		return nil, err
	}
	if refusal := runtimeSelectorRefusal(args); refusal != "" {
		return structuredToolResult(runtimeOperationStatus{Status: "invalid-request", Note: refusal}), nil
	}
	return structuredToolResult(runtimeOperationStatus{Success: true, Status: "not-found", Note: note}), nil
}

// runtimeSelectorRefusal extends refusesConnectionArgs with the legacy environment-name spellings, which
// these lenient tools would otherwise ignore silently.
func runtimeSelectorRefusal(args map[string]any) string {
	if refusal := refusesConnectionArgs(args); refusal != "" {
		return refusal
	}
	for key := range args {
		if environmentNameAliases[strings.ToLower(key)] {
			return environmentNameRefusal
		}
	}
	return ""
}
