package creatio

// manage-user, manage-role, manage-license and manage-access: clio master's ManageUserCommand,
// ManageRoleCommand, ManageLicenseCommand and ManageAccessCommand over AdministrationService and
// AdministrationClient. Their read actions are the inspect-* tools' (inspect.go); the writes call the native
// AdministrationService, RightsService, GridUtilitiesService and DataService routes clio calls, in clio's
// order, and verify the persisted state the way clio does. A refusal clio words itself (ArgumentException,
// AdministrationStateException) is shown; any other failure becomes the command's fixed message, because an
// administration endpoint can echo credentials.

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
)

const (
	adminWriteAllEmployees         = "a29a3ba5-4b0d-de11-9a51-005056c00008"
	adminWriteAllExternalUsers     = "720b771c-e7a7-4f31-9cfb-52cd21c3739f"
	adminWriteSystemAdministrators = "83a43ebc-f36b-1410-298d-001e8c82bcad"
	adminWriteUserTypeID           = "472e97c7-6bd7-df11-9b2a-001d60e938c6"
	adminWritePasswordPrefix       = "CLIO_ADMIN_PASSWORD_"
	// adminWriteSafePasswordVersion is the Creatio floor for password payloads (RequiresSafePasswordLogging).
	adminWriteSafePasswordVersion = "10.1.585.0"
	adminWriteGateHint            = "Run clio install-gate for this environment."
)

// AdminWriteManageTool is one of the four manage-* tools: its inspect twin (actions and failure text) and the
// message for an unknown action.
type AdminWriteManageTool struct {
	Name    string
	inspect InspectTool
	unknown string
}

var (
	AdminWriteManageUser = AdminWriteManageTool{Name: "manage-user", inspect: InspectUserTool,
		unknown: "Unknown user action. Use list, create, update, delete, lock-status, unlock or password."}
	AdminWriteManageRole = AdminWriteManageTool{Name: "manage-role", inspect: InspectRoleTool,
		unknown: "Unknown role action. See manage-role help for supported actions."}
	AdminWriteManageLicense = AdminWriteManageTool{Name: "manage-license", inspect: InspectLicenseTool,
		unknown: "Unknown license action. See manage-license help."}
	AdminWriteManageAccess = AdminWriteManageTool{Name: "manage-access", inspect: InspectAccessTool,
		unknown: "Unknown access action. See manage-access help."}
)

// AdminWriteManageRequest adds the write arguments to the inspection request. GUIDs are lower-case; a GUID
// the caller left out is the empty GUID (clio's non-nullable Guid), a nullable one is nil.
type AdminWriteManageRequest struct {
	InspectRequest
	ContactID           *string
	ParentID            *string
	Active              *bool
	External            bool
	PasswordEnv         *string
	ForceChangePassword bool
	FunctionalID        string
	PackageID           string
	IncludeManual       bool
	GrantorID           string
	BeginIP             *string
	EndIP               *string
	Position            *int
}

// ManageAdministration runs one manage-* action as clio's InternalExecute does: the version and package
// gates the action triggers, then the command.
func (c *Client) ManageAdministration(ctx context.Context, tool AdminWriteManageTool, request AdminWriteManageRequest) CommandResult {
	if refusal, err := c.adminWriteManageGates(ctx, tool, request.Action); err != nil {
		return NewCommandResult(-1, "Error", "[InvalidOperationException] "+err.Error())
	} else if refusal != nil {
		return AdminWriteRequirementFailure(refusal)
	}
	result, err := c.adminWriteManageAction(ctx, tool, request)
	if err != nil {
		var refusal inspectRefusal
		if errors.As(err, &refusal) {
			return CommandFailure(refusal.Error())
		}
		return CommandFailure(tool.inspect.failure)
	}
	answer := CommandResult{ExitCode: 0, Messages: []LogMessage{{MessageType: "Info", Value: string(result.stjJSON())}}}
	if tool.Name == "manage-role" && result.isObject() {
		if completed := result.get("completed"); completed != nil && completed.kind == jkBool && !completed.flag {
			answer.ExitCode = 1
			answer.Messages = append(answer.Messages, LogMessage{MessageType: "Error", Value: "The association was removed but " +
				"follow-up processing failed. Inspect the receipt and effective membership; use role-redistribute if license reconciliation is required."})
		}
	}
	return answer
}

// adminWriteManageGates are the RequiresCreatioVersion and RequiresPackage properties of clio's options:
// the version first, then the package, each only for the action that triggers it.
func (c *Client) adminWriteManageGates(ctx context.Context, tool AdminWriteManageTool, action string) (*AdminWriteRequirementError, error) {
	switch tool.Name + "/" + action {
	case "manage-user/create", "manage-user/password":
		return c.AdminWriteEnsureVersion(ctx, adminWriteSafePasswordVersion,
			"Password writes require the verified native logging behavior; older implementations may log plaintext passwords.", false), nil
	case "manage-role/remove-functional", "manage-license/role-redistribute":
		return c.AdminWriteEnsurePackage(ctx, "cliogate", "2.0.0.50", adminWriteGateHint)
	case "manage-access/operation-position":
		if refusal := c.AdminWriteEnsureVersion(ctx, adminWriteSafePasswordVersion, "", false); refusal != nil {
			return refusal, nil
		}
		return c.AdminWriteEnsurePackage(ctx, "cliogate", "2.0.0.52", adminWriteGateHint)
	}
	return nil, nil
}

