package main

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Synonyms an agent naturally guesses for find-app's arguments. clio recovers them into the real arguments
// instead of answering an unfiltered list, and matches them ignoring case.
var (
	findAppSearchPatternAliases = []string{"name", "query", "pattern", "filter", "search", "app-name", "appName", "searchPattern", "search_pattern"}
	findAppCodeAliases          = []string{"app-code", "application-code", "appCode", "applicationCode"}
)

func init() {
	registerTool(map[string]any{
		"name": "find-app",
		"description": "Finds installed Creatio applications AND their sections in one call, matching a case-insensitive substring " +
			"against application name/code/description and section captions/codes; use it to resolve an imprecise app name to its real code. " +
			"Omit both filters to enumerate every application with its sections. Returned codes feed get-app-info, list-app-sections, create-app-section.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
			"search-pattern": map[string]string{"type": "string", "description": "Case-insensitive substring matched against application name, code, description, and section captions/codes. Omit to return all applications."},
			"code":           map[string]string{"type": "string", "description": "Exact installed application code to match. Optional."},
		}},
	}, func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
		input, refusal := findAppArguments(args)
		if refusal != "" {
			// clio reports argument problems inside its envelope, so they stay success:false results.
			return structuredToolResult(creatio.FindAppResponse{Error: refusal}), nil
		}
		client, failure, err := envs.resolve("find-app", args, scopeName)
		if err != nil {
			return nil, err
		}
		if failure != nil {
			return structuredToolResult(creatio.FindAppResponse{Error: redacted(failure)}), nil
		}
		return structuredToolResult(client.FindApp(ctx, input)), nil
	})
}

// findAppArguments binds search-pattern and code, recovers their known synonyms, and returns clio's
// "Unknown args" message for anything else.
func findAppArguments(args map[string]any) (creatio.FindAppRequest, string) {
	var input creatio.FindAppRequest
	var err error
	if input.SearchPattern, err = optionalStringArg(args, "find-app", "search-pattern"); err != nil {
		return input, err.Error()
	}
	if input.Code, err = optionalStringArg(args, "find-app", "code"); err != nil {
		return input, err.Error()
	}
	isAlias := func(aliases []string, key string) bool {
		for _, alias := range aliases {
			if strings.EqualFold(alias, key) {
				return true
			}
		}
		return false
	}
	recoverAlias := func(bound string, aliases []string) string {
		if strings.TrimSpace(bound) != "" {
			return bound
		}
		keys := make([]string, 0, len(args))
		for key := range args {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if text, ok := args[key].(string); ok && isAlias(aliases, key) && strings.TrimSpace(text) != "" {
				return strings.TrimSpace(text)
			}
		}
		return bound
	}
	input.SearchPattern = recoverAlias(input.SearchPattern, findAppSearchPatternAliases)
	input.Code = recoverAlias(input.Code, findAppCodeAliases)
	var unknown []string
	for key := range args {
		if key == "environment-name" || key == "search-pattern" || key == "code" ||
			isAlias(findAppSearchPatternAliases, key) || isAlias(findAppCodeAliases, key) {
			continue
		}
		// clio passes find-app an empty rename map, so a legacy environmentName spelling is listed as unknown.
		unknown = append(unknown, fmt.Sprintf("'%s'", describeCallerKey(key)))
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return input, "Unknown args: " + joinCallerKeys(unknown) + ". Valid: environment-name, search-pattern, code. Use search-pattern for a substring filter."
	}
	return input, ""
}
