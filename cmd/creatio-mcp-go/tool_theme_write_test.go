package main

import (
	"context"
	"encoding/json"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"testing"
)

func TestThemePackageEmailValidationThroughExecutor(t *testing.T) {
	session := connectTestClient(t, newMCPServer(nil), mcp.NewClient(&mcp.Implementation{Name: "validation", Version: "test"}, nil))
	cases := []struct {
		tool  string
		args  map[string]any
		error string
	}{
		{"create-theme", map[string]any{"environment-name": "unused"}, "theme-css-source-missing:"},
		{"update-theme", map[string]any{"environment-name": "unused"}, "id is required"},
		{"delete-theme", map[string]any{"environment-name": "unused"}, "id is required"},
		{"build-theme", map[string]any{}, "primary is required"},
		{"set-user-theme", map[string]any{"environment-name": "unused", "reset": true, "theme": "other"}, "Specify either"},
		{"update-email-template", map[string]any{"environment-name": "unused", "email-id": "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"}, "confirm=true is required"},
	}
	for _, c := range cases {
		for _, call := range callPaths(c.tool, c.args) {
			result, err := session.CallTool(context.Background(), call)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := json.Marshal(result.StructuredContent)
			if !containsTestText(string(body), c.error) {
				t.Errorf("%s = %s expected %s", c.tool, body, c.error)
			}
		}
	}
}
func containsTestText(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
