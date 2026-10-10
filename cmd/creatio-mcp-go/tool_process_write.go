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

// processWriteBlankEnvironment is clio's refusal of an explicit blank environment-name (FromError); an absent
// one goes to the default target instead (decision W1).
func processWriteBlankEnvironment(args map[string]any) *mcp.CallToolResult {
	if value, ok := args["environment-name"].(string); ok && strings.TrimSpace(value) == "" {
		return structuredToolResult(creatio.ProcessWriteError(processWriteMissingEnvironment))
	}
	return nil
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

func init() {
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
			if refusal := processWriteBlankEnvironment(args); refusal != nil {
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
