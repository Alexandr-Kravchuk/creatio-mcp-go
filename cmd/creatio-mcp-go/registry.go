package main

import (
	"context"
	"fmt"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registeredTool is a hidden tool that lives in its own file and registers itself from init().
// New tools use this instead of adding a case to invokeHiddenTool, so they can be added in parallel
// without touching shared code.
type registeredTool struct {
	contract map[string]any
	invoke   func(ctx context.Context, client *creatio.Client, args map[string]any) (*mcp.CallToolResult, error)
}

var registeredTools = map[string]registeredTool{}

// registerTool adds a hidden tool. Its contract must carry "name", "description" and "inputSchema",
// the same shape get-tool-contract returns for the built-in hidden tools.
func registerTool(contract map[string]any, invoke func(context.Context, *creatio.Client, map[string]any) (*mcp.CallToolResult, error)) {
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
	registeredTools[name] = registeredTool{contract: contract, invoke: invoke}
}

func toolContract(name string) (map[string]any, bool) {
	if contract, ok := hiddenToolContracts[name]; ok {
		return contract, true
	}
	tool, ok := registeredTools[name]
	return tool.contract, ok
}
