package main

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/cliocontract"
	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestEveryServedToolHasClioContract keeps the hand-written fallback contracts unused: every tool this
// server answers must be one clio has, so get-tool-contract serves clio's own contract for it.
func TestEveryServedToolHasClioContract(t *testing.T) {
	contracts := cliocontract.Load().Contracts
	for _, name := range servedToolNames() {
		if _, ok := contracts[name]; !ok {
			t.Errorf("%s is served here but missing from docs/clio-inventory.json", name)
		}
	}
}

// TestNoServedToolTakesAnArgsArgument guards the {"args": {...}} unwrapping: a tool whose own argument is
// named args would be misread.
func TestNoServedToolTakesAnArgsArgument(t *testing.T) {
	for _, name := range servedToolNames() {
		if name == "clio-run" || name == "clio-run-destructive" || name == "get-tool-contract" || name == "list-apps" {
			continue // read their arguments themselves
		}
		var contract struct {
			InputSchema struct {
				Properties []struct {
					Name string `json:"name"`
				} `json:"properties"`
			} `json:"input-schema"`
		}
		if err := json.Unmarshal(cliocontract.Load().Contracts[name], &contract); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, property := range contract.InputSchema.Properties {
			if property.Name == "args" {
				// A direct call {"args": ..., "<other>": ...} is refused as ambiguous before the tool runs,
				// so such a tool (clio-run-destructive) needs its own handler, as clio-run has in main.go.
				t.Errorf("%s takes an argument named args: give it its own handler like clio-run, not registerTool", name)
			}
		}
	}
}

// TestToolsListMatchesClio pins tools/list to clio's entries: the resident tools served here, in clio's
// order, with clio's description, input schema and annotations.
func TestToolsListMatchesClio(t *testing.T) {
	session := connectTestClient(t, newMCPServer(&creatio.Client{}), mcp.NewClient(&mcp.Implementation{Name: "probe-client", Version: "test"}, nil))
	listed, err := session.ListTools(context.Background(), &mcp.ListToolsParams{})
	if err != nil {
		t.Fatal(err)
	}
	clio := map[string]cliocontract.Tool{}
	for _, tool := range cliocontract.Load().ToolsList {
		clio[tool.Name] = tool
	}
	var names []string
	for _, tool := range listed.Tools {
		names = append(names, tool.Name)
		want := clio[tool.Name]
		if tool.Description != want.Description {
			t.Errorf("%s: description differs from clio's", tool.Name)
		}
		if !sameJSON(t, tool.InputSchema, want.InputSchema) {
			t.Errorf("%s: input schema differs from clio's", tool.Name)
		}
		a := want.Annotations
		got := tool.Annotations
		if got == nil || got.ReadOnlyHint != *a.ReadOnlyHint || got.IdempotentHint != *a.IdempotentHint ||
			got.DestructiveHint == nil || *got.DestructiveHint != *a.DestructiveHint ||
			got.OpenWorldHint == nil || *got.OpenWorldHint != *a.OpenWorldHint {
			t.Errorf("%s: annotations %+v differ from clio's", tool.Name, got)
		}
	}
	if want := servedResidentNames(); !reflect.DeepEqual(names, want) {
		t.Fatalf("tools/list = %v, want %v", names, want)
	}
	for _, name := range []string{"list-apps", "list-environments", "clio-run", "get-tool-contract", "get-page", "find-app"} {
		if !contains(names, name) {
			t.Errorf("tools/list lacks %s", name)
		}
	}
}

