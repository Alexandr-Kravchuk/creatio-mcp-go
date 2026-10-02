package main

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// inspectArgKind is the CLR type clio's binder reads an inspect-* argument into. The kind decides both what
// JSON is accepted and the wording of clio's invalid-parameter-type refusal.
type inspectArgKind int

const (
	inspectArgString inspectArgKind = iota
	inspectArgGUID                  // Guid: only the hyphenated 36-character form, reported as "an object"
	inspectArgInt                   // int: an integral number or a decimal string, reported as "a number"
	inspectArgBool                  // bool, reported as "a boolean"
)

type inspectArgSpec struct {
	name        string
	kind        inspectArgKind
	nullable    bool // a Guid?, int? or bool? property (and every string) accepts null
	description string
}

// The four argument records are clio's ManageUserArgs, ManageRoleArgs, ManageLicenseArgs and
// ManageAccessArgs. The write-only arguments are still type-checked, as clio's binder checks them before
// the inspection tool rejects a write action.
var (
	inspectUserArgs = []inspectArgSpec{
		{"action", inspectArgString, true, "list | lock-status"},
		{"id", inspectArgGUID, false, "Exact account GUID; required for lock-status, a filter for list."},
		{"user-login", inspectArgString, true, "Exact login filter for list."},
		{"contact-id", inspectArgGUID, true, "Existing contact GUID (manage-user only)."},
		{"active", inspectArgBool, true, "Explicit active state (manage-user only)."},
		{"external", inspectArgBool, false, "Create an external user (manage-user only)."},
		{"password-env", inspectArgString, true, "Password reference (manage-user only)."},
		{"force-change-password", inspectArgBool, false, "Require a password change at next login (manage-user only)."},
		{"offset", inspectArgInt, false, "Read offset."},
		{"limit", inspectArgInt, false, "Page size from 1 to 200."},
	}
	inspectRoleArgs = []inspectArgSpec{
		{"action", inspectArgString, true, "list | memberships | members | functional-roles"},
		{"id", inspectArgGUID, false, "Role GUID: a filter for list, the role for members and functional-roles."},
		{"name", inspectArgString, true, "Exact name filter for list."},
		{"type", inspectArgInt, true, "0 organization, 1 division, 2 manager, 3 team, 6 functional; a filter for list."},
		{"parent-id", inspectArgGUID, true, "Parent role GUID (manage-role only)."},
		{"user-id", inspectArgGUID, false, "User GUID for memberships."},
		{"functional-id", inspectArgGUID, false, "Functional role GUID (manage-role only)."},
		{"effective", inspectArgBool, false, "Read effective memberships, including inheritance and delegation."},
		{"offset", inspectArgInt, false, "Read offset."},
		{"limit", inspectArgInt, false, "Page size from 1 to 200."},
	}
	inspectLicenseArgs = []inspectArgSpec{
		{"include-manual", inspectArgBool, false, "Redistribution option (manage-license only)."},
		{"action", inspectArgString, true, "user-list | role-list"},
		{"user-id", inspectArgGUID, false, "Target user GUID for user-list."},
		{"role-id", inspectArgGUID, false, "Target role GUID for role-list."},
		{"package-id", inspectArgGUID, false, "License package GUID (manage-license only)."},
		{"offset", inspectArgInt, false, "Read offset."},
		{"limit", inspectArgInt, false, "Read page size from 1 to 200."},
	}
	inspectAccessArgs = []inspectArgSpec{
		{"action", inspectArgString, true, "ip-list | delegations | operations | operation-grants"},
		{"id", inspectArgGUID, false, "IP rule or operation grant record GUID (manage-access only)."},
		{"unit-id", inspectArgGUID, false, "User or role GUID for ip-list; user GUID for delegations."},
		{"grantor-id", inspectArgGUID, false, "Delegating user or role GUID (manage-access only)."},
		{"operation-id", inspectArgGUID, false, "System operation GUID for operation-grants."},
		{"code", inspectArgString, true, "Exact operation code filter for operations."},
		{"begin-ip", inspectArgString, true, "Beginning IPv4 address (manage-access only)."},
		{"end-ip", inspectArgString, true, "Ending IPv4 address (manage-access only)."},
		{"position", inspectArgInt, true, "Operation grant priority (manage-access only)."},
		{"offset", inspectArgInt, false, "Read offset."},
		{"limit", inspectArgInt, false, "Read page size from 1 to 200."},
	}
)

const inspectGuidanceNote = " Read get-guidance name=administration before interpreting inheritance, managers, passwords, licenses or access rules."

