package creatio

// clio's RightManagementServiceClient and ObjectRecordCounter: the object read through
// RightManagementService.svc/GetAdministratedObject (the Object permissions designer's own service), its save
// through SaveAdministratedObject, and the DataService lookups around them.

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	rightsGetRoute  = "ServiceModel/RightManagementService.svc/GetAdministratedObject"
	rightsSaveRoute = "ServiceModel/RightManagementService.svc/SaveAdministratedObject"

	rightsOperationRowsField = "entitySchemaOperationsRights"
	rightsRecordRulesField   = "entitySchemaRecordDefRights"
	rightsOpsSwitchField     = "administratedByOperations"
	rightsRecordsSwitchField = "administratedByRecords"
)

var rightsOperationFields = []string{"canRead", "canAppend", "canEdit", "canDelete"}
var rightsLevelFields = []string{"readRightLevel", "editRightLevel", "deleteRightLevel"}

// objectRightsInfo is ObjectRightsInfo.
type objectRightsInfo struct {
	found               bool
	name, caption       string
	administeredOps     bool
	roles               []roleRow
	readError           string
	snapshot            *jnode
	timedOut            bool
	administeredRecords bool
	rules               []recordRule
	schemaUID           string
}

func (i objectRightsInfo) state() objectRightsState {
	return objectRightsState{administered: i.administeredOps, roles: i.roles}
}

func (i objectRightsInfo) recordState() recordState {
	return recordState{administered: i.administeredRecords, rules: i.rules}
}

func (i objectRightsInfo) isRead() bool { return i.readError == "" && i.found }

func (i objectRightsInfo) failureReason() string {
	if i.isRead() {
		return ""
	}
	if i.readError != "" {
		return "its operation permissions could not be read: " + i.readError
	}
	return "the schema was not found"
}

func rightsReadFailed(name, readError string, timedOut bool) objectRightsInfo {
	return objectRightsInfo{found: true, name: name, readError: readError, timedOut: timedOut}
}

// objectRightsReader is one call's reader: the SysSchema UIds it resolves are kept for the call, as clio's
// transient client keeps them.
type objectRightsReader struct {
	client *Client
	uids   map[string][]string
}

func (c *Client) newObjectRightsReader() *objectRightsReader {
	return &objectRightsReader{client: c, uids: map[string][]string{}}
}

// getObjectRights is GetObjectRights: a failed read is reported in readError, never as "not administered".
func (r *objectRightsReader) getObjectRights(ctx context.Context, schemaName string, request rightsRequest) objectRightsInfo {
	node, readError, timedOut := r.tryGetAdministratedObject(ctx, schemaName, request)
	if node == nil {
		if readError != "" {
			return rightsReadFailed(schemaName, readError, timedOut)
		}
		return objectRightsInfo{name: schemaName}
	}
	info := objectRightsInfo{found: true, name: schemaName, caption: rightsStr(node.get("caption")),
		administeredOps: rightsFlag(node, rightsOpsSwitchField), roles: rightsProjectRoles(node), snapshot: node,
		administeredRecords: rightsFlag(node, rightsRecordsSwitchField), rules: rightsProjectRules(node), schemaUID: emptyGUID}
	if name := node.get("name"); name.isString() {
		info.name = name.text
	}
	if uid, ok := DataWriteParseGUID(rightsStr(node.get("uId"))); ok {
		info.schemaUID = uid
	}
	return info
}

func (r *objectRightsReader) tryGetAdministratedObject(ctx context.Context, schemaName string, request rightsRequest) (*jnode, string, bool) {
	candidates, err := r.resolveSchemaUIDs(ctx, schemaName, request)
	if err != nil {
		return nil, rightsDisplayFailure(err), rightsIsTimeout(err)
	}
	if len(candidates) == 0 {
		return nil, "", false
	}
	firstError := ""
	for _, candidate := range candidates {
		node, fetchError, timedOut := r.fetchNode(ctx, candidate, request)
		if node != nil {
			return node, "", false
		}
		if timedOut {
			if firstError == "" {
				return nil, fetchError, true
			}
			return nil, rightsDisplayError(firstError + "; then " + fetchError), true
		}
		if firstError == "" {
			firstError = fetchError
		}
	}
	return nil, firstError, false
}