// TestGetToolContractServesClioContracts pins the index and every per-tool contract to clio's answers.
func TestGetToolContractServesClioContracts(t *testing.T) {
	session := connectTestClient(t, newMCPServer(&creatio.Client{}), mcp.NewClient(&mcp.Implementation{Name: "probe-client", Version: "test"}, nil))
	inventory := cliocontract.Load()

	index := contractAnswer(t, session, nil)
	if index["success"] != true {
		t.Fatalf("index = %#v", index)
	}
	var wantIndex []any
	for _, entry := range inventory.Index {
		if name := cliocontract.EntryName(entry); isServedTool(name) {
			var value any
			_ = json.Unmarshal(entry, &value)
			wantIndex = append(wantIndex, value)
		}
	}
	if !reflect.DeepEqual(index["index"], wantIndex) {
		t.Fatalf("index differs from clio's entries for the served tools")
	}
	if names := indexNames(t, index); !contains(names, "list-environments") || contains(names, "get-tool-contract") {
		t.Fatalf("index names = %v", names)
	}

	for _, name := range servedToolNames() {
		answer := contractAnswer(t, session, map[string]any{"args": map[string]any{"tool-names": []any{name}}})
		tools, _ := answer["tools"].([]any)
		var want any
		_ = json.Unmarshal(inventory.Contracts[name], &want)
		if answer["success"] != true || len(tools) != 1 || !reflect.DeepEqual(tools[0], want) {
			t.Errorf("%s: contract differs from clio's", name)
		}
	}

	full := contractAnswer(t, session, map[string]any{"detail": "full"})
	var fullNames []string
	for _, tool := range full["tools"].([]any) {
		fullNames = append(fullNames, tool.(map[string]any)["name"].(string))
	}
	var wantFull []string
	for _, name := range inventory.FullDetail {
		if isServedTool(name) {
			wantFull = append(wantFull, name)
		}
	}
	if !reflect.DeepEqual(fullNames, wantFull) {
		t.Fatalf("detail=full = %v, want %v", fullNames, wantFull)
	}
}

func TestGetToolContractArgumentShapes(t *testing.T) {
	session := connectTestClient(t, newMCPServer(&creatio.Client{}), mcp.NewClient(&mcp.Implementation{Name: "probe-client", Version: "test"}, nil))
	for _, args := range []map[string]any{
		{"args": map[string]any{"tool-names": []any{"get-page"}}},
		{"tool-names": []any{"get-page"}},
		{"name": "get-page"},
		{"args": map[string]any{"tool-names": []any{"GET-PAGE"}}},
	} {
		answer := contractAnswer(t, session, args)
		tools, _ := answer["tools"].([]any)
		if len(tools) != 1 || tools[0].(map[string]any)["name"] != "get-page" {
			t.Errorf("%v = %#v", args, answer)
		}
	}
	for _, args := range []map[string]any{{}, {"args": nil}, {"args": map[string]any{}}, {"args": map[string]any{"tool-names": []any{}}}} {
		if answer := contractAnswer(t, session, args); answer["index"] == nil {
			t.Errorf("%v did not answer the index", args)
		}
	}

	missing := contractAnswer(t, session, map[string]any{"args": map[string]any{"tool-names": []any{"get-pag"}}})
	notFound, _ := missing["not-found"].([]any)
	if missing["success"] != false || len(notFound) != 1 || missing["error"] == nil {
		t.Fatalf("missing = %#v", missing)
	}
	errorValue := missing["error"].(map[string]any)
	if errorValue["code"] != "tool-not-found" || errorValue["message"] != "Tool 'get-pag' is not registered by clio MCP. "+contractDiscoveryHint ||
		!reflect.DeepEqual(errorValue["suggestions"], []any{"get-page", "create-page", "get-theme"}) {
		t.Fatalf("missing error = %#v", errorValue)
	}
	mixed := contractAnswer(t, session, map[string]any{"args": map[string]any{"tool-names": []any{"get-page", "nope"}}})
	if mixed["success"] != true || len(mixed["tools"].([]any)) != 1 || len(mixed["not-found"].([]any)) != 1 || mixed["error"] != nil {
		t.Fatalf("mixed = %#v", mixed)
	}
	empty := contractAnswer(t, session, map[string]any{"args": map[string]any{"tool-names": []any{nil}}})
	if empty["success"] != false || empty["error"].(map[string]any)["code"] != "missing-required-parameter" {
		t.Fatalf("empty name = %#v", empty)
	}
	alias := contractAnswer(t, session, map[string]any{"toolName": "x", "bogus": 1})
	if message, _ := alias["error"].(map[string]any)["message"].(string); !strings.HasPrefix(message,
		"Rename: 'toolName' -> 'tool-names'. tool-names must be an array of strings. Unknown args: 'bogus'. Valid: tool-names (array of strings).") {
		t.Fatalf("alias = %#v", alias)
	}

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "get-tool-contract",
		Arguments: map[string]any{"args": map[string]any{"tool-names": "get-page"}}})
	if err != nil || !result.IsError || result.Content[0].(*mcp.TextContent).Text !=
		"invalid-parameter-type: argument 'tool-names' for MCP tool 'get-tool-contract' must be an array. Received an incompatible JSON value." {
		t.Fatalf("string tool-names = %#v, err = %v", result, err)
	}
}

