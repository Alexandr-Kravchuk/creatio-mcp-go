package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// themeGetLegacyAliases are the spellings clio's get-theme answers with a rename hint, in sorted order.
var themeGetLegacyAliases = []string{"outputFile", "output_file"}

func init() {
	registerTool(map[string]any{
		"name": "check-theming-access",
		"description": "Check whether the caller can manage custom themes on the single configured environment. Runs on any Creatio version: " +
			"it reads only RightsService (the CanManageThemes system operation) and LicenseService (the CanCustomizeBranding license). " +
			"Returns { success, canManageThemes, canCustomizeBranding, themeServiceMinVersion, error? }; themeServiceMinVersion is the " +
			"Creatio version the theme write commands need. Advisory only.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
	}, func(ctx context.Context, client *creatio.Client, args map[string]any) (*mcp.CallToolResult, error) {
		// clio refuses unknown keys inside its envelope, so these failures stay success:false results.
		if refusal := unknownArgumentError(args, nil); refusal != "" {
			return structuredToolResult(creatio.ThemingAccessFailure(refusal)), nil
		}
		return structuredToolResult(client.CheckThemingAccess(ctx)), nil
	})
	registerTool(map[string]any{
		"name": "get-theme",
		"description": "Read the content (theme.css) and metadata of a custom Creatio theme by id on the single configured environment " +
			"(Creatio 10.0.0 or later; the floor is not pre-checked). The theme is resolved through the GetAvailableThemes catalog and its CSS " +
			"read from the catalog's cssFilePath. Returns { success, id, caption, cssClassName, cssFilePath, cssContent?, cssContentLength?, error? }. " +
			"Set output-file to write the CSS to disk instead of the response: the path must be inside the workspace or the OS temp " +
			"directory and must not exist yet.",
		"inputSchema": map[string]any{"type": "object", "required": []string{"id"}, "properties": map[string]any{
			"id":          map[string]string{"type": "string", "description": "Id (a GUID) of the theme to read (see list-themes)."},
			"output-file": map[string]string{"type": "string", "description": "Optional path to write the theme CSS to; when set, cssContent is omitted from the result."},
		}},
	}, invokeGetTheme)
}

func invokeGetTheme(ctx context.Context, client *creatio.Client, args map[string]any) (*mcp.CallToolResult, error) {
	id, err := optionalStringArg(args, "get-theme", "id")
	if err != nil {
		return nil, err
	}
	outputFile, err := optionalStringArg(args, "get-theme", "output-file")
	if err != nil {
		return nil, err
	}
	for _, alias := range themeGetLegacyAliases {
		if _, ok := args[alias]; ok {
			return structuredToolResult(creatio.ThemeGetFailure(fmt.Sprintf("Rename: '%s' -> 'output-file'.", alias))), nil
		}
	}
	if refusal := unknownArgumentError(args, map[string]bool{"id": true, "output-file": true}); refusal != "" {
		return structuredToolResult(creatio.ThemeGetResult{Error: refusal}), nil
	}
	if strings.TrimSpace(id) == "" {
		return structuredToolResult(creatio.ThemeGetResult{Error: "id is required and cannot be empty."}), nil
	}
	return structuredToolResult(client.GetTheme(ctx, id, outputFile)), nil
}
