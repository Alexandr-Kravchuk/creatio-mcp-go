package main

import (
	"context"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func init() {
	registerTool(map[string]any{
		"name": "get-user-culture",
		"description": "Resolve the logged-in Creatio user's profile culture (e.g. en-US, uk-UA) on the target Creatio environment, " +
			"read from ApplicationInfoService.svc/GetApplicationInfo (no cliogate required). Call it once per session before generating names, " +
			"labels or captions, and write them in that language. On success returns { success:true, culture }. On failure returns " +
			"{ success:false, reason } - then ASK the user which language to use; do not fall back to the host locale or a silent en-US.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
	}, invokeGetUserCulture)
}

func invokeGetUserCulture(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
	// clio answers a misspelled or unsupported argument inside its envelope, never as a protocol error.
	if refusal := unknownArgumentError(args, environmentArgs(scopeDirect)...); refusal != "" {
		return structuredToolResult(creatio.UserCultureFailure(refusal)), nil
	}
	client, failure, err := envs.resolve("get-user-culture", args, scopeDirect)
	if err != nil {
		return nil, err
	}
	if failure != nil {
		return structuredToolResult(creatio.UserCultureFailure(redacted(failure))), nil
	}
	return structuredToolResult(client.GetUserCulture(ctx)), nil
}
