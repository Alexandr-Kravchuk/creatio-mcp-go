package main

import (
	"context"
	"fmt"
	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func init() {
	registerTool(map[string]any{"name": "set-record-rights"}, func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
		var input struct {
			Entity    string `json:"entity"`
			RecordID  string `json:"record-id"`
			Grantee   string `json:"grantee"`
			Operation string `json:"operation"`
			Level     string `json:"level"`
			Revoke    bool   `json:"revoke"`
		}
		if err := decodeStrictArgs(withoutEnvironmentArgs(args, scopeName), &input); err != nil {
			return nil, fmt.Errorf("decode set-record-rights arguments: %w", err)
		}
		client, failure, err := envs.resolve("set-record-rights", args, scopeName)
		if err != nil {
			return nil, err
		}
		if failure != nil {
			return structuredToolResult(creatio.RecordRightsResponse{Error: "[EnvironmentResolutionException] " + failure.Error()}), nil
		}
		return structuredToolResult(client.SetRecordRights(ctx, creatio.RecordRightsWriteRequest{Entity: input.Entity, RecordID: input.RecordID, Grantee: input.Grantee, Operation: input.Operation, Level: input.Level, Revoke: input.Revoke})), nil
	}, withAnnotations(toolAnnotations{ReadOnly: false, Destructive: true, Idempotent: true, OpenWorld: false}))
}
