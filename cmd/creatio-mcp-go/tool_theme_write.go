package main

// Theme write tools (clio master 914dab286: CreateThemeTool, UpdateThemeTool, DeleteThemeTool,
// ClearThemesCacheTool). Each resolves the environment, then enforces clio's 10.0.0 ThemeService floor
// (RequiresCreatioVersion on the command's options) before the command runs, as clio's BaseTool does.

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/redact"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// themeWriteEnvironmentAliases are McpToolArgumentSupport.EnvironmentNameAliases. A tool that uses clio's
// dictionary directly matches them case-insensitively; one that copies them into its own dictionary
// (new(EnvironmentNameAliases, StringComparer.Ordinal)) matches them exactly.
var themeWriteEnvironmentAliases = map[string]string{
	"environmentName": "environment-name", "environment_name": "environment-name", "environment": "environment-name",
}

// themeWriteArgSpec is one tool's argument record: the accepted names (bound case-insensitively, as clio's
// web-default serializer binds them), the legacy spellings answered with a rename, and the hint text.
type themeWriteArgSpec struct {
	tool    string
	strings []string
	bools   []string
	arrays  map[string]string // name -> element kind: "string" or "int"
	aliases map[string]string
	// envAliasesIgnoreCase: the tool passes clio's EnvironmentNameAliases unchanged.
	envAliasesIgnoreCase bool
	hint                 string
}

// themeWriteArgs is a bound argument record.
type themeWriteArgs struct {
	text   map[string]string
	flags  map[string]*bool
	lists  map[string][]string
	ints   map[string][]int
	extras []string
	raw    map[string]any
}

func (a themeWriteArgs) str(name string) string { return a.text[name] }

// bind reads the record the way clio's binder does: a known name in any letter case binds (the last one
// wins), a value of the wrong JSON type is the invalid-parameter-type failure raised before the tool runs,
// and every other key lands in the overflow bag the tool answers with BuildLegacyAliasError.
func (s themeWriteArgSpec) bind(args map[string]any) (themeWriteArgs, error) {
	bound := themeWriteArgs{text: map[string]string{}, flags: map[string]*bool{}, lists: map[string][]string{},
		ints: map[string][]int{}, raw: map[string]any{}}
	canonical := map[string]string{}
	for _, name := range s.strings {
		canonical[strings.ToLower(name)] = name
	}
	for _, name := range s.bools {
		canonical[strings.ToLower(name)] = name
	}
	for name := range s.arrays {
		canonical[strings.ToLower(name)] = name
	}
	keys := make([]string, 0, len(args))
	for key := range args {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		name, known := canonical[strings.ToLower(key)]
		if !known {
			bound.extras = append(bound.extras, key)
			continue
		}
		value := args[key]
		bound.raw[name] = value
		switch {
		case s.arrays[name] != "":
			if err := s.bindArray(&bound, name, value); err != nil {
				return bound, err
			}
		case themeWriteContains(s.bools, name):
			switch typed := value.(type) {
			case nil:
				bound.flags[name] = nil
			case bool:
				bound.flags[name] = &typed
			default:
				return bound, themeWriteTypeError(name, s.tool, "a boolean")
			}
		default:
			switch typed := value.(type) {
			case nil:
				bound.text[name] = ""
			case string:
				bound.text[name] = typed
			default:
				return bound, themeWriteTypeError(name, s.tool, "a string")
			}
		}
	}
	return bound, nil
}

func (s themeWriteArgSpec) bindArray(bound *themeWriteArgs, name string, value any) error {
	if value == nil {
		return nil
	}
	items, ok := value.([]any)
	if !ok {
		return themeWriteTypeError(name, s.tool, "an array")
	}
	for _, item := range items {
		switch s.arrays[name] {
		case "int":
			number, ok := item.(float64)
			if !ok || number != math.Trunc(number) || number < math.MinInt32 || number > math.MaxInt32 {
				return themeWriteTypeError(name, s.tool, "an array")
			}
			bound.ints[name] = append(bound.ints[name], int(number))
		default:
			text, ok := item.(string)
			if !ok && item != nil {
				return themeWriteTypeError(name, s.tool, "an array")
			}
			bound.lists[name] = append(bound.lists[name], text)
		}
	}
	if bound.ints[name] == nil && s.arrays[name] == "int" {
		bound.ints[name] = []int{}
	}
	if bound.lists[name] == nil && s.arrays[name] != "int" {
		bound.lists[name] = []string{}
	}
	return nil
}