func (r *objectRightsReader) fetchNode(ctx context.Context, uid string, request rightsRequest) (*jnode, string, bool) {
	body, _ := json.Marshal(map[string]string{"schemaUId": uid})
	response, err := r.client.rightsPostNode(ctx, rightsGetRoute, body, request)
	if err == nil {
		err = rightsCheckResponseShape(r.client, rightsGetRoute, response, "administratedObject")
	}
	if err != nil {
		return nil, rightsDisplayFailure(err), rightsIsTimeout(err)
	}
	success := rightsResponseSuccess(response)
	if success {
		if object := response.get("administratedObject"); object.isObject() {
			return object, "", false
		}
	}
	if response != nil && response.kind == jkObject && !success {
		return nil, rightsServiceMessage(rightsErrorMessage(response), "GetAdministratedObject reported failure."), false
	}
	return nil, "GetAdministratedObject returned no administrated object.", false
}

// rightsCheckResponseShape fails as System.Text.Json would on a member of the wrong type: success must be a
// boolean, the object (when named) an object.
func rightsCheckResponseShape(c *Client, route string, response *jnode, objectField string) error {
	if response == nil || response.kind != jkObject {
		return nil
	}
	if value := processDescribeProperty(response, "success"); value != nil && value.kind != jkBool {
		return &rightsFault{message: "Unexpected response from " + c.serviceURL(route) + ": " + rightsSanitize(string(response.stjJSON()), 200)}
	}
	if objectField != "" {
		if value := processDescribeProperty(response, objectField); value != nil && value.kind != jkObject && value.kind != jkNull {
			return &rightsFault{message: "Unexpected response from " + c.serviceURL(route) + ": " + rightsSanitize(string(response.stjJSON()), 200)}
		}
	}
	return nil
}

func rightsResponseSuccess(response *jnode) bool {
	value := processDescribeProperty(response, "success")
	return value != nil && value.kind == jkBool && value.flag
}

func rightsErrorMessage(response *jnode) string {
	if value := processDescribeProperty(processDescribeProperty(response, "errorInfo"), "message"); value != nil && value.kind == jkString {
		return value.text
	}
	return ""
}

func rightsServiceMessage(message, fallback string) string {
	if strings.TrimSpace(message) == "" {
		return fallback
	}
	return rightsDisplayError(message)
}

// resolveSchemaUIDs is QueryEntitySchemaUIds: the EntitySchemaManager rows of the name, base row first.
func (r *objectRightsReader) resolveSchemaUIDs(ctx context.Context, schemaName string, request rightsRequest) ([]string, error) {
	if cached, ok := r.uids[schemaName]; ok {
		return cached, nil
	}
	query := rightsSelectQuery("SysSchema",
		[]rightsColumn{{path: "ExtendParent", alias: "ExtendParent", orderDirection: 1, orderPosition: 0}, {path: "UId", alias: "UId"}},
		[]rightsFilter{{"Name", schemaName, rightsTextType}, {"ManagerName", "EntitySchemaManager", rightsTextType}}, 20, false)
	rows, err := r.client.rightsSelect(ctx, query, request)
	if err != nil {
		return nil, err
	}
	uids := []string{}
	seen := map[string]bool{}
	for _, row := range rows {
		uid, ok := DataWriteParseGUID(rightsStr(processDescribeProperty(row, "UId")))
		if !ok || uid == emptyGUID || seen[uid] {
			continue
		}
		seen[uid] = true
		uids = append(uids, uid)
	}
	r.uids[schemaName] = uids
	return uids, nil
}

