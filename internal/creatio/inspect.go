package creatio

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// The inspect-* tools are the read-only halves of clio's manage-user, manage-role, manage-license and
// manage-access. They read SysAdminUnit and its satellite tables through DataService SelectQuery and two
// read-only AdministrationService methods, and print the result as one Info message holding the
// System.Text.Json serialization of what Creatio returned. The write actions of those commands are not
// reachable from here.

const (
	inspectSelectTimeout = 60 * time.Second
	// clio's AdministrationClient refuses a response longer than 4 Mi characters; bytes are the closest
	// measure available before decoding.
	inspectMaxResponseBytes = 4 << 20

	inspectIsUserBlockedRoute = "rest/AdministrationService/GetIsUserBlocked"
	inspectUserLicensesRoute  = "rest/AdministrationService/GetAvailableLicPackages"

	inspectUnitTypeColumn = "SysAdminUnitTypeValue"
)

// inspectUnitColumns is clio's AdministrationService.UnitColumns, in its order: Creatio returns row keys
// in request order, and the row is printed as returned.
var inspectUnitColumns = []string{"Id", "Name", inspectUnitTypeColumn, "ParentRole", "Active", "Contact", "ConnectionType",
	"SynchronizeWithLDAP", "ForceChangePassword"}

// InspectTool names one of the four inspection tools with the actions it accepts and the message its
// command logs for an unexpected failure. clio never shows transport or server text here, because an
// administration endpoint can echo credentials.
type InspectTool struct {
	Name    string
	actions []string
	failure string
}

var (
	InspectUserTool = InspectTool{Name: "inspect-user", actions: []string{"list", "lock-status"},
		failure: "The user operation failed. Check permissions and inspect the account before retrying; partial changes may exist."}
	InspectRoleTool = InspectTool{Name: "inspect-role", actions: []string{"list", "memberships", "members", "functional-roles"},
		failure: "The role operation failed. Check permissions and inspect the role before retrying; partial changes may exist."}
	InspectLicenseTool = InspectTool{Name: "inspect-license", actions: []string{"user-list", "role-list"},
		failure: "The license operation failed. Check permissions, license availability and target state before retrying; partial changes may exist."}
	InspectAccessTool = InspectTool{Name: "inspect-access", actions: []string{"ip-list", "delegations", "operations", "operation-grants"},
		failure: "The access operation failed. Check permissions and inspect the target before retrying; partial changes may exist."}
)

// InspectRequest carries the bound arguments. GUIDs are lower-case hyphenated, and the empty GUID when
// the caller left them out, as clio's non-nullable Guid properties default. A nil text filter was absent
// or null; an empty one was sent as "" and is validated or filtered on as clio does.
type InspectRequest struct {
	Action      string
	ID          string
	UserID      string
	RoleID      string
	UnitID      string
	OperationID string
	UserLogin   *string
	Name        *string
	Code        *string
	Type        *int
	Effective   bool
	Offset      int
	Limit       int
}

// inspectRefusal is a message clio shows verbatim: its ArgumentException and AdministrationStateException.
// Every other failure becomes the tool's generic message.
type inspectRefusal string

func (r inspectRefusal) Error() string { return string(r) }

// inspectGUID marks a filter value as a GUID parameter (dataValueType 0) rather than text.
type inspectGUID string

type inspectFilter struct {
	column string
	value  any // inspectGUID, string, int or []int
}

// Inspect runs one inspection action. The action check comes first, as clio's tool method runs it before
// the command; everything after it fails into the command's own messages.
func (c *Client) Inspect(ctx context.Context, tool InspectTool, request InspectRequest) CommandResult {
	if !inspectAccepts(tool, request.Action) {
		return CommandFailure("This inspection tool accepts only: " + strings.Join(tool.actions, ", ") + ".")
	}
	result, err := c.inspectAction(ctx, tool, request)
	if err != nil {
		var refusal inspectRefusal
		if errors.As(err, &refusal) {
			return CommandFailure(refusal.Error())
		}
		return CommandFailure(tool.failure)
	}
	return CommandResult{ExitCode: 0, Messages: []LogMessage{{MessageType: "Info", Value: string(result.stjJSON())}}}
}

func inspectAccepts(tool InspectTool, action string) bool {
	for _, candidate := range tool.actions {
		if candidate == action {
			return true
		}
	}
	return false
}