func TestLegacyStdioClientGetsFullContractsInsteadOfTheIndex(t *testing.T) {
	session := connectTestClient(t, newMCPServer(&creatio.Client{}), mcp.NewClient(&mcp.Implementation{Name: "mcp_client", Version: "1.0"}, nil))
	answer := contractAnswer(t, session, nil)
	tools, _ := answer["tools"].([]any)
	if answer["index"] != nil || len(tools) != len(servedIndex()) {
		t.Fatalf("legacy client answer has %d tools, index has %d", len(tools), len(servedIndex()))
	}
}

// TestResidentToolsAcceptClioWrappedArguments checks the shape tools/list publishes reaches the tool.
func TestResidentToolsAcceptClioWrappedArguments(t *testing.T) {
	session := connectTestClient(t, newMCPServer(&creatio.Client{}), mcp.NewClient(&mcp.Implementation{Name: "probe-client", Version: "test"}, nil))
	text := func(call *mcp.CallToolParams) (string, bool) {
		t.Helper()
		result, err := session.CallTool(context.Background(), call)
		if err != nil {
			t.Fatal(err)
		}
		return result.Content[0].(*mcp.TextContent).Text, result.IsError
	}
	// validate-page needs no Creatio; the wrapped and the flat call must answer alike.
	body := map[string]any{"body": "define(\"X\", [], function() { return {}; });"}
	flat, _ := text(&mcp.CallToolParams{Name: "validate-page", Arguments: body})
	wrapped, _ := text(&mcp.CallToolParams{Name: "validate-page", Arguments: map[string]any{"args": body}})
	if flat != wrapped {
		t.Fatalf("wrapped validate-page = %s, flat = %s", wrapped, flat)
	}
	got, isError := text(&mcp.CallToolParams{Name: "validate-page", Arguments: map[string]any{"args": body, "x": 1}})
	if !isError || got != "Tool 'validate-page' received an ambiguous argument shape: a \"args\" object AND top-level key(s) \"x\". "+
		"Send exactly one shape — wrapped {\"args\": {...}} or flat {...} — so there is no doubt which value wins." {
		t.Fatalf("ambiguous = %s", got)
	}
	got, isError = text(&mcp.CallToolParams{Name: "list-apps", Arguments: map[string]any{"foo": 1}})
	if !isError || !strings.HasPrefix(got, "Tool 'list-apps' received unknown argument(s) \"foo\". Valid arguments: \"environment-name\".") {
		t.Fatalf("list-apps unknown = %s", got)
	}
	got, isError = text(&mcp.CallToolParams{Name: "clio-run", Arguments: map[string]any{"args": map[string]any{"command": "validate-page", "args": body}}})
	if isError || got != flat {
		t.Fatalf("wrapped clio-run = %s", got)
	}
}

func servedResidentNames() []string {
	var names []string
	for _, tool := range cliocontract.Load().ToolsList {
		if isServedTool(tool.Name) {
			names = append(names, tool.Name)
		}
	}
	return names
}

func contractAnswer(t *testing.T, session *mcp.ClientSession, args map[string]any) map[string]any {
	t.Helper()
	params := &mcp.CallToolParams{Name: "get-tool-contract"}
	if args != nil {
		params.Arguments = args
	}
	result, err := session.CallTool(context.Background(), params)
	if err != nil || result.IsError {
		t.Fatalf("get-tool-contract %v = %#v, err = %v", args, result, err)
	}
	if result.StructuredContent != nil || len(result.Content) != 1 {
		t.Fatalf("get-tool-contract answers with one text block, as clio does: %#v", result)
	}
	var answer map[string]any
	if err := json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &answer); err != nil {
		t.Fatal(err)
	}
	return answer
}

func indexNames(t *testing.T, answer map[string]any) []string {
	t.Helper()
	entries, ok := answer["index"].([]any)
	if !ok {
		t.Fatalf("no index in %#v", answer)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.(map[string]any)["name"].(string))
	}
	return names
}

func sameJSON(t *testing.T, got any, want json.RawMessage) bool {
	t.Helper()
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var a, b any
	_ = json.Unmarshal(encoded, &a)
	_ = json.Unmarshal(want, &b)
	return reflect.DeepEqual(a, b)
}

func contains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}