// findObjectsByTitle is FindObjectsByTitle: the objects whose title the name is, one per code, ordered by code.
func (r *objectRightsReader) findObjectsByTitle(ctx context.Context, title string, request rightsRequest) ([]objectTitleMatch, error) {
	query := rightsSelectQuery("SysSchema",
		[]rightsColumn{{path: "Name", alias: "Name", orderDirection: 1, orderPosition: 0}, {path: "Caption", alias: "Caption"}},
		[]rightsFilter{{"Caption", title, rightsTextType}, {"ManagerName", "EntitySchemaManager", rightsTextType}}, 100, true)
	rows, err := r.client.rightsSelect(ctx, query, request)
	if err != nil {
		return nil, err
	}
	matches := []objectTitleMatch{}
	index := map[string]bool{}
	for _, row := range rows {
		name := rightsStr(processDescribeProperty(row, "Name"))
		if _, ok := rightsNormalizeSchemaName(name); !ok {
			continue
		}
		key := strings.ToUpper(strings.TrimSpace(name))
		if index[key] {
			continue
		}
		index[key] = true
		matches = append(matches, objectTitleMatch{name: strings.TrimSpace(name), caption: rightsStr(processDescribeProperty(row, "Caption"))})
	}
	return matches, nil
}

// resolveGranteeName is ResolveGranteeName: the SysAdminUnit name, or nil when no such role or user exists.
func (r *objectRightsReader) resolveGranteeName(ctx context.Context, id string, request rightsRequest) (*string, error) {
	query := rightsSelectQuery("SysAdminUnit", []rightsColumn{{path: "Name", alias: "Name"}},
		[]rightsFilter{{"Id", id, rightsGUIDType}}, 1, false)
	rows, err := r.client.rightsSelect(ctx, query, request)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	value := processDescribeProperty(rows[0], "Name")
	if value == nil || value.kind != jkString {
		return nil, nil
	}
	name := value.text
	return &name, nil
}

// countRecords is ObjectRecordCounter.CountRecords: one short attempt of at most 10 s.
func (r *objectRightsReader) countRecords(ctx context.Context, schemaName string, request rightsRequest) (int64, error) {
	if request.timeout > 10*time.Second {
		request.timeout = 10 * time.Second
	}
	rows, err := r.client.rightsSelect(ctx, rightsCountQuery(schemaName, "Count"), request)
	if err != nil {
		return 0, err
	}
	if len(rows) > 0 {
		if value := processDescribeProperty(rows[0], "Count"); value != nil && value.kind == jkInteger {
			if count, parseErr := strconv.ParseInt(value.text, 10, 64); parseErr == nil {
				return count, nil
			}
		}
	}
	return 0, &rightsFault{message: fmt.Sprintf("The record count of '%s' returned no row.", schemaName)}
}

// ---- saves ----

// rightsSaveResult is ObjectRightsSaveResult: "" on success, otherwise why, and whether no answer came.
type rightsSaveResult struct {
	err            string
	outcomeUnknown bool
}

func (s rightsSaveResult) succeeded() bool { return s.err == "" }

// saveOperations is Save(snapshot, ObjectRightsState): the switch and only the rows the plan changes or adds.
func (r *objectRightsReader) saveOperations(ctx context.Context, snapshot *jnode, after objectRightsState, request rightsRequest) rightsSaveResult {
	payload := snapshot.clone()
	rows := payload.get(rightsOperationRowsField)
	if !rows.isArray() {
		rows = newArray()
		payload.set(rightsOperationRowsField, rows)
	}
	if conflict := rightsApplyPlannedRows(rows, after.roles); conflict != "" {
		return rightsSaveResult{err: conflict}
	}
	payload.set(rightsOpsSwitchField, newBool(after.administered))
	payload.set(rightsRecordRulesField, jsonNullNode())
	payload.set("entitySchemaColumnsRights", jsonNullNode())
	payload.set("entityOperationGrantees", jsonNullNode())
	return r.sendSave(ctx, payload, request)
}

