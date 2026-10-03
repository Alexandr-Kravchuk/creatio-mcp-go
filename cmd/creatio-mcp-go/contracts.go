package main

// clio's tool contracts, served from internal/cliocontract (generated from docs/clio-inventory.json):
// the resident tools/list entries, the get-tool-contract index and every per-tool contract. Only the
// tools this server implements are offered; everything else clio has is reported by
// scripts/compare-contracts.py as not ported yet.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/cliocontract"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// residentBuiltIns are the resident tools with their own handler in main.go; every other served tool is
// dispatched by name through invokeHiddenTool, after unwrapArgs. A tool whose own arguments include
// "args" (clio-run-destructive) must be added here with its own handler, like clio-run.
var residentBuiltIns = map[string]bool{
	"list-apps": true, "list-environments": true, "clio-run": true, "get-tool-contract": true,
}

// servedToolNames is every tool this server answers, sorted.
func servedToolNames() []string {
	names := make([]string, 0, len(residentBuiltIns)+len(hiddenToolContracts)+len(registeredTools))
	for name := range residentBuiltIns {
		names = append(names, name)
	}
	for name := range hiddenToolContracts {
		names = append(names, name)
	}
	for name := range registeredTools {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func isServedTool(name string) bool {
	if residentBuiltIns[name] {
		return true
	}
	_, ok := toolContract(name)
	return ok
}

// residentTools are clio's tools/list entries for the tools served here, in clio's order, with clio's
// description, input schema and annotations.
func residentTools() []*mcp.Tool {
	var tools []*mcp.Tool
	for _, tool := range cliocontract.Load().ToolsList {
		if !isServedTool(tool.Name) {
			continue
		}
		listed := &mcp.Tool{Name: tool.Name, Description: tool.Description, InputSchema: tool.InputSchema}
		if a := tool.Annotations; a != nil {
			// The SDK omits a false readOnlyHint/idempotentHint; MCP defines an absent hint as false.
			listed.Annotations = &mcp.ToolAnnotations{
				ReadOnlyHint: a.ReadOnlyHint != nil && *a.ReadOnlyHint, IdempotentHint: a.IdempotentHint != nil && *a.IdempotentHint,
				DestructiveHint: a.DestructiveHint, OpenWorldHint: a.OpenWorldHint,
			}
		}
		tools = append(tools, listed)
	}
	return tools
}

// residentOrder ranks tools/list entries in clio's order; the SDK itself lists tools sorted by name.
func residentOrder() map[string]int {
	order := map[string]int{}
	for position, tool := range cliocontract.Load().ToolsList {
		order[tool.Name] = position
	}
	return order
}

func orderToolsList(result mcp.Result, order map[string]int) {
	listed, ok := result.(*mcp.ListToolsResult)
	if !ok {
		return
	}
	rank := func(name string) int {
		if position, ok := order[name]; ok {
			return position
		}
		return len(order)
	}
	sort.SliceStable(listed.Tools, func(i, j int) bool { return rank(listed.Tools[i].Name) < rank(listed.Tools[j].Name) })
}

// unwrapArgs accepts clio's published shape {"args": {...}} next to the flat shape every tool here
// reads. A wrapper next to other top-level keys is refused with clio's text (McpToolErrorFilter).
func unwrapArgs(tool string, args map[string]any) (map[string]any, error) {
	wrapped, ok := args["args"]
	if !ok {
		return args, nil
	}
	if len(args) > 1 {
		extra := make([]string, 0, len(args)-1)
		for key := range args {
			if key != "args" {
				extra = append(extra, `"`+key+`"`)
			}
		}
		sort.Strings(extra)
		return nil, fmt.Errorf("Tool '%s' received an ambiguous argument shape: a \"args\" object AND top-level key(s) %s. "+
			"Send exactly one shape — wrapped {\"args\": {...}} or flat {...} — so there is no doubt which value wins.",
			tool, strings.Join(extra, ", "))
	}
	switch value := wrapped.(type) {
	case nil:
		return map[string]any{}, nil
	case map[string]any:
		return value, nil
	case string:
		return nil, fmt.Errorf("invalid-parameter-type: argument 'args' for MCP tool '%s' must be a JSON object, not a JSON string. "+
			"Send the object itself — for example {\"args\": {\"<argument-name>\": \"<value>\"}} — not a string containing JSON text. "+
			"The value was not parsed: a JSON-encoded object is refused rather than silently decoded.", tool)
	default:
		return nil, fmt.Errorf("invalid-parameter-type: argument 'args' for MCP tool '%s' must be an object. Received an incompatible JSON value.", tool)
	}
}

// isWrapped reports whether a call used clio's {"args": {...}} shape.
func isWrapped(args map[string]any) bool {
	_, ok := args["args"]
	return ok && len(args) == 1
}

// decodeCallArgs reads a tools/call argument object.
func decodeCallArgs(raw json.RawMessage) (map[string]any, error) {
	args := map[string]any{}
	if len(raw) == 0 || string(raw) == "null" {
		return args, nil
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, err
	}
	if args == nil {
		args = map[string]any{}
	}
	return args, nil
}

// unknownArgumentsError is clio's refusal for a key that is not a field of the tool's args record.
func unknownArgumentsError(tool string, valid []string, unknown []string) error {
	sort.Strings(unknown)
	quoted := func(keys []string) string {
		out := make([]string, len(keys))
		for i, key := range keys {
			out[i] = `"` + key + `"`
		}
		return strings.Join(out, ", ")
	}
	return fmt.Errorf("Tool '%s' received unknown argument(s) %s. Valid arguments: %s. "+
		"Use those names, either flat at the top level or wrapped in \"args\" ({\"args\": {...}}). "+
		"Nothing ran: the call was refused rather than executed with default values.",
		tool, quoted(unknown), quoted(valid))
}

// legacyContractClient is the CAADT 1.4.0 stdio client, which clio answers with full contracts instead
// of the compact index (ToolContractGetTool.IsLegacyStdioClient).
func legacyContractClient(session *mcp.ServerSession) bool {
	if session == nil {
		return false
	}
	params := session.InitializeParams()
	return params != nil && params.ClientInfo != nil && params.ClientInfo.Name == "mcp_client" && params.ClientInfo.Version == "1.0"
}

const contractDiscoveryHint = "Call get-tool-contract with no arguments for a compact index of every tool."

// expectedContractArgsHint is ToolContractGetTool.ExpectedArgsShapeHint.
const expectedContractArgsHint = "Expected args shape: {\"tool-names\": [\"list-pages\", ...] } or omit tool-names to list all. " +
	"Argument shapes accepted at runtime by any tool whose only parameter is an args record: wrapped {\"args\": {\"<field>\": \"<value>\"}} " +
	"(the shape tools/list publishes) and flat {\"<field>\": \"<value>\"} (normalized to the wrapped shape on arrival). " +
	"Rules: every flat key must be a real field name — a payload carrying any unknown key (even beside a valid one) is refused, " +
	"never answered with defaults; mixing an \"args\" object with extra top-level keys is refused as ambiguous; two top-level keys " +
	"differing only in casing name one argument and are refused the same way, since names are matched case-insensitively; " +
	"an args value must be a JSON object, never a string containing JSON text."

// toolNamesAliases are the spellings clio answers with a rename to tool-names (ToolContractGetTool.LegacyAliases,
// without "name", which clio accepts as a flat tool name).
var toolNamesAliases = map[string]bool{"toolNames": true, "tool_names": true, "toolName": true, "tool-name": true, "tool_name": true, "names": true}

// contractNames reads tool-names (an array) or name (a string or an array).
func contractNames(key string, value any) ([]string, error) {
	invalid := fmt.Errorf("invalid-parameter-type: argument '%s' for MCP tool 'get-tool-contract' must be an array. Received an incompatible JSON value.", key)
	switch typed := value.(type) {
	case nil:
		return nil, nil
	case string:
		if key == "tool-names" {
			return nil, invalid
		}
		return []string{typed}, nil
	case []any:
		names := make([]string, 0, len(typed))
		for _, item := range typed {
			switch text := item.(type) {
			case string:
				names = append(names, text)
			case nil:
				names = append(names, "") // refused later as an empty name, as clio does
			default:
				return nil, invalid
			}
		}
		return names, nil
	default:
		return nil, invalid
	}
}

// getToolContract answers get-tool-contract the way clio's ToolContractGetTool does, over the tools
// served here: the compact index, detail=full, or named contracts with per-name misses.
func getToolContract(args map[string]any, legacyClient bool) (*mcp.CallToolResult, error) {
	args, err := unwrapArgs("get-tool-contract", args)
	if err != nil {
		return nil, err
	}
	detail, _ := args["detail"].(string)
	if value, ok := args["detail"]; ok && value != nil && detail == "" {
		if _, isString := value.(string); !isString {
			return nil, errors.New("invalid-parameter-type: argument 'detail' for MCP tool 'get-tool-contract' must be a string. Received an incompatible JSON value.")
		}
	}
	keys := make([]string, 0, len(args))
	for key := range args {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var names, renames, unknown []string
	for _, key := range keys {
		switch {
		case key == "detail":
		case key == "tool-names" || key == "name":
			found, err := contractNames(key, args[key])
			if err != nil {
				return nil, err
			}
			names = append(names, found...)
		case toolNamesAliases[key]:
			renames = append(renames, fmt.Sprintf("Rename: '%s' -> 'tool-names'.", key))
		default:
			unknown = append(unknown, "'"+key+"'")
		}
	}
	if len(renames)+len(unknown) > 0 {
		message := ""
		if len(renames) > 0 {
			message = strings.Join(renames, " ") + " tool-names must be an array of strings. "
		}
		if len(unknown) > 0 {
			message += "Unknown args: " + strings.Join(unknown, ", ") + ". Valid: tool-names (array of strings). Omit args to list all tools. "
		}
		return contractResult(map[string]any{"success": false, "error": map[string]any{
			"code": "invalid-parameter-alias", "message": message + expectedContractArgsHint,
		}}), nil
	}
	inventory := cliocontract.Load()
	if len(names) == 0 {
		if strings.EqualFold(detail, "full") {
			return contractResult(map[string]any{"success": true, "tools": servedContracts(inventory.FullDetail)}), nil
		}
		entries := servedIndex()
		if legacyClient {
			indexNames := make([]string, len(entries))
			for i, entry := range entries {
				indexNames[i] = cliocontract.EntryName(entry)
			}
			return contractResult(map[string]any{"success": true, "tools": servedContracts(indexNames)}), nil
		}
		return contractResult(map[string]any{"success": true, "index": entries}), nil
	}
	return contractResult(namedContracts(names)), nil
}

// servedIndex is clio's index restricted to the tools served here; like clio's, it leaves
// get-tool-contract itself out.
func servedIndex() []json.RawMessage {
	entries := []json.RawMessage{}
	for _, entry := range cliocontract.Load().Index {
		if isServedTool(cliocontract.EntryName(entry)) {
			entries = append(entries, entry)
		}
	}
	return entries
}

func servedContracts(names []string) []any {
	contracts := []any{}
	for _, name := range names {
		if contract, ok := contractFor(name); ok {
			contracts = append(contracts, contract)
		}
	}
	return contracts
}

// contractFor is clio's contract of a served tool, or the hand-written one for a tool clio's
// inventory does not know (a test keeps that set empty).
func contractFor(name string) (any, bool) {
	if !isServedTool(name) {
		return nil, false
	}
	if contract, ok := cliocontract.Load().Contracts[name]; ok {
		return contract, true
	}
	contract, ok := toolContract(name)
	return contract, ok
}

func namedContracts(requested []string) map[string]any {
	seen := map[string]bool{}
	found, missing := []any{}, []any{}
	for index, name := range requested {
		name = strings.TrimSpace(name)
		if name == "" {
			return map[string]any{"success": false, "error": map[string]any{
				"code": "missing-required-parameter", "message": "tool-names must contain non-empty tool names.",
				"field-errors": []any{map[string]any{"field": fmt.Sprintf("tool-names[%d]", index),
					"code": "missing-required-parameter", "message": "Provide a non-empty tool name."}},
			}}
		}
		if seen[strings.ToLower(name)] {
			continue
		}
		seen[strings.ToLower(name)] = true
		if contract, ok := contractFor(canonicalToolName(name)); ok {
			found = append(found, contract)
			continue
		}
		missing = append(missing, map[string]any{"name": name, "error": map[string]any{
			"code":        "tool-not-found",
			"message":     fmt.Sprintf("Tool '%s' is not registered by clio MCP. %s", name, contractDiscoveryHint),
			"suggestions": suggestToolNames(name, servedToolNames()),
		}})
	}
	answer := map[string]any{"success": len(found) > 0}
	if len(found) > 0 {
		answer["tools"] = found
	} else {
		answer["error"] = missing[0].(map[string]any)["error"]
	}
	if len(missing) > 0 {
		answer["not-found"] = missing
	}
	return answer
}

// canonicalToolName matches a requested name case-insensitively, as clio's contract catalog does.
func canonicalToolName(name string) string {
	for _, served := range servedToolNames() {
		if strings.EqualFold(served, name) {
			return served
		}
	}
	return name
}

// suggestToolNames is clio's McpToolArgumentSupport.SuggestToolNames: a set-/update- synonym first, then
// the smallest case-insensitive edit distance, then the name; three at most.
func suggestToolNames(requested string, candidates []string) []string {
	ranking := requested
	if len(ranking) > 64 {
		ranking = ranking[:64]
	}
	synonym := ""
	lower := strings.ToLower(ranking)
	if strings.HasPrefix(lower, "set-") {
		synonym = "update-" + ranking[4:]
	} else if strings.HasPrefix(lower, "update-") {
		synonym = "set-" + ranking[7:]
	}
	pool := []string{}
	for _, name := range candidates {
		if strings.TrimSpace(name) != "" && !strings.EqualFold(name, requested) {
			pool = append(pool, name)
		}
	}
	sort.SliceStable(pool, func(i, j int) bool {
		si, sj := strings.EqualFold(pool[i], synonym), strings.EqualFold(pool[j], synonym)
		if si != sj {
			return si
		}
		di, dj := levenshtein(ranking, pool[i]), levenshtein(ranking, pool[j])
		if di != dj {
			return di < dj
		}
		return strings.ToLower(pool[i]) < strings.ToLower(pool[j])
	})
	if len(pool) > 3 {
		pool = pool[:3]
	}
	return pool
}

func levenshtein(a, b string) int {
	left, right := []rune(strings.ToLower(a)), []rune(strings.ToLower(b))
	previous := make([]int, len(right)+1)
	for j := range previous {
		previous[j] = j
	}
	for i := 1; i <= len(left); i++ {
		current := make([]int, len(right)+1)
		current[0] = i
		for j := 1; j <= len(right); j++ {
			cost := 1
			if left[i-1] == right[j-1] {
				cost = 0
			}
			current[j] = min(previous[j]+1, current[j-1]+1, previous[j-1]+cost)
		}
		previous = current
	}
	return previous[len(right)]
}

// contractResult answers with one text block, as clio does for get-tool-contract.
func contractResult(value any) *mcp.CallToolResult {
	encoded, err := json.Marshal(value)
	if err != nil {
		return toolError(err)
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(encoded)}}}
}

// residentHandler serves a resident tool through the same dispatch as a direct call by name; the
// receiving middleware normally answers these calls before the SDK reaches this handler.
func residentHandler(envs *environments, hostTools hiddenToolServices) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, err := decodeCallArgs(req.Params.Arguments)
		if err != nil {
			return toolError(err), nil
		}
		return callHiddenTool(ctx, envs, hostTools, req.Params.Name, args, req.Session, req.Params.GetProgressToken()), nil
	}
}

// callHiddenTool is a direct call of a served tool by its own name, in either of clio's argument shapes.
func callHiddenTool(ctx context.Context, envs *environments, hostTools hiddenToolServices, name string, args map[string]any,
	session *mcp.ServerSession, token any) *mcp.CallToolResult {
	args, err := unwrapArgs(name, args)
	if err != nil {
		return toolError(err)
	}
	result, err := invokeHiddenTool(ctx, envs, hostTools, name, args, progressReporter(ctx, session, token))
	if err != nil {
		// clio's McpToolErrorFilter reports a failed direct call with this prefix.
		return toolError(clioFailure("MCP tool '"+name+"' failed: ", err))
	}
	return result
}