func (c *Client) inspectAction(ctx context.Context, tool InspectTool, request InspectRequest) (*jnode, error) {
	switch tool.Name + "/" + request.Action {
	case "inspect-user/list":
		return c.inspectListUnits(ctx, inspectOptionalID(request.ID), request.UserLogin, nil, request.Offset, request.Limit, false)
	case "inspect-user/lock-status":
		if _, err := c.inspectUser(ctx, request.ID); err != nil {
			return nil, err
		}
		blocked, err := c.inspectPost(ctx, inspectIsUserBlockedRoute, "GetIsUserBlockedResult", request.ID, false)
		if err != nil {
			return nil, err
		}
		return toJNode(orderedFields{{"id", request.ID}, {"blocked", blocked}}), nil
	case "inspect-role/list":
		return c.inspectListUnits(ctx, inspectOptionalID(request.ID), request.Name, request.Type, request.Offset, request.Limit, true)
	case "inspect-role/memberships":
		if err := c.inspectRequireUser(ctx, request.UserID, "The member ID must identify a user."); err != nil {
			return nil, err
		}
		if request.Effective {
			return c.inspectSelect(ctx, "SysAdminUnitInRole", []string{"Id", "SysAdminUnit", "SysAdminUnitRoleId"},
				[]inspectFilter{{"SysAdminUnit", inspectGUID(request.UserID)}}, request.Offset, request.Limit)
		}
		return c.inspectSelect(ctx, "SysUserInRole", []string{"Id", "SysUser", "SysRole"},
			[]inspectFilter{{"SysUser", inspectGUID(request.UserID)}}, request.Offset, request.Limit)
	case "inspect-role/members":
		if _, err := c.inspectRole(ctx, request.ID); err != nil {
			return nil, err
		}
		if request.Effective {
			return c.inspectSelect(ctx, "SysAdminUnitInRole", []string{"Id", "SysAdminUnit", "SysAdminUnitRoleId"},
				[]inspectFilter{{"SysAdminUnitRoleId", inspectGUID(request.ID)}, {"SysAdminUnit.SysAdminUnitTypeValue", []int{4, 5, 7}}},
				request.Offset, request.Limit)
		}
		return c.inspectSelect(ctx, "SysUserInRole", []string{"Id", "SysUser", "SysRole"},
			[]inspectFilter{{"SysRole", inspectGUID(request.ID)}}, request.Offset, request.Limit)
	case "inspect-role/functional-roles":
		roleType, err := c.inspectRole(ctx, request.ID)
		if err != nil {
			return nil, err
		}
		if roleType > 3 {
			return nil, inspectRefusal("An organizational or manager role is required.")
		}
		return c.inspectSelect(ctx, "SysFuncRoleInOrgRole", []string{"Id", "OrgRole", "FuncRole"},
			[]inspectFilter{{"OrgRole", inspectGUID(request.ID)}}, request.Offset, request.Limit)
	case "inspect-license/user-list":
		userType, err := c.inspectUser(ctx, request.UserID)
		if err != nil {
			return nil, err
		}
		if userType == 7 {
			return nil, inspectRefusal("Technical accounts do not support personal user licenses.")
		}
		return c.inspectPost(ctx, inspectUserLicensesRoute, "GetAvailableLicPackagesResult", request.UserID, true)
	case "inspect-license/role-list":
		if _, err := c.inspectRole(ctx, request.RoleID); err != nil {
			return nil, err
		}
		return c.inspectSelect(ctx, "SysLicPackageInRole", []string{"Id", "SysRole", "SysLicPackage"},
			[]inspectFilter{{"SysRole", inspectGUID(request.RoleID)}}, request.Offset, request.Limit)
	case "inspect-access/ip-list":
		if _, err := c.inspectUnit(ctx, request.UnitID); err != nil {
			return nil, err
		}
		return c.inspectSelect(ctx, "SysAdminUnitIPRange", []string{"Id", "SysAdminUnit", "BeginIP", "EndIP"},
			[]inspectFilter{{"SysAdminUnit", inspectGUID(request.UnitID)}}, request.Offset, request.Limit)
	case "inspect-access/delegations":
		if _, err := c.inspectUser(ctx, request.UnitID); err != nil {
			return nil, err
		}
		return c.inspectSelect(ctx, "SysAdminUnitGrantedRight", []string{"Id", "GrantorSysAdminUnit", "GranteeSysAdminUnit"},
			[]inspectFilter{{"GranteeSysAdminUnit", inspectGUID(request.UnitID)}}, request.Offset, request.Limit)
	case "inspect-access/operations":
		filters := []inspectFilter{}
		if request.Code != nil {
			filters = append(filters, inspectFilter{"Code", *request.Code})
		}
		return c.inspectSelect(ctx, "SysAdminOperation", []string{"Id", "Name", "Code", "Description"}, filters, request.Offset, request.Limit)
	case "inspect-access/operation-grants":
		if err := inspectRequireID(request.OperationID); err != nil {
			return nil, err
		}
		rows, err := c.inspectSelect(ctx, "SysAdminOperation", []string{"Id"}, []inspectFilter{{"Id", inspectGUID(request.OperationID)}}, 0, 2)
		if err != nil {
			return nil, err
		}
		if len(rows.items) != 1 {
			return nil, inspectRefusal("Administration record count differs from the expected state. Inspect before retrying.")
		}
		return c.inspectSelect(ctx, "SysAdminOperationGrantee", []string{"Id", "SysAdminOperation", "SysAdminUnit", "CanExecute", "Position"},
			[]inspectFilter{{"SysAdminOperation", inspectGUID(request.OperationID)}}, request.Offset, request.Limit)
	}
	return nil, fmt.Errorf("unsupported inspection action %q", request.Action)
}

