package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestMobileGuideRefusalsMatchClio replays the answers clio 8.1.0.142 gave without a stand
// (scripts/fake-stands/t27/mobile_refusals.py, internal/mobileguide/testdata/clio-refusals): "closedport" is a
// port nothing listens on, "safe" is a Safe environment on it. A direct call by name is compared without clio's
// "[clio] Executed/Attempted '…' directly" trailer, which this server does not write, and the direct call
// without an "args" record is not compared: here it cannot be told from an empty record.
func TestMobileGuideRefusalsMatchClio(t *testing.T) {
	clearCreatioVariables(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLIO_HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "appsettings.json")
	writeSettings(t, path, "closedport", map[string]map[string]any{
		"closedport": {"Uri": "http://127.0.0.1:9", "Login": "example-user", "Password": "replace-me"},
		"safe":       {"Uri": "http://127.0.0.1:9", "Login": "example-user", "Password": "replace-me", "Safe": true},
	})
	session := connectTestClient(t, newMCPServerWithHiddenTools(newEnvironments(path), defaultHiddenToolServices()),
		mcp.NewClient(&mcp.Implementation{Name: "refusals", Version: "test"}, nil))
	cases := []struct {
		label string
		args  map[string]any
	}{
		{"no-args", nil},
		{"empty-args", map[string]any{}},
		{"blank-schema", map[string]any{"schema-name": "   ", "environment-name": "closedport"}},
		{"version-two-part", map[string]any{"schema-name": "UsrX_FormPage", "environment-name": "closedport", "version": "8.3"}},
		{"version-network-path", map[string]any{"schema-name": "UsrX_FormPage", "environment-name": "closedport", "version": "//host/x"}},
		{"version-text", map[string]any{"schema-name": "UsrX_FormPage", "environment-name": "closedport", "version": "abc"}},
		{"unknown-env", map[string]any{"schema-name": "UsrX_FormPage", "environment-name": "nope"}},
		{"safe-env", map[string]any{"schema-name": "UsrX_FormPage", "environment-name": "safe"}},
		{"closed-port", map[string]any{"schema-name": "UsrX_FormPage", "environment-name": "closedport"}},
		{"closed-port-target", map[string]any{"schema-name": "UsrX_FormPage", "environment-name": "closedport", "target-schema-name": "UsrX_Mobile"}},
		{"no-env", map[string]any{"schema-name": "UsrX_FormPage"}},
		{"wrong-type", map[string]any{"schema-name": 5, "environment-name": "closedport"}},
		{"unknown-arg", map[string]any{"schema-name": "UsrX_FormPage", "environment-name": "closedport", "bogus": 1}},
	}
	text := func(result *mcp.CallToolResult) string {
		var parts []string
		for _, content := range result.Content {
			if block, ok := content.(*mcp.TextContent); ok {
				parts = append(parts, block.Text)
			}
		}
		prefix := ""
		if result.IsError {
			prefix = "isError "
		}
		return prefix + strings.Join(parts, "\n")
	}
	for _, tc := range cases {
		for _, route := range []string{"clio-run", "raw"} {
			if tc.label == "no-args" && route == "raw" {
				continue
			}
			want, err := os.ReadFile(filepath.Join("..", "..", "internal", "mobileguide", "testdata", "clio-refusals", tc.label+"."+route+".txt"))
			if err != nil {
				t.Fatal(err)
			}
			expected := string(want)
			var params *mcp.CallToolParams
			if route == "clio-run" {
				inner := map[string]any{"command": mobileGuideTool}
				if tc.args != nil {
					inner["args"] = tc.args
				}
				params = &mcp.CallToolParams{Name: "clio-run", Arguments: inner}
			} else {
				if index := strings.LastIndex(expected, "\n[clio] "); index >= 0 {
					expected = expected[:index]
				}
				params = &mcp.CallToolParams{Name: mobileGuideTool, Arguments: map[string]any{"args": tc.args}}
			}
			result, err := session.CallTool(context.Background(), params)
			if err != nil {
				t.Fatal(err)
			}
			if got := text(result); got != expected {
				t.Errorf("%s.%s:\n got: %s\nwant: %s", tc.label, route, got, expected)
			}
		}
	}
}
