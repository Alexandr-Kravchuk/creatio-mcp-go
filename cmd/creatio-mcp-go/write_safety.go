package main

// Write safety the way clio enforces it (clio master 914dab286, measured against clio 8.1.0.134):
//
//   - Every tool carries clio's four MCP hints (ReadOnly, Destructive, Idempotent, OpenWorld). clio publishes
//     all four for its tools/list entries and the destructive flag in its get-tool-contract index; both are
//     served from internal/cliocontract. ReadOnly of a hidden tool, which clio does not publish, comes from
//     the tool's registration (withAnnotations, copied from clio's [McpServerTool] attribute).
//   - clio-run and clio-run-destructive are one executor under two names. Both are annotated destructive, so
//     the host asks before running them, and both run ANY tool, read or write: clio removed the old
//     "use the other executor" refusal (ClioRunExecutor.RunAsync) because agents looped on it. Each answer
//     carries _meta "clio-run": {dispatchedTool, destructive} as the audit trail of what actually ran.
//   - A tool absent from tools/list that is NOT read-only, called directly by its raw name, is not run: the
//     host cannot prompt for a tool it never saw, so the call answers clio's confirmation-required outcome
//     (McpDurableCallToolHandler), which points at clio-run. The gate keys on readOnlyHint, not on
//     destructiveHint, so additive writes are gated too (clio issue #953).
//   - Resident tools are never gated: the host prompts for them from their own annotations.

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/cliocontract"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// toolAnnotations are clio's [McpServerTool] hints.
type toolAnnotations struct {
	ReadOnly    bool
	Destructive bool
	Idempotent  bool
	OpenWorld   bool
}

// readOnlyAnnotations is what clio declares for a read tool (ReadOnly, Idempotent), and the default of
// registerTool.
var readOnlyAnnotations = toolAnnotations{ReadOnly: true, Idempotent: true}

// localWriteAnnotations are clio's hints for a read of Creatio that can also write a local file
// (get-schema, get-theme, get-sql-schema and the like: an optional output path) or change local state
// (start-creatio): not read-only, not destructive, idempotent.
var localWriteAnnotations = toolAnnotations{Idempotent: true}

// builtInToolAnnotations covers the built-in hidden tools of main.go whose clio counterpart is not read-only.
var builtInToolAnnotations = map[string]toolAnnotations{
	"get-sql-schema": localWriteAnnotations,
	"start-creatio":  localWriteAnnotations,
}

// clioToolFacts is what clio publishes about its tools (internal/cliocontract): the four hints of every
// tool in its tools/list, and the destructive flag of every tool in its get-tool-contract index.
type clioToolFacts struct {
	resident    map[string]toolAnnotations
	destructive map[string]bool
}

var clioFacts = sync.OnceValue(func() clioToolFacts {
	facts := clioToolFacts{resident: map[string]toolAnnotations{}, destructive: map[string]bool{}}
	inventory := cliocontract.Load()
	flag := func(value *bool) bool { return value != nil && *value }
	for _, tool := range inventory.ToolsList {
		var annotations toolAnnotations
		if a := tool.Annotations; a != nil {
			annotations = toolAnnotations{ReadOnly: flag(a.ReadOnlyHint), Destructive: flag(a.DestructiveHint),
				Idempotent: flag(a.IdempotentHint), OpenWorld: flag(a.OpenWorldHint)}
		}
		facts.resident[tool.Name] = annotations
	}
	for _, raw := range inventory.Index {
		var entry struct {
			Name        string `json:"name"`
			Destructive *bool  `json:"destructive"`
		}
		if json.Unmarshal(raw, &entry) == nil && entry.Destructive != nil {
			facts.destructive[entry.Name] = *entry.Destructive
		}
	}
	return facts
})

// isClioResident reports whether clio lists the tool in tools/list. clio never gates a direct call to it.
func isClioResident(name string) bool {
	_, ok := clioFacts().resident[name]
	return ok
}

// annotationsOf returns a tool's hints: clio's own for a tool in its tools/list; otherwise the registered
// ones (clio publishes no hints for a hidden tool), with the destructive flag of clio's index.
func annotationsOf(name string) toolAnnotations {
	facts := clioFacts()
	if annotations, ok := facts.resident[name]; ok {
		return annotations
	}
	annotations := readOnlyAnnotations
	if tool, ok := registeredTools[name]; ok {
		annotations = tool.annotations
	} else if builtIn, ok := builtInToolAnnotations[name]; ok {
		annotations = builtIn
	}
	if destructive, ok := facts.destructive[name]; ok {
		annotations.Destructive = destructive
	}
	return annotations
}

