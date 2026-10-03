package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestInstallApplicationIsGatedAndReportsAMissingPackage(t *testing.T) {
	session := connectTestClient(t, newMCPServer(&creatio.Client{}), mcp.NewClient(&mcp.Implementation{Name: "install-probe", Version: "test"}, nil))
	raw, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "install-application", Arguments: map[string]any{"name": "/nowhere/UsrPkg.gz"}})
	if err != nil || !raw.IsError || !schemaWriteTestContainsCode(raw, "confirmation-required") {
		t.Fatalf("raw gate %#v %v", raw, err)
	}
	for _, executor := range []string{"clio-run", "clio-run-destructive"} {
		result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: executor, Arguments: map[string]any{"command": "install-application", "args": map[string]any{"name": "/nowhere/UsrPkg.gz"}}})
		if err != nil || result.IsError {
			t.Fatalf("%s %#v %v", executor, result, err)
		}
		var answer creatio.CommandResult
		_ = json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &answer)
		if answer.ExitCode != 1 || len(answer.Messages) != 2 || answer.Messages[0].Value != "Specified package not found by path /nowhere/UsrPkg.gz" ||
			answer.Messages[1].Value != `Failed package: "/nowhere/UsrPkg.gz".` {
			t.Fatalf("%s %+v", executor, answer)
		}
	}
	bad, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "clio-run", Arguments: map[string]any{"command": "install-application", "args": map[string]any{"name": "x", "check-compilation-errors": "yes"}}})
	if err != nil || !bad.IsError {
		t.Fatalf("binding refusal %#v %v", bad, err)
	}
}
