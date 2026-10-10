package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestUserTaskToolsMCP(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, ".clio"), 0o755)
	_ = os.MkdirAll(filepath.Join(root, "packages", "UsrPkg"), 0o755)
	_ = os.WriteFile(filepath.Join(root, ".clio", "workspaceSettings.json"), []byte(`{"Packages":["UsrPkg"]}`), 0o644)
	_ = os.WriteFile(filepath.Join(root, "packages", "UsrPkg", "descriptor.json"), []byte(`{"Descriptor":{"UId":"7d2b9c4e-5a1f-4e3b-9c8d-13f13f13f001","Name":"UsrPkg"}}`), 0o644)
	session, seen := processWriteTestServer(t, "", func(path, body string) string {
		switch {
		case strings.HasSuffix(path, "/CreateNewSchema"):
			return `{"success":true,"schema":{"uId":"aaaaaaaa-1111-2222-3333-444444444444","parameters":[]}}`
		case strings.HasSuffix(path, "/SaveSchema"):
			return `{"success":true,"schemaUid":"aaaaaaaa-1111-2222-3333-444444444444"}`
		}
		return `{"success":true}`
	})
	answer, _ := processWriteCall(t, session, "create-user-task", map[string]any{"code": "UsrTask", "package-name": "UsrPkg", "title": "Task", "workspace-path": root,
		"parameters": []any{map[string]any{"code": "Rows", "title": "Rows", "type": "CompositeValueList", "items": []any{map[string]any{"code": "Id", "title": "Id", "type": "Guid", "required": true}}}},
		"unknown":    1})
	code, messages, note := processWriteEnvelope(t, answer)
	if code != 0 || note != "" || messages[len(messages)-1] != "Info: Done add-user-task" {
		t.Fatalf("%d %q", code, messages)
	}
	if save := seen()["/0/ServiceModel/ProcessUserTaskSchemaDesignerService.svc/SaveSchema"]; !strings.Contains(save, `"caption":[{"cultureName":"en-US","value":"Task"}]`) ||
		!strings.Contains(save, `"itemProperties":[{"uId":`) || !strings.Contains(save, `"name":"Id","caption":[{"cultureName":"en-US","value":"Id"}],"itemProperties":[],"type":0,"required":true`) {
		t.Fatalf("save %s", save)
	}
	answer, _ = processWriteCall(t, session, "modify-user-task-parameters", map[string]any{"user-task-name": "UsrTask", "workspace-path": root})
	if code, messages, _ := processWriteEnvelope(t, answer); code != 1 || !strings.HasPrefix(messages[0], "Error: Specify at least one") {
		t.Fatalf("%d %q", code, messages)
	}
	refusals := []struct {
		tool string
		args map[string]any
		want string
	}{
		{"create-user-task", map[string]any{"parameters": []any{map[string]any{"required": "yes"}}}, "argument 'parameters' for MCP tool 'create-user-task' contains a value that does not match the documented shape"},
		{"create-user-task", map[string]any{"title": 1}, "argument 'title' for MCP tool 'create-user-task' must be a string"},
		{"modify-user-task-parameters", map[string]any{"remove-parameter-names": []any{1}}, "argument 'remove-parameter-names' for MCP tool 'modify-user-task-parameters' contains a value"},
		{"modify-user-task-parameters", map[string]any{"set-parameter-directions": "x"}, "argument 'set-parameter-directions' for MCP tool 'modify-user-task-parameters' must be an array"},
		{"modify-user-task-parameters", map[string]any{"add-parameter-items": []any{map[string]any{"parent-parameter-name": 3}}}, "argument 'add-parameter-items' for MCP tool 'modify-user-task-parameters' contains a value"},
	}
	for _, item := range refusals {
		_, result := processWriteCall(t, session, item.tool, item.args)
		if !result.IsError || !strings.Contains(result.Content[0].(*mcp.TextContent).Text, item.want) {
			t.Errorf("%s %v: %#v", item.tool, item.args, result)
		}
	}
	for name, destructive := range map[string]bool{"create-user-task": false, "modify-user-task-parameters": true} {
		if got := annotationsOf(name); got.ReadOnly || got.Destructive != destructive || got.Idempotent || got.OpenWorld {
			t.Errorf("%s annotations %#v", name, got)
		}
	}
}
