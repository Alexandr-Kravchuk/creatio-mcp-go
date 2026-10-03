package main

import (
	"context"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func init() {
	registerTool(map[string]any{
		"name": "get-sys-setting",
		"description": "Read the All-Users default value of a Creatio system setting by code on the target Creatio environment." +
			"An unknown code or an unconfigured setting answers success:true with an empty value; SecureText values are masked as ***. " +
			"Use list-sys-settings to discover codes.",
		"inputSchema": map[string]any{"type": "object", "required": []string{"code"}, "properties": map[string]any{
			"code": map[string]string{"type": "string", "description": "Sys-setting code (e.g., 'SchemaNamePrefix')."},
		}},
	}, invokeGetSysSetting)
	registerTool(map[string]any{
		"name": "list-sys-settings",
		"description": "List Creatio system settings with their All-Users default values, value-type-name and is-cacheable/is-personal flags. " +
			"Binary settings are listed with value <binary>; SecureText values are masked as ***; a setting without an All-Users value reads undefined.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
	}, invokeListSysSettings)
}

// Both sys-setting tools are lenient in clio: unknown keys are ignored. A target that cannot be resolved is
// classified as a Configuration failure.
func invokeGetSysSetting(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
	code, err := optionalStringArg(args, "get-sys-setting", "code")
	if err != nil {
		return nil, err
	}
	client, failure, err := envs.resolve("get-sys-setting", args, scopeName)
	if err != nil {
		return nil, err
	}
	if failure != nil {
		return structuredToolResult(creatio.SysSettingGetResult{Code: code,
			SysSettingFailure: creatio.SysSettingConfigurationFailure(creatio.SysSettingFailureLabel(false), redacted(failure))}), nil
	}
	return structuredToolResult(client.GetSysSetting(ctx, code)), nil
}

func invokeListSysSettings(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
	client, failure, err := envs.resolve("list-sys-settings", args, scopeName)
	if err != nil {
		return nil, err
	}
	if failure != nil {
		return structuredToolResult(creatio.SysSettingsListResult{Settings: []creatio.SysSettingItem{},
			SysSettingFailure: creatio.SysSettingConfigurationFailure(creatio.SysSettingFailureLabel(true), redacted(failure))}), nil
	}
	return structuredToolResult(client.ListSysSettings(ctx)), nil
}
