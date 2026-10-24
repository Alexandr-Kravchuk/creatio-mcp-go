package main

// clio's process-designer write tools (ProcessDesigner/*Tool.cs): create-business-process,
// modify-business-process, modify-business-process-as-new-version and set-active-business-process-version.
// Each refuses unknown keys and checks its own arguments before the environment is resolved; the
// CrtProcessBuilder package gate and the ProcessDesignService call follow in internal/creatio.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const processWriteMissingEnvironment = "environment-name is required and cannot be empty."

// processWriteStrings reads clio's optional string arguments; a value of another JSON type is clio's
// invalid-parameter-type refusal, raised before the tool runs.
func processWriteStrings(tool string, args map[string]any, names ...string) (map[string]string, error) {
	values := make(map[string]string, len(names))
	for _, name := range names {
		value, err := optionalStringArg(args, tool, name)
		if err != nil {
			return nil, err
		}
		values[name] = value
	}
	return values, nil
}

// processWriteJSONDocument is McpToolArgumentSupport.TryReadJsonDocumentArgument: the argument is a JSON
// object (or array) or a string holding one. It returns the document text, or clio's refusal.
func processWriteJSONDocument(args map[string]any, name string, array bool) (string, string) {
	value, present := args[name]
	if !present || value == nil {
		return "", name + " is required and cannot be empty."
	}
	expected, kind := "a JSON object", "object"
	if array {
		expected, kind = "a JSON array", "array"
	}
	if text, ok := value.(string); ok {
		if strings.TrimSpace(text) == "" {
			return "", name + " is required and cannot be empty."
		}
		return text, ""
	}
	actual := processWriteJSONKind(value)
	if actual != kind {
		return "", fmt.Sprintf("%s must be %s, or a string holding one. Received a JSON %s.", name, expected, actual)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Sprintf("%s must be %s, or a string holding one. Received a JSON %s.", name, expected, actual)
	}
	return string(encoded), ""
}

// processWriteAbsentJSONDocument is McpToolArgumentSupport.IsAbsentJsonDocument.
func processWriteAbsentJSONDocument(args map[string]any, name string) bool {
	value, present := args[name]
	if !present || value == nil {
		return true
	}
	text, ok := value.(string)
	return ok && strings.TrimSpace(text) == ""
}

// processWriteJSONKind is the JSON name of a value as clio's DescribeKind reports it.
func processWriteJSONKind(value any) string {
	switch value.(type) {
	case map[string]any:
		return "object"
	case []any:
		return "array"
	case string:
		return "string"
	case bool:
		return "boolean"
	case nil:
		return "null"
	default:
		return "number"
	}
}

// processWriteTarget is ProcessTargetArguments.Validate after the unknown-key check: exactly one identity,
// reported as clio's FromError (exit -1).
func processWriteTarget(name, uid, nameArg, uidArg string) *mcp.CallToolResult {
	hasName, hasUID := strings.TrimSpace(name) != "", strings.TrimSpace(uid) != ""
	if hasName == hasUID {
		if hasName {
			return structuredToolResult(creatio.ProcessWriteError(fmt.Sprintf("Provide only one of %s or %s, not both.", nameArg, uidArg)))
		}
		return structuredToolResult(creatio.ProcessWriteError(fmt.Sprintf("one of %s or %s is required.", nameArg, uidArg)))
	}
	return nil
}

// processWriteBlankEnvironment is clio's refusal of an absent or blank environment-name (FromError).
func processWriteBlankEnvironment(envs *environments, args map[string]any) *mcp.CallToolResult {
	if envs.nameRequired(args) {
		return structuredToolResult(creatio.ProcessWriteError(processWriteMissingEnvironment))
	}
	return nil
}

// processWriteDecorated is ToolMiniContract.Decorate over a command-style refusal: the refusal keeps its exit
// code and leading text, and the mini-contract follows it when the call lacks a required argument.
func processWriteDecorated(refusal *mcp.CallToolResult, missing []string, render func([]string) string) *mcp.CallToolResult {
	if len(missing) == 0 || refusal == nil {
		return refusal
	}
	result, ok := refusal.StructuredContent.(creatio.ProcessWriteResult)
	if !ok || len(result.Messages) == 0 {
		return refusal
	}
	result.Messages = []creatio.LogMessage{{MessageType: result.Messages[0].MessageType,
		Value: creatio.MiniContractDecorate(result.Messages[0].Value, missing, render)}}
	return structuredToolResult(result)
}

