package main

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// targetPackageAliases are the misspellings clio answers with a rename hint instead of ignoring them.
var targetPackageAliases = map[string]string{"packageName": "package", "package_name": "package", "package-name": "package"}

func init() {
	registerTool(map[string]any{
		"name": "get-target-package",
		"description": "Resolve the package a run's design-time writes land in on the single configured Creatio instance, and verify it can receive them. " +
			"Pass package to resolve a package the user named (checks it exists and is not locked); omit package to resolve the package the " +
			"CurrentPackageId system setting names. On success=false with resolutionFailed=true Creatio answered and there is no usable target; " +
			"with resolutionFailed=false Creatio could not be asked, so retry.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
			"package": map[string]string{"type": "string", "description": "Package the user named. Omit to resolve the package the CurrentPackageId system setting names."},
		}},
	}, func(ctx context.Context, client *creatio.Client, args map[string]any) (*mcp.CallToolResult, error) {
		message := refusesConnectionArgs(args)
		if message == "" {
			message = targetPackageArgumentError(args)
		}
		if message != "" {
			resolutionFailed := false
			return structuredToolResult(creatio.TargetPackageResult{ResolutionFailed: &resolutionFailed, Error: message}), nil
		}
		var input struct {
			Package *string `json:"package,omitempty"`
		}
		if err := decodeStrictArgs(args, &input); err != nil {
			return nil, fmt.Errorf("decode get-target-package arguments: %w", err)
		}
		packageName := ""
		if input.Package != nil {
			packageName = *input.Package
		}
		return structuredToolResult(client.GetTargetPackage(ctx, packageName)), nil
	})
}

// targetPackageArgumentError reproduces clio's message for keys other than package: known misspellings get a
// rename hint, anything else is listed as unknown.
func targetPackageArgumentError(args map[string]any) string {
	keys := make([]string, 0, len(args))
	for key := range args {
		if key != "package" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	var renames, unknown []string
	for _, key := range keys {
		shown := strings.NewReplacer("'", "", `"`, "").Replace(key)
		if canonical, ok := targetPackageAliases[key]; ok {
			renames = append(renames, fmt.Sprintf("'%s' -> '%s'", shown, canonical))
		} else {
			unknown = append(unknown, fmt.Sprintf("'%s'", shown))
		}
	}
	parts := []string{}
	if len(renames) > 0 {
		parts = append(parts, "Rename: "+joinCallerKeys(renames)+".")
	}
	if len(unknown) > 0 {
		parts = append(parts, "Unknown args: "+joinCallerKeys(unknown)+". Valid: package.")
	}
	return strings.Join(parts, " ")
}