// inspectOptionalID is clio's `Id == Guid.Empty ? null : Id` for the list actions.
func inspectOptionalID(id string) *string {
	if id == "" || id == emptyGUID {
		return nil
	}
	return &id
}

func inspectRequireID(id string) error {
	if id == "" || id == emptyGUID {
		return inspectRefusal("Administration IDs must be nonempty GUIDs.")
	}
	return nil
}

// inspectListUnits is clio's AdministrationService.ListUnits: users are unit types 4, 5 and 7, roles 0, 1,
// 2, 3 and 6, and an explicit type replaces that default filter.
func (c *Client) inspectListUnits(ctx context.Context, id, name *string, unitType *int, offset, limit int, rolesOnly bool) (*jnode, error) {
	filters := []inspectFilter{}
	if id != nil {
		if err := inspectRequireID(*id); err != nil {
			return nil, err
		}
		filters = append(filters, inspectFilter{"Id", inspectGUID(*id)})
	}
	if name != nil {
		if strings.TrimSpace(*name) == "" || utf16Length(*name) > 250 {
			return nil, inspectRefusal("Provide a nonblank name of at most 250 characters.")
		}
		filters = append(filters, inspectFilter{"Name", *name})
	}
	switch {
	case unitType != nil:
		filters = append(filters, inspectFilter{inspectUnitTypeColumn, *unitType})
	case rolesOnly:
		filters = append(filters, inspectFilter{inspectUnitTypeColumn, []int{0, 1, 2, 3, 6}})
	default:
		filters = append(filters, inspectFilter{inspectUnitTypeColumn, []int{4, 5, 7}})
	}
	if rolesOnly && unitType != nil && !inspectIsRoleType(*unitType) {
		return nil, inspectRefusal("Role inspection accepts only role types 0, 1, 2, 3 or 6.")
	}
	return c.inspectSelect(ctx, "SysAdminUnit", inspectUnitColumns, filters, offset, limit)
}

func inspectIsRoleType(unitType int) bool {
	switch unitType {
	case 0, 1, 2, 3, 6:
		return true
	}
	return false
}

// inspectUnit is clio's GetUnit: exactly one SysAdminUnit row of any type. It returns the unit type.
func (c *Client) inspectUnit(ctx context.Context, id string) (int, error) {
	rows, err := c.inspectListUnitsAnyType(ctx, id)
	if err != nil {
		return 0, err
	}
	if len(rows.items) != 1 {
		return 0, inspectRefusal("The administration ID does not identify one existing user or role.")
	}
	value := rows.items[0].get(inspectUnitTypeColumn)
	if value == nil || value.kind != jkInteger {
		return 0, fmt.Errorf("SysAdminUnit row has no integer %s", inspectUnitTypeColumn)
	}
	unitType, err := strconv.Atoi(value.text)
	if err != nil {
		return 0, fmt.Errorf("SysAdminUnit row %s: %w", inspectUnitTypeColumn, err)
	}
	return unitType, nil
}