func (c *Client) adminWriteManageAction(ctx context.Context, tool AdminWriteManageTool, r AdminWriteManageRequest) (*jnode, error) {
	if inspectAccepts(tool.inspect, r.Action) {
		return c.inspectAction(ctx, tool.inspect, r.InspectRequest)
	}
	switch tool.Name + "/" + r.Action {
	case "manage-user/create":
		password, err := adminWriteReadPassword(r.PasswordEnv)
		if err != nil {
			return nil, err
		}
		contact := emptyGUID
		if r.ContactID != nil {
			contact = *r.ContactID
		}
		return c.adminWriteCreateUser(ctx, r.ID, r.UserLogin, contact, r.External, password, r.ForceChangePassword)
	case "manage-user/update":
		return c.adminWriteUpdateUser(ctx, r.ID, r.UserLogin, r.ContactID, r.Active)
	case "manage-user/delete":
		if err := c.adminWriteDeleteUser(ctx, r.ID); err != nil {
			return nil, err
		}
		return toJNode(orderedFields{{"id", r.ID}, {"deleted", true}}), nil
	case "manage-user/unlock":
		return c.adminWriteUnlockUser(ctx, r.ID)
	case "manage-user/password":
		password, err := adminWriteReadPassword(r.PasswordEnv)
		if err != nil {
			return nil, err
		}
		if err := c.adminWriteChangePassword(ctx, r.ID, password, r.ForceChangePassword); err != nil {
			return nil, err
		}
		return toJNode(orderedFields{{"id", r.ID}, {"passwordAccepted", true}, {"authenticationVerified", false},
			{"ForceChangePassword", r.ForceChangePassword}}), nil
	case "manage-role/create":
		roleType := -1
		if r.Type != nil {
			roleType = *r.Type
		}
		parent := emptyGUID
		if r.ParentID != nil {
			parent = *r.ParentID
		}
		return c.adminWriteCreateRole(ctx, r.ID, r.Name, roleType, parent)
	case "manage-role/update":
		return c.adminWriteUpdateRole(ctx, r.ID, r.Name, r.ParentID)
	case "manage-role/delete":
		if err := c.adminWriteDeleteRole(ctx, r.ID); err != nil {
			return nil, err
		}
		return toJNode(orderedFields{{"id", r.ID}, {"deleted", true}}), nil
	case "manage-role/ensure-manager":
		parent := emptyGUID
		if r.ParentID != nil {
			parent = *r.ParentID
		}
		return c.adminWriteEnsureManager(ctx, parent)
	case "manage-role/add-member", "manage-role/remove-member":
		return c.adminWriteSetMembership(ctx, r.UserID, r.ID, r.Action == "remove-member")
	case "manage-role/add-functional", "manage-role/remove-functional":
		return c.adminWriteSetFunctionalRole(ctx, r.ID, r.FunctionalID, r.Action == "remove-functional")
	case "manage-license/user-assign", "manage-license/user-remove":
		return c.adminWriteSetUserLicense(ctx, r.UserID, r.PackageID, r.Action == "user-remove")
	case "manage-license/role-assign", "manage-license/role-remove":
		rows, err := c.adminWriteSetRoleLicense(ctx, r.RoleID, r.PackageID, r.Action == "role-remove")
		if err != nil {
			return nil, err
		}
		return toJNode(orderedFields{{"associations", rows}, {"redistributionRequired", true}, {"userAssignmentsVerified", false}}), nil
	case "manage-license/role-redistribute":
		if _, err := c.inspectRole(ctx, r.RoleID); err != nil {
			return nil, err
		}
		return c.adminWritePost(ctx, "rest/CreatioApiGateway/ScheduleRoleLicenseRedistribution", "ScheduleRoleLicenseRedistributionResult",
			orderedFields{{"roleId", r.RoleID}, {"redistributeManuallyAssignedLicenses", r.IncludeManual}}, adminWriteSuccessObject)
	case "manage-access/ip-create", "manage-access/ip-update":
		return c.adminWriteSetIPRange(ctx, r.ID, r.UnitID, r.BeginIP, r.EndIP, r.Action == "ip-create")
	case "manage-access/ip-delete":
		if err := c.adminWriteDeleteIPRange(ctx, r.ID, r.UnitID); err != nil {
			return nil, err
		}
		return toJNode(orderedFields{{"id", r.ID}, {"deleted", true}}), nil
	case "manage-access/delegate", "manage-access/revoke-delegation":
		return c.adminWriteSetDelegation(ctx, r.GrantorID, r.UnitID, r.Action == "revoke-delegation")
	case "manage-access/grant-operation", "manage-access/deny-operation", "manage-access/revoke-operation":
		return c.adminWriteSetOperationGrant(ctx, r.OperationID, r.UnitID, r.Action == "grant-operation", r.Action == "revoke-operation")
	case "manage-access/operation-position":
		if r.Position == nil {
			return nil, inspectRefusal("Supply an explicit position for operation-position.")
		}
		return c.adminWriteSetOperationPosition(ctx, r.ID, *r.Position)
	}
	return nil, inspectRefusal(tool.unknown)
}

// adminWriteReadPassword is ManageUserCommand.ReadPassword: the password comes from the named
// CLIO_ADMIN_PASSWORD_<SUFFIX> variable of this server's process, never from the arguments.
func adminWriteReadPassword(reference *string) (string, error) {
	const invalid = "Supply a populated password reference named CLIO_ADMIN_PASSWORD_<SUFFIX> using uppercase letters, digits or underscores."
	if reference == nil || !strings.HasPrefix(*reference, adminWritePasswordPrefix) || len(*reference) == len(adminWritePasswordPrefix) {
		return "", inspectRefusal(invalid)
	}
	for _, r := range (*reference)[len(adminWritePasswordPrefix):] {
		if !(r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_') {
			return "", inspectRefusal(invalid)
		}
	}
	password := os.Getenv(*reference)
	if password == "" {
		return "", inspectRefusal(invalid)
	}
	return password, nil
}

// --- units -----------------------------------------------------------------------------------------------

// adminWriteListAny is ListUnits(id, name, null, offset, limit) without a type filter, as clio's existence
// checks call it.
func (c *Client) adminWriteListAny(ctx context.Context, id, name *string, limit int) (*jnode, error) {
	filters := []inspectFilter{}
	if id != nil {
		if err := inspectRequireID(*id); err != nil {
			return nil, err
		}
		filters = append(filters, inspectFilter{"Id", inspectGUID(*id)})
	}
	if name != nil {
		if err := adminWriteRequireName(*name); err != nil {
			return nil, err
		}
		filters = append(filters, inspectFilter{"Name", *name})
	}
	return c.inspectSelect(ctx, "SysAdminUnit", inspectUnitColumns, filters, 0, limit)
}

func adminWriteRequireName(name string) error {
	if dataWriteBlank(name) || utf16Length(name) > 250 {
		return inspectRefusal("Provide a nonblank name of at most 250 characters.")
	}
	return nil
}

// adminWriteGetUnit is GetUnit: the one SysAdminUnit row with the ID.
func (c *Client) adminWriteGetUnit(ctx context.Context, id string) (*jnode, error) {
	rows, err := c.inspectListUnitsAnyType(ctx, id)
	if err != nil {
		return nil, err
	}
	if len(rows.items) != 1 {
		return nil, inspectRefusal("The administration ID does not identify one existing user or role.")
	}
	return rows.items[0], nil
}

func (c *Client) adminWriteGetUser(ctx context.Context, id, message string) (*jnode, error) {
	unit, err := c.adminWriteGetUnit(ctx, id)
	if err != nil {
		return nil, err
	}
	if !inspectIsUserType(adminWriteType(unit)) {
		return nil, inspectRefusal(message)
	}
	return unit, nil
}

func (c *Client) adminWriteGetRole(ctx context.Context, id string) (*jnode, error) {
	unit, err := c.adminWriteGetUnit(ctx, id)
	if err != nil {
		return nil, err
	}
	if !inspectIsRoleType(adminWriteType(unit)) {
		return nil, inspectRefusal("The ID must identify a role.")
	}
	return unit, nil
}

// adminWriteType is TypeOf: SysAdminUnitTypeValue as an integer (-1 when it is not one, which no check accepts).
func adminWriteType(unit *jnode) int {
	return adminWriteInt(unit, "SysAdminUnitTypeValue")
}

func adminWriteInt(row *jnode, column string) int {
	value := row.get(column)
	if value == nil || value.kind != jkInteger {
		return -1
	}
	number, err := strconv.Atoi(value.text)
	if err != nil {
		return -1
	}
	return number
}

func adminWriteBool(row *jnode, column string) bool {
	value := row.get(column)
	return value != nil && value.kind == jkBool && value.flag
}

func adminWriteString(row *jnode, column string) string {
	value := row.get(column)
	if value == nil || value.kind != jkString {
		return ""
	}
	return value.text
}