// saveRecords is Save(snapshot, DefaultRecordRightsState): the switch, and the full rule list when it changes.
func (r *objectRightsReader) saveRecords(ctx context.Context, snapshot *jnode, after recordState, request rightsRequest) rightsSaveResult {
	payload := snapshot.clone()
	payload.set(rightsRecordsSwitchField, newBool(after.administered))
	if (recordState{administered: after.administered, rules: rightsProjectRules(payload)}).rulesDifferFrom(after) {
		payload.set(rightsRecordRulesField, rightsBuildRecordRules(payload, after.rules))
	} else {
		payload.set(rightsRecordRulesField, jsonNullNode())
	}
	payload.set(rightsOperationRowsField, jsonNullNode())
	payload.set("entitySchemaColumnsRights", jsonNullNode())
	payload.set("entityOperationGrantees", jsonNullNode())
	return r.sendSave(ctx, payload, request)
}

// sendSave sends the save exactly once; a save the deadline leaves no time for is not sent.
func (r *objectRightsReader) sendSave(ctx context.Context, payload *jnode, request rightsRequest) rightsSaveResult {
	if request.deadline != nil && request.deadline.spent() {
		return rightsSaveResult{err: "the call's time limit was spent before the save, so it was not sent"}
	}
	body := newObject()
	body.set("administratedObject", payload)
	response, err := r.client.rightsPostNode(ctx, rightsSaveRoute, body.stjJSON(), request)
	if err == nil {
		err = rightsCheckResponseShape(r.client, rightsSaveRoute, response, "administratedObject")
	}
	if err != nil {
		return rightsSaveResult{err: rightsDisplayFailure(err), outcomeUnknown: rightsLeavesOutcomeUnknown(err)}
	}
	if rightsResponseSuccess(response) {
		return rightsSaveResult{}
	}
	return rightsSaveResult{err: rightsServiceMessage(rightsErrorMessage(response), "SaveAdministratedObject reported failure.")}
}

// rightsApplyPlannedRows is ApplyPlannedRows: unchanged rows stay as read; a changed row is the one read row of
// the grantee at the position, or a new row.
func rightsApplyPlannedRows(rows *jnode, planned []roleRow) string {
	type entry struct {
		node *jnode
		read roleRow
	}
	unmatched := []entry{}
	index := 0
	for _, item := range rows.items {
		if !item.isObject() {
			continue
		}
		unmatched = append(unmatched, entry{item, rightsProjectRow(item, index)})
		index++
	}
	changed := []roleRow{}
	for _, row := range planned {
		match := -1
		for position := range unmatched {
			if unmatched[position].read.sameRowAs(&row) {
				match = position
				break
			}
		}
		if match >= 0 {
			unmatched = append(unmatched[:match], unmatched[match+1:]...)
		} else {
			changed = append(changed, row)
		}
	}
	for _, row := range changed {
		candidates := []int{}
		for position := range unmatched {
			if unmatched[position].read.granteeID == row.granteeID && unmatched[position].read.position == row.position {
				candidates = append(candidates, position)
			}
		}
		if len(candidates) > 1 {
			return fmt.Sprintf("%d rows of %s sit at position %d, so the row to change is ambiguous; nothing was sent",
				len(candidates), rightsDisplay(row.granteeName), row.position)
		}
		var node *jnode
		if len(candidates) == 1 {
			node = unmatched[candidates[0]].node
			unmatched = append(unmatched[:candidates[0]], unmatched[candidates[0]+1:]...)
		} else {
			node = newObject()
			unit := newObject()
			unit.set("id", newString(row.granteeID))
			node.set("sysAdminUnit", unit)
			node.set("position", newInt(row.position))
			rows.items = append(rows.items, node)
		}
		for operation, field := range rightsOperationFields {
			node.set(field, newBool(row.ops[operation]))
		}
	}
	return ""
}

