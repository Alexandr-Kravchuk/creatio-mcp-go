package main

import (
	"context"
	"fmt"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// sync-pages: clio's PageSyncTool. Pages that fail the offline checks are answered before the environment is
// resolved; an unusable environment fails only the pages still to be saved.
func init() {
	registerTool(map[string]any{"name": "sync-pages"}, func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
		var input struct {
			Pages           []creatio.PageSyncPageInput `json:"pages"`
			Validate        *bool                       `json:"validate"`
			Verify          *bool                       `json:"verify"`
			OutputDirectory string                      `json:"output-directory"`
		}
		if err := decodeStrictArgs(withoutEnvironmentArgs(args, scopeName), &input); err != nil {
			return nil, fmt.Errorf("decode sync-pages arguments: %w", err)
		}
		client, failure, err := envs.resolve("sync-pages", args, scopeName)
		if err != nil {
			return nil, err
		}
		request := creatio.PageSyncRequest{EnvironmentName: pageWriteArgText(args, "environment-name"), Pages: input.Pages,
			Validate: input.Validate == nil || *input.Validate, Verify: input.Verify != nil && *input.Verify,
			OutputDirectory: input.OutputDirectory}
		results := creatio.PageSyncPrepass(request)
		if failure != nil {
			return structuredToolResult(creatio.PageSyncFillPending(results, request, redacted(failure))), nil
		}
		return structuredToolResult(client.SyncPages(ctx, request, results)), nil
	}, withAnnotations(toolAnnotations{ReadOnly: false, Destructive: true, Idempotent: false, OpenWorld: false}))
}