func themeWriteTypeError(name, tool, kind string) error {
	return fmt.Errorf("invalid-parameter-type: argument '%s' for MCP tool '%s' must be %s. Received an incompatible JSON value.", name, tool, kind)
}

func themeWriteContains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

// aliasError is clio's McpToolArgumentSupport.BuildLegacyAliasError over the overflow bag.
func (s themeWriteArgSpec) aliasError(bound themeWriteArgs) string {
	if len(bound.extras) == 0 {
		return ""
	}
	var renamed, unknown []string
	for _, key := range bound.extras {
		canonical, ok := s.aliases[key]
		if !ok {
			for alias, target := range themeWriteEnvironmentAliases {
				if key == alias || (s.envAliasesIgnoreCase && strings.EqualFold(key, alias)) {
					canonical, ok = target, true
					break
				}
			}
		}
		if ok {
			renamed = append(renamed, fmt.Sprintf("'%s' -> '%s'", themeWriteCallerKey(key), canonical))
		} else {
			unknown = append(unknown, fmt.Sprintf("'%s'", themeWriteCallerKey(key)))
		}
	}
	var parts []string
	if len(renamed) > 0 {
		parts = append(parts, "Rename: "+joinCallerKeys(renamed)+".")
	}
	if len(unknown) > 0 {
		parts = append(parts, "Unknown args: "+joinCallerKeys(unknown)+". "+s.hint)
	}
	return strings.Join(parts, " ")
}

// themeWriteCallerKey is McpToolArgumentSupport.DescribeCallerKey: sanitized for display, at most 120
// units, quotes stripped.
func themeWriteCallerKey(key string) string {
	return describeCallerKey(creatio.ThemeWriteSanitize(key, 120))
}

const themeWriteEnvironmentRequired = "environment-name is required and cannot be empty."

var createThemeSpec = themeWriteArgSpec{
	tool: "create-theme",
	strings: []string{"environment-name", "css-content", "css-class-name", "caption", "id", "package-name", "primary",
		"secondary", "accent", "success", "error", "heading-font", "body-font"},
	arrays: map[string]string{"font-weights": "int"},
	aliases: map[string]string{"cssContent": "css-content", "css_content": "css-content", "cssClassName": "css-class-name",
		"css_class_name": "css-class-name", "packageName": "package-name", "package_name": "package-name",
		"headingFont": "heading-font", "heading_font": "heading-font", "bodyFont": "body-font", "body_font": "body-font",
		"fontWeights": "font-weights", "font_weights": "font-weights"},
	hint: "Valid: environment-name, css-content, css-class-name, caption, id, package-name, " +
		createThemeBrandParameterNames + ".",
}

// createThemeBrandParameterNames is CreateThemeTool.BrandParameterNames.
const createThemeBrandParameterNames = "primary, secondary, accent, success, error, heading-font, body-font, font-weights"

var updateThemeSpec = themeWriteArgSpec{
	tool:    "update-theme",
	strings: []string{"environment-name", "id", "caption", "css-class-name", "css-content"},
	aliases: map[string]string{"cssContent": "css-content", "css_content": "css-content", "cssClassName": "css-class-name",
		"css_class_name": "css-class-name"},
	hint: "Valid: environment-name, id, caption, css-class-name, css-content.",
}

var deleteThemeSpec = themeWriteArgSpec{
	tool: "delete-theme", strings: []string{"environment-name", "id"}, envAliasesIgnoreCase: true,
	hint: "Valid: environment-name, id.",
}

