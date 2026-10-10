package main

// Branding write tools (clio master 914dab286: UploadImageTool; the arguments, checks and answers match
// 8.1.0.134). Each binds clio's argument record (themeWriteArgSpec), answers clio's own refusals inside its
// result, then resolves the environment; a resolution failure is the redacted exception message, as
// clio's BaseTool.ExecuteResolved reports it.

import (
	"context"
	"strings"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/redact"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var uploadImageSpec = themeWriteArgSpec{
	tool: "upload-image", strings: []string{"environment-name", "file"},
	hint: "Valid: environment-name, file.",
}

func init() {
	// clio: [McpServerTool(ReadOnly = false, Destructive = false, Idempotent = false, OpenWorld = false)]
	registerTool(map[string]any{"name": "upload-image"}, invokeUploadImage, withAnnotations(toolAnnotations{}))
}

func invokeUploadImage(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
	bound, err := uploadImageSpec.bind(args)
	if err != nil {
		return nil, err
	}
	fail := func(message string) *mcp.CallToolResult {
		return structuredToolResult(creatio.ImageUploadFailure(message))
	}
	if refusal := uploadImageSpec.aliasError(bound); refusal != "" {
		return fail(refusal), nil
	}
	if strings.TrimSpace(bound.str("environment-name")) == "" {
		return fail(themeWriteEnvironmentRequired), nil
	}
	if strings.TrimSpace(bound.str("file")) == "" {
		return fail("file is required and cannot be empty."), nil
	}
	client, failure, err := envs.resolve("upload-image", bound.raw, scopeName)
	if err != nil {
		return nil, err
	}
	if failure != nil {
		return fail(redacted(failure)), nil
	}
	result := client.UploadImage(ctx, bound.str("file"))
	if !result.Success {
		message := result.Error
		if strings.TrimSpace(message) == "" {
			message = "UploadImage returned success=false."
		} else {
			message = redact.Text(message)
		}
		return fail(message), nil
	}
	return structuredToolResult(result), nil
}

var setLogoSpec = themeWriteArgSpec{
	tool: "set-logo",
	strings: []string{"environment-name", "logo", "login-logo", "menu-logo", "configuration-logo", "dark-logo",
		"favicon", "package"},
	aliases: map[string]string{"loginLogo": "login-logo", "login_logo": "login-logo", "menuLogo": "menu-logo",
		"menu_logo": "menu-logo", "configurationLogo": "configuration-logo", "configuration_logo": "configuration-logo",
		"darkLogo": "dark-logo", "dark_logo": "dark-logo", "faviconImage": "favicon", "favicon_image": "favicon",
		"packageName": "package", "package_name": "package", "package-name": "package"},
	hint: "Valid: environment-name, logo, login-logo, menu-logo, configuration-logo, dark-logo, favicon, package.",
}

func init() {
	// clio: [McpServerTool(ReadOnly = false, Destructive = true, Idempotent = true, OpenWorld = false)]
	registerTool(map[string]any{"name": "set-logo"}, invokeSetLogo,
		withAnnotations(toolAnnotations{Destructive: true, Idempotent: true}))
}

// themeWriteRedactAll is SensitiveErrorTextRedactor.RedactAll; an empty list stays omitted.
func themeWriteRedactAll(texts []string) []string {
	if len(texts) == 0 {
		return nil
	}
	redactedTexts := make([]string, 0, len(texts))
	for _, text := range texts {
		redactedTexts = append(redactedTexts, redact.Text(text))
	}
	return redactedTexts
}