// inspectListUnitsAnyType is ListUnits(id, null, null, 0, 2) without the rolesOnly filter, which is how
// clio's GetUnit looks a unit up.
func (c *Client) inspectListUnitsAnyType(ctx context.Context, id string) (*jnode, error) {
	if err := inspectRequireID(id); err != nil {
		return nil, err
	}
	return c.inspectSelect(ctx, "SysAdminUnit", inspectUnitColumns, []inspectFilter{{"Id", inspectGUID(id)}}, 0, 2)
}

func (c *Client) inspectUser(ctx context.Context, id string) (int, error) {
	unitType, err := c.inspectUnit(ctx, id)
	if err == nil && !inspectIsUserType(unitType) {
		return 0, inspectRefusal("The ID must identify a user account.")
	}
	return unitType, err
}

// inspectRequireUser is the user check clio's membership reads run with their own wording.
func (c *Client) inspectRequireUser(ctx context.Context, id, message string) error {
	unitType, err := c.inspectUnit(ctx, id)
	if err == nil && !inspectIsUserType(unitType) {
		return inspectRefusal(message)
	}
	return err
}

func (c *Client) inspectRole(ctx context.Context, id string) (int, error) {
	unitType, err := c.inspectUnit(ctx, id)
	if err == nil && !inspectIsRoleType(unitType) {
		return 0, inspectRefusal("The ID must identify a role.")
	}
	return unitType, err
}

func inspectIsUserType(unitType int) bool { return unitType == 4 || unitType == 5 || unitType == 7 }

// inspectSelect is clio's AdministrationClient.Select: a pageable SelectQuery ordered by Id with one
// equality (or IN) filter per column, validated fail-closed before its rows are returned.
func (c *Client) inspectSelect(ctx context.Context, schema string, columns []string, filters []inspectFilter, offset, limit int) (*jnode, error) {
	if offset < 0 || limit < 1 || limit > 200 || len(columns) == 0 {
		return nil, inspectRefusal("Administration reads require columns, a nonnegative offset and a limit from 1 to 200.")
	}
	columnItems := orderedFields{}
	for _, column := range columns {
		direction, position := 0, -1
		if column == "Id" {
			direction, position = 1, 0
		}
		columnItems = append(columnItems, field{column, orderedFields{
			{"expression", orderedFields{{"expressionType", 0}, {"columnPath", column}}},
			{"orderDirection", direction}, {"orderPosition", position},
		}})
	}
	filterItems := orderedFields{}
	for _, filter := range filters {
		filterItems = append(filterItems, field{filter.column, inspectFilterNode(filter)})
	}
	body := toJNode(orderedFields{
		{"rootSchemaName", schema}, {"operationType", 0}, {"rowCount", limit}, {"rowsOffset", offset},
		{"isPageable", true},
		{"columns", orderedFields{{"items", columnItems}}},
		{"filters", orderedFields{{"filterType", 6}, {"logicalOperation", 0}, {"isEnabled", true}, {"items", filterItems}}},
	})
	response, err := c.postDataServiceJSON(ctx, "SelectQuery", body.stjJSON(), inspectSelectTimeout, inspectMaxResponseBytes)
	if err != nil {
		return nil, err
	}
	root, err := inspectParseJSON(response)
	if err != nil {
		return nil, err
	}
	if err := inspectRequireSuccess(root); err != nil {
		return nil, err
	}
	rows := root.get("rows")
	if !rows.isArray() {
		return nil, errors.New("SelectQuery response has no rows array")
	}
	if missing := root.get("notFoundColumns"); missing != nil && (!missing.isArray() || len(missing.items) != 0) {
		return nil, errors.New("SelectQuery reported columns it could not find")
	}
	for _, row := range rows.items {
		if !row.isObject() {
			return nil, errors.New("SelectQuery returned a row that is not an object")
		}
		for _, column := range columns {
			if _, ok := row.props[column]; !ok {
				return nil, fmt.Errorf("SelectQuery row has no %s column", column)
			}
		}
	}
	return rows, nil
}

func inspectFilterNode(filter inspectFilter) orderedFields {
	left := orderedFields{{"expressionType", 0}, {"columnPath", filter.column}}
	if values, ok := filter.value.([]int); ok {
		parameters := []any{}
		for _, value := range values {
			parameters = append(parameters, inspectParameter(value))
		}
		return orderedFields{{"filterType", 4}, {"comparisonType", 3}, {"isEnabled", true},
			{"leftExpression", left}, {"rightExpressions", parameters}}
	}
	return orderedFields{{"filterType", 1}, {"comparisonType", 3}, {"isEnabled", true},
		{"leftExpression", left}, {"rightExpression", inspectParameter(filter.value)}}
}

