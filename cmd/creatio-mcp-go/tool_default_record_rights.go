package main

// set-default-record-rights and apply-default-record-rights (clio 8.1.0.142 SetDefaultRecordRightsTool /
// ApplyDefaultRecordRightsTool).

import (
	"context"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func init() {
	registerTool(rightsContract(defaultRecordRightsContracts, "set-default-record-rights"), invokeSetDefaultRecordRights,
		withAnnotations(toolAnnotations{Destructive: true, Idempotent: true}))
	registerTool(rightsContract(defaultRecordRightsContracts, "apply-default-record-rights"), invokeApplyDefaultRecordRights,
		withAnnotations(toolAnnotations{Destructive: true}))
}

var setDefaultRecordRightsArguments = []string{"environment-name", "entity-schema-name", "author", "grantee", "operations", "level",
	"do-not-apply-for-manager", "revoke", "enable-record-permissions", "disable-record-permissions", "preview"}

func invokeSetDefaultRecordRights(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
	bind := rightsArgs{tool: "set-default-record-rights", args: args}
	var input creatio.RecordRightsSetRequest
	var err error
	if input.EntitySchemaName, err = bind.str("entity-schema-name"); err != nil {
		return nil, err
	}
	for _, field := range []struct {
		name   string
		target **string
	}{{"author", &input.Author}, {"grantee", &input.Grantee}, {"operations", &input.Operations}, {"level", &input.Level}} {
		if *field.target, err = bind.strPtr(field.name); err != nil {
			return nil, err
		}
	}
	if input.DoNotApplyForManager, err = bind.boolPtr("do-not-apply-for-manager"); err != nil {
		return nil, err
	}
	for _, field := range []struct {
		name   string
		target *bool
	}{{"revoke", &input.Revoke}, {"enable-record-permissions", &input.Enable}, {"disable-record-permissions", &input.Disable},
		{"preview", &input.Preview}} {
		if *field.target, err = bind.flag(field.name); err != nil {
			return nil, err
		}
	}
	client, refusal, err := rightsTarget(ctx, envs, bind.tool, args, setDefaultRecordRightsArguments)
	if err != nil || refusal != nil {
		return refusal, err
	}
	return structuredToolResult(client.SetDefaultRecordRights(ctx, input)), nil
}

var applyDefaultRecordRightsArguments = []string{"environment-name", "entity-schema-name", "wait", "timeout-seconds"}

func invokeApplyDefaultRecordRights(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
	bind := rightsArgs{tool: "apply-default-record-rights", args: args}
	var input creatio.RecordRightsApplyRequest
	var err error
	if input.EntitySchemaName, err = bind.str("entity-schema-name"); err != nil {
		return nil, err
	}
	if input.Wait, err = bind.boolPtr("wait"); err != nil {
		return nil, err
	}
	if input.TimeoutSeconds, err = bind.intPtr("timeout-seconds"); err != nil {
		return nil, err
	}
	client, refusal, err := rightsTarget(ctx, envs, bind.tool, args, applyDefaultRecordRightsArguments)
	if err != nil || refusal != nil {
		return refusal, err
	}
	return structuredToolResult(client.ApplyDefaultRecordRights(ctx, input)), nil
}
