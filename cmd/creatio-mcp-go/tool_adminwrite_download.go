package main

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/redact"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// download-sys-setting-file: clio's DownloadSysSettingFileTool. The file name and code are checked before the
// environment is resolved; every later failure is the redacted message in clio's Failure envelope.
func init() {
	registerTool(map[string]any{"name": "download-sys-setting-file"}, invokeAdminWriteDownload,
		withAnnotations(toolAnnotations{ReadOnly: false, Destructive: false, Idempotent: false, OpenWorld: false}))
}

func invokeAdminWriteDownload(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
	const tool = "download-sys-setting-file"
	text, err := adminWriteStrings(args, tool, "environment-name", "code", "file-name")
	if err != nil {
		return nil, err
	}
	fileName := adminWriteDeref(text["file-name"])
	if adminWriteBlankArg(text["file-name"]) || !filepath.IsAbs(fileName) {
		return structuredToolResult(creatio.AdminWriteDownloadFailure("file-name must be an absolute path including the destination filename.")), nil
	}
	if adminWriteBlankArg(text["environment-name"]) || adminWriteBlankArg(text["code"]) {
		return structuredToolResult(creatio.AdminWriteDownloadFailure("environment-name and code are required.")), nil
	}
	client, failure, err := envs.resolve(tool, args, scopeName)
	if err != nil {
		return nil, err
	}
	if failure != nil {
		return structuredToolResult(creatio.AdminWriteDownloadFailure(redacted(failure))), nil
	}
	result, err := client.DownloadSysSettingFile(ctx, adminWriteDeref(text["code"]), fileName)
	if err != nil {
		var requirement *creatio.AdminWriteRequirementError
		if errors.As(err, &requirement) {
			return structuredToolResult(creatio.AdminWriteDownloadFailure(requirement.Message)), nil
		}
		return structuredToolResult(creatio.AdminWriteDownloadFailure(redact.Text(err.Error()))), nil
	}
	return structuredToolResult(result), nil
}

func adminWriteBlankArg(value *string) bool {
	if value == nil {
		return true
	}
	for _, r := range *value {
		if r != ' ' && r != '\t' && r != '\n' && r != '\r' && r != '\v' && r != '\f' && r != 0x85 && r != 0xA0 {
			return false
		}
	}
	return true
}
