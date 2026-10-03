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
	contract map[string]any
	invoke   func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error)
}

var registeredTools = map[string]registeredTool{}

// registerTool adds a hidden tool. Its contract must carry "name", "description" and "inputSchema",
// the same shape get-tool-contract returns for the built-in hidden tools.
func registerTool(contract map[string]any, invoke func(context.Context, *environments, map[string]any) (*mcp.CallToolResult, error)) {
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
	addEnvironmentNameProperty(contract)
	registeredTools[name] = registeredTool{contract: contract, invoke: invoke}
}

// toolsWithoutEnvironmentName never contact Creatio, or name their environment differently, so their
// contract does not advertise environment-name.
var toolsWithoutEnvironmentName = map[string]bool{
	"validate-page": true, "get-fsm-mode": true, "start-creatio": true, "find-empty-iis-port": true,
}

// environmentNameProperty is clio's description of the argument (McpToolDescriptions.EnvironmentName).
var environmentNameProperty = map[string]string{"type": "string", "description": "Registered clio environment name. Preferred."}

// addEnvironmentNameProperty advertises environment-name in a contract's input schema, where clio accepts it.
func addEnvironmentNameProperty(contract map[string]any) {
	name, _ := contract["name"].(string)
	schema, _ := contract["inputSchema"].(map[string]any)
	if toolsWithoutEnvironmentName[name] || schema == nil {
		return
	}
	properties, _ := schema["properties"].(map[string]any)
	if properties == nil {
		properties = map[string]any{}
		schema["properties"] = properties
	}
	if _, ok := properties["environment-name"]; !ok {
		properties["environment-name"] = environmentNameProperty
	}
}

func init() {
	for _, contract := range hiddenToolContracts {
		addEnvironmentNameProperty(contract)
	}
}

func toolContract(name string) (map[string]any, bool) {
	if contract, ok := hiddenToolContracts[name]; ok {
		return contract, true
	}
	tool, ok := registeredTools[name]
	return tool.contract, ok
}
