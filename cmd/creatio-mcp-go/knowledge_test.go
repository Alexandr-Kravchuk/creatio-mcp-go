package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func knowledgeTestSession(t *testing.T) *mcp.ClientSession {
	t.Helper()
	t.Setenv("CLIO_HOME", t.TempDir()) // no settings and no installed knowledge
	return connectTestClient(t, newMCPServerWithHiddenTools(nil, defaultHiddenToolServices()),
		mcp.NewClient(&mcp.Implementation{Name: "probe-client", Version: "test"}, nil))
}

func TestKnowledgeInstructionsAndCapabilities(t *testing.T) {
	session := knowledgeTestSession(t)
	result := session.InitializeResult()
	if result.Instructions != knowledgeServerInstructions || !strings.Contains(result.Instructions, "get-guidance name=core-rules") {
		t.Fatalf("instructions not sent: %q", result.Instructions)
	}
	if result.Capabilities.Prompts == nil || result.Capabilities.Resources == nil {
		t.Fatalf("prompts/resources capabilities missing: %+v", result.Capabilities)
	}
}

func TestGetGuidanceWithoutKnowledgeIsUnavailable(t *testing.T) {
	session := knowledgeTestSession(t)
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "get-guidance",
		Arguments: map[string]any{"guide": "core-rules", "environment-name": "ignored"}})
	if err != nil {
		t.Fatal(err)
	}
	var response map[string]any
	json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &response)
	if response["success"] != false || response["errorCode"] != "guidance-unavailable" || response["feedbackPolicy"] == nil {
		t.Fatalf("unexpected answer %v", response)
	}
	if response["error"] != "Guidance 'core-rules' is unavailable because no compatible verified knowledge bundle is active." {
		t.Fatalf("error text %v", response["error"])
	}
}

func TestPromptsRenderLikeClio(t *testing.T) {
	session := knowledgeTestSession(t)
	list, err := session.ListPrompts(context.Background(), nil)
	if err != nil || len(list.Prompts) != len(knowledgePrompts) || len(list.Prompts) < 70 {
		t.Fatalf("prompts/list: %v (%d)", err, len(list.Prompts))
	}
	got, err := session.GetPrompt(context.Background(), &mcp.GetPromptParams{Name: "restart-by-environment-name",
		Arguments: map[string]string{"environmentName": "ENVX"}})
	if err != nil {
		t.Fatal(err)
	}
	text := got.Messages[0].Content.(*mcp.TextContent).Text
	if !strings.HasPrefix(text, "Restart the Creatio application pool for environment `ENVX` using the\r\n") || strings.Contains(text, "ARG:") {
		t.Fatalf("rendered %q", text)
	}
	// update-sys-setting chooses its payload sentence from which of value / value-file-path is present.
	neither, err := session.GetPrompt(context.Background(), &mcp.GetPromptParams{Name: "update-sys-setting",
		Arguments: map[string]string{"environmentName": "E", "code": "C"}})
	if err != nil || !strings.Contains(neither.Messages[0].Content.(*mcp.TextContent).Text, "(neither was supplied)") {
		t.Fatalf("neither branch: %v", err)
	}
	both, _ := session.GetPrompt(context.Background(), &mcp.GetPromptParams{Name: "update-sys-setting",
		Arguments: map[string]string{"environmentName": "E", "code": "C", "value": "1", "valueFilePath": "/f"}})
	if !strings.Contains(both.Messages[0].Content.(*mcp.TextContent).Text, "You supplied BOTH") {
		t.Fatal("both branch not rendered")
	}
	if _, err := session.GetPrompt(context.Background(), &mcp.GetPromptParams{Name: "restart-by-environment-name"}); err == nil {
		t.Fatal("a missing required argument must fail")
	}
	if _, err := session.GetPrompt(context.Background(), &mcp.GetPromptParams{Name: "no-such-prompt"}); err == nil {
		t.Fatal("an unknown prompt must fail")
	}
}

func TestKnowledgeResources(t *testing.T) {
	session := knowledgeTestSession(t)
	templates, err := session.ListResourceTemplates(context.Background(), nil)
	if err != nil || len(templates.ResourceTemplates) != 5 {
		t.Fatalf("templates: %v", err)
	}
	list, err := session.ListResources(context.Background(), nil)
	if err != nil || len(list.Resources) != 2 || list.Resources[0].URI != "docs://help/restart" {
		t.Fatalf("resources/list without knowledge: %v %+v", err, list)
	}
	help, err := session.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: "docs://help/command/zz-nope"})
	if err != nil || help.Contents[0].Text != "zz-nope command does not provide documentation." {
		t.Fatalf("help fallback: %v", err)
	}
	if _, err := session.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: "docs://knowledge/com.creatio.clio/core-rules"}); err == nil ||
		!strings.Contains(err.Error(), "[guidance-unavailable]") {
		t.Fatalf("unavailable knowledge must fail with guidance-unavailable: %v", err)
	}
	if _, err := session.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: "docs://zz/zz"}); err == nil {
		t.Fatal("an unknown URI must fail")
	}
}
