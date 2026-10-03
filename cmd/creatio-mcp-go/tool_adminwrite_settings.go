package main

import (
	"context"
	"fmt"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func init() {
	registerTool(map[string]any{"name": "create-sys-setting"}, invokeAdminWriteCreateSetting,
		withAnnotations(toolAnnotations{ReadOnly: false, Destructive: true, Idempotent: false, OpenWorld: false}))
	registerTool(map[string]any{"name": "update-sys-setting"}, invokeAdminWriteUpdateSetting,
		withAnnotations(toolAnnotations{ReadOnly: false, Destructive: true, Idempotent: true, OpenWorld: false}))
}

func invokeAdminWriteCreateSetting(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
	var in struct {
		Code                string  `json:"code"`
		Name                string  `json:"name"`
		ValueTypeName       string  `json:"value-type-name"`
		Value               *string `json:"value"`
		Description         string  `json:"description"`
		IsCacheable         *bool   `json:"is-cacheable"`
		IsPersonal          *bool   `json:"is-personal"`
		ReferenceSchemaName string  `json:"reference-schema-name"`
	}
	if err := decodeStrictArgs(withoutEnvironmentArgs(args, scopeName), &in); err != nil {
		return nil, fmt.Errorf("decode create-sys-setting arguments: %w", err)
	}
	client, failure, err := envs.resolve("create-sys-setting", args, scopeName)
	if err != nil {
		return nil, err
	}
	if failure != nil {
		return structuredToolResult(creatio.AdminWriteCreateSettingResult{Code: in.Code, ValueTypeName: in.ValueTypeName,
			SysSettingFailure: creatio.SysSettingConfigurationFailure("Failed creating sys-setting.", redacted(failure))}), nil
	}
	return structuredToolResult(client.AdminWriteCreateSysSetting(ctx, creatio.AdminWriteCreateSetting{
		Code: in.Code, Name: in.Name, ValueTypeName: in.ValueTypeName, Value: in.Value,
		Description: in.Description, IsCacheable: in.IsCacheable, IsPersonal: in.IsPersonal,
		ReferenceSchemaName: in.ReferenceSchemaName})), nil
}

func invokeAdminWriteUpdateSetting(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
	var in struct {
		Code          string  `json:"code"`
		Value         *string `json:"value"`
		ValueTypeName string  `json:"value-type-name"`
		ValueFilePath string  `json:"value-file-path"`
	}
	if err := decodeStrictArgs(withoutEnvironmentArgs(args, scopeName), &in); err != nil {
		return nil, fmt.Errorf("decode update-sys-setting arguments: %w", err)
	}
	client, failure, err := envs.resolve("update-sys-setting", args, scopeName)
	if err != nil {
		return nil, err
	}
	if failure != nil {
		return structuredToolResult(creatio.AdminWriteUpdateSettingResult{Code: in.Code,
			SysSettingFailure: creatio.SysSettingConfigurationFailure("Failed updating sys-setting.", redacted(failure))}), nil
	}
	return structuredToolResult(client.AdminWriteUpdateSysSetting(ctx, creatio.AdminWriteUpdateSetting{
		Code: in.Code, Value: in.Value, ValueTypeName: in.ValueTypeName, ValueFilePath: in.ValueFilePath})), nil
}
