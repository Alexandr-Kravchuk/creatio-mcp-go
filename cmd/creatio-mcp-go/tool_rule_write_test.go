package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestRuleWriteMCPConfirmationAndBatchValidation(t *testing.T) {
	session := connectTestClient(t, newMCPServer(&creatio.Client{}), mcp.NewClient(&mcp.Implementation{Name: "rule-probe", Version: "test"}, nil))
	for _, name := range []string{"delete-entity-business-rules", "delete-page-business-rules"} {
		result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: map[string]any{}})
		if err != nil || !result.IsError || result.StructuredContent.(map[string]any)["code"] != "confirmation-required" {
			t.Fatalf("direct %s = %#v, %v", name, result, err)
		}
		for _, executor := range []string{"clio-run", "clio-run-destructive"} {
			result, err = session.CallTool(context.Background(), &mcp.CallToolParams{Name: executor, Arguments: map[string]any{"command": name, "args": map[string]any{}}})
			if err != nil || result.IsError {
				t.Fatalf("%s %s = %#v, %v", executor, name, result, err)
			}
			text := result.Content[0].(*mcp.TextContent).Text
			var response creatio.RuleWriteResponse
			if json.Unmarshal([]byte(text), &response) != nil || response.Results == nil || len(response.Results) != 0 || !strings.Contains(response.Error, "rule-names is required") {
				t.Fatalf("batch validation %s", text)
			}
			if name == "delete-page-business-rules" && !strings.Contains(text, "environment-name, package-name, page-schema-name are required.") {
				t.Fatalf("aggregate %s", text)
			}
			audit := result.Meta["clio-run"].(map[string]any)
			if audit["dispatchedTool"] != name || audit["destructive"] != true {
				t.Fatalf("audit %#v", audit)
			}
		}
	}
}