var clearThemesCacheSpec = themeWriteArgSpec{
	tool: "clear-themes-cache", strings: []string{"environment-name"}, envAliasesIgnoreCase: true,
	hint: "Valid: environment-name.",
}

func init() {
	registerTool(map[string]any{"name": "create-theme"}, invokeCreateTheme,
		withAnnotations(toolAnnotations{OpenWorld: true}))
	registerTool(map[string]any{"name": "update-theme"}, invokeUpdateTheme,
		withAnnotations(toolAnnotations{Destructive: true, Idempotent: true}))
	registerTool(map[string]any{"name": "delete-theme"}, invokeDeleteTheme,
		withAnnotations(toolAnnotations{Destructive: true}))
	registerTool(map[string]any{"name": "clear-themes-cache"}, invokeClearThemesCache,
		withAnnotations(toolAnnotations{Idempotent: true}))
}

// themeWriteResolve resolves the environment and enforces the ThemeService floor. A non-nil result is the
// tool's answer; err is a binding failure raised before the tool runs.
func themeWriteResolve(ctx context.Context, envs *environments, tool string, args map[string]any,
	onFailure func(string) *mcp.CallToolResult) (*creatio.Client, string, *mcp.CallToolResult, error) {
	client, failure, err := envs.resolve(tool, args, scopeName)
	if err != nil {
		return nil, "", nil, err
	}
	if failure != nil {
		return nil, "", onFailure(redacted(failure)), nil
	}
	version, versionErr := client.ThemeWriteEnsureVersion(ctx)
	if versionErr != nil {
		return nil, "", onFailure(redact.Text(versionErr.Error())), nil
	}
	return client, version, nil, nil
}

// themeWriteResolveCommand is themeWriteResolve for the command-style tools: a resolution failure is clio's
// FromResolverError envelope and an unmet floor its exit-code-78 envelope.
func themeWriteResolveCommand(ctx context.Context, envs *environments, tool string, args map[string]any) (*creatio.Client, *mcp.CallToolResult, error) {
	client, failure, err := envs.resolve(tool, args, scopeName)
	if err != nil {
		return nil, nil, err
	}
	if failure != nil {
		return nil, resolverFailureEnvelope(failure), nil
	}
	if _, versionErr := client.ThemeWriteEnsureVersion(ctx); versionErr != nil {
		return nil, structuredToolResult(creatio.ThemeWriteVersionFailure(versionErr)), nil
	}
	return client, nil, nil
}

func invokeCreateTheme(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
	bound, err := createThemeSpec.bind(args)
	if err != nil {
		return nil, err
	}
	fail := func(message string) *mcp.CallToolResult {
		return structuredToolResult(creatio.ThemeWriteCreateFailure(message, nil))
	}
	if refusal := createThemeSpec.aliasError(bound); refusal != "" {
		return fail(refusal), nil
	}
	if strings.TrimSpace(bound.str("environment-name")) == "" {
		return fail(themeWriteEnvironmentRequired), nil
	}
	hasCSS := strings.TrimSpace(bound.str("css-content")) != ""
	brandMode := strings.TrimSpace(bound.str("primary")) != ""
	anyBrand := brandMode || len(bound.ints["font-weights"]) > 0
	for _, name := range []string{"secondary", "accent", "success", "error", "heading-font", "body-font"} {
		anyBrand = anyBrand || strings.TrimSpace(bound.str(name)) != ""
	}
	switch {
	case hasCSS && anyBrand:
		return fail("theme-css-source-conflict: css-content and the brand parameters (" + createThemeBrandParameterNames + ") " +
			"are mutually exclusive. Provide inline CSS or brand colours, not both."), nil
	case !hasCSS && !brandMode && anyBrand:
		return fail("theme-brand-primary-missing: primary is required for the brand mode — the other brand parameters " +
			"only refine the palette derived from it or select the build template. Pass primary, or provide css-content instead."), nil
	case !hasCSS && !brandMode:
		return fail("theme-css-source-missing: provide either css-content (inline CSS) or primary (brand colours; " +
			"clio builds the theme CSS server-side and creates the theme in one call)."), nil
	}
	client, version, answer, err := themeWriteResolve(ctx, envs, "create-theme", bound.raw, fail)
	if answer != nil || err != nil {
		return answer, err
	}
	options := creatio.ThemeWriteCreateOptions{ID: bound.str("id"), Caption: bound.str("caption"),
		CSSClassName: bound.str("css-class-name"), CSSContent: bound.str("css-content"), PackageName: bound.str("package-name")}
	var warnings []string
	if brandMode {
		build := client.BuildThemeForCreate(ctx, themeWriteBrandInput(bound), version)
		warnings = build.Warnings
		if !build.Success {
			return structuredToolResult(creatio.ThemeWriteCreateFailure("theme-build-failed: "+redact.Text(build.Error), warnings)), nil
		}
		options.CSSContent = build.CSS
	}
	return structuredToolResult(client.CreateTheme(ctx, options, warnings)), nil
}