// requiresConfirmation reports whether a direct call by raw name must answer confirmation-required.
func requiresConfirmation(name string) bool {
	return !isClioResident(name) && !annotationsOf(name).ReadOnly
}

// confirmationRequired is clio's McpDurableCallToolHandler.ConfirmationRequiredResult. It lists the
// argument names the caller sent, never their values, so a credential is not echoed back.
func confirmationRequired(name string, args map[string]any) *mcp.CallToolResult {
	destructive := annotationsOf(name).Destructive
	reason := "writes durable state to the target (additive)"
	if destructive {
		reason = "can overwrite or delete existing state"
	}
	text := "Tool '" + name + "' " + reason + " and was NOT executed: it is not advertised in tools/list, " +
		"so the host cannot show its own confirmation prompt. To proceed, call the advertised executor " +
		"`clio-run` with {\"command\":\"" + name + "\",\"args\":{…}} (re-supply your " +
		"own arguments under `args`) — the host gates that call."
	payload := confirmationPayload{
		Code: "confirmation-required", CorrelationID: newCorrelationID(), RequestedName: name, CanonicalName: name,
		Destructive: destructive, WriteCapable: true,
		Retry: confirmationRetry{Tool: "clio-run", Arguments: map[string]string{"command": name}},
	}
	for key := range args {
		payload.ArgumentNames = append(payload.ArgumentNames, key)
	}
	sort.Strings(payload.ArgumentNames)
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: text}}, StructuredContent: payload}
}

type confirmationPayload struct {
	Code          string            `json:"code"`
	CorrelationID string            `json:"correlation-id"`
	RequestedName string            `json:"requested-name"`
	CanonicalName string            `json:"canonical-name"`
	Destructive   bool              `json:"destructive"`
	WriteCapable  bool              `json:"write-capable"`
	ArgumentNames []string          `json:"argument-names,omitempty"`
	Retry         confirmationRetry `json:"retry"`
}

type confirmationRetry struct {
	Tool      string            `json:"tool"`
	Arguments map[string]string `json:"arguments"`
}

// attachDispatchAudit is clio's ClioRunExecutor.AttachDispatchAudit: the result's _meta records which tool
// the executor ran and whether it is destructive, outside the content the model reads.
func attachDispatchAudit(result *mcp.CallToolResult, name string) {
	if result == nil {
		return
	}
	if result.Meta == nil {
		result.Meta = mcp.Meta{}
	}
	result.Meta["clio-run"] = map[string]any{"dispatchedTool": name, "destructive": annotationsOf(name).Destructive}
}

// clioRunTarget resolves the executor's command the way clio's ClioRunExecutor does: a missing command is
// recovered from the wrapped shape {"args":{"command":"x","args":{...}}} or {"args":{"command":"x",...}};
// the executors themselves cannot be a target.
func clioRunTarget(input clioRunArgs) (string, map[string]any, error) {
	command, args := strings.TrimSpace(input.Command), input.Args
	if command == "" {
		if wrapped, ok := args["command"].(string); ok && strings.TrimSpace(wrapped) != "" {
			command = strings.TrimSpace(wrapped)
			if inner, ok := args["args"].(map[string]any); ok {
				args = inner
			} else {
				flat := make(map[string]any, len(args))
				for key, value := range args {
					if key != "command" {
						flat[key] = value
					}
				}
				args = flat
			}
		}
	}
	if command == "" {
		return "", nil, fmt.Errorf("Error: 'command' is required — the target clio MCP tool name (kebab-case). " +
			"Call shape: {\"command\":\"<tool>\",\"args\":{...}}.")
	}
	if strings.EqualFold(command, "clio-run") || strings.EqualFold(command, "clio-run-destructive") {
		return "", nil, fmt.Errorf("Error: '%s' cannot be a clio-run target (self/cross-dispatch is not allowed). "+
			"Pass a concrete clio MCP tool name as 'command'.", command)
	}
	return command, args, nil
}

// newCorrelationID is a random GUID in .NET's default "D" format, as clio stamps on its outcomes.
func newCorrelationID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// newOperationID is a random GUID in .NET's "N" format (32 hex digits), clio's operation-id.
func newOperationID() string {
	return strings.ReplaceAll(newCorrelationID(), "-", "")
}
