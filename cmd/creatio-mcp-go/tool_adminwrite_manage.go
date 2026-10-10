package main

import (
	"context"
	"sort"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// manage-user, manage-role, manage-license and manage-access: clio's ManageUserTool, ManageRoleTool,
// ManageLicenseTool and ManageAccessTool (the Manage methods). They take the same argument records as the
// inspect-* tools, bound the same way; the contract served is clio's.
func init() {
	destructive := withAnnotations(toolAnnotations{ReadOnly: false, Destructive: true, Idempotent: false, OpenWorld: false})
	for _, entry := range []struct {
		tool  creatio.AdminWriteManageTool
		specs []inspectArgSpec
	}{
		{creatio.AdminWriteManageUser, inspectUserArgs},
		{creatio.AdminWriteManageRole, inspectRoleArgs},
		{creatio.AdminWriteManageLicense, inspectLicenseArgs},
		{creatio.AdminWriteManageAccess, inspectAccessArgs},
	} {
		tool, specs := entry.tool, entry.specs
		registerTool(map[string]any{"name": tool.Name}, func(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
			request, err := bindAdminWriteManageArgs(tool.Name, specs, args)
			if err != nil {
				return nil, err
			}
			client, failure, err := envs.resolve(tool.Name, args, scopeName)
			if err != nil {
				return nil, err
			}
			if failure != nil {
				return resolverFailureEnvelope(failure), nil
			}
			return structuredToolResult(client.ManageAdministration(ctx, tool, request)), nil
		}, destructive)
	}
}

// bindAdminWriteManageArgs reads every argument of clio's Manage*Args record as its System.Text.Json binder
// does, with clio's defaults: empty GUIDs, offset 0, limit 100, force-change-password true. The arguments
// are checked in sorted order (a Go map has no request order).
func bindAdminWriteManageArgs(tool string, specs []inspectArgSpec, args map[string]any) (creatio.AdminWriteManageRequest, error) {
	request := creatio.AdminWriteManageRequest{InspectRequest: creatio.InspectRequest{Limit: 100}, ForceChangePassword: true}
	guids := map[string]string{}
	ordered := append([]inspectArgSpec{}, specs...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].name < ordered[j].name })
	for _, spec := range ordered {
		value, present := args[spec.name]
		if !present || (value == nil && spec.nullable) {
			continue
		}
		switch spec.kind {
		case inspectArgString:
			text, ok := value.(string)
			if !ok {
				return request, inspectTypeError(tool, spec.name, "a string")
			}
			switch spec.name {
			case "action":
				request.Action = text
			case "user-login":
				request.UserLogin = &text
			case "name":
				request.Name = &text
			case "code":
				request.Code = &text
			case "password-env":
				request.PasswordEnv = &text
			case "begin-ip":
				request.BeginIP = &text
			case "end-ip":
				request.EndIP = &text
			}
		case inspectArgGUID:
			text, ok := value.(string)
			guid, valid := inspectStrictGUID(text)
			if !ok || !valid {
				return request, inspectTypeError(tool, spec.name, "an object")
			}
			guids[spec.name] = guid
			switch spec.name {
			case "contact-id":
				request.ContactID = &guid
			case "parent-id":
				request.ParentID = &guid
			}
		case inspectArgInt:
			number, ok := inspectInt(value)
			if !ok {
				return request, inspectTypeError(tool, spec.name, "a number")
			}
			switch spec.name {
			case "offset":
				request.Offset = number
			case "limit":
				request.Limit = number
			case "type":
				request.Type = &number
			case "position":
				request.Position = &number
			}
		case inspectArgBool:
			flag, ok := value.(bool)
			if !ok {
				return request, inspectTypeError(tool, spec.name, "a boolean")
			}
			switch spec.name {
			case "effective":
				request.Effective = flag
			case "active":
				request.Active = &flag
			case "external":
				request.External = flag
			case "force-change-password":
				request.ForceChangePassword = flag
			case "include-manual":
				request.IncludeManual = flag
			}
		}
	}
	guid := func(name string) string {
		if value, ok := guids[name]; ok {
			return value
		}
		return "00000000-0000-0000-0000-000000000000"
	}
	request.ID, request.UserID, request.RoleID = guid("id"), guid("user-id"), guid("role-id")
	request.UnitID, request.OperationID = guid("unit-id"), guid("operation-id")
	request.FunctionalID, request.PackageID, request.GrantorID = guid("functional-id"), guid("package-id"), guid("grantor-id")
	return request, nil
}
