package main

// pkg-to-db and pkg-to-file-system: clio's LoadPackagesTool, two BaseTool methods with one flat, required
// argument named environmentName (camelCase, unlike most tools). Unknown keys are ignored, as clio's binder
// ignores them. clio: [McpServerTool(ReadOnly = false, Destructive = true, Idempotent = false, OpenWorld = false)].

import (
	"context"
	"fmt"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// pkgWriteMissingFlat is the binder's failure for a required flat parameter that is absent; the dispatcher
// reports it as "tool '<name>' failed: ...".
func pkgWriteMissingFlat(name string) error {
	return fmt.Errorf("The arguments dictionary is missing a value for the required parameter '%s'. (Parameter 'arguments')", name)
}

// pkgWriteRequiredFlatString reads a required flat string parameter. A null is refused like an absent value.
func pkgWriteRequiredFlatString(args map[string]any, tool, name string) (string, error) {
	if value, ok := args[name]; !ok || value == nil {
		return "", pkgWriteMissingFlat(name)
	}
	return optionalStringArg(args, tool, name)
}

// pkgWriteBlankEnvironment refuses a blank environment name the way clio's resolver does (ToolCommandResolver:
// neither a name nor a uri), whichever argument carries the name.
func pkgWriteBlankEnvironment(envs *environments, name string) *mcp.CallToolResult {
	if !pkgWriteBlank(name) || envs.hasDefaultTarget() {
		return nil
	}
	return resolverFailureEnvelope(&environmentError{message: missingTargetMessage})
}

// pkgWriteNamedEnvironment resolves a tool whose environment argument is not environment-name.
func pkgWriteNamedEnvironment(envs *environments, tool, name string) (*creatio.Client, error, error) {
	return envs.resolve(tool, map[string]any{"environment-name": name}, scopeName)
}

func init() {
	for _, item := range []struct {
		name string
		toDB bool
	}{{"pkg-to-db", true}, {"pkg-to-file-system", false}} {
		item := item
		registerTool(map[string]any{"name": item.name}, func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
			name, err := pkgWriteRequiredFlatString(args, item.name, "environmentName")
			if err != nil {
				return nil, err
			}
			if refusal := pkgWriteBlankEnvironment(envs, name); refusal != nil {
				return refusal, nil
			}
			client, failure, err := pkgWriteNamedEnvironment(envs, item.name, name)
			if err != nil {
				return nil, err
			}
			if failure != nil {
				return resolverFailureEnvelope(failure), nil
			}
			return structuredToolResult(client.SyncPackages(ctx, item.toDB)), nil
		}, withAnnotations(toolAnnotations{Destructive: true}))
	}
}
