package main

import (
	"context"
	"fmt"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// create-sys-setting and update-sys-setting: clio's SysSettingCreateTool and SysSettingUpdateTool. Their
// argument records are lenient (unknown keys are ignored); a value of the wrong JSON type is clio's
// invalid-parameter-type refusal.
func init() {
	registerTool(map[string]any{"name": "create-sys-setting"}, invokeAdminWriteCreateSetting,
		withAnnotations(toolAnnotations{ReadOnly: false, Destructive: true, Idempotent: false, OpenWorld: false}))
	registerTool(map[string]any{"name": "update-sys-setting"}, invokeAdminWriteUpdateSetting,
		withAnnotations(toolAnnotations{ReadOnly: false, Destructive: true, Idempotent: true, OpenWorld: false}))
}

// adminWriteStringArg is a string? argument: nil when absent or null.
func adminWriteStringArg(args map[string]any, tool, name string) (*string, error) {
	value, ok := args[name]
	if !ok || value == nil {
		return nil, nil
	}
	text, ok := value.(string)
	if !ok {
		return nil, fmt.Errorf("invalid-parameter-type: argument '%s' for MCP tool '%s' must be a string. Received an incompatible JSON value.", name, tool)
	}
	return &text, nil
}

// adminWriteBoolArg is a bool? argument: nil when absent or null.
func adminWriteBoolArg(args map[string]any, tool, name string) (*bool, error) {
	value, ok := args[name]
	if !ok || value == nil {
		return nil, nil
	}
	flag, ok := value.(bool)
	if !ok {
		return nil, fmt.Errorf("invalid-parameter-type: argument '%s' for MCP tool '%s' must be a boolean. Received an incompatible JSON value.", name, tool)
	}
	return &flag, nil
}

func adminWriteStrings(args map[string]any, tool string, names ...string) (map[string]*string, error) {
	values := map[string]*string{}
	for _, name := range names {
		value, err := adminWriteStringArg(args, tool, name)
		if err != nil {
			return nil, err
		}
		values[name] = value
	}
	return values, nil
}

func invokeAdminWriteCreateSetting(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
	const tool = "create-sys-setting"
	text, err := adminWriteStrings(args, tool, "code", "name", "value-type-name", "value", "description", "reference-schema-name")
	if err != nil {
		return nil, err
	}
	cacheable, err := adminWriteBoolArg(args, tool, "is-cacheable")
	if err != nil {
		return nil, err
	}
	personal, err := adminWriteBoolArg(args, tool, "is-personal")
	if err != nil {
		return nil, err
	}
	input := creatio.AdminWriteCreateSetting{Code: text["code"], Name: text["name"], ValueTypeName: text["value-type-name"],
		Value: text["value"], Description: text["description"], ReferenceSchemaName: text["reference-schema-name"],
		IsCacheable: cacheable, IsPersonal: personal}
	client, failure, err := envs.resolve(tool, args, scopeName)
	if err != nil {
		return nil, err
	}
	if failure != nil {
		return structuredToolResult(creatio.AdminWriteCreateFailure(adminWriteDeref(input.Code), adminWriteDeref(input.ValueTypeName),
			creatio.SysSettingConfigurationFailure("Failed creating sys-setting.", redacted(failure)))), nil
	}
	return structuredToolResult(client.CreateSysSetting(ctx, input)), nil
}

func invokeAdminWriteUpdateSetting(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
	const tool = "update-sys-setting"
	text, err := adminWriteStrings(args, tool, "code", "value", "value-type-name", "value-file-path")
	if err != nil {
		return nil, err
	}
	input := creatio.AdminWriteUpdateSetting{Code: text["code"], Value: text["value"], ValueTypeName: text["value-type-name"],
		ValueFilePath: text["value-file-path"]}
	client, failure, err := envs.resolve(tool, args, scopeName)
	if err != nil {
		return nil, err
	}
	if failure != nil {
		return structuredToolResult(creatio.AdminWriteUpdateFailure(adminWriteDeref(input.Code),
			creatio.SysSettingConfigurationFailure("Failed updating sys-setting.", redacted(failure)))), nil
	}
	return structuredToolResult(client.UpdateSysSetting(ctx, input)), nil
}

func adminWriteDeref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
