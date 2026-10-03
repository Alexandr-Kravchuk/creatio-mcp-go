package main

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registeredTool is a hidden tool that lives in its own file and registers itself from init().
// New tools use this instead of adding a case to invokeHiddenTool, so they can be added in parallel
// without touching shared code.
type registeredTool struct {
	contract    map[string]any
	invoke      func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error)
	annotations toolAnnotations
}

// toolOption adjusts a tool at registration.
type toolOption func(*registeredTool)

// withAnnotations sets the hints of clio's [McpServerTool] attribute. ReadOnly is the one that matters for
// a tool absent from clio's tools/list: clio publishes no annotations for such a tool, only its destructive
// flag (taken from the inventory, see annotationsOf), yet refuses a direct call to it unless it is read-only.
// Without the option a tool is read-only.
func withAnnotations(annotations toolAnnotations) toolOption {
	return func(tool *registeredTool) { tool.annotations = annotations }
}

var registeredTools = map[string]registeredTool{}

// registerTool adds a hidden tool. Its contract must carry "name"; "description" and "inputSchema" are
// served only for a tool clio's inventory does not know. Agents read clio's own contract, which
// get-tool-contract serves from internal/cliocontract (generated from docs/clio-inventory.json), and
// a tool clio lists as resident appears in tools/list with clio's schema and annotations.
func registerTool(contract map[string]any, invoke func(context.Context, *environments, map[string]any) (*mcp.CallToolResult, error),
	options ...toolOption) {
	name, _ := contract["name"].(string)
	if name == "" {
		panic("registerTool: contract has no name")
	}
	if _, exists := hiddenToolContracts[name]; exists {
		panic(fmt.Sprintf("registerTool: %q is already a built-in hidden tool", name))
	}
	if _, exists := registeredTools[name]; exists {
		panic(fmt.Sprintf("registerTool: %q registered twice", name))
	}
	tool := registeredTool{contract: contract, invoke: invoke, annotations: readOnlyAnnotations}
	for _, option := range options {
		option(&tool)
	}
	registeredTools[name] = tool
}

func toolContract(name string) (map[string]any, bool) {
	if contract, ok := hiddenToolContracts[name]; ok {
		return contract, true
	}
	tool, ok := registeredTools[name]
	return tool.contract, ok
}