// themeWriteBrandInput is the brand half of create-theme's arguments (ThemeBrandArgs plus primary).
func themeWriteBrandInput(bound themeWriteArgs) creatio.ThemeBuildOptions {
	return creatio.ThemeBuildOptions{Primary: bound.str("primary"), Secondary: bound.str("secondary"), Accent: bound.str("accent"),
		Success: bound.str("success"), Error: bound.str("error"), CSSClassName: bound.str("css-class-name"),
		Caption: bound.str("caption"), ID: bound.str("id"), HeadingFont: bound.str("heading-font"),
		BodyFont: bound.str("body-font"), FontWeights: bound.ints["font-weights"]}
}

func invokeUpdateTheme(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
	bound, err := updateThemeSpec.bind(args)
	if err != nil {
		return nil, err
	}
	if refusal := updateThemeSpec.aliasError(bound); refusal != "" {
		return structuredToolResult(creatio.CommandFailure(refusal)), nil
	}
	for _, name := range []string{"environment-name", "id", "caption", "css-class-name", "css-content"} {
		if strings.TrimSpace(bound.str(name)) == "" {
			return structuredToolResult(creatio.CommandFailure(name + " is required and cannot be empty.")), nil
		}
	}
	client, answer, err := themeWriteResolveCommand(ctx, envs, "update-theme", bound.raw)
	if answer != nil || err != nil {
		return answer, err
	}
	return structuredToolResult(client.UpdateTheme(ctx, bound.str("id"), bound.str("caption"), bound.str("css-class-name"),
		bound.str("css-content"))), nil
}

func invokeDeleteTheme(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
	bound, err := deleteThemeSpec.bind(args)
	if err != nil {
		return nil, err
	}
	if refusal := deleteThemeSpec.aliasError(bound); refusal != "" {
		return structuredToolResult(creatio.CommandFailure(refusal)), nil
	}
	for _, name := range []string{"environment-name", "id"} {
		if strings.TrimSpace(bound.str(name)) == "" {
			return structuredToolResult(creatio.CommandFailure(name + " is required and cannot be empty.")), nil
		}
	}
	client, answer, err := themeWriteResolveCommand(ctx, envs, "delete-theme", bound.raw)
	if answer != nil || err != nil {
		return answer, err
	}
	return structuredToolResult(client.DeleteTheme(ctx, bound.str("id"))), nil
}

func invokeClearThemesCache(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
	bound, err := clearThemesCacheSpec.bind(args)
	if err != nil {
		return nil, err
	}
	if refusal := clearThemesCacheSpec.aliasError(bound); refusal != "" {
		return structuredToolResult(creatio.CommandFailure(refusal)), nil
	}
	if strings.TrimSpace(bound.str("environment-name")) == "" {
		return structuredToolResult(creatio.CommandFailure(themeWriteEnvironmentRequired)), nil
	}
	client, answer, err := themeWriteResolveCommand(ctx, envs, "clear-themes-cache", bound.raw)
	if answer != nil || err != nil {
		return answer, err
	}
	return structuredToolResult(client.ClearThemesCache(ctx)), nil
}
