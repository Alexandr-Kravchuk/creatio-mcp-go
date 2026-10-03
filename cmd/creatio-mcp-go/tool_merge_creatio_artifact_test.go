package main

import (
	"context"
	"strings"
	"testing"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMergeCreatioArtifactIsResidentAndStrict(t *testing.T) {
	client, _ := creatio.NewClient(creatio.Config{BaseURL: "http://127.0.0.1:1", Login: "example", Password: "replace-me"})
	session := connectTestClient(t, newMCPServer(client), mcp.NewClient(&mcp.Implementation{Name: "merge-probe", Version: "test"}, nil))
	args := map[string]any{"artifact-path": "Pkg/Schemas/UsrX/UsrX.cs", "base-content": "a", "ours-content": "b", "theirs-content": "c"}
	for _, call := range []*mcp.CallToolParams{{Name: "merge-creatio-artifact", Arguments: args},
		{Name: "merge-creatio-artifact", Arguments: map[string]any{"args": args}},
		{Name: "clio-run", Arguments: map[string]any{"command": "merge-creatio-artifact", "args": args}}} {
		result, err := session.CallTool(context.Background(), call)
		if err != nil || result.IsError {
			t.Fatalf("%v: %#v %v", call.Arguments, result, err)
		}
		if text := result.Content[0].(*mcp.TextContent).Text; !strings.Contains(text, `"status":"not-implemented","artifact-kind":"csharp-source"`) {
			t.Fatalf("answer %s", text)
		}
	}
	refused, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "merge-creatio-artifact",
		Arguments: map[string]any{"environment-name": "dev", "artifact-path": "a.cs", "base-content": "a", "ours-content": "b", "theirs-content": "c"}})
	if err != nil || !refused.IsError || !strings.HasPrefix(refused.Content[0].(*mcp.TextContent).Text, "invalid-parameter-type: argument 'args'") {
		t.Fatalf("refused %#v %v", refused, err)
	}
}
