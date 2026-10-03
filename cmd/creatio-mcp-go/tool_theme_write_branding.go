package main

// Branding write tools (clio master 914dab286: UploadImageTool; the arguments, checks and answers match
// 8.1.0.134). Each binds clio's argument record (themeWriteArgSpec), answers clio's own refusals inside its
// result, then resolves the environment; a resolution failure is the redacted exception message, as
// clio's BaseTool.ExecuteResolved reports it.

import (
	"context"
	"strings"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/redact"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var uploadImageSpec = themeWriteArgSpec{
	tool: "upload-image", strings: []string{"environment-name", "file"},
	hint: "Valid: environment-name, file.",
}

func init() {
	// clio: [McpServerTool(ReadOnly = false, Destructive = false, Idempotent = false, OpenWorld = false)]
	registerTool(map[string]any{"name": "upload-image"}, invokeUploadImage, withAnnotations(toolAnnotations{}))
}

func invokeUploadImage(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
	bound, err := uploadImageSpec.bind(args)
	if err != nil {
		return nil, err
	}
	fail := func(message string) *mcp.CallToolResult {
		return structuredToolResult(creatio.ImageUploadFailure(message))
	}
	if refusal := uploadImageSpec.aliasError(bound); refusal != "" {
		return fail(refusal), nil
	}
	if strings.TrimSpace(bound.str("environment-name")) == "" {
		return fail(themeWriteEnvironmentRequired), nil
	}
	if strings.TrimSpace(bound.str("file")) == "" {
		return fail("file is required and cannot be empty."), nil
	}
	client, failure, err := envs.resolve("upload-image", bound.raw, scopeName)
	if err != nil {
		return nil, err
	}
	if failure != nil {
		return fail(redacted(failure)), nil
	}
	result := client.UploadImage(ctx, bound.str("file"))
	if !result.Success {
		message := result.Error
		if strings.TrimSpace(message) == "" {
			message = "UploadImage returned success=false."
		} else {
			message = redact.Text(message)
		}
		return fail(message), nil
	}
	return structuredToolResult(result), nil
}
