package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// create-related-page-addon: clio's CreateRelatedPageAddonTool. Its own argument guard answers before the
// environment is resolved.
func init() {
	registerTool(map[string]any{"name": "create-related-page-addon"}, func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
		var input struct {
			EntitySchemaName string          `json:"entity-schema-name"`
			PackageName      string          `json:"package-name"`
			Pages            json.RawMessage `json:"pages"`
			TypeColumnUID    string          `json:"type-column-uid"`
			SchemaType       string          `json:"schema-type"`
		}
		if err := decodeStrictArgs(withoutEnvironmentArgs(args, scopeDirect), &input); err != nil {
			return nil, fmt.Errorf("decode create-related-page-addon arguments: %w", err)
		}
		request := creatio.CreateRelatedPageAddonRequest{EntitySchemaName: input.EntitySchemaName, PackageName: input.PackageName,
			TypeColumnUID: input.TypeColumnUID, SchemaType: input.SchemaType}
		if len(input.Pages) == 0 || string(input.Pages) == "null" {
			request.PagesMissing = true
		} else if err := json.Unmarshal(input.Pages, &request.Pages); err != nil {
			return nil, fmt.Errorf("decode create-related-page-addon arguments: pages: %w", err)
		}
		if problem := creatio.RelatedPageAddonArgumentFailure(request); problem != "" {
			return structuredToolResult(creatio.CreateRelatedPageAddonResponse{Error: problem}), nil
		}
		client, failure, err := envs.resolve("create-related-page-addon", args, scopeDirect)
		if err != nil {
			return nil, err
		}
		if failure != nil {
			return structuredToolResult(creatio.CreateRelatedPageAddonResponse{Error: failure.Error()}), nil
		}
		return structuredToolResult(client.CreateRelatedPageAddon(ctx, request)), nil
	}, withAnnotations(toolAnnotations{ReadOnly: false, Destructive: true, Idempotent: false, OpenWorld: false}))
}
