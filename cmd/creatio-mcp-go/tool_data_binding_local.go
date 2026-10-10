package main

import (
	"context"
	"fmt"
	"math"
	"strings"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// dataWriteLocalBindingArgs binds the string arguments of clio's CreateDataBindingArgs, AddDataBindingRowArgs
// and RemoveDataBindingRowArgs; keys outside the record are ignored, as clio's binder ignores them.
func dataWriteLocalBindingArgs(tool string, args map[string]any, keys ...string) (creatio.DataWriteLocalBindingRequest, error) {
	var request creatio.DataWriteLocalBindingRequest
	targets := map[string]*string{"package-name": &request.PackageName, "schema-name": &request.SchemaName, "binding-name": &request.BindingName,
		"values": &request.ValuesJSON, "localizations": &request.LocalizationsJSON, "key-value": &request.KeyValue, "workspace-path": &request.WorkspacePath}
	for _, key := range keys {
		if key == "install-type" {
			if value, ok := args[key]; ok && value != nil {
				number, isNumber := value.(float64)
				if !isNumber || number != math.Trunc(number) || math.Abs(number) > math.MaxInt32 {
					return request, fmt.Errorf("invalid-parameter-type: argument 'install-type' for MCP tool '%s' must be a number. Received an incompatible JSON value.", tool)
				}
				request.InstallType = int(number)
			}
			continue
		}
		value, err := optionalStringArg(args, tool, key)
		if err != nil {
			return request, err
		}
		*targets[key] = value
	}
	return request, nil
}

func init() {
	annotations := withAnnotations(toolAnnotations{ReadOnly: false, Destructive: true, Idempotent: false, OpenWorld: false})

	// clio's CreateDataBindingTool: a schema with a built-in offline template runs without an environment;
	// any other schema resolves the environment (failure reported in the command envelope) for its runtime schema.
	registerTool(map[string]any{"name": "create-data-binding"}, func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
		const tool = "create-data-binding"
		request, err := dataWriteLocalBindingArgs(tool, args, "package-name", "schema-name", "workspace-path", "binding-name", "install-type", "values", "localizations")
		if err != nil {
			return nil, err
		}
		environmentName, err := optionalStringArg(args, tool, "environment-name")
		if err != nil {
			return nil, err
		}
		if request.WorkspacePath, err = creatio.DataWriteValidateWorkspacePath(request.WorkspacePath); err != nil {
			return nil, err
		}
		request.Remote = strings.TrimSpace(environmentName) != ""
		if creatio.DataWriteHasBindingTemplate(request.SchemaName) {
			return structuredToolResult(creatio.CreateDataBindingLocal(ctx, nil, request)), nil
		}
		if strings.TrimSpace(environmentName) == "" {
			return resolverFailureEnvelope(&environmentError{message: missingTargetMessage}), nil
		}
		client, failure, err := envs.resolve(tool, args, scopeName)
		if err != nil {
			return nil, err
		}
		if failure != nil {
			return resolverFailureEnvelope(failure), nil
		}
		return structuredToolResult(creatio.CreateDataBindingLocal(ctx, client, request)), nil
	}, annotations)

	// clio's AddDataBindingRowTool and RemoveDataBindingRowTool: workspace files only, no environment.
	registerTool(map[string]any{"name": "add-data-binding-row"}, func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
		request, err := dataWriteLocalBindingArgs("add-data-binding-row", args, "package-name", "binding-name", "workspace-path", "values", "localizations")
		if err != nil {
			return nil, err
		}
		if request.WorkspacePath, err = creatio.DataWriteValidateWorkspacePath(request.WorkspacePath); err != nil {
			return nil, err
		}
		return structuredToolResult(creatio.AddDataBindingRowLocal(ctx, request)), nil
	}, annotations)

	registerTool(map[string]any{"name": "remove-data-binding-row"}, func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
		request, err := dataWriteLocalBindingArgs("remove-data-binding-row", args, "package-name", "binding-name", "workspace-path", "key-value")
		if err != nil {
			return nil, err
		}
		if request.WorkspacePath, err = creatio.DataWriteValidateWorkspacePath(request.WorkspacePath); err != nil {
			return nil, err
		}
		return structuredToolResult(creatio.RemoveDataBindingRowLocal(ctx, request)), nil
	}, annotations)
}
