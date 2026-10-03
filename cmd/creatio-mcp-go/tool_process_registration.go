package main

import (
	"context"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func init() {
	registerTool(map[string]any{"name": "register-process-element"}, func(_ context.Context, _ *environments, args map[string]any) (*mcp.CallToolResult, error) {
		values := map[string]string{}
		for _, key := range []string{"workspace-path", "package-name", "user-task-uid", "caption"} {
			value, err := optionalStringArg(args, "register-process-element", key)
			if err != nil {
				return nil, err
			}
			values[key] = value
		}
		return structuredToolResult(creatio.RegisterProcessElement(values["workspace-path"], values["package-name"], values["user-task-uid"], values["caption"])), nil
	}, withAnnotations(toolAnnotations{ReadOnly: false, Destructive: false, Idempotent: true, OpenWorld: false}))
}