func init() {
	registerInspectTool(creatio.InspectUserTool, inspectUserArgs,
		"Inspect user administration of the single configured environment. Actions: list (SysAdminUnit users, types 4, 5 and 7, "+
			"filtered by id or user-login, paged by offset/limit), lock-status ({id, blocked} from AdministrationService.GetIsUserBlocked). "+
			"Returns clio's command envelope { exit-code, execution-log-messages }: one Info message holding the rows as JSON, or one Error.")
	registerInspectTool(creatio.InspectRoleTool, inspectRoleArgs,
		"Inspect role administration of the single configured environment. Actions: list (roles, types 0, 1, 2, 3 and 6, filtered by "+
			"id, name or type), memberships (a user's SysUserInRole rows, or SysAdminUnitInRole with effective), members (a role's users), "+
			"functional-roles (SysFuncRoleInOrgRole of an organizational role). Returns clio's command envelope with the rows as JSON.")
	registerInspectTool(creatio.InspectLicenseTool, inspectLicenseArgs,
		"Inspect license administration of the single configured environment. Actions: user-list (license packages available to a user "+
			"from AdministrationService.GetAvailableLicPackages), role-list (SysLicPackageInRole rows of a role). Returns clio's command "+
			"envelope with the result as JSON.")
	registerInspectTool(creatio.InspectAccessTool, inspectAccessArgs,
		"Inspect access administration of the single configured environment. Actions: ip-list (SysAdminUnitIPRange rows of a user or "+
			"role), delegations (SysAdminUnitGrantedRight rows granted to a user), operations (SysAdminOperation, optionally by exact code), "+
			"operation-grants (SysAdminOperationGrantee rows of an operation). Returns clio's command envelope with the rows as JSON.")
}

func registerInspectTool(tool creatio.InspectTool, specs []inspectArgSpec, description string) {
	properties := map[string]any{}
	for _, spec := range specs {
		property := map[string]any{"description": spec.description}
		switch spec.kind {
		case inspectArgString, inspectArgGUID:
			property["type"] = "string"
		case inspectArgInt:
			property["type"] = "integer"
		case inspectArgBool:
			property["type"] = "boolean"
		}
		switch spec.name {
		case "limit":
			property["default"] = 100
		case "force-change-password":
			property["default"] = true
		}
		properties[spec.name] = property
	}
	registerTool(map[string]any{
		"name":        tool.Name,
		"description": description + inspectGuidanceNote,
		"inputSchema": map[string]any{"type": "object", "required": []string{"action"}, "properties": properties},
	}, func(ctx context.Context, client *creatio.Client, args map[string]any) (*mcp.CallToolResult, error) {
		// clio ignores unknown keys here; only an environment selector is refused, inside the envelope.
		if refusal := refusesConnectionArgs(args); refusal != "" {
			return structuredToolResult(creatio.InspectFailure(refusal)), nil
		}
		bound, err := bindInspectArgs(tool.Name, specs, args)
		if err != nil {
			return nil, err
		}
		return structuredToolResult(client.Inspect(ctx, tool, bound)), nil
	})
}

// bindInspectArgs reads the arguments as clio's System.Text.Json binder does and fills the request with
// clio's defaults: empty GUIDs, offset 0, limit 100. clio reports the first bad value in request order;
// a Go map has none, so the arguments are checked in sorted order.
func bindInspectArgs(tool string, specs []inspectArgSpec, args map[string]any) (creatio.InspectRequest, error) {
	request := creatio.InspectRequest{Limit: 100}
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
			}
		case inspectArgGUID:
			text, ok := value.(string)
			guid, valid := inspectStrictGUID(text)
			if !ok || !valid {
				return request, inspectTypeError(tool, spec.name, "an object")
			}
			guids[spec.name] = guid
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
			}
		case inspectArgBool:
			flag, ok := value.(bool)
			if !ok {
				return request, inspectTypeError(tool, spec.name, "a boolean")
			}
			if spec.name == "effective" {
				request.Effective = flag
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
	return request, nil
}

func inspectTypeError(tool, name, expected string) error {
	return fmt.Errorf("invalid-parameter-type: argument '%s' for MCP tool '%s' must be %s. Received an incompatible JSON value.", name, tool, expected)
}

// inspectStrictGUID accepts what System.Text.Json reads into a Guid: exactly 36 characters with hyphens at
// 8, 13, 18 and 23, hex digits of either case. It returns the lower-case form .NET writes back.
func inspectStrictGUID(text string) (string, bool) {
	if len(text) != 36 {
		return "", false
	}
	for index, char := range text {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			if char != '-' {
				return "", false
			}
		} else if !strings.ContainsRune("0123456789abcdefABCDEF", char) {
			return "", false
		}
	}
	return strings.ToLower(text), true
}

// inspectInt accepts an integral JSON number in the Int32 range, or a string holding one, which clio's
// binder reads with AllowReadingFromString. The dispatcher has already decoded numbers to float64, so 1.0
// cannot be told from 1 here, while clio refuses 1.0.
func inspectInt(value any) (int, bool) {
	switch typed := value.(type) {
	case float64:
		if typed != math.Trunc(typed) || typed < math.MinInt32 || typed > math.MaxInt32 {
			return 0, false
		}
		return int(typed), true
	case string:
		number, err := strconv.ParseInt(typed, 10, 32)
		if err != nil || strings.HasPrefix(typed, "+") {
			return 0, false
		}
		return int(number), true
	}
	return 0, false
}
