package main

// get-object-rights and set-object-rights (clio 8.1.0.142 GetObjectRightsTool / SetObjectRightsTool), and the
// argument binding the four object-rights tools share.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/redact"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func init() {
	registerTool(rightsContract(objectRightsContracts, "get-object-rights"), invokeGetObjectRights,
		withAnnotations(toolAnnotations{ReadOnly: true, Idempotent: true}))
	registerTool(rightsContract(objectRightsContracts, "set-object-rights"), invokeSetObjectRights,
		withAnnotations(toolAnnotations{Destructive: true, Idempotent: true}))
}

// rightsContract is clio's get-tool-contract answer for the tool, served while the inventory does not know it.
func rightsContract(contracts map[string]string, name string) map[string]any {
	var contract map[string]any
	if err := json.Unmarshal([]byte(contracts[name]), &contract); err != nil || contract["name"] != name {
		panic(fmt.Sprintf("contract of %s: %v", name, err))
	}
	return contract
}

// rightsArgs binds a tool's arguments the way clio's binder does before the tool runs: a value of the wrong
// JSON type is clio's invalid-parameter-type failure, raised.
type rightsArgs struct {
	tool string
	args map[string]any
}

func (a rightsArgs) str(name string) (string, error) { return optionalStringArg(a.args, a.tool, name) }

// strPtr is a string argument that may be absent: nil when absent or null.
func (a rightsArgs) strPtr(name string) (*string, error) {
	if value, ok := a.args[name]; !ok || value == nil {
		return nil, nil
	}
	text, err := a.str(name)
	return &text, err
}

func (a rightsArgs) boolPtr(name string) (*bool, error) {
	value, ok := a.args[name]
	if !ok || value == nil {
		return nil, nil
	}
	flag, isBool := value.(bool)
	if !isBool {
		return nil, fmt.Errorf("invalid-parameter-type: argument '%s' for MCP tool '%s' must be a boolean. Received an incompatible JSON value.", name, a.tool)
	}
	return &flag, nil
}

func (a rightsArgs) flag(name string) (bool, error) {
	value, err := a.boolPtr(name)
	return value != nil && *value, err
}

// intPtr is an Int32 argument: a whole JSON number, or a string holding one (clio's binder reads numbers from
// strings); anything else is clio's "must be a number" failure.
func (a rightsArgs) intPtr(name string) (*int, error) {
	value, ok := a.args[name]
	if !ok || value == nil {
		return nil, nil
	}
	switch typed := value.(type) {
	case float64:
		if typed == math.Trunc(typed) && typed >= math.MinInt32 && typed <= math.MaxInt32 {
			number := int(typed)
			return &number, nil
		}
	case string:
		if number, err := strconv.ParseInt(typed, 10, 32); err == nil {
			result := int(number)
			return &result, nil
		}
	}
	return nil, fmt.Errorf("invalid-parameter-type: argument '%s' for MCP tool '%s' must be a number. Received an incompatible JSON value.", name, a.tool)
}

// rightsTarget binds environment-name, refuses unknown argument names (BuildLegacyAliasError, before any read
// or write) and resolves the environment. A resolution failure is ObjectRightsToolResponse's answer: the
// resolver's exception type with the redacted text, or the redacted Safe-environment refusal.
func rightsTarget(ctx context.Context, envs *environments, tool string, args map[string]any, valid []string) (*creatio.Client, *mcp.CallToolResult, error) {
	if _, err := optionalStringArg(args, tool, "environment-name"); err != nil {
		return nil, nil, err
	}
	if message := unknownArgumentError(args, valid...); message != "" {
		return nil, structuredToolResult(creatio.ObjectRightsFailure(message)), nil
	}
	client, failure, err := envs.resolve(tool, args, scopeName)
	if err != nil {
		return nil, nil, err
	}
	if failure != nil {
		var target *environmentError
		if errors.As(failure, &target) && target.safe {
			return nil, structuredToolResult(creatio.ObjectRightsFailure(redact.Text(failure.Error()))), nil
		}
		return nil, structuredToolResult(creatio.ObjectRightsFailure(redact.Text(resolverErrorText(failure)))), nil
	}
	return client, nil, nil
}

var getObjectRightsArguments = []string{"environment-name", "entity-schema-name", "grantee", "author", "include-connected"}

func invokeGetObjectRights(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
	bind := rightsArgs{tool: "get-object-rights", args: args}
	var input creatio.ObjectRightsGetRequest
	var err error
	if input.EntitySchemaName, err = bind.str("entity-schema-name"); err != nil {
		return nil, err
	}
	if input.Grantee, err = bind.str("grantee"); err != nil {
		return nil, err
	}
	if input.IncludeConnected, err = bind.flag("include-connected"); err != nil {
		return nil, err
	}
	if input.Author, err = bind.str("author"); err != nil {
		return nil, err
	}
	client, refusal, err := rightsTarget(ctx, envs, bind.tool, args, getObjectRightsArguments)
	if err != nil || refusal != nil {
		return refusal, err
	}
	return structuredToolResult(client.GetObjectRights(ctx, input)), nil
}

var setObjectRightsArguments = []string{"environment-name", "entity-schema-name", "grantee", "operations", "revoke",
	"enable-operation-permissions", "disable-operation-permissions", "preview"}

func invokeSetObjectRights(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
	bind := rightsArgs{tool: "set-object-rights", args: args}
	var input creatio.ObjectRightsSetRequest
	var err error
	if input.EntitySchemaName, err = bind.str("entity-schema-name"); err != nil {
		return nil, err
	}
	if input.Grantee, err = bind.strPtr("grantee"); err != nil {
		return nil, err
	}
	if input.Operations, err = bind.strPtr("operations"); err != nil {
		return nil, err
	}
	for _, field := range []struct {
		name   string
		target *bool
	}{{"revoke", &input.Revoke}, {"enable-operation-permissions", &input.Enable}, {"disable-operation-permissions", &input.Disable},
		{"preview", &input.Preview}} {
		if *field.target, err = bind.flag(field.name); err != nil {
			return nil, err
		}
	}
	client, refusal, err := rightsTarget(ctx, envs, bind.tool, args, setObjectRightsArguments)
	if err != nil || refusal != nil {
		return refusal, err
	}
	return structuredToolResult(client.SetObjectRights(ctx, input)), nil
}
