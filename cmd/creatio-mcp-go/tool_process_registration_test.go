package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestRegisterProcessElementMCP(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		".clio/workspaceSettings.json":                     `{"Packages":["UsrPkg"]}`,
		"packages/UsrPkg/descriptor.json":                  `{"Descriptor":{"Name":"UsrPkg","UId":"11111111-2222-3333-4444-555555555555"}}`,
		"packages/UsrPkg/Schemas/UsrTask1/descriptor.json": `{"Descriptor":{"UId":"aaaaaaaa-0000-0000-0000-000000000001","Name":"UsrTask1","ManagerName":"ProcessUserTaskSchemaManager"}}`,
		"packages/UsrPkg/Schemas/UsrTask1/metadata.json":   `{"MetaData":{"Schema":{"ManagerName":"ProcessUserTaskSchemaManager","A2":"UsrTask1","UId":"aaaaaaaa-0000-0000-0000-000000000001","B6":"11111111-2222-3333-4444-555555555555"}}}`,
	}
	for path, content := range files {
		full := filepath.Join(root, path)
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	session := connectTestClient(t, newMCPServer(&creatio.Client{}), mcp.NewClient(&mcp.Implementation{Name: "registration-probe", Version: "test"}, nil))
	args := map[string]any{"workspace-path": root, "package-name": "UsrPkg", "user-task-uid": "AAAAAAAA-0000-0000-0000-000000000001", "caption": "Approve", "ignored": true}
	answer, _ := processWriteCall(t, session, processRegistrationTool, args)
	code, messages, note := processWriteEnvelope(t, answer)
	if code != 0 || note != "Install with push-pkg or push-workspace to apply registration. No environment was changed." || len(messages) != 2 ||
		!strings.HasSuffix(messages[1], filepath.Join("UsrRegisterTaskaaaaaaaa000000000000000000000001MsSql", "UsrRegisterTaskaaaaaaaa000000000000000000000001MsSql.sql")) {
		t.Fatalf("%d %q %q", code, note, messages)
	}
	answer, _ = processWriteCall(t, session, processRegistrationTool, map[string]any{"package-name": "UsrPkg"})
	if code, messages, _ := processWriteEnvelope(t, answer); code != 1 || messages[0] != "Error: A workspace path, package name, nonempty user-task UId and caption are required." {
		t.Fatalf("%d %q", code, messages)
	}
	for value, want := range map[any]string{"aaaaaaaa000000000000000000000001": "must be an object", 5: "must be an object", "x": "must be an object"} {
		_, result := processWriteCall(t, session, processRegistrationTool, map[string]any{"user-task-uid": value})
		if !result.IsError || !strings.Contains(result.Content[0].(*mcp.TextContent).Text, "argument 'user-task-uid' for MCP tool 'register-process-element' "+want) {
			t.Errorf("%v: %#v", value, result)
		}
	}
	_, result := processWriteCall(t, session, processRegistrationTool, map[string]any{"user-task-uid": nil})
	if !result.IsError {
		t.Fatalf("null uid %#v", result)
	}
	if got := annotationsOf(processRegistrationTool); got.ReadOnly || got.Destructive || !got.Idempotent || got.OpenWorld {
		t.Fatalf("annotations %#v", got)
	}
}
