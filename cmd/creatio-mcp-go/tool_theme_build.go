package main

import (
	"context"
	"fmt"
	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var buildThemeSpec = themeWriteArgSpec{tool: "build-theme", strings: []string{"primary", "css-class-name", "caption", "id", "secondary", "accent", "success", "error", "heading-font", "body-font", "version", "environment-name", "workspace-directory", "package-name"}, arrays: map[string]string{"font-weights": "int"}, aliases: map[string]string{"cssClassName": "css-class-name", "css_class_name": "css-class-name", "headingFont": "heading-font", "heading_font": "heading-font", "bodyFont": "body-font", "body_font": "body-font", "fontWeights": "font-weights", "font_weights": "font-weights", "workspaceDirectory": "workspace-directory", "workspace_directory": "workspace-directory", "packageName": "package-name", "package_name": "package-name"}, hint: "Valid: primary, css-class-name, caption, id, secondary, accent, success, error, heading-font, body-font, font-weights, version, environment-name, workspace-directory, package-name."}

func init() {
	registerTool(map[string]any{"name": "build-theme"}, invokeBuildTheme, withAnnotations(toolAnnotations{Idempotent: true, OpenWorld: true}))
	registerTool(map[string]any{"name": "generate-process-model"}, func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
		const tool = "generate-process-model"
		input := creatio.ProcessModelRequest{}
		values := map[string]*string{"code": &input.Code, "destination-path": &input.DestinationPath, "namespace": &input.Namespace, "culture": &input.Culture}
		for name, out := range values {
			s, err := optionalStringArg(args, tool, name)
			if err != nil {
				return nil, err
			}
			*out = s
		}
		if args["destination-path"] == nil {
			input.DestinationPath = "."
		}
		if args["namespace"] == nil {
			input.Namespace = "AtfTIDE.ProcessModels"
		}
		if args["culture"] == nil {
			input.Culture = "en-US"
		}
		client, failure, err := envs.resolve(tool, args, scopeName)
		if err != nil {
			return nil, err
		}
		if failure != nil {
			return structuredToolResult(creatio.CommandFailure(redacted(failure))), nil
		}
		return structuredToolResult(client.GenerateProcessModel(ctx, input)), nil
	}, withAnnotations(toolAnnotations{Destructive: true}))
}

func invokeBuildTheme(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
	bound, err := buildThemeSpec.bind(args)
	if err != nil {
		return nil, err
	}
	fail := func(message string) (*mcp.CallToolResult, error) {
		return structuredToolResult(creatio.ThemeBuildOutcome{Error: message}), nil
	}
	if message := buildThemeSpec.aliasError(bound); message != "" {
		return fail(message)
	}
	if strings.TrimSpace(bound.str("primary")) == "" {
		return fail("primary is required and cannot be empty.")
	}
	workspace, pkg := bound.str("workspace-directory"), bound.str("package-name")
	write := strings.TrimSpace(workspace) != "" || strings.TrimSpace(pkg) != ""
	if write {
		if strings.TrimSpace(workspace) == "" || strings.TrimSpace(pkg) == "" {
			return fail("workspace-directory and package-name must be provided together to write into a workspace package; omit both to return the css + descriptor strings instead.")
		}
		if !filepath.IsAbs(workspace) {
			return fail(fmt.Sprintf("workspace-directory must be a fully-qualified absolute path. Drive-relative ('C:ws') and root-relative ('\\ws') paths are rejected because the MCP server working directory differs from the caller's. Received: '%s'.", workspace))
		}
		if !regexp.MustCompile(`^[A-Za-z0-9_]+$`).MatchString(pkg) {
			return fail(fmt.Sprintf("package-name must be a simple identifier matching '^[A-Za-z0-9_]+$'. Path separators, '..', and absolute paths are rejected to keep the write inside the workspace. Received: '%s'.", pkg))
		}
		if _, err := os.Stat(filepath.Join(workspace, ".clio", "workspaceSettings.json")); err != nil {
			return fail(fmt.Sprintf("build-theme: '%s' is not a clio workspace (missing .clio/workspaceSettings.json). Create it first with create-workspace.", workspace))
		}
		info, err := os.Stat(filepath.Join(workspace, "packages", pkg))
		if err != nil || !info.IsDir() {
			return fail(fmt.Sprintf("build-theme: package '%s' does not exist in the workspace (expected at '%s'). Add it first with add-package.", pkg, filepath.Join(workspace, "packages", pkg)))
		}
	}
	options := themeWriteBrandInput(bound)
	options.Version = bound.str("version")
	options.EnvironmentName = bound.str("environment-name")
	if options.Version != "" && options.EnvironmentName != "" {
		return fail("build-theme: --version and --environment-name are mutually exclusive. Pass one or neither.")
	}
	var warnings []string
	if options.Version == "" && options.EnvironmentName != "" {
		client, failure, err := envs.resolve("build-theme", bound.raw, scopeName)
		if err != nil {
			return nil, err
		}
		if failure != nil {
			warnings = append(warnings, fmt.Sprintf("build-theme: could not resolve environment '%s' — built against the newest supported version instead. Pass version to target a specific template, or omit environment-name to use the credential-passthrough tenant's version.", options.EnvironmentName))
		} else if version, err := client.ThemeWriteEnsureVersion(ctx); err == nil {
			options.Version = version
		}
	}
	options.EnvironmentName = ""
	result := creatio.BuildTheme(ctx, options)
	if !result.Success {
		return structuredToolResult(result), nil
	}
	result.Warnings = append(warnings, result.Warnings...)
	if write {
		class, err := creatio.ThemeWriteResolveClassName(options.CSSClassName, options.Caption)
		if err != nil {
			return fail(err.Error())
		}
		directory := filepath.Join(workspace, "packages", pkg, "Files", "themes", class)
		if err := os.MkdirAll(directory, 0755); err != nil {
			return fail(err.Error())
		}
		if err := os.WriteFile(filepath.Join(directory, "theme.css"), []byte(result.CSS), 0644); err != nil {
			return fail(err.Error())
		}
		if err := os.WriteFile(filepath.Join(directory, "theme.json"), []byte(result.Descriptor), 0644); err != nil {
			return fail(err.Error())
		}
		result.Path = directory
		result.CSS = ""
		result.Descriptor = ""
	}
	return structuredToolResult(result), nil
}
