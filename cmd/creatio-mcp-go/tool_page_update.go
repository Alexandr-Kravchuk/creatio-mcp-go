package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// update-page: clio's PageUpdateTool. The offline gates run first; then the .clio-pages baseline arms the
// conflict check, the command saves, and the baseline is refreshed from the post-save checksum.
func init() {
	registerTool(map[string]any{"name": "update-page"}, func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
		var input struct {
			SchemaName         string `json:"schema-name"`
			Body               string `json:"body"`
			BodyFile           string `json:"body-file"`
			DryRun             *bool  `json:"dry-run"`
			Resources          string `json:"resources"`
			OptionalProperties string `json:"optional-properties"`
			Verify             *bool  `json:"verify"`
			Mode               string `json:"mode"`
			TargetPackageUID   string `json:"target-package-uid"`
			TargetSchemaUID    string `json:"target-schema-uid"`
			Force              *bool  `json:"force"`
			OutputDirectory    string `json:"output-directory"`
			Checksum           string `json:"checksum"`
			Validate           *bool  `json:"validate"`
			IncludeOperations  *bool  `json:"include-operations"`
		}
		if err := decodeStrictArgs(withoutEnvironmentArgs(args, scopeDirect), &input); err != nil {
			return nil, fmt.Errorf("decode update-page arguments: %w", err)
		}
		client, failure, err := envs.resolve("update-page", args, scopeDirect)
		if err != nil {
			return nil, err
		}
		request := creatio.PageUpdateRequest{SchemaName: input.SchemaName, Body: input.Body, BodyFile: input.BodyFile,
			DryRun: input.DryRun != nil && *input.DryRun, Resources: input.Resources, OptionalProperties: input.OptionalProperties,
			Mode: input.Mode, TargetPackageUID: input.TargetPackageUID, TargetSchemaUID: input.TargetSchemaUID,
			Force: input.Force != nil && *input.Force, Validate: input.Validate == nil || *input.Validate,
			ExpectedChecksum: strings.TrimSpace(input.Checksum), EnvironmentName: pageWriteArgText(args, "environment-name"),
			EnvironmentURI: pageWriteArgText(args, "uri")}
		return structuredToolResult(updatePage(ctx, client, failure, &request, input.OutputDirectory,
			input.Verify != nil && *input.Verify, input.IncludeOperations)), nil
	}, withAnnotations(toolAnnotations{ReadOnly: false, Destructive: true, Idempotent: false, OpenWorld: false}))
}

func updatePage(ctx context.Context, client *creatio.Client, failure error, request *creatio.PageUpdateRequest,
	outputDirectory string, verify bool, includeOperations *bool) creatio.PageUpdateResponse {
	preflight := creatio.PageUpdateCheck(request)
	if preflight.Failure != nil {
		response := *preflight.Failure
		if preflight.SyntaxOnly && failure != nil {
			response = creatio.PageUpdateResponse{Error: failure.Error()}
		}
		response.MarkDryRunFailure(request.DryRun, request.SchemaName)
		return response
	}
	if failure != nil {
		response := creatio.PageUpdateResponse{Error: redacted(failure)}
		response.MarkDryRunFailure(request.DryRun, request.SchemaName)
		return response
	}
	metaPath, refresh, baselineWarning := client.ArmPageBaseline(request, outputDirectory)
	response := client.UpdatePage(ctx, request)
	if response.Success && verify {
		response.Page = client.VerifyPage(ctx, request.SchemaName, includeOperations)
	}
	if response.ContentValidationFailure && !strings.Contains(response.Error, creatio.PageWriteEscapeHatchHint) {
		response.Error += creatio.PageWriteEscapeHatchHint
	}
	refreshWarning := ""
	if (refresh || request.ConditionalBaselineApplied()) && response.Success && !request.DryRun {
		refreshWarning = creatio.RefreshPageBaseline(metaPath, request, &response)
	}
	warnings := append(append([]string{}, preflight.Warnings...), response.Warnings...)
	for _, warning := range []string{baselineWarning, refreshWarning} {
		if strings.TrimSpace(warning) != "" {
			warnings = append(warnings, warning)
		}
	}
	response.Warnings = nil
	if len(warnings) > 0 {
		response.Warnings = warnings
	}
	creatio.PageWriteSucceededWithGap(&response, request.Validate)
	response.MarkDryRunFailure(request.DryRun, request.SchemaName)
	return response
}

// pageWriteArgText reads a string argument from the flat or wrapped arguments, or "" when absent.
func pageWriteArgText(args map[string]any, name string) string {
	if wrapped, ok := args["args"].(map[string]any); ok {
		if value, ok := wrapped[name].(string); ok {
			return value
		}
	}
	value, _ := args[name].(string)
	return value
}
