package main

// add-package-dependency, remove-package-dependency, unlock-for-hotfix and finish-hotfix: clio's command-style
// package tools (BaseTool over AddPackageDependencyCommand, RemovePackageDependencyCommand and
// PackageHotFixCommand). clio binds them leniently (unknown keys are ignored) and answers a value of the
// wrong JSON type with an invalid-parameter-type MCP error.

import (
	"context"
	"fmt"
	"strings"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// pkgWriteShapeError is clio's binder refusal for a nested value that does not match the argument record.
func pkgWriteShapeError(tool, name string) error {
	return fmt.Errorf("invalid-parameter-type: argument '%s' for MCP tool '%s' contains a value that does not match the documented shape. Received an incompatible JSON value.", name, tool)
}

// pkgWriteTypeError is clio's binder refusal for a top-level value of the wrong JSON type.
func pkgWriteTypeError(tool, name, expected string) error {
	return fmt.Errorf("invalid-parameter-type: argument '%s' for MCP tool '%s' must be %s. Received an incompatible JSON value.", name, tool, expected)
}

// pkgWriteArray reads an optional JSON array argument: absent or null is nil.
func pkgWriteArray(args map[string]any, tool, name string) ([]any, error) {
	value, ok := args[name]
	if !ok || value == nil {
		return nil, nil
	}
	items, ok := value.([]any)
	if !ok {
		return nil, pkgWriteTypeError(tool, name, "an array")
	}
	return items, nil
}

// pkgWriteDependencyObjects reads add-package-dependency's [{name, version?}] the way clio's tool flattens
// it: null items and items without a name are dropped, "name:version" when a version is given.
func pkgWriteDependencyObjects(args map[string]any, tool string) ([]string, error) {
	items, err := pkgWriteArray(args, tool, "dependencies")
	if err != nil {
		return nil, err
	}
	result := make([]string, 0, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		object, ok := item.(map[string]any)
		if !ok {
			return nil, pkgWriteShapeError(tool, "dependencies")
		}
		name, err := pkgWriteNestedString(object, "name")
		if err != nil {
			return nil, pkgWriteShapeError(tool, "dependencies")
		}
		version, err := pkgWriteNestedString(object, "version")
		if err != nil {
			return nil, pkgWriteShapeError(tool, "dependencies")
		}
		if pkgWriteBlank(name) {
			continue
		}
		if pkgWriteBlank(version) {
			result = append(result, name)
		} else {
			result = append(result, name+":"+version)
		}
	}
	return result, nil
}

// pkgWriteDependencyNames reads remove-package-dependency's string list; blank entries are dropped.
func pkgWriteDependencyNames(args map[string]any, tool string) ([]string, error) {
	items, err := pkgWriteArray(args, tool, "dependencies")
	if err != nil {
		return nil, err
	}
	result := make([]string, 0, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		text, ok := item.(string)
		if !ok {
			return nil, pkgWriteShapeError(tool, "dependencies")
		}
		if !pkgWriteBlank(text) {
			result = append(result, text)
		}
	}
	return result, nil
}

// optionalNestedString reads a string field of a nested object: absent or null is empty.
func pkgWriteNestedString(object map[string]any, name string) (string, error) {
	value, ok := object[name]
	if !ok || value == nil {
		return "", nil
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("not a string")
	}
	return text, nil
}

func pkgWriteBlank(text string) bool { return strings.TrimSpace(text) == "" }

// pkgWriteCommandTool registers a command-style tool whose environment failure is clio's resolver envelope.
func pkgWriteCommandTool(name string, annotations toolAnnotations,
	run func(ctx context.Context, client *creatio.Client, args map[string]any) (creatio.CommandResult, error),
	prepare func(args map[string]any) error) {
	registerTool(map[string]any{"name": name}, func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
		if prepare != nil {
			if err := prepare(args); err != nil {
				return nil, err
			}
		}
		client, failure, err := envs.resolve(name, args, scopeName)
		if err != nil {
			return nil, err
		}
		if failure != nil {
			return resolverFailureEnvelope(failure), nil
		}
		result, err := run(ctx, client, args)
		if err != nil {
			return nil, err
		}
		return structuredToolResult(result), nil
	}, withAnnotations(annotations))
}

func init() {
	// clio: [McpServerTool(ReadOnly = false, Destructive = false, Idempotent = true, OpenWorld = false)]
	addDependency := func(args map[string]any) (string, []string, error) {
		const tool = "add-package-dependency"
		packageName, err := optionalStringArg(args, tool, "package-name")
		if err != nil {
			return "", nil, err
		}
		dependencies, err := pkgWriteDependencyObjects(args, tool)
		return packageName, dependencies, err
	}
	pkgWriteCommandTool("add-package-dependency", toolAnnotations{Idempotent: true},
		func(ctx context.Context, client *creatio.Client, args map[string]any) (creatio.CommandResult, error) {
			packageName, dependencies, err := addDependency(args)
			if err != nil {
				return creatio.CommandResult{}, err
			}
			return client.AddPackageDependencies(ctx, packageName, dependencies), nil
		},
		func(args map[string]any) error { _, _, err := addDependency(args); return err })

	// clio: [McpServerTool(ReadOnly = false, Destructive = true, Idempotent = true, OpenWorld = false)]
	removeDependency := func(args map[string]any) (string, []string, error) {
		const tool = "remove-package-dependency"
		packageName, err := optionalStringArg(args, tool, "package-name")
		if err != nil {
			return "", nil, err
		}
		dependencies, err := pkgWriteDependencyNames(args, tool)
		return packageName, dependencies, err
	}
	pkgWriteCommandTool("remove-package-dependency", toolAnnotations{Destructive: true, Idempotent: true},
		func(ctx context.Context, client *creatio.Client, args map[string]any) (creatio.CommandResult, error) {
			packageName, dependencies, err := removeDependency(args)
			if err != nil {
				return creatio.CommandResult{}, err
			}
			return client.RemovePackageDependencies(ctx, packageName, dependencies), nil
		},
		func(args map[string]any) error { _, _, err := removeDependency(args); return err })

	// clio: unlock-for-hotfix [McpServerTool(ReadOnly = false, Destructive = true, Idempotent = true)] on master;
	// 8.1.0.134's index (the contract this server follows) publishes destructive: false.
	// finish-hotfix: [McpServerTool(ReadOnly = false, Destructive = false, Idempotent = false)].
	for _, hotfix := range []struct {
		name        string
		enable      bool
		annotations toolAnnotations
	}{
		{"unlock-for-hotfix", true, toolAnnotations{Idempotent: true}},
		{"finish-hotfix", false, toolAnnotations{}},
	} {
		hotfix := hotfix
		pkgWriteCommandTool(hotfix.name, hotfix.annotations,
			func(ctx context.Context, client *creatio.Client, args map[string]any) (creatio.CommandResult, error) {
				packageName, err := optionalStringArg(args, hotfix.name, "package-name")
				if err != nil {
					return creatio.CommandResult{}, err
				}
				return client.SetPackageHotfix(ctx, packageName, hotfix.enable), nil
			},
			func(args map[string]any) error {
				_, err := optionalStringArg(args, hotfix.name, "package-name")
				return err
			})
	}
}
