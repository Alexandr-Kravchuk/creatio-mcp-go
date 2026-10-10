package main

// validate-process-graph: clio's ValidateProcessGraphTool. Read-only and local once the CrtProcessBuilder
// gate passes: the graph is described inline and checked against the BPMN connection rules. Every refusal
// is a {success:false, error} answer, never a raised failure.

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const processGraphTool = "validate-process-graph"

const processGraphNoGraph = "No graph was supplied: 'nodes' is absent or empty, so there is nothing to validate. This is a missing " +
	"argument, not a finding about a process - validate-process-graph checks a graph you DESCRIBE inline " +
	"(nodes:[{name,type}], edges:[{source,target,flow-kind}]); it never reads a process out of the " +
	"environment. To inspect an existing process use describe-business-process."

// processGraphNodeAliases and processGraphEdgeAliases are the tool's legacy spellings (case-insensitive).
var processGraphNodeAliases = map[string]string{"nodetype": "type", "elementtype": "type", "node-type": "type", "nodename": "name", "id": "name"}
var processGraphEdgeAliases = map[string]string{"from": "source", "to": "target", "kind": "flow-kind", "flowkind": "flow-kind", "sourceref": "source", "targetref": "target"}

// processGraphEntry is one bound node or edge: its known string fields and the keys it does not know.
type processGraphEntry struct {
	strings map[string]*string
	results []string
	extra   []string
}

func processGraphShapeError(name string) error {
	return fmt.Errorf("invalid-parameter-type: argument '%s' for MCP tool '%s' contains a value that does not match the documented shape. Received an incompatible JSON value.", name, processGraphTool)
}

// processGraphBindList binds nodes or edges the way clio's binder does: a list of objects (or nulls) whose
// known fields are strings; anything else is an invalid-parameter-type refusal.
func processGraphBindList(args map[string]any, name string, fields []string, withResults bool) ([]*processGraphEntry, error) {
	value, present := args[name]
	if !present || value == nil {
		return nil, nil
	}
	items, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("invalid-parameter-type: argument '%s' for MCP tool '%s' must be an array. Received an incompatible JSON value.", name, processGraphTool)
	}
	known := map[string]bool{}
	for _, field := range fields {
		known[field] = true
	}
	entries := make([]*processGraphEntry, len(items))
	for index, item := range items {
		if item == nil {
			continue
		}
		object, ok := item.(map[string]any)
		if !ok {
			return nil, processGraphShapeError(name)
		}
		entry := &processGraphEntry{strings: map[string]*string{}}
		for key, raw := range object {
			switch {
			case known[key]:
				if raw == nil {
					continue
				}
				text, ok := raw.(string)
				if !ok {
					return nil, processGraphShapeError(name)
				}
				entry.strings[key] = &text
			case withResults && key == "results":
				if raw == nil {
					continue
				}
				list, ok := raw.([]any)
				if !ok {
					return nil, processGraphShapeError(name)
				}
				entry.results = make([]string, 0, len(list))
				for _, result := range list {
					text, isText := result.(string)
					if result != nil && !isText {
						return nil, processGraphShapeError(name)
					}
					entry.results = append(entry.results, text)
				}
			default:
				entry.extra = append(entry.extra, key)
			}
		}
		sort.Strings(entry.extra)
		entries[index] = entry
	}
	return entries, nil
}

// processGraphAliasError is McpToolArgumentSupport.BuildLegacyAliasError over one entry's unknown keys.
func processGraphAliasError(keys []string, aliases map[string]string, hint string) string {
	var renamed, unknown []string
	for _, key := range keys {
		if canonical, ok := aliases[strings.ToLower(key)]; ok {
			renamed = append(renamed, fmt.Sprintf("'%s' -> '%s'", describeCallerKey(key), canonical))
		} else {
			unknown = append(unknown, fmt.Sprintf("'%s'", describeCallerKey(key)))
		}
	}
	var parts []string
	if len(renamed) > 0 {
		parts = append(parts, "Rename: "+joinCallerKeys(renamed)+".")
	}
	if len(unknown) > 0 {
		parts = append(parts, "Unknown args: "+joinCallerKeys(unknown)+". "+hint)
	}
	return strings.Join(parts, " ")
}

