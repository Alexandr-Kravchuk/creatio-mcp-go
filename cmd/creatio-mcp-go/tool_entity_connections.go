package main

// get-entity-connections and update-entity-connections (clio 8.1.0.142 GetEntityConnectionsTool /
// UpdateEntityConnectionsTool).

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func init() {
	registerTool(rightsContract(entityConnectionsContracts, "get-entity-connections"), invokeGetEntityConnections,
		withAnnotations(toolAnnotations{ReadOnly: true, Idempotent: true}))
	registerTool(rightsContract(entityConnectionsContracts, "update-entity-connections"), invokeUpdateEntityConnections,
		withAnnotations(toolAnnotations{Destructive: true, Idempotent: true}))
}

var getEntityConnectionsArguments = []string{"entity-schema-name", "package-name", "environment-name", "uri", "login", "password"}

func invokeGetEntityConnections(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
	bind := rightsArgs{tool: "get-entity-connections", args: args}
	name, packageName, err := entityConnectionsTargetArgs(bind)
	if err != nil {
		return nil, err
	}
	client, refusal, err := entityConnectionsTarget(envs, bind, getEntityConnectionsArguments, name, packageName, false, nil)
	if err != nil || refusal != nil {
		return refusal, err
	}
	return structuredToolResult(client.GetEntityConnections(ctx, name, packageName)), nil
}

var updateEntityConnectionsArguments = []string{"entity-schema-name", "package-name", "connections", "environment-name", "uri", "login", "password"}

func invokeUpdateEntityConnections(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
	bind := rightsArgs{tool: "update-entity-connections", args: args}
	name, packageName, err := entityConnectionsTargetArgs(bind)
	if err != nil {
		return nil, err
	}
	connections, err := entityConnectionsList(bind)
	if err != nil {
		return nil, err
	}
	client, refusal, err := entityConnectionsTarget(envs, bind, updateEntityConnectionsArguments, name, packageName, true, connections)
	if err != nil || refusal != nil {
		return refusal, err
	}
	return structuredToolResult(client.UpdateEntityConnections(ctx, name, packageName, connections)), nil
}

func entityConnectionsTargetArgs(bind rightsArgs) (string, string, error) {
	name, err := bind.str("entity-schema-name")
	if err != nil {
		return "", "", err
	}
	packageName, err := bind.str("package-name")
	if err != nil {
		return "", "", err
	}
	for _, key := range []string{"environment-name", "uri", "login", "password"} {
		if _, err := bind.str(key); err != nil {
			return "", "", err
		}
	}
	return name, packageName, nil
}

// entityConnectionsList binds connections as clio's string[]: absent or null is "not given", a null item is
// kept (and refused as an empty name), any other non-string item breaks the documented shape.
func entityConnectionsList(bind rightsArgs) ([]*string, error) {
	value, ok := bind.args["connections"]
	if !ok || value == nil {
		return nil, nil
	}
	items, isArray := value.([]any)
	if !isArray {
		return nil, fmt.Errorf("invalid-parameter-type: argument 'connections' for MCP tool '%s' must be an array. Received an incompatible JSON value.", bind.tool)
	}
	list := make([]*string, len(items))
	for index, item := range items {
		switch typed := item.(type) {
		case nil:
		case string:
			text := typed
			list[index] = &text
		default:
			return nil, fmt.Errorf("invalid-parameter-type: argument 'connections' for MCP tool '%s' contains a value that does not match the "+
				"documented shape. Received an incompatible JSON value.", bind.tool)
		}
	}
	return list, nil
}

// entityConnectionsTarget is the tool's own order: unknown names, the required arguments, then the environment.
// A resolution failure is the tool's answer, and the Safe-environment refusal is raised, as clio's tool does.
func entityConnectionsTarget(envs *environments, bind rightsArgs, valid []string, name, packageName string, update bool,
	connections []*string) (*creatio.Client, *mcp.CallToolResult, error) {
	if message := unknownArgumentError(bind.args, valid...); message != "" {
		return nil, structuredToolResult(creatio.EntityConnectionsFailure(message)), nil
	}
	if strings.TrimSpace(name) == "" {
		return nil, structuredToolResult(creatio.EntityConnectionsFailure(creatio.EntityConnectionsNameRequired)), nil
	}
	if strings.TrimSpace(packageName) == "" {
		return nil, structuredToolResult(creatio.EntityConnectionsFailure(creatio.EntityConnectionsPackageRequired)), nil
	}
	if update && connections == nil {
		return nil, structuredToolResult(creatio.EntityConnectionsFailure(creatio.EntityConnectionsListRequired)), nil
	}
	client, failure, err := envs.resolve(bind.tool, bind.args, scopeDirect)
	if err != nil {
		return nil, nil, err
	}
	if failure != nil {
		var target *environmentError
		if errors.As(failure, &target) && target.safe {
			return nil, nil, failure
		}
		return nil, structuredToolResult(creatio.EntityConnectionsFailure(failure.Error())), nil
	}
	return client, nil, nil
}
