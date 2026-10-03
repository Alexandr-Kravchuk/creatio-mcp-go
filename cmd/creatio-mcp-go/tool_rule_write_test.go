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
	expected := map[string]string{
		"create-entity-business-rules": "rules is required and must contain at least one rule.",
		"update-entity-business-rules": "rules is required and must contain at least one rule.",
		"delete-entity-business-rules": "rule-names is required and must contain at least one rule name.",
		"create-page-business-rules":   "environment-name, package-name, page-schema-name are required. rules is required and must contain at least one rule.",
		"update-page-business-rules":   "environment-name, package-name, page-schema-name are required. rules is required and must contain at least one rule.",
		"delete-page-business-rules":   "environment-name, package-name, page-schema-name are required. rule-names is required and must contain at least one rule name.",
	}
	for name, message := range expected {
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
			if json.Unmarshal([]byte(text), &response) != nil || response.Results == nil || len(response.Results) != 0 || response.Error != message {
				t.Fatalf("%s batch validation %s", name, text)
			}
			audit := result.Meta["clio-run"].(map[string]any)
			if audit["dispatchedTool"] != name || audit["destructive"] != true {
				t.Fatalf("audit %#v", audit)
			}
		}
	}
}

func TestRuleWriteMCPBindingRefusals(t *testing.T) {
	session := connectTestClient(t, newMCPServer(&creatio.Client{}), mcp.NewClient(&mcp.Implementation{Name: "rule-probe", Version: "test"}, nil))
	cases := []struct {
		tool string
		args map[string]any
		want string
	}{
		{"create-entity-business-rules", map[string]any{"rules": "abc"}, "invalid-parameter-type: argument 'rules' for MCP tool 'create-entity-business-rules' must be an array. Received an incompatible JSON value."},
		{"update-page-business-rules", map[string]any{"rules": []any{map[string]any{"actions": []any{map[string]any{"type": "apply-filter"}}}}}, "invalid-parameter-type: argument 'rules' for MCP tool 'update-page-business-rules' contains a value that does not match the documented shape. Received an incompatible JSON value."},
		{"create-entity-business-rules", map[string]any{"package-name": 5, "rules": []any{}}, "invalid-parameter-type: argument 'package-name' for MCP tool 'create-entity-business-rules' must be a string. Received an incompatible JSON value."},
		{"delete-entity-business-rules", map[string]any{"rule-names": []any{5}}, "invalid-parameter-type: argument 'rule-names' for MCP tool 'delete-entity-business-rules' contains a value that does not match the documented shape. Received an incompatible JSON value."},
	}
	for _, item := range cases {
		result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "clio-run", Arguments: map[string]any{"command": item.tool, "args": item.args}})
		if err != nil || !result.IsError || !strings.Contains(result.Content[0].(*mcp.TextContent).Text, item.want) {
			t.Fatalf("%s %#v: %#v %v", item.tool, item.args, result, err)
		}
	}
	// Unknown keys are ignored, as clio's binder ignores them.
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "clio-run", Arguments: map[string]any{"command": "delete-entity-business-rules", "args": map[string]any{"zz": 1}}})
	if err != nil || result.IsError || !strings.Contains(result.Content[0].(*mcp.TextContent).Text, "rule-names is required") {
		t.Fatalf("unknown key %#v %v", result, err)
	}
}

func TestRuleWriteAnnotationsMatchClio(t *testing.T) {
	for _, name := range []string{"create-entity-business-rules", "create-page-business-rules", "update-entity-business-rules", "update-page-business-rules", "delete-entity-business-rules", "delete-page-business-rules"} {
		annotations := annotationsOf(name)
		if annotations.ReadOnly || !annotations.Destructive || annotations.OpenWorld || annotations.Idempotent != !strings.HasPrefix(name, "create-") {
			t.Fatalf("%s annotations %#v", name, annotations)
		}
	}
}
