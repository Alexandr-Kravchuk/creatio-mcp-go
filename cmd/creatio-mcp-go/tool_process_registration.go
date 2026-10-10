package main

// register-process-element: clio's RegisterProcessElementTool. Offline: it writes package-owned SQL
// registration scripts into a local workspace and contacts no environment. clio binds its arguments
// leniently (unknown keys are ignored); user-task-uid binds as a GUID in its 36-character form.

import (
	"context"
	"fmt"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const processRegistrationTool = "register-process-element"

// processRegistrationGUID binds a System.Text.Json Guid: absent is Guid.Empty ("" here), a 36-character
// string is parsed, anything else (null included) is the binder's refusal, which clio words as "an object".
func processRegistrationGUID(args map[string]any, name string) (string, error) {
	value, present := args[name]
	if !present {
		return "", nil
	}
	text, ok := value.(string)
	if ok && len(text) == 36 {
		if parsed := creatio.ProcessWriteParseGUID(text); parsed != "" {
			return parsed, nil
		}
	}
	return "", fmt.Errorf("invalid-parameter-type: argument '%s' for MCP tool '%s' must be an object. Received an incompatible JSON value.", name, processRegistrationTool)
}

func init() {
	registerTool(map[string]any{"name": processRegistrationTool},
		func(_ context.Context, _ *environments, args map[string]any) (*mcp.CallToolResult, error) {
			values, err := processWriteStrings(processRegistrationTool, args, "workspace-path", "package-name", "caption")
			if err != nil {
				return nil, err
			}
			uid, err := processRegistrationGUID(args, "user-task-uid")
			if err != nil {
				return nil, err
			}
			return structuredToolResult(creatio.RegisterProcessElement(creatio.ProcessWriteRegisterRequest{
				WorkspacePath: values["workspace-path"], PackageName: values["package-name"], UserTaskUID: uid, Caption: values["caption"],
			})), nil
		}, withAnnotations(toolAnnotations{ReadOnly: false, Destructive: false, Idempotent: true, OpenWorld: false}))
}