// processWriteEnvironment resolves the target the way the other command-style tools do.
func processWriteEnvironment(ctx context.Context, envs *environments, tool string, args map[string]any) (*creatio.Client, *mcp.CallToolResult, error) {
	client, failure, err := envs.resolve(tool, args, scopeName)
	if err != nil {
		return nil, nil, err
	}
	if failure != nil {
		return nil, resolverFailureEnvelope(failure), nil
	}
	return client, nil, nil
}

// processWriteBoolArg reads an optional boolean the way clio's binder does: absent or null is unset.
func processWriteBoolArg(args map[string]any, tool, name string) (bool, error) {
	value, ok := args[name]
	if !ok || value == nil {
		return false, nil
	}
	flag, ok := value.(bool)
	if !ok {
		return false, fmt.Errorf("invalid-parameter-type: argument '%s' for MCP tool '%s' must be a boolean. Received an incompatible JSON value.", name, tool)
	}
	return flag, nil
}

func init() {
	registerTool(map[string]any{"name": "create-business-process"},
		func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
			const tool = "create-business-process"
			values, err := processWriteStrings(tool, args, "environment-name", "package-name")
			if err != nil {
				return nil, err
			}
			if refusal := unknownArgumentError(args, "environment-name", "package-name", "descriptor"); refusal != "" {
				return structuredToolResult(creatio.ProcessWriteFailure(refusal)), nil
			}
			// ENG-103025: a refusal for a missing environment or descriptor gains the mini-contract.
			var missing []string
			if environmentNameBlank(args) {
				missing = append(missing, "environment-name")
			}
			if processWriteAbsentJSONDocument(args, "descriptor") {
				missing = append(missing, "descriptor")
			}
			if refusal := processWriteBlankEnvironment(envs, args); refusal != nil {
				return processWriteDecorated(refusal, missing, creatio.MiniContractCreateBusinessProcess), nil
			}
			descriptor, refusal := processWriteJSONDocument(args, "descriptor", false)
			if refusal != "" {
				return processWriteDecorated(structuredToolResult(creatio.ProcessWriteFailure(refusal)), missing, creatio.MiniContractCreateBusinessProcess), nil
			}
			client, failure, err := processWriteEnvironment(ctx, envs, tool, args)
			if failure != nil || err != nil {
				return failure, err
			}
			return structuredToolResult(client.CreateBusinessProcess(ctx, creatio.ProcessWriteBuildRequest{
				Environment: values["environment-name"], Descriptor: descriptor, PackageName: values["package-name"],
			})), nil
		}, withAnnotations(toolAnnotations{ReadOnly: false, Destructive: true, Idempotent: false, OpenWorld: false}))

	registerTool(map[string]any{"name": "modify-business-process"},
		func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
			const tool = "modify-business-process"
			values, err := processWriteStrings(tool, args, "environment-name", "process-name", "process-uid")
			if err != nil {
				return nil, err
			}
			confirm, err := processWriteBoolArg(args, tool, "confirm-layout-change")
			if err != nil {
				return nil, err
			}
			// ENG-103025: a refusal for a missing environment, process or operations gains the mini-contract.
			var missing []string
			if environmentNameBlank(args) {
				missing = append(missing, "environment-name")
			}
			if strings.TrimSpace(values["process-name"]) == "" && strings.TrimSpace(values["process-uid"]) == "" {
				missing = append(missing, "process-name or process-uid")
			}
			if processWriteAbsentJSONDocument(args, "operations") {
				missing = append(missing, "operations")
			}
			if refusal := unknownArgumentError(args, "environment-name", "process-name", "process-uid", "operations", "confirm-layout-change"); refusal != "" {
				return processWriteDecorated(structuredToolResult(creatio.ProcessWriteFailure(refusal)), missing, creatio.MiniContractModifyBusinessProcess), nil
			}
			if refusal := processWriteBlankEnvironment(envs, args); refusal != nil {
				return processWriteDecorated(refusal, missing, creatio.MiniContractModifyBusinessProcess), nil
			}
			if refusal := processWriteTarget(values["process-name"], values["process-uid"], "process-name", "process-uid"); refusal != nil {
				return processWriteDecorated(refusal, missing, creatio.MiniContractModifyBusinessProcess), nil
			}
			operations, refusal := processWriteJSONDocument(args, "operations", true)
			if refusal != "" {
				return processWriteDecorated(structuredToolResult(creatio.ProcessWriteFailure(refusal)), missing, creatio.MiniContractModifyBusinessProcess), nil
			}
			client, failure, err := processWriteEnvironment(ctx, envs, tool, args)
			if failure != nil || err != nil {
				return failure, err
			}
			return structuredToolResult(client.ModifyBusinessProcess(ctx, creatio.ProcessWriteModifyRequest{
				Environment: values["environment-name"], ProcessName: values["process-name"], ProcessUID: values["process-uid"],
				Operations: operations, ConfirmLayoutChange: confirm,
			})), nil
		}, withAnnotations(toolAnnotations{ReadOnly: false, Destructive: true, Idempotent: false, OpenWorld: false}))

	registerTool(map[string]any{"name": "modify-business-process-as-new-version"},
		func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
			const tool = "modify-business-process-as-new-version"
			values, err := processWriteStrings(tool, args, "environment-name", "process-name", "process-uid", "package-name")
			if err != nil {
				return nil, err
			}
			if refusal := unknownArgumentError(args, "environment-name", "process-name", "process-uid", "package-name", "operations"); refusal != "" {
				return structuredToolResult(creatio.ProcessWriteFailure(refusal)), nil
			}
			if refusal := processWriteBlankEnvironment(envs, args); refusal != nil {
				return refusal, nil
			}
			if refusal := processWriteTarget(values["process-name"], values["process-uid"], "process-name", "process-uid"); refusal != nil {
				return refusal, nil
			}
			operations := ""
			if !processWriteAbsentJSONDocument(args, "operations") {
				var refusal string
				if operations, refusal = processWriteJSONDocument(args, "operations", true); refusal != "" {
					return structuredToolResult(creatio.ProcessWriteFailure(refusal)), nil
				}
			}
			client, failure, err := processWriteEnvironment(ctx, envs, tool, args)
			if failure != nil || err != nil {
				return failure, err
			}
			return structuredToolResult(client.ModifyBusinessProcessAsNewVersion(ctx, creatio.ProcessWriteModifyRequest{
				Environment: values["environment-name"], ProcessName: values["process-name"], ProcessUID: values["process-uid"],
				Operations: operations, PackageName: values["package-name"],
			})), nil
		}, withAnnotations(toolAnnotations{ReadOnly: false, Destructive: false, Idempotent: false, OpenWorld: false}))

	registerTool(map[string]any{"name": "set-active-business-process-version"},
		func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
			const tool = "set-active-business-process-version"
			values, err := processWriteStrings(tool, args, "environment-name", "version-name", "version-uid")
			if err != nil {
				return nil, err
			}
			if refusal := unknownArgumentError(args, "environment-name", "version-name", "version-uid"); refusal != "" {
				return structuredToolResult(creatio.ProcessWriteFailure(refusal)), nil
			}
			if refusal := processWriteBlankEnvironment(envs, args); refusal != nil {
				return refusal, nil
			}
			if refusal := processWriteTarget(values["version-name"], values["version-uid"], "version-name", "version-uid"); refusal != nil {
				return refusal, nil
			}
			client, refusal, err := processWriteEnvironment(ctx, envs, tool, args)
			if refusal != nil || err != nil {
				return refusal, err
			}
			return structuredToolResult(client.SetActiveBusinessProcessVersion(ctx, creatio.ProcessWriteActivateRequest{
				Environment: values["environment-name"], VersionName: values["version-name"], VersionUID: values["version-uid"],
			})), nil
		}, withAnnotations(toolAnnotations{ReadOnly: false, Destructive: true, Idempotent: true, OpenWorld: false}))
}
