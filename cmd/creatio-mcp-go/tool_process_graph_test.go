package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func processGraphCallAll(t *testing.T, session *mcp.ClientSession, args map[string]any) map[string]any {
	t.Helper()
	var first map[string]any
	for _, call := range callPaths(processGraphTool, args) {
		result, err := session.CallTool(context.Background(), call)
		if err != nil || result.IsError {
			t.Fatalf("%s: %#v %v", call.Name, result, err)
		}
		var answer map[string]any
		_ = json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &answer)
		if first == nil {
			first = answer
		} else if encoded, again := processGraphJSON(first), processGraphJSON(answer); encoded != again {
			t.Fatalf("paths disagree: %s vs %s", encoded, again)
		}
	}
	return first
}

func processGraphJSON(value any) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func TestValidateProcessGraphMCP(t *testing.T) {
	if requiresConfirmation(processGraphTool) {
		t.Fatal("validate-process-graph is read-only in clio and must be callable by its raw name")
	}
	session, _ := processWriteTestServer(t, "99.0.0.0", func(path, body string) string { return `{}` })
	answer := processGraphCallAll(t, session, map[string]any{
		"nodes": []any{map[string]any{"name": "s", "type": "startEvent"}, map[string]any{"name": "g", "type": "exclusiveGateway"},
			map[string]any{"name": "a", "type": "readDataUserTask"}, map[string]any{"name": "e", "type": "endEvent"}},
		"edges": []any{map[string]any{"source": "s", "target": "g"}, map[string]any{"source": "g", "target": "a", "flow-kind": "Conditional", "condition": "1 > 0"},
			map[string]any{"source": "g", "target": "e", "flow-kind": " default "}, map[string]any{"source": "a", "target": "e", "results": nil}},
	})
	if answer["success"] != true || answer["has-errors"] != false || len(answer["findings"].([]any)) != 0 {
		t.Fatalf("%v", answer)
	}
	answer = processGraphCallAll(t, session, map[string]any{"nodes": []any{map[string]any{"name": "s", "type": "startEvent"}}})
	findings := answer["findings"].([]any)
	first := findings[0].(map[string]any)
	if answer["has-errors"] != true || first["rule-id"] != "R1" || first["severity"] != "error" || first["node-name"] != "s" || first["source"] != nil {
		t.Fatalf("%v", answer)
	}
	refusals := []struct {
		args map[string]any
		want string
	}{
		{map[string]any{}, processGraphNoGraph},
		{map[string]any{"nodes": []any{}}, processGraphNoGraph},
		{map[string]any{"nodes": []any{map[string]any{"name": "s"}}, "graph": 1}, "Unknown args: 'graph'. Valid: environment-name, nodes, edges."},
		{map[string]any{"nodes": []any{map[string]any{"id": "s", "nodeType": "startEvent", "foo": 1}}}, "nodes[0]: Rename: 'id' -> 'name', 'nodeType' -> 'type'. Unknown args: 'foo'. Valid keys on a node: name, type."},
		{map[string]any{"nodes": []any{map[string]any{"name": "s"}}, "edges": []any{nil}}, `edges[0] is null. Every entry must be an object, e.g. {"source":"s","target":"e"}.`},
		{map[string]any{"nodes": []any{map[string]any{"name": "s"}}, "edges": []any{map[string]any{"from": "s", "to": "e", "bad": 1}}}, "edges[0]: Rename: 'from' -> 'source', 'to' -> 'target'. Unknown args: 'bad'. Valid keys on an edge: source, target, flow-kind, condition, results."},
		{map[string]any{"nodes": []any{map[string]any{"name": "s"}}, "edges": []any{map[string]any{"source": "s", "target": "e", "flow-kind": "maybe"}}}, "Unknown 'flow-kind' value 'maybe'. Use 'sequence' (or omit it), 'conditional' or 'default'. It is refused rather than treated as a plain flow, because the rules that care about the difference would then answer about a graph you did not describe."},
		{map[string]any{"nodes": []any{map[string]any{"name": "s"}}, "environment-name": ""}, "environment-name is required and cannot be empty."},
	}
	for _, item := range refusals {
		answer := processGraphCallAll(t, session, item.args)
		if answer["success"] != false || answer["error"] != item.want || answer["findings"] != nil || answer["has-errors"] != nil {
			t.Errorf("%v: %v", item.args, answer)
		}
	}
	for _, args := range []map[string]any{{"nodes": "x"}, {"nodes": []any{5}}, {"nodes": []any{map[string]any{"name": 5}}}, {"nodes": []any{map[string]any{"name": "s"}}, "edges": []any{map[string]any{"results": []any{1}}}}} {
		result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: processGraphTool, Arguments: args})
		if err != nil || !result.IsError || !strings.Contains(result.Content[0].(*mcp.TextContent).Text, "invalid-parameter-type: argument '") {
			t.Errorf("%v: %#v %v", args, result, err)
		}
	}
}

func TestValidateProcessGraphMCPRefusesWithoutCrtProcessBuilder(t *testing.T) {
	session, _ := processWriteTestServer(t, "", func(path, body string) string { return `{}` })
	answer := processGraphCallAll(t, session, map[string]any{"nodes": []any{map[string]any{"name": "s", "type": "startEvent"}},
		"edges": []any{map[string]any{"source": "s", "target": "e", "flow-kind": "maybe"}}})
	if answer["success"] != false || !strings.HasPrefix(answer["error"].(string), "To use this command, you need to install the CrtProcessBuilder package. Install the package") {
		t.Fatalf("%v", answer)
	}
}