// processGraphEntryError is the tool's BuildEntryError: a null entry or an entry with unknown keys.
func processGraphEntryError(nodes, edges []*processGraphEntry) string {
	for index, node := range nodes {
		if node == nil {
			return fmt.Sprintf(`nodes[%d] is null. Every entry must be an object, e.g. {"name":"s","type":"startEvent"}.`, index)
		}
		if message := processGraphAliasError(node.extra, processGraphNodeAliases, "Valid keys on a node: name, type."); message != "" {
			return fmt.Sprintf("nodes[%d]: %s", index, message)
		}
	}
	for index, edge := range edges {
		if edge == nil {
			return fmt.Sprintf(`edges[%d] is null. Every entry must be an object, e.g. {"source":"s","target":"e"}.`, index)
		}
		if message := processGraphAliasError(edge.extra, processGraphEdgeAliases, "Valid keys on an edge: source, target, flow-kind, condition, results."); message != "" {
			return fmt.Sprintf("edges[%d]: %s", index, message)
		}
	}
	return ""
}

// processGraphFlowKind is the tool's ParseFlowKind.
func processGraphFlowKind(value *string) (creatio.ProcessGraphFlowKind, string) {
	kind := ""
	if value != nil {
		kind = strings.ToLower(strings.TrimSpace(*value))
	}
	switch kind {
	case "", "sequence":
		return creatio.ProcessGraphSequence, ""
	case "conditional":
		return creatio.ProcessGraphConditional, ""
	case "default":
		return creatio.ProcessGraphDefault, ""
	}
	return 0, fmt.Sprintf("Unknown 'flow-kind' value '%s'. Use 'sequence' (or omit it), 'conditional' or "+
		"'default'. It is refused rather than treated as a plain flow, because the rules that "+
		"care about the difference would then answer about a graph you did not describe.", *value)
}

func processGraphRefusal(message string) *mcp.CallToolResult {
	return structuredToolResult(creatio.ProcessGraphResponse{Success: false, Error: message})
}

func init() {
	registerTool(map[string]any{"name": processGraphTool},
		func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
			environment, err := optionalStringArg(args, processGraphTool, "environment-name")
			if err != nil {
				return nil, err
			}
			nodes, err := processGraphBindList(args, "nodes", []string{"name", "type"}, false)
			if err != nil {
				return nil, err
			}
			edges, err := processGraphBindList(args, "edges", []string{"source", "target", "flow-kind", "condition"}, true)
			if err != nil {
				return nil, err
			}
			if refusal := unknownArgumentError(args, "environment-name", "nodes", "edges"); refusal != "" {
				return processGraphRefusal(refusal), nil
			}
			if len(nodes) == 0 {
				return processGraphRefusal(processGraphNoGraph), nil
			}
			if refusal := processGraphEntryError(nodes, edges); refusal != "" {
				return processGraphRefusal(refusal), nil
			}
			if _, present := args["environment-name"]; present && strings.TrimSpace(environment) == "" {
				return processGraphRefusal(processWriteMissingEnvironment), nil
			}
			client, failure, err := envs.resolve(processGraphTool, args, scopeName)
			if err != nil {
				return nil, err
			}
			if failure != nil {
				return processGraphRefusal(redacted(failure)), nil
			}
			refusal, err := client.ProcessGraphPackageGate(ctx)
			if err != nil {
				return processGraphRefusal(fmt.Sprintf("validate-process-graph failed: %s. Expected args: "+
					`{"nodes":[{"name":"s","type":"startEvent"}],"edges":[{"source":"s","target":"r","flow-kind":"sequence"}]}.`, redacted(err))), nil
			}
			if refusal != "" {
				return processGraphRefusal(refusal), nil
			}
			graphNodes := make([]*creatio.ProcessGraphNode, len(nodes))
			for index, node := range nodes {
				graphNodes[index] = &creatio.ProcessGraphNode{Name: node.strings["name"], Type: node.strings["type"]}
			}
			graphEdges := make([]*creatio.ProcessGraphEdge, len(edges))
			for index, edge := range edges {
				kind, refusal := processGraphFlowKind(edge.strings["flow-kind"])
				if refusal != "" {
					return processGraphRefusal(refusal), nil
				}
				graphEdges[index] = &creatio.ProcessGraphEdge{Source: edge.strings["source"], Target: edge.strings["target"], FlowKind: kind,
					Condition: edge.strings["condition"], Results: edge.results}
			}
			hasErrors, findings := creatio.ValidateProcessGraph(graphNodes, graphEdges)
			return structuredToolResult(creatio.ProcessGraphResponse{Success: true, HasErrors: &hasErrors, Findings: findings}), nil
		}, withAnnotations(toolAnnotations{ReadOnly: true, Destructive: false, Idempotent: true, OpenWorld: false}))
}