// rightsBuildRecordRules is BuildRecordRules: the full planned rule list.
func rightsBuildRecordRules(node *jnode, planned []recordRule) *jnode {
	type entry struct {
		node *jnode
		read recordRule
	}
	read := []entry{}
	for _, item := range rightsRuleNodes(node) {
		read = append(read, entry{item, rightsProjectRule(item)})
	}
	rules := newArray()
	for _, rule := range planned {
		match := -1
		for index := range read {
			if read[index].read.samePairAs(&rule) {
				match = index
				break
			}
		}
		var ruleNode *jnode
		var readRule *recordRule
		if match >= 0 {
			ruleNode = read[match].node.clone()
			previous := read[match].read
			readRule = &previous
		} else {
			ruleNode = newObject()
			author := newObject()
			author.set("id", newString(rule.authorID))
			grantee := newObject()
			grantee.set("id", newString(rule.granteeID))
			ruleNode.set("authorSysAdminUnit", author)
			ruleNode.set("granteeSysAdminUnit", grantee)
		}
		for operation, field := range rightsLevelFields {
			if readRule == nil || readRule.levels[operation] != rule.levels[operation] {
				ruleNode.set(field, newInt(rule.levels[operation]))
			}
		}
		if readRule == nil || readRule.doNotApplyForManager != rule.doNotApplyForManager {
			ruleNode.set("doNotApplyForManager", newBool(rule.doNotApplyForManager))
		}
		if match >= 0 {
			read = append(read[:match], read[match+1:]...)
		}
		rules.items = append(rules.items, ruleNode)
	}
	return rules
}

// ---- projection ----

func rightsProjectRoles(node *jnode) []roleRow {
	rows := []roleRow{}
	index := 0
	if list := node.get(rightsOperationRowsField); list.isArray() {
		for _, item := range list.items {
			if item.isObject() {
				rows = append(rows, rightsProjectRow(item, index))
				index++
			}
		}
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].position < rows[j].position })
	return rows
}

func rightsProjectRow(row *jnode, index int) roleRow {
	unit := row.get("sysAdminUnit")
	name := "(unknown)"
	if value := unit.get("name"); value.isString() {
		name = value.text
	}
	projected := roleRow{granteeID: rightsUnitID(unit), granteeName: name, position: index}
	if position, ok := rightsInt(row.get("position")); ok {
		projected.position = position
	}
	for operation, field := range rightsOperationFields {
		projected.ops[operation] = rightsFlag(row, field)
	}
	return projected
}

func rightsRuleNodes(node *jnode) []*jnode {
	nodes := []*jnode{}
	if list := node.get(rightsRecordRulesField); list.isArray() {
		for _, item := range list.items {
			if item.isObject() {
				nodes = append(nodes, item)
			}
		}
	}
	return nodes
}

func rightsProjectRules(node *jnode) []recordRule {
	rules := []recordRule{}
	for _, item := range rightsRuleNodes(node) {
		rules = append(rules, rightsProjectRule(item))
	}
	return rules
}

func rightsProjectRule(rule *jnode) recordRule {
	author, grantee := rule.get("authorSysAdminUnit"), rule.get("granteeSysAdminUnit")
	projected := recordRule{authorID: rightsUnitID(author), authorName: rightsStr(author.get("name")),
		granteeID: rightsUnitID(grantee), granteeName: rightsStr(grantee.get("name")),
		doNotApplyForManager: rightsFlag(rule, "doNotApplyForManager")}
	for operation, field := range rightsLevelFields {
		if level, ok := rightsInt(rule.get(field)); ok {
			projected.levels[operation] = level
		}
	}
	return projected
}

func rightsUnitID(unit *jnode) string {
	if id, ok := DataWriteParseGUID(rightsStr(unit.get("id"))); ok {
		return id
	}
	return emptyGUID
}

func (n *jnode) isString() bool { return n != nil && n.kind == jkString }

func rightsStr(node *jnode) string {
	if node.isString() {
		return node.text
	}
	return ""
}

func rightsFlag(node *jnode, name string) bool {
	value := node.get(name)
	return value != nil && value.kind == jkBool && value.flag
}

func rightsInt(node *jnode) (int, bool) {
	if node == nil || node.kind != jkInteger {
		return 0, false
	}
	value, err := strconv.ParseInt(node.text, 10, 32)
	return int(value), err == nil
}
