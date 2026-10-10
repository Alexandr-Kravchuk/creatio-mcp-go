package main

import (
	"context"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// create-server-to-server-oauth-app: clio's CreateServerToServerOAuthAppTool. Every failure, an unknown
// environment included, is success:false with the message; unknown keys are ignored.
func init() {
	registerTool(map[string]any{"name": "create-server-to-server-oauth-app"}, invokeAdminWriteOAuthApp,
		withAnnotations(toolAnnotations{ReadOnly: false, Destructive: true, Idempotent: false, OpenWorld: false}))
}

func invokeAdminWriteOAuthApp(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
	const tool = "create-server-to-server-oauth-app"
	text, err := adminWriteStrings(args, tool, "system-user-id", "system-user", "client-name", "client-application-url", "client-description")
	if err != nil {
		return nil, err
	}
	client, failure, err := envs.resolve(tool, args, scopeName)
	if err != nil {
		return nil, err
	}
	if failure != nil {
		return structuredToolResult(creatio.AdminWriteOAuthAppFailure(redacted(failure))), nil
	}
	return structuredToolResult(client.CreateServerToServerOAuthApp(ctx, creatio.AdminWriteOAuthAppRequest{
		SystemUserID: adminWriteDeref(text["system-user-id"]), SystemUser: adminWriteDeref(text["system-user"]),
		ClientName: adminWriteDeref(text["client-name"]), ClientApplicationURL: adminWriteDeref(text["client-application-url"]),
		ClientDescription: adminWriteDeref(text["client-description"])})), nil
}
