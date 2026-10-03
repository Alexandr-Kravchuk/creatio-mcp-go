package main

import (
	"context"
	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"strings"
)

func init() {
	spec := themeWriteArgSpec{tool: "set-user-theme", strings: []string{"environment-name", "theme"}, bools: []string{"reset"}, hint: "Valid: environment-name, theme, reset."}
	registerTool(map[string]any{"name": "set-user-theme"}, func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
		bound, err := spec.bind(args)
		if err != nil {
			return nil, err
		}
		fail := func(message string) *mcp.CallToolResult {
			return structuredToolResult(creatio.UserThemeFailure(message))
		}
		if message := spec.aliasError(bound); message != "" {
			return fail(message), nil
		}
		if strings.TrimSpace(bound.str("environment-name")) == "" {
			return fail(themeWriteEnvironmentRequired), nil
		}
		reset := bound.flags["reset"] != nil && *bound.flags["reset"]
		hasTheme := strings.TrimSpace(bound.str("theme")) != ""
		if reset && hasTheme {
			return fail("Specify either a theme to apply or reset=true, not both."), nil
		}
		if !reset && !hasTheme {
			return fail("A theme is required unless reset=true. Provide theme (id, css-class-name, or caption), or set reset=true."), nil
		}
		client, _, answer, err := themeWriteResolve(ctx, envs, spec.tool, bound.raw, fail)
		if answer != nil || err != nil {
			return answer, err
		}
		return structuredToolResult(client.SetUserTheme(ctx, bound.str("theme"), reset)), nil
	}, withAnnotations(toolAnnotations{Destructive: true, Idempotent: true}))
}
