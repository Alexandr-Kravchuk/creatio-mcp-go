package main

import (
	"context"
	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"strings"
)

func init() {
	spec := themeWriteArgSpec{tool: "advise-theme-palette", strings: []string{"operation", "primary", "role", "color", "secondary", "accent", "success", "error", "version"}, bools: []string{"full-stops"}, arrays: map[string]string{"colors": "string", "candidate-hexes": "string"}, aliases: map[string]string{"candidateHexes": "candidate-hexes", "candidate_hexes": "candidate-hexes", "fullStops": "full-stops", "full_stops": "full-stops"}, hint: "Valid: operation, colors, primary, role, color, secondary, accent, candidate-hexes, success, error, version, full-stops."}
	registerTool(map[string]any{"name": "advise-theme-palette"}, func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
		bound, err := spec.bind(args)
		if err != nil {
			return nil, err
		}
		fail := func(message string) *mcp.CallToolResult {
			return structuredToolResult(map[string]any{"success": false, "error": message})
		}
		if message := spec.aliasError(bound); message != "" {
			return fail(message), nil
		}
		if strings.TrimSpace(bound.str("operation")) == "" {
			return fail("operation is required and cannot be empty."), nil
		}
		full := bound.flags["full-stops"] != nil && *bound.flags["full-stops"]
		return structuredToolResult(creatio.AdviseThemePalette(creatio.ThemeAdvisorOptions{Operation: bound.str("operation"), Primary: bound.str("primary"), Role: bound.str("role"), Color: bound.str("color"), Secondary: bound.str("secondary"), Accent: bound.str("accent"), Success: bound.str("success"), Error: bound.str("error"), Version: bound.str("version"), Colors: bound.lists["colors"], CandidateHexes: bound.lists["candidate-hexes"], FullStops: full})), nil
	}, withAnnotations(readOnlyAnnotations))
}
