package main

import (
	"context"
	"encoding/json"
	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"strings"
)

func init() {
	registerTool(map[string]any{"name": "update-email-template"}, func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
		var input creatio.EmailTemplateUpdateOptions
		body, err := json.Marshal(withoutEnvironmentArgs(args, scopeName))
		if err != nil {
			return nil, err
		}
		if err = json.Unmarshal(body, &input); err != nil {
			return nil, err
		}
		name, err := optionalStringArg(args, "update-email-template", "environment-name")
		if err != nil {
			return nil, err
		}
		if _, ok := creatio.DataWriteParseGUID(input.EmailID); !ok {
			return structuredToolResult(creatio.EmailTemplateUpdateFailure("email-id must be a GUID.")), nil
		}
		if strings.TrimSpace(name) == "" {
			return structuredToolResult(creatio.EmailTemplateUpdateFailure("environment-name is required.")), nil
		}
		// Confirmation precedes resolution and remote reads: a refused call never reaches Creatio.
		if !input.Confirm {
			return structuredToolResult(creatio.EmailTemplateUpdateFailure("confirm=true is required; no email content was changed.")), nil
		}
		client, failure, err := envs.resolve("update-email-template", args, scopeName)
		if err != nil {
			return nil, err
		}
		if failure != nil {
			return structuredToolResult(creatio.EmailTemplateUpdateFailure(redacted(failure))), nil
		}
		return structuredToolResult(client.UpdateEmailTemplate(ctx, input)), nil
	}, withAnnotations(toolAnnotations{Destructive: true, Idempotent: true}))
}