// inspectParameter is clio's Parameter: dataValueType 0 for a GUID, 12 for a boolean, 4 for an integer
// and 1 for text.
func inspectParameter(value any) orderedFields {
	dataValueType := 1
	switch typed := value.(type) {
	case inspectGUID:
		dataValueType, value = 0, string(typed)
	case bool:
		dataValueType = 12
	case int:
		dataValueType = 4
	}
	return orderedFields{{"expressionType", 2}, {"parameter", orderedFields{{"dataValueType", dataValueType}, {"value", value}}}}
}

// inspectRequireSuccess is clio's RequireSuccess: an object whose success (or Success) is literally true.
func inspectRequireSuccess(root *jnode) error {
	if !root.isObject() {
		return errors.New("Creatio returned an invalid administration response")
	}
	success := root.get("success")
	if success == nil {
		success = root.get("Success")
	}
	if success == nil {
		return errors.New("Creatio returned an invalid administration response")
	}
	if success.kind != jkBool || !success.flag {
		return errors.New("Creatio rejected the administration operation")
	}
	return nil
}

// inspectPost calls a read-only AdministrationService method with {"userId": id}. A boolean result is
// returned as a JSON boolean node; an array result arrives as a JSON string holding the array.
func (c *Client) inspectPost(ctx context.Context, route, resultProperty, userID string, arrayResult bool) (*jnode, error) {
	body := toJNode(orderedFields{{"userId", userID}})
	response, err := c.callService(ctx, serviceCall{Route: route, Body: body.stjJSON(), Timeout: inspectSelectTimeout, Limit: inspectMaxResponseBytes})
	if err != nil {
		return nil, err
	}
	root, err := inspectParseJSON(response)
	if err != nil {
		return nil, err
	}
	result := root.get(resultProperty)
	if !root.isObject() || result == nil {
		return nil, fmt.Errorf("%s response has no %s", route, resultProperty)
	}
	if !arrayResult {
		if result.kind != jkBool {
			return nil, fmt.Errorf("%s returned a non-boolean %s", route, resultProperty)
		}
		return result, nil
	}
	if result.kind != jkString {
		return nil, fmt.Errorf("%s returned a non-string %s", route, resultProperty)
	}
	inner, err := inspectParseJSON([]byte(result.text))
	if err != nil {
		return nil, err
	}
	if !inner.isArray() {
		return nil, fmt.Errorf("%s returned %s that is not an array", route, resultProperty)
	}
	return inner, nil
}

// inspectParseJSON parses an ordered tree that keeps every number's text as Creatio wrote it, which is
// what System.Text.Json writes back for a JsonElement. parseJNode rewrites non-integers the Newtonsoft way.
func inspectParseJSON(data []byte) (*jnode, error) {
	if strings.TrimSpace(string(data)) == "" {
		return nil, errors.New("Creatio returned an empty administration response")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	node, err := inspectReadJSON(decoder)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, errors.New("additional text found after the JSON value")
	}
	return node, nil
}

func inspectReadJSON(decoder *json.Decoder) (*jnode, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	switch value := token.(type) {
	case json.Delim:
		var node *jnode
		if value == '{' {
			node = newObject()
		} else {
			node = newArray()
		}
		for decoder.More() {
			key := ""
			if value == '{' {
				keyToken, err := decoder.Token()
				if err != nil {
					return nil, err
				}
				key = keyToken.(string)
			}
			child, err := inspectReadJSON(decoder)
			if err != nil {
				return nil, err
			}
			if value == '{' {
				node.set(key, child)
			} else {
				node.items = append(node.items, child)
			}
		}
		_, err := decoder.Token()
		return node, err
	case string:
		return newString(value), nil
	case bool:
		return newBool(value), nil
	case nil:
		return jsonNullNode(), nil
	case json.Number:
		if strings.ContainsAny(string(value), ".eE") {
			return &jnode{kind: jkFloat, text: string(value)}, nil
		}
		return &jnode{kind: jkInteger, text: string(value)}, nil
	}
	return nil, fmt.Errorf("unexpected JSON token %v", token)
}