func invokeSetLogo(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
	bound, err := setLogoSpec.bind(args)
	if err != nil {
		return nil, err
	}
	fail := func(message string) *mcp.CallToolResult {
		return structuredToolResult(creatio.SetLogoToolFailure(message))
	}
	if refusal := setLogoSpec.aliasError(bound); refusal != "" {
		return fail(refusal), nil
	}
	if strings.TrimSpace(bound.str("environment-name")) == "" {
		return fail(themeWriteEnvironmentRequired), nil
	}
	anyImage := false
	for _, name := range []string{"logo", "login-logo", "menu-logo", "configuration-logo", "dark-logo", "favicon"} {
		anyImage = anyImage || strings.TrimSpace(bound.str(name)) != ""
	}
	if !anyImage {
		return fail(creatio.SetLogoNoImageError), nil
	}
	client, failure, err := envs.resolve("set-logo", bound.raw, scopeName)
	if err != nil {
		return nil, err
	}
	if failure != nil {
		return fail(redacted(failure)), nil
	}
	result := client.SetLogo(ctx, creatio.SetLogoRequest{Logo: bound.str("logo"), LoginLogo: bound.str("login-logo"),
		MenuLogo: bound.str("menu-logo"), ConfigurationLogo: bound.str("configuration-logo"), DarkLogo: bound.str("dark-logo"),
		Favicon: bound.str("favicon"), Package: bound.str("package")})
	answer := creatio.SetLogoToolResult{Success: result.Success, Applied: result.Applied, Bound: result.Bound,
		Package: result.Package, Warnings: themeWriteRedactAll(result.Warnings)}
	if len(answer.Applied) == 0 {
		answer.Applied = nil
	}
	if len(answer.Bound) == 0 {
		answer.Bound = nil
	}
	if !result.Success {
		answer.Error = "ApplyLogos returned success=false."
		if strings.TrimSpace(result.Error) != "" {
			answer.Error = redact.Text(result.Error)
		}
	}
	return structuredToolResult(answer), nil
}

var setBackgroundImageSpec = themeWriteArgSpec{
	tool:    "set-background-image",
	strings: []string{"environment-name", "image-id", "file", "package"},
	bools:   []string{"keep-icon-background"},
	aliases: map[string]string{"imageId": "image-id", "image_id": "image-id", "packageName": "package",
		"package_name": "package", "package-name": "package", "keepIconBackground": "keep-icon-background",
		"keep_icon_background": "keep-icon-background"},
	hint: "Valid: environment-name, file, image-id, package, keep-icon-background.",
}

func init() {
	// clio: [McpServerTool(ReadOnly = false, Destructive = true, Idempotent = true, OpenWorld = false)]
	registerTool(map[string]any{"name": "set-background-image"}, invokeSetBackgroundImage,
		withAnnotations(toolAnnotations{Destructive: true, Idempotent: true}))
}

func invokeSetBackgroundImage(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
	bound, err := setBackgroundImageSpec.bind(args)
	if err != nil {
		return nil, err
	}
	fail := func(message string) *mcp.CallToolResult {
		return structuredToolResult(creatio.SetBackgroundToolFailure(message))
	}
	if refusal := setBackgroundImageSpec.aliasError(bound); refusal != "" {
		return fail(refusal), nil
	}
	if strings.TrimSpace(bound.str("environment-name")) == "" {
		return fail(themeWriteEnvironmentRequired), nil
	}
	hasFile, hasImageID := strings.TrimSpace(bound.str("file")) != "", strings.TrimSpace(bound.str("image-id")) != ""
	if hasFile && hasImageID {
		return fail(creatio.SetBackgroundBothSourcesError), nil
	}
	if !hasFile && !hasImageID {
		return fail(creatio.SetBackgroundNoSourceError), nil
	}
	client, failure, err := envs.resolve("set-background-image", bound.raw, scopeName)
	if err != nil {
		return nil, err
	}
	if failure != nil {
		return fail(redacted(failure)), nil
	}
	keep := bound.flags["keep-icon-background"] != nil && *bound.flags["keep-icon-background"]
	result := client.SetBackground(ctx, creatio.SetBackgroundRequest{ImageID: bound.str("image-id"), File: bound.str("file"),
		Package: bound.str("package"), KeepIconBackground: keep})
	if result.Fault != nil {
		// clio's command threw: BaseTool.ExecuteResolved answers the redacted message alone.
		return fail(redacted(result.Fault)), nil
	}
	answer := creatio.SetBackgroundToolResult{Success: result.Success, Package: result.Package,
		Warnings: themeWriteRedactAll(result.Warnings)}
	if len(result.Bound) > 0 {
		answer.Bound = result.Bound
	}
	if result.Success {
		answer.ImageID = result.ImageID
	} else {
		answer.Error = "SetBackground returned success=false."
		if strings.TrimSpace(result.Error) != "" {
			answer.Error = redact.Text(result.Error)
		}
	}
	return structuredToolResult(answer), nil
}