// adminWriteLookupID is LookupId: a lookup object's value, a GUID string, or the empty GUID.
func adminWriteLookupID(row *jnode, column string) string {
	value := row.get(column)
	if value.isObject() {
		value = value.get("value")
	}
	if value == nil || value.kind != jkString {
		return emptyGUID
	}
	if guid, ok := adminWriteParseGUID(value.text); ok {
		return guid
	}
	return emptyGUID
}

func adminWriteNewGUID() string {
	random := make([]byte, 16)
	_, _ = rand.Read(random)
	random[6] = random[6]&0x0f | 0x40
	random[8] = random[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", random[0:4], random[4:6], random[6:8], random[8:10], random[10:16])
}

// --- native calls ----------------------------------------------------------------------------------------

type adminWriteResponseKind int

const (
	adminWriteErrorString adminWriteResponseKind = iota
	adminWriteSuccessObject
	adminWriteTrue
	adminWriteBoolean
)

// adminWritePost is AdministrationClient.Post: one request, then the method-specific result contract.
func (c *Client) adminWritePost(ctx context.Context, route, resultProperty string, body orderedFields, kind adminWriteResponseKind) (*jnode, error) {
	payload := toJNode(body)
	response, err := c.callService(ctx, serviceCall{Route: route, Body: payload.stjJSON(), Timeout: inspectSelectTimeout, Limit: inspectMaxResponseBytes})
	if err != nil {
		return nil, err
	}
	root, err := inspectParseJSON(response)
	if err != nil {
		return nil, err
	}
	result := root.get(resultProperty)
	if !root.isObject() || result == nil {
		return nil, errors.New("Creatio returned an invalid administration response")
	}
	switch kind {
	case adminWriteTrue, adminWriteBoolean:
		if result.kind != jkBool {
			return nil, errors.New("Creatio returned an invalid administration response")
		}
		if kind == adminWriteTrue && !result.flag {
			return nil, errors.New("Creatio rejected the administration operation")
		}
		return result, nil
	}
	if result.kind != jkString {
		return nil, errors.New("Creatio returned an invalid administration response")
	}
	if kind == adminWriteErrorString {
		if result.text != "" {
			return nil, errors.New("Creatio rejected the administration operation")
		}
		return result, nil
	}
	inner, err := inspectParseJSON([]byte(result.text))
	if err != nil {
		return nil, err
	}
	if err := inspectRequireSuccess(inner); err != nil {
		return nil, err
	}
	return inner, nil
}

func (c *Client) adminWriteActualize(ctx context.Context) error {
	_, err := c.adminWritePost(ctx, "rest/AdministrationService/ActualizeAdminUnitInRole", "ActualizeAdminUnitInRoleResult", orderedFields{}, adminWriteTrue)
	return err
}

// adminWriteParameter is AdministrationClient.Parameter for a typed value.
func adminWriteParameter(value any) orderedFields { return inspectParameter(value) }

func adminWriteIdentityFilter(id string) orderedFields {
	return orderedFields{{"filterType", 6}, {"logicalOperation", 0}, {"isEnabled", true}, {"items", orderedFields{
		{"identity", orderedFields{{"filterType", 1}, {"comparisonType", 3}, {"isEnabled", true},
			{"leftExpression", orderedFields{{"expressionType", 0}, {"columnPath", "Id"}}},
			{"rightExpression", adminWriteParameter(inspectGUID(id))}}},
	}}}
}

// adminWriteWriteEntity is AdministrationClient.WriteEntity: InsertQuery or UpdateQuery of one exact row.
func (c *Client) adminWriteWriteEntity(ctx context.Context, schema, id string, values []inspectFilter, insert bool) error {
	if id == "" || id == emptyGUID {
		return inspectRefusal("An exact entity ID is required.")
	}
	columns := orderedFields{}
	for _, value := range values {
		columns = append(columns, field{value.column, adminWriteParameter(value.value)})
	}
	operation, endpoint := 2, "UpdateQuery"
	if insert {
		columns = append(columns, field{"Id", adminWriteParameter(inspectGUID(id))})
		operation, endpoint = 1, "InsertQuery"
	}
	body := toJNode(orderedFields{{"rootSchemaName", schema}, {"operationType", operation},
		{"columnValues", orderedFields{{"items", columns}}}, {"filters", adminWriteIdentityFilter(id)}})
	return c.adminWriteDataServiceWrite(ctx, endpoint, body)
}

// adminWriteDeleteEntity is AdministrationClient.DeleteEntity.
func (c *Client) adminWriteDeleteEntity(ctx context.Context, schema, id string) error {
	if id == "" || id == emptyGUID {
		return inspectRefusal("An exact entity ID is required.")
	}
	body := toJNode(orderedFields{{"rootSchemaName", schema}, {"operationType", 3}, {"filters", adminWriteIdentityFilter(id)}})
	return c.adminWriteDataServiceWrite(ctx, "DeleteQuery", body)
}

func (c *Client) adminWriteDataServiceWrite(ctx context.Context, endpoint string, body *jnode) error {
	response, err := c.postDataServiceJSON(ctx, endpoint, body.stjJSON(), inspectSelectTimeout, inspectMaxResponseBytes)
	if err != nil {
		return err
	}
	root, err := inspectParseJSON(response)
	if err != nil {
		return err
	}
	if err := inspectRequireSuccess(root); err != nil {
		return err
	}
	if affected := root.get("rowsAffected"); affected == nil || affected.kind != jkInteger || affected.text != "1" {
		return inspectRefusal("Administration write did not affect exactly one record. Inspect before retrying.")
	}
	return nil
}

func adminWriteRequireCount(rows *jnode, count int) error {
	if len(rows.items) != count {
		return inspectRefusal("Administration record count differs from the expected state. Inspect before retrying.")
	}
	return nil
}

// adminWriteJSONObject is JsonSerializer.Serialize(Dictionary<string, object>): the jsonObject argument of
// SaveRole and UpdateOrCreateUser.
func adminWriteJSONObject(values orderedFields) string {
	return string(toJNode(values).stjJSON())
}

// --- users -----------------------------------------------------------------------------------------------

func (c *Client) adminWriteSaveUser(ctx context.Context, values orderedFields) error {
	_, err := c.adminWritePost(ctx, "rest/AdministrationService/UpdateOrCreateUser", "UpdateOrCreateUserResult",
		orderedFields{{"jsonObject", adminWriteJSONObject(values)}, {"roleId", ""}}, adminWriteErrorString)
	return err
}

func (c *Client) adminWriteRequireContact(ctx context.Context, id string) error {
	if err := inspectRequireID(id); err != nil {
		return err
	}
	rows, err := c.inspectSelect(ctx, "Contact", []string{"Id"}, []inspectFilter{{"Id", inspectGUID(id)}}, 0, 2)
	if err != nil {
		return err
	}
	if len(rows.items) != 1 {
		return inspectRefusal("The contact ID must identify one existing contact.")
	}
	return nil
}

func (c *Client) adminWriteCreateUser(ctx context.Context, id string, login *string, contact string, external bool, password string, forceChange bool) (*jnode, error) {
	if err := inspectRequireID(id); err != nil {
		return nil, err
	}
	name := ""
	if login != nil {
		name = *login
	}
	if err := adminWriteRequireName(name); err != nil {
		return nil, err
	}
	if password == "" {
		return nil, inspectRefusal("Provide a nonempty password through the secret source.")
	}
	if err := c.adminWriteRequireContact(ctx, contact); err != nil {
		return nil, err
	}
	byID, err := c.adminWriteListAny(ctx, &id, nil, 1)
	if err != nil {
		return nil, err
	}
	exists := len(byID.items) != 0
	if !exists {
		byLogin, err := c.adminWriteListAny(ctx, nil, &name, 1)
		if err != nil {
			return nil, err
		}
		exists = len(byLogin.items) != 0
	}
	if exists {
		return nil, inspectRefusal("The account ID or login already exists. Inspect it and use update explicitly.")
	}
	connection := 0
	if external {
		connection = 1
	}
	if err := c.adminWriteSaveUser(ctx, orderedFields{{"Id", id}, {"Name", name}, {"Contact", contact},
		{"SysAdminUnitType", adminWriteUserTypeID}, {"ConnectionType", connection}, {"Active", true},
		{"UserPassword", password}, {"ForceChangePassword", forceChange}}); err != nil {
		return nil, err
	}
	user, err := c.adminWriteGetUser(ctx, id, "The ID must identify a user account.")
	if err != nil {
		return nil, err
	}
	if adminWriteString(user, "Name") != name || adminWriteLookupID(user, "Contact") != contact || adminWriteInt(user, "ConnectionType") != connection {
		return nil, inspectRefusal("Created account readback differs from the request. Inspect before retrying.")
	}
	return user, nil
}

func (c *Client) adminWriteUpdateUser(ctx context.Context, id string, login, contact *string, active *bool) (*jnode, error) {
	if _, err := c.adminWriteGetUser(ctx, id, "The ID must identify a user account."); err != nil {
		return nil, err
	}
	if login == nil && contact == nil && active == nil {
		return nil, inspectRefusal("Supply a login, contact ID or active state to update.")
	}
	values := orderedFields{{"Id", id}}
	if login != nil {
		if err := adminWriteRequireName(*login); err != nil {
			return nil, err
		}
		values = append(values, field{"Name", *login})
	}
	if contact != nil {
		if err := c.adminWriteRequireContact(ctx, *contact); err != nil {
			return nil, err
		}
		values = append(values, field{"Contact", *contact})
	}
	if active != nil {
		values = append(values, field{"Active", *active})
	}
	if err := c.adminWriteSaveUser(ctx, values); err != nil {
		return nil, err
	}
	user, err := c.adminWriteGetUser(ctx, id, "The ID must identify a user account.")
	if err != nil {
		return nil, err
	}
	if (login != nil && adminWriteString(user, "Name") != *login) || (contact != nil && adminWriteLookupID(user, "Contact") != *contact) ||
		(active != nil && adminWriteBool(user, "Active") != *active) {
		return nil, inspectRefusal("Account update could not be verified.")
	}
	return user, nil
}

func (c *Client) adminWriteChangePassword(ctx context.Context, id, password string, forceChange bool) error {
	before, err := c.adminWriteGetUser(ctx, id, "The ID must identify a user account.")
	if err != nil {
		return err
	}
	if adminWriteBool(before, "SynchronizeWithLDAP") {
		return inspectRefusal("Change an LDAP-managed account password in its identity provider.")
	}
	if password == "" {
		return inspectRefusal("Provide a nonempty password through the secret source.")
	}
	if err := c.adminWriteSaveUser(ctx, orderedFields{{"Id", id}, {"UserPassword", password}, {"ForceChangePassword", forceChange}}); err != nil {
		return err
	}
	after, err := c.adminWriteGetUser(ctx, id, "The ID must identify a user account.")
	if err != nil {
		return err
	}
	if adminWriteString(before, "Name") != adminWriteString(after, "Name") || adminWriteBool(before, "Active") != adminWriteBool(after, "Active") ||
		adminWriteLookupID(before, "Contact") != adminWriteLookupID(after, "Contact") || adminWriteBool(after, "ForceChangePassword") != forceChange {
		return inspectRefusal("Password service accepted the request but account readback differs. Inspect before retrying.")
	}
	return nil
}

func (c *Client) adminWriteIsBlocked(ctx context.Context, id string) (bool, error) {
	if _, err := c.adminWriteGetUser(ctx, id, "The ID must identify a user account."); err != nil {
		return false, err
	}
	result, err := c.adminWritePost(ctx, inspectIsUserBlockedRoute, "GetIsUserBlockedResult", orderedFields{{"userId", id}}, adminWriteBoolean)
	if err != nil {
		return false, err
	}
	return result.flag, nil
}

func (c *Client) adminWriteUnlockUser(ctx context.Context, id string) (*jnode, error) {
	before, err := c.adminWriteGetUser(ctx, id, "The ID must identify a user account.")
	if err != nil {
		return nil, err
	}
	if _, err := c.adminWritePost(ctx, "rest/AdministrationService/UnblockUser", "UnblockUserResult", orderedFields{{"userId", id}}, adminWriteSuccessObject); err != nil {
		return nil, err
	}
	blocked, err := c.adminWriteIsBlocked(ctx, id)
	if err != nil {
		return nil, err
	}
	if blocked {
		return nil, inspectRefusal("The account remains blocked after unlocking.")
	}
	after, err := c.adminWriteGetUser(ctx, id, "The ID must identify a user account.")
	if err != nil {
		return nil, err
	}
	if adminWriteBool(before, "Active") != adminWriteBool(after, "Active") {
		return nil, inspectRefusal("The account active state changed during unlock. Inspect it before proceeding.")
	}
	return after, nil
}

func (c *Client) adminWriteDeleteUser(ctx context.Context, id string) error {
	if _, err := c.adminWriteGetUser(ctx, id, "The ID must identify a user account."); err != nil {
		return err
	}
	if _, err := c.adminWritePost(ctx, "rest/AdministrationService/DeleteUser", "DeleteUserResult", orderedFields{{"userId", id}}, adminWriteSuccessObject); err != nil {
		return err
	}
	rows, err := c.adminWriteListAny(ctx, &id, nil, 1)
	if err != nil {
		return err
	}
	if len(rows.items) != 0 {
		return inspectRefusal("The account still exists after deletion.")
	}
	return nil
}

// --- roles -----------------------------------------------------------------------------------------------

func (c *Client) adminWriteSaveRole(ctx context.Context, values orderedFields) error {
	_, err := c.adminWritePost(ctx, "rest/AdministrationService/SaveRole", "SaveRoleResult",
		orderedFields{{"jsonObject", adminWriteJSONObject(values)}}, adminWriteSuccessObject)
	return err
}

// adminWriteValidateParent is ValidateParent.
func adminWriteValidateParent(parent *jnode, childType int) error {
	parentType := adminWriteType(parent)
	parentID := strings.ToLower(adminWriteString(parent, "Id"))
	if childType == 6 && parentType != 6 && parentID != adminWriteAllEmployees && parentID != adminWriteAllExternalUsers {
		return inspectRefusal("A functional role must belong to another functional role or the All employees/All external users anchor.")
	}
	if !(parentType == 0 || parentType == 1 || parentType == 3 || parentType == 6) || (childType != 6 && parentType == 6) {
		return inspectRefusal("The parent role type is incompatible with the requested role.")
	}
	return nil
}

func (c *Client) adminWriteCreateRole(ctx context.Context, id string, name *string, roleType int, parentID string) (*jnode, error) {
	if err := inspectRequireID(id); err != nil {
		return nil, err
	}
	roleName := ""
	if name != nil {
		roleName = *name
	}
	if err := adminWriteRequireName(roleName); err != nil {
		return nil, err
	}
	if !(roleType == 0 || roleType == 1 || roleType == 3 || roleType == 6) {
		return nil, inspectRefusal("Role type must be organization (0), division (1), team (3), or functional (6).")
	}
	existing, err := c.adminWriteListAny(ctx, &id, nil, 1)
	if err != nil {
		return nil, err
	}
	if len(existing.items) != 0 {
		return nil, inspectRefusal("The role ID already exists. Read it and use update explicitly.")
	}
	parent, err := c.adminWriteGetUnit(ctx, parentID)
	if err != nil {
		return nil, err
	}
	if err := adminWriteValidateParent(parent, roleType); err != nil {
		return nil, err
	}
	typeRows, err := c.inspectSelect(ctx, "SysAdminUnitType", []string{"Id", "Value"}, []inspectFilter{{"Value", roleType}}, 0, 2)
	if err != nil {
		return nil, err
	}
	if len(typeRows.items) != 1 {
		return nil, inspectRefusal("Creatio did not return one matching role type.")
	}
	if err := c.adminWriteSaveRole(ctx, orderedFields{{"Id", id}, {"Name", roleName}, {"ParentRole", parentID},
		{"SysAdminUnitType", adminWriteString(typeRows.items[0], "Id")}, {"ConnectionType", adminWriteInt(parent, "ConnectionType")},
		{"Active", true}}); err != nil {
		return nil, err
	}
	result, err := c.adminWriteGetUnit(ctx, id)
	if err != nil {
		return nil, err
	}
	if adminWriteType(result) != roleType || adminWriteString(result, "Name") != roleName || adminWriteLookupID(result, "ParentRole") != parentID {
		return nil, inspectRefusal("Role readback differs from the requested state. Inspect it before retrying.")
	}
	return result, nil
}

func (c *Client) adminWriteUpdateRole(ctx context.Context, id string, name, parentID *string) (*jnode, error) {
	current, err := c.adminWriteGetRole(ctx, id)
	if err != nil {
		return nil, err
	}
	if name == nil && parentID == nil {
		return nil, inspectRefusal("Supply a role name or parent to update.")
	}
	values := orderedFields{{"Id", id}}
	if name != nil {
		if err := adminWriteRequireName(*name); err != nil {
			return nil, err
		}
		values = append(values, field{"Name", *name})
	}
	if parentID != nil {
		if adminWriteType(current) == 2 {
			return nil, inspectRefusal("A manager role cannot be moved to another organizational role.")
		}
		parent, err := c.adminWriteGetRole(ctx, *parentID)
		if err != nil {
			return nil, err
		}
		if err := adminWriteValidateParent(parent, adminWriteType(current)); err != nil {
			return nil, err
		}
		if err := c.adminWriteValidateHierarchy(ctx, id, *parentID); err != nil {
			return nil, err
		}
		if adminWriteInt(parent, "ConnectionType") != adminWriteInt(current, "ConnectionType") {
			return nil, inspectRefusal("The parent must have the same connection type as the role.")
		}
		values = append(values, field{"ParentRole", *parentID})
	}
	if err := c.adminWriteSaveRole(ctx, values); err != nil {
		return nil, err
	}
	if err := c.adminWriteActualize(ctx); err != nil {
		return nil, err
	}
	result, err := c.adminWriteGetRole(ctx, id)
	if err != nil {
		return nil, err
	}
	if (name != nil && adminWriteString(result, "Name") != *name) || (parentID != nil && adminWriteLookupID(result, "ParentRole") != *parentID) {
		return nil, inspectRefusal("Role update could not be verified.")
	}
	return result, nil
}

func (c *Client) adminWriteValidateHierarchy(ctx context.Context, id, parentID string) error {
	visited := map[string]bool{id: true}
	cursor := parentID
	for cursor != emptyGUID {
		if visited[cursor] || len(visited)+1 > 200 {
			return inspectRefusal("The requested role hierarchy is cyclic or exceeds the supported depth.")
		}
		visited[cursor] = true
		role, err := c.adminWriteGetRole(ctx, cursor)
		if err != nil {
			return err
		}
		cursor = adminWriteLookupID(role, "ParentRole")
	}
	return nil
}

func (c *Client) adminWriteManagerChildren(ctx context.Context, parentID string) (*jnode, error) {
	return c.inspectSelect(ctx, "SysAdminUnit", inspectUnitColumns,
		[]inspectFilter{{"ParentRole", inspectGUID(parentID)}, {inspectUnitTypeColumn, 2}}, 0, 2)
}

func (c *Client) adminWriteEnsureManager(ctx context.Context, parentID string) (*jnode, error) {
	parent, err := c.adminWriteGetRole(ctx, parentID)
	if err != nil {
		return nil, err
	}
	if parentType := adminWriteType(parent); parentType != 0 && parentType != 1 && parentType != 3 {
		return nil, inspectRefusal("Manager roles require an organizational parent.")
	}
	children, err := c.adminWriteManagerChildren(ctx, parentID)
	if err != nil {
		return nil, err
	}
	if len(children.items) > 1 {
		return nil, inspectRefusal("More than one manager role exists for this parent. Resolve the ambiguity explicitly.")
	}
	if len(children.items) == 1 {
		return children.items[0], nil
	}
	if adminWriteInt(parent, "ConnectionType") != 0 {
		return nil, inspectRefusal("Creating a missing external-user manager role is not verified; use the native administrator UI.")
	}
	if _, err := c.adminWritePost(ctx, "rest/AdministrationService/SaveChiefsRole", "SaveChiefsRoleResult",
		orderedFields{{"id", parentID}, {"name", adminWriteString(parent, "Name")}}, adminWriteSuccessObject); err != nil {
		return nil, err
	}
	children, err = c.adminWriteManagerChildren(ctx, parentID)
	if err != nil {
		return nil, err
	}
	if len(children.items) != 1 {
		return nil, inspectRefusal("Manager creation did not produce a unique manager child. Inspect before retrying.")
	}
	return children.items[0], nil
}

func (c *Client) adminWriteDeleteRole(ctx context.Context, id string) error {
	role, err := c.adminWriteGetRole(ctx, id)
	if err != nil {
		return err
	}
	if id == adminWriteSystemAdministrators || adminWriteLookupID(role, "ParentRole") == emptyGUID {
		return inspectRefusal("Built-in root and system-administrator roles cannot be deleted by this command.")
	}
	children, err := c.inspectSelect(ctx, "SysAdminUnit", []string{"Id"}, []inspectFilter{{"ParentRole", inspectGUID(id)}}, 0, 1)
	if err != nil {
		return err
	}
	members, err := c.inspectSelect(ctx, "SysUserInRole", []string{"Id"}, []inspectFilter{{"SysRole", inspectGUID(id)}}, 0, 1)
	if err != nil {
		return err
	}
	if len(children.items) != 0 || len(members.items) != 0 {
		return inspectRefusal("Remove or move the role's children and direct members before deleting it.")
	}
	if _, err := c.adminWritePost(ctx, "rest/GridUtilitiesService/DeleteRecords", "DeleteRecordsResult",
		orderedFields{{"rootSchema", "VwSysAdminUnit"}, {"primaryColumnValues", []any{id}}}, adminWriteSuccessObject); err != nil {
		return err
	}
	if err := c.adminWriteActualize(ctx); err != nil {
		return err
	}
	rows, err := c.adminWriteListAny(ctx, &id, nil, 1)
	if err != nil {
		return err
	}
	if len(rows.items) != 0 {
		return inspectRefusal("The role still exists after deletion.")
	}
	return nil
}

// adminWriteGUIDList is JsonSerializer.Serialize(new[] { id }): a JSON array text.
func adminWriteGUIDList(id string) string { return `["` + id + `"]` }

func (c *Client) adminWriteSetMembership(ctx context.Context, userID, roleID string, remove bool) (*jnode, error) {
	user, err := c.adminWriteGetUser(ctx, userID, "The member ID must identify a user.")
	if err != nil {
		return nil, err
	}
	role, err := c.adminWriteGetRole(ctx, roleID)
	if err != nil {
		return nil, err
	}
	if !remove && adminWriteInt(user, "ConnectionType") != adminWriteInt(role, "ConnectionType") {
		return nil, inspectRefusal("The user and role must have matching connection types.")
	}
	if remove {
		_, err = c.adminWritePost(ctx, "rest/AdministrationService/RemoveUsersInRoles", "RemoveUsersInRolesResult",
			orderedFields{{"roleIds", adminWriteGUIDList(roleID)}, {"userIds", adminWriteGUIDList(userID)}, {"recordIds", "[]"}}, adminWriteSuccessObject)
	} else {
		_, err = c.adminWritePost(ctx, "rest/AdministrationService/AddUserRoles", "AddUserRolesResult",
			orderedFields{{"userId", userID}, {"roleIds", adminWriteGUIDList(roleID)}}, adminWriteErrorString)
	}
	if err != nil {
		return nil, err
	}
	if err := c.adminWriteActualize(ctx); err != nil {
		return nil, err
	}
	direct, err := c.inspectSelect(ctx, "SysUserInRole", []string{"Id", "SysUser", "SysRole"},
		[]inspectFilter{{"SysUser", inspectGUID(userID)}, {"SysRole", inspectGUID(roleID)}}, 0, 2)
	if err != nil {
		return nil, err
	}
	want := 1
	if remove {
		want = 0
	}
	if len(direct.items) != want {
		return nil, inspectRefusal("Direct membership does not match the requested state.")
	}
	return direct, nil
}

func (c *Client) adminWriteSetFunctionalRole(ctx context.Context, roleID, functionalID string, remove bool) (*jnode, error) {
	org, err := c.adminWriteGetRole(ctx, roleID)
	if err != nil {
		return nil, err
	}
	if adminWriteType(org) > 3 {
		return nil, inspectRefusal("An organizational or manager role is required.")
	}
	functional, err := c.adminWriteGetRole(ctx, functionalID)
	if err != nil {
		return nil, err
	}
	if adminWriteType(functional) != 6 {
		return nil, inspectRefusal("The associated role must be functional (type 6).")
	}
	if adminWriteInt(org, "ConnectionType") != adminWriteInt(functional, "ConnectionType") {
		return nil, inspectRefusal("Associated roles must have matching connection types.")
	}
	filters := []inspectFilter{{"OrgRole", inspectGUID(roleID)}, {"FuncRole", inspectGUID(functionalID)}}
	columns := []string{"Id", "OrgRole", "FuncRole"}
	rows, err := c.inspectSelect(ctx, "SysFuncRoleInOrgRole", columns, filters, 0, 2)
	if err != nil {
		return nil, err
	}
	if len(rows.items) > 1 {
		return nil, inspectRefusal("Duplicate functional associations require explicit repair.")
	}
	var receipt *jnode
	alreadyAbsent := remove && len(rows.items) == 0
	if remove && len(rows.items) == 1 {
		if receipt, err = c.adminWritePost(ctx, "rest/CreatioApiGateway/RemoveFunctionalRoleAssociation", "RemoveFunctionalRoleAssociationResult",
			orderedFields{{"associationId", strings.ToLower(adminWriteString(rows.items[0], "Id"))}}, adminWriteSuccessObject); err != nil {
			return nil, err
		}
	} else if !remove && len(rows.items) == 0 {
		if _, err := c.adminWritePost(ctx, "rest/AdministrationService/AddFuncRolesInOrgRole", "AddFuncRolesInOrgRoleResult",
			orderedFields{{"orgRoleId", roleID}, {"funcRoleIds", adminWriteGUIDList(functionalID)}}, adminWriteErrorString); err != nil {
			return nil, err
		}
	}
	if receipt == nil {
		if err := c.adminWriteActualize(ctx); err != nil {
			return nil, err
		}
	}
	if rows, err = c.inspectSelect(ctx, "SysFuncRoleInOrgRole", columns, filters, 0, 2); err != nil {
		return nil, err
	}
	want := 1
	if remove {
		want = 0
	}
	if err := adminWriteRequireCount(rows, want); err != nil {
		return nil, err
	}
	completed := receipt == nil || adminWriteBool(receipt, "completed")
	reconcile := alreadyAbsent || (receipt != nil && adminWriteBool(receipt, "licenseReconciliationRequired"))
	return toJNode(orderedFields{{"associations", rows}, {"redistributionReceipt", receipt}, {"completed", completed},
		{"licenseReconciliationRequired", reconcile}, {"userAssignmentsVerified", false}}), nil
}

// --- licenses --------------------------------------------------------------------------------------------

func (c *Client) adminWriteUserLicenses(ctx context.Context, userID string) (*jnode, error) {
	user, err := c.adminWriteGetUser(ctx, userID, "The ID must identify a user account.")
	if err != nil {
		return nil, err
	}
	if adminWriteType(user) == 7 {
		return nil, inspectRefusal("Technical accounts do not support personal user licenses.")
	}
	return c.inspectPost(ctx, inspectUserLicensesRoute, "GetAvailableLicPackagesResult", userID, true)
}

func adminWriteFindPackage(packages *jnode, packageID string) []*jnode {
	var found []*jnode
	for _, item := range packages.items {
		if id, ok := adminWriteParseGUID(adminWriteString(item, "Id")); ok && id == packageID {
			found = append(found, item)
		}
	}
	return found
}

func (c *Client) adminWriteSetUserLicense(ctx context.Context, userID, packageID string, remove bool) (*jnode, error) {
	if err := inspectRequireID(packageID); err != nil {
		return nil, err
	}
	user, err := c.adminWriteGetUser(ctx, userID, "The ID must identify a user account.")
	if err != nil {
		return nil, err
	}
	if !remove && !adminWriteBool(user, "Active") {
		return nil, inspectRefusal("Activate the user before assigning a license.")
	}
	licenses, err := c.adminWriteUserLicenses(ctx, userID)
	if err != nil {
		return nil, err
	}
	packages := adminWriteFindPackage(licenses, packageID)
	if len(packages) != 1 {
		return nil, inspectRefusal("The package ID must identify one available license package.")
	}
	if !remove && !adminWriteBool(packages[0], "Enabled") {
		return nil, inspectRefusal("The selected license is unavailable for assignment to this account.")
	}
	items := newObject()
	items.set(packageID, newBool(!remove))
	if _, err := c.adminWritePost(ctx, "rest/AdministrationService/UpdateLicenseInfo", "UpdateLicenseInfoResult",
		orderedFields{{"userId", userID}, {"licenseItems", string(items.stjJSON())}}, adminWriteErrorString); err != nil {
		return nil, err
	}
	if licenses, err = c.adminWriteUserLicenses(ctx, userID); err != nil {
		return nil, err
	}
	packages = adminWriteFindPackage(licenses, packageID)
	if len(packages) != 1 || adminWriteBool(packages[0], "Checked") == remove {
		return nil, inspectRefusal("License assignment readback differs from the request.")
	}
	return packages[0], nil
}

func (c *Client) adminWriteSetRoleLicense(ctx context.Context, roleID, packageID string, remove bool) (*jnode, error) {
	if _, err := c.adminWriteGetRole(ctx, roleID); err != nil {
		return nil, err
	}
	if err := inspectRequireID(packageID); err != nil {
		return nil, err
	}
	known, err := c.inspectSelect(ctx, "SysLicPackage", []string{"Id"}, []inspectFilter{{"Id", inspectGUID(packageID)}}, 0, 2)
	if err != nil {
		return nil, err
	}
	if err := adminWriteRequireCount(known, 1); err != nil {
		return nil, err
	}
	columns := []string{"Id", "SysRole", "SysLicPackage"}
	filter := []inspectFilter{{"SysRole", inspectGUID(roleID)}, {"SysLicPackage", inspectGUID(packageID)}}
	rows, err := c.inspectSelect(ctx, "SysLicPackageInRole", columns, filter, 0, 2)
	if err != nil {
		return nil, err
	}
	if len(rows.items) > 1 {
		return nil, inspectRefusal("Duplicate role license associations require explicit repair.")
	}
	if remove && len(rows.items) == 1 {
		id, _ := adminWriteParseGUID(adminWriteString(rows.items[0], "Id"))
		err = c.adminWriteDeleteEntity(ctx, "SysLicPackageInRole", id)
	} else if !remove && len(rows.items) == 0 {
		err = c.adminWriteWriteEntity(ctx, "SysLicPackageInRole", adminWriteNewGUID(), filter, true)
	}
	if err != nil {
		return nil, err
	}
	if rows, err = c.inspectSelect(ctx, "SysLicPackageInRole", columns, filter, 0, 2); err != nil {
		return nil, err
	}
	want := 1
	if remove {
		want = 0
	}
	if err := adminWriteRequireCount(rows, want); err != nil {
		return nil, err
	}
	return rows, nil
}

// --- access ----------------------------------------------------------------------------------------------

var adminWriteIPColumns = []string{"Id", "SysAdminUnit", "BeginIP", "EndIP"}

// adminWriteParseIPv4 is ParseIpv4: a canonical dotted IPv4 address only.
func adminWriteParseIPv4(value *string) (net.IP, error) {
	refusal := inspectRefusal("Use a canonical dotted IPv4 address. IPv6 writes are unavailable because affected Creatio versions fail mixed-family login checks.")
	if value == nil {
		return nil, refusal
	}
	address := net.ParseIP(*value)
	if address == nil || address.To4() == nil || strings.Contains(*value, ":") || address.To4().String() != *value {
		return nil, refusal
	}
	return address.To4(), nil
}

func (c *Client) adminWriteSetIPRange(ctx context.Context, id, unitID string, beginIP, endIP *string, create bool) (*jnode, error) {
	if err := inspectRequireID(id); err != nil {
		return nil, err
	}
	if _, err := c.adminWriteGetUnit(ctx, unitID); err != nil {
		return nil, err
	}
	begin, err := adminWriteParseIPv4(beginIP)
	if err != nil {
		return nil, err
	}
	end, err := adminWriteParseIPv4(endIP)
	if err != nil {
		return nil, err
	}
	for index := range begin {
		if begin[index] > end[index] {
			return nil, inspectRefusal("Each beginning IPv4 octet must be at most the corresponding ending octet; split the range if needed.")
		}
	}
	filter := []inspectFilter{{"Id", inspectGUID(id)}}
	rows, err := c.inspectSelect(ctx, "SysAdminUnitIPRange", adminWriteIPColumns, filter, 0, 2)
	if err != nil {
		return nil, err
	}
	want := 1
	if create {
		want = 0
	}
	if err := adminWriteRequireCount(rows, want); err != nil {
		return nil, err
	}
	if !create && adminWriteLookupID(rows.items[0], "SysAdminUnit") != unitID {
		return nil, inspectRefusal("The access rule belongs to a different user or role.")
	}
	if err := c.adminWriteWriteEntity(ctx, "SysAdminUnitIPRange", id, []inspectFilter{{"SysAdminUnit", inspectGUID(unitID)},
		{"BeginIP", begin.String()}, {"EndIP", end.String()}}, create); err != nil {
		return nil, err
	}
	if rows, err = c.inspectSelect(ctx, "SysAdminUnitIPRange", adminWriteIPColumns, filter, 0, 2); err != nil {
		return nil, err
	}
	if err := adminWriteRequireCount(rows, 1); err != nil {
		return nil, err
	}
	row := rows.items[0]
	if adminWriteLookupID(row, "SysAdminUnit") != unitID || adminWriteString(row, "BeginIP") != begin.String() || adminWriteString(row, "EndIP") != end.String() {
		return nil, inspectRefusal("IP access rule readback differs from the request.")
	}
	return row, nil
}

func (c *Client) adminWriteDeleteIPRange(ctx context.Context, id, unitID string) error {
	if err := inspectRequireID(id); err != nil {
		return err
	}
	if _, err := c.adminWriteGetUnit(ctx, unitID); err != nil {
		return err
	}
	filter := []inspectFilter{{"Id", inspectGUID(id)}}
	rows, err := c.inspectSelect(ctx, "SysAdminUnitIPRange", adminWriteIPColumns, filter, 0, 2)
	if err != nil {
		return err
	}
	if err := adminWriteRequireCount(rows, 1); err != nil {
		return err
	}
	if adminWriteLookupID(rows.items[0], "SysAdminUnit") != unitID {
		return inspectRefusal("The access rule belongs to a different user or role.")
	}
	if err := c.adminWriteDeleteEntity(ctx, "SysAdminUnitIPRange", id); err != nil {
		return err
	}
	rows, err = c.inspectSelect(ctx, "SysAdminUnitIPRange", adminWriteIPColumns, filter, 0, 1)
	if err != nil {
		return err
	}
	return adminWriteRequireCount(rows, 0)
}

func (c *Client) adminWriteSetDelegation(ctx context.Context, grantorID, granteeID string, remove bool) (*jnode, error) {
	grantor, err := c.adminWriteGetUnit(ctx, grantorID)
	if err != nil {
		return nil, err
	}
	grantee, err := c.adminWriteGetUser(ctx, granteeID, "The ID must identify a user account.")
	if err != nil {
		return nil, err
	}
	if adminWriteType(grantor) == 7 || adminWriteType(grantee) == 7 || grantorID == granteeID {
		return nil, inspectRefusal("Delegation requires distinct identities and does not support technical users.")
	}
	columns := []string{"Id", "GrantorSysAdminUnit", "GranteeSysAdminUnit"}
	filters := []inspectFilter{{"GrantorSysAdminUnit", inspectGUID(grantorID)}, {"GranteeSysAdminUnit", inspectGUID(granteeID)}}
	rows, err := c.inspectSelect(ctx, "SysAdminUnitGrantedRight", columns, filters, 0, 2)
	if err != nil {
		return nil, err
	}
	if len(rows.items) > 1 {
		return nil, inspectRefusal("Duplicate delegations require explicit repair.")
	}
	if remove && len(rows.items) == 1 {
		id, _ := adminWriteParseGUID(adminWriteString(rows.items[0], "Id"))
		_, err = c.adminWritePost(ctx, "rest/AdministrationService/RemoveSysAdminUnitGrantedRights", "RemoveSysAdminUnitGrantedRightsResult",
			orderedFields{{"selectedRecords", adminWriteGUIDList(id)}}, adminWriteErrorString)
	} else if !remove && len(rows.items) == 0 {
		_, err = c.adminWritePost(ctx, "rest/AdministrationService/AddSysAdminUnitGrantedRights", "AddSysAdminUnitGrantedRightsResult",
			orderedFields{{"masterRecordId", grantorID}, {"selectedRecords", adminWriteGUIDList(granteeID)}}, adminWriteErrorString)
	}
	if err != nil {
		return nil, err
	}
	if err := c.adminWriteActualize(ctx); err != nil {
		return nil, err
	}
	if rows, err = c.inspectSelect(ctx, "SysAdminUnitGrantedRight", columns, filters, 0, 2); err != nil {
		return nil, err
	}
	want := 1
	if remove {
		want = 0
	}
	if err := adminWriteRequireCount(rows, want); err != nil {
		return nil, err
	}
	return rows, nil
}

var adminWriteGrantColumns = []string{"Id", "SysAdminOperation", "SysAdminUnit", "CanExecute", "Position"}

func (c *Client) adminWriteRequireOperation(ctx context.Context, id string) error {
	if err := inspectRequireID(id); err != nil {
		return err
	}
	rows, err := c.inspectSelect(ctx, "SysAdminOperation", []string{"Id"}, []inspectFilter{{"Id", inspectGUID(id)}}, 0, 2)
	if err != nil {
		return err
	}
	return adminWriteRequireCount(rows, 1)
}

func (c *Client) adminWriteSetOperationGrant(ctx context.Context, operationID, unitID string, canExecute, remove bool) (*jnode, error) {
	if err := c.adminWriteRequireOperation(ctx, operationID); err != nil {
		return nil, err
	}
	if _, err := c.adminWriteGetUnit(ctx, unitID); err != nil {
		return nil, err
	}
	filters := []inspectFilter{{"SysAdminOperation", inspectGUID(operationID)}, {"SysAdminUnit", inspectGUID(unitID)}}
	rows, err := c.inspectSelect(ctx, "SysAdminOperationGrantee", adminWriteGrantColumns, filters, 0, 2)
	if err != nil {
		return nil, err
	}
	if len(rows.items) > 1 {
		return nil, inspectRefusal("Duplicate operation grants require explicit repair.")
	}
	if remove && len(rows.items) == 1 {
		id, _ := adminWriteParseGUID(adminWriteString(rows.items[0], "Id"))
		_, err = c.adminWritePost(ctx, "rest/RightsService/DeleteAdminOperationGrantee", "DeleteAdminOperationGranteeResult",
			orderedFields{{"recordIds", []any{id}}}, adminWriteSuccessObject)
	} else if !remove {
		_, err = c.adminWritePost(ctx, "rest/RightsService/SetAdminOperationGrantee", "SetAdminOperationGranteeResult",
			orderedFields{{"adminOperationId", operationID}, {"adminUnitIds", []any{unitID}}, {"canExecute", canExecute}}, adminWriteSuccessObject)
	}
	if err != nil {
		return nil, err
	}
	if rows, err = c.inspectSelect(ctx, "SysAdminOperationGrantee", adminWriteGrantColumns, filters, 0, 2); err != nil {
		return nil, err
	}
	want := 1
	if remove {
		want = 0
	}
	if err := adminWriteRequireCount(rows, want); err != nil {
		return nil, err
	}
	if !remove && adminWriteBool(rows.items[0], "CanExecute") != canExecute {
		return nil, inspectRefusal("Operation permission readback differs from the request.")
	}
	return rows, nil
}

func (c *Client) adminWriteSetOperationPosition(ctx context.Context, grantID string, position int) (*jnode, error) {
	if err := inspectRequireID(grantID); err != nil {
		return nil, err
	}
	if position < 0 {
		return nil, inspectRefusal("Operation grant position must be nonnegative.")
	}
	filters := []inspectFilter{{"Id", inspectGUID(grantID)}}
	rows, err := c.inspectSelect(ctx, "SysAdminOperationGrantee", adminWriteGrantColumns, filters, 0, 2)
	if err != nil {
		return nil, err
	}
	if err := adminWriteRequireCount(rows, 1); err != nil {
		return nil, err
	}
	invalidate := func() error {
		_, err := c.adminWritePost(ctx, "rest/CreatioApiGateway/InvalidateAdministrationRightsCache", "InvalidateAdministrationRightsCacheResult",
			orderedFields{}, adminWriteTrue)
		return err
	}
	if err := invalidate(); err != nil {
		return nil, err
	}
	if _, err := c.adminWritePost(ctx, "rest/RightsService/SetAdminOperationGranteePosition", "SetAdminOperationGranteePositionResult",
		orderedFields{{"granteeId", grantID}, {"position", position}}, adminWriteSuccessObject); err != nil {
		return nil, err
	}
	if err := invalidate(); err != nil {
		return nil, err
	}
	if rows, err = c.inspectSelect(ctx, "SysAdminOperationGrantee", adminWriteGrantColumns, filters, 0, 2); err != nil {
		return nil, err
	}
	if err := adminWriteRequireCount(rows, 1); err != nil {
		return nil, err
	}
	if adminWriteInt(rows.items[0], "Position") != position {
		return nil, inspectRefusal("Operation priority readback differs from the request.")
	}
	return rows.items[0], nil
}
