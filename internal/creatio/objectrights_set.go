package creatio

// set-object-rights (clio's SetObjectRightsCommand with the MCP options of SetObjectRightsTool): grant or revoke
// one role's operations on one object, or turn its operation permissions on or off. On MCP the host's approval
// of the call is the confirmation; preview writes nothing.

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ObjectRightsSetRequest is SetObjectRightsArgs; a nil pointer is an argument that was not given.
type ObjectRightsSetRequest struct {
	EntitySchemaName string
	Grantee          *string
	Operations       *string
	Revoke           bool
	Enable           bool
	Disable          bool
	Preview          bool

	clock *rightsClock
}

// objectRightsChange is the Change record every output line of a call is rendered from.
type objectRightsChange struct {
	schemaName, caption string
	granteeLabel        string
	request             objectRightsRequest
	plan                objectRightsPlan
}

func (c objectRightsChange) label() string { return rightsFormatObject(c.schemaName, c.caption) }

func (c objectRightsChange) operations() string {
	return rightsFormatOperationList(c.request.operations)
}

func (c objectRightsChange) summary() string {
	if c.request.switchOnly() {
		state := "ON"
		if c.request.disable {
			state = "OFF"
		}
		return fmt.Sprintf("Turn operation permissions %s on %s.", state, c.label())
	}
	verb := "Grant"
	if c.request.revoke {
		verb = "Revoke"
	}
	return fmt.Sprintf("%s [%s] for grantee %s on %s.", verb, c.operations(), c.granteeLabel, c.label())
}

func granteeRowOf(state objectRightsState, grantee string) *roleRow {
	for index := range state.roles {
		if state.roles[index].granteeID == grantee {
			row := state.roles[index]
			return &row
		}
	}
	return nil
}

func (c objectRightsChange) rowBefore() *roleRow {
	return granteeRowOf(c.plan.before, c.request.grantee)
}
func (c objectRightsChange) rowAfter() *roleRow { return granteeRowOf(c.plan.after, c.request.grantee) }
func (c objectRightsChange) rowChanges() bool {
	after := c.rowAfter()
	return after != nil && !after.sameRowAs(c.rowBefore())
}

// SetObjectRights runs set-object-rights: each request one attempt of at most 25 s, all within 100 s.
func (c *Client) SetObjectRights(ctx context.Context, input ObjectRightsSetRequest) ObjectRightsResponse {
	log := &rightsLog{}
	clock := realRightsClock()
	if input.clock != nil {
		clock = *input.clock
	}
	request := rightsRequest{timeout: 25 * time.Second, clock: clock, deadline: newRightsDeadline(100*time.Second, clock)}
	return log.response(c.setObjectRights(ctx, input, request, log))
}

func (c *Client) setObjectRights(ctx context.Context, input ObjectRightsSetRequest, request rightsRequest, log *rightsLog) int {
	named, change, ok := parseObjectRightsInputs(input, log)
	if !ok {
		return 1
	}
	reader := c.newObjectRightsReader()
	if !change.switchOnly() {
		name, ok := rightsResolveUnitName(ctx, reader, change.grantee, "grantee", request, log)
		if !ok {
			return 1
		}
		change.granteeName = name
	}
	readRights := func(name string) objectRightsInfo { return reader.getObjectRights(ctx, name, request) }
	resolution, ok := rightsResolveObjectName(ctx, named, readRights, reader, request, log)
	if !ok {
		return 1
	}
	if len(resolution.titleMatches) > 0 {
		log.error("Error: %s. Pass the code — a title is not unique, and this call changes access rights. Nothing was changed.",
			rightsNotACode(named, resolution.titleMatches))
		return 1
	}
	if !resolution.byCode.isRead() {
		log.error("Error: '%s': %s. Nothing was changed.", resolution.code, resolution.byCode.failureReason())
		return 1
	}
	schemaName, before := resolution.code, resolution.byCode
	plan := planObjectRights(before.state(), change)
	current := objectRightsChange{schemaName: schemaName, caption: before.caption, request: change, plan: plan}
	if !change.switchOnly() {
		current.granteeLabel = fmt.Sprintf("'%s' (%s)", rightsDisplay(change.granteeName), change.grantee)
	}
	if plan.refused() {
		log.error("Error: %s Nothing was changed.", objectRightsRefusalMessage(current))
		return 1
	}
	if !plan.changes() {
		log.info("%s: %s (no change).", current.label(), objectRightsNoChangeReason(current))
		return 0
	}
	facts := describeObjectRightsPlan(current)
	if input.Preview {
		log.info("PREVIEW — nothing was changed. %s", current.summary())
		writeRightsFacts(facts, log)
		return 0
	}
	if !rightsHasTimeFor(request, 2, schemaName, "save", "the save and the read-back need", "nothing was changed", log) {
		return 1
	}
	save := reader.saveOperations(ctx, before.snapshot, plan.after, request)
	actual := reader.getObjectRights(ctx, schemaName, request)
	if save.succeeded() {
		return reportObjectRightsSaved(current, facts, actual, log)
	}
	return reportObjectRightsFailedSave(current, facts, save, actual, log)
}

// parseObjectRightsInputs is TryParseInputs with TryReadCallShape and TryParseOperations.
func parseObjectRightsInputs(input ObjectRightsSetRequest, log *rightsLog) (string, objectRightsRequest, bool) {
	change := objectRightsRequest{grantee: emptyGUID, revoke: input.Revoke, enable: input.Enable, disable: input.Disable}
	named, ok := rightsReadObjectName(input.EntitySchemaName, log)
	if !ok {
		return "", change, false
	}
	if input.Enable && input.Disable {
		log.error("Error: --enable-operation-permissions and --disable-operation-permissions turn the switch opposite ways; pass one of them.")
		return "", change, false
	}
	namesRow := input.Grantee != nil || input.Operations != nil
	if input.Disable {
		if namesRow || input.Revoke {
			log.error("Error: --disable-operation-permissions is a call of its own: it turns operation permissions OFF and keeps every row " +
				"as it is, so --grantee, --operations and --revoke do not go with it. To clear a role's operations, revoke them in a " +
				"separate call.")
			return "", change, false
		}
		return named, change, true
	}
	if input.Enable && !namesRow && !input.Revoke {
		return named, change, true
	}
	if !namesRow && !input.Revoke {
		log.error("Error: name the change: --grantee and --operations for a grant or a revoke, or --enable-operation-permissions / " +
			"--disable-operation-permissions alone to turn operation permissions on or off.")
		return "", change, false
	}
	if input.Revoke && input.Enable {
		log.error("Error: --enable-operation-permissions applies to a grant; a revoke never turns operation permissions on.")
		return "", change, false
	}
	grantee, ok := "", false
	if input.Grantee != nil {
		grantee, ok = DataWriteParseGUID(*input.Grantee)
	}
	if !ok || grantee == emptyGUID {
		log.error("Error: --grantee must be a SysAdminUnit id (GUID).")
		return "", change, false
	}
	change.grantee = grantee
	if input.Operations == nil {
		log.error("Error: --operations is required: name the operations to grant or revoke (read,create,edit,delete).")
		return "", change, false
	}
	operations, message := parseObjectOperations(*input.Operations)
	if message != "" {
		log.error(message)
		return "", change, false
	}
	change.operations = operations
	return named, change, true
}

// rightsSplitTokens is Split(',', RemoveEmptyEntries | TrimEntries).
func rightsSplitTokens(raw string) []string {
	tokens := []string{}
	for _, token := range strings.Split(raw, ",") {
		if token = strings.TrimSpace(token); token != "" {
			tokens = append(tokens, token)
		}
	}
	return tokens
}

func parseObjectOperations(raw string) ([]objectOperation, string) {
	seen := map[objectOperation]bool{}
	for _, token := range rightsSplitTokens(raw) {
		found := false
		for index, name := range objectOperationNames {
			if strings.EqualFold(name, token) {
				seen[objectOperation(index)] = true
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Sprintf("Error: --operations: unknown operation '%s'. Use %s.", rightsDisplay(token), objectOperationsAccepted)
		}
	}
	if len(seen) == 0 {
		return nil, "Error: --operations: no operation given. Use " + objectOperationsAccepted + "."
	}
	operations := []objectOperation{}
	for operation := range seen {
		operations = append(operations, operation)
	}
	sort.Slice(operations, func(i, j int) bool { return operations[i] < operations[j] })
	return operations, ""
}

func objectRightsRefusalMessage(change objectRightsChange) string {
	label := change.label()
	plan := change.plan
	switch plan.refusal {
	case refusalEnableNotRequested:
		text := label + " does not use operation permissions yet. Granting would turn them ON, and from then on its rows decide who can reach it"
		if len(plan.rowsBecomingEffective) > 0 {
			text += " (" + rightsFormatRows(plan.rowsBecomingEffective) + ")"
		}
		if plan.addsAllEmployeesRow {
			text += "; an 'All employees' row with read/create/edit/delete would be added below them"
		}
		return text + ". Re-run with --enable-operation-permissions if that is intended."
	case refusalRevokeOnNotAdministered:
		return label + " is not administered by operation permissions (they are OFF), so the tool does not revoke on it. To limit access, " +
			"turn operation permissions on first with a grant and --enable-operation-permissions; see get-guidance object-rights."
	case refusalLeavesNoGrantingRow:
		if change.request.switchOnly() {
			return "turning operation permissions on would leave no row on " + label + " that grants any operation (" +
				rightsFormatRows(plan.rowsBecomingEffective) + "), so every internal user would be cut off. Grant the operations in the " +
				"same call instead (--grantee, --operations and --enable-operation-permissions); for internal users grant them to All " +
				"employees, whose row decides before a new row of any other role, which goes at the lowest priority."
		}
		return "after this revoke no row on " + label + " would grant any operation. A revoke never turns operation permissions off: to " +
			"turn them OFF instead — the object becomes available to ALL internal users and every row is kept as it is — call " +
			"set-object-rights with --entity-schema-name and --disable-operation-permissions only."
	case refusalDuplicateGranteeRows:
		positions := make([]string, len(plan.duplicatePositions))
		for index, position := range plan.duplicatePositions {
			positions[index] = fmt.Sprint(position)
		}
		return fmt.Sprintf("grantee %s has %d rows on %s (positions %s). Which of them decides depends on the other rows, so the tool "+
			"changes none of them. Remove the duplicates in the Object permissions designer, then re-run.",
			change.granteeLabel, len(plan.duplicatePositions), label, strings.Join(positions, ", "))
	}
	return fmt.Sprintf("the change on %s is refused (%s).", label, objectRightsRefusalNames[plan.refusal])
}

func objectRightsNoChangeReason(change objectRightsChange) string {
	if !change.request.switchOnly() {
		row := change.rowBefore()
		if row == nil {
			return fmt.Sprintf("grantee %s has no row, so there is nothing to revoke", change.granteeLabel)
		}
		granteeRow := fmt.Sprintf("the row of grantee %s at position %d", change.granteeLabel, row.position)
		if change.request.revoke {
			return fmt.Sprintf("%s already has none of [%s]", granteeRow, change.operations())
		}
		return fmt.Sprintf("%s already has [%s]", granteeRow, change.operations())
	}
	if change.request.disable {
		return "operation permissions are already OFF — the object is available to all internal users"
	}
	return "operation permissions are already ON"
}

func describeObjectRightsPlan(change objectRightsChange) []string {
	plan := change.plan
	label := change.label()
	facts := []string{}
	if plan.enables {
		facts = append(facts, "Operation permissions on "+label+" are turned ON: from then on its rows decide, in priority order, who can reach it.")
		if len(plan.rowsBecomingEffective) > 0 {
			facts = append(facts, "Rows that start to decide: "+rightsFormatRows(plan.rowsBecomingEffective)+".")
		}
		if plan.addsAllEmployeesRow {
			if row := granteeRowOf(plan.after, allEmployeesID); row != nil {
				facts = append(facts, fmt.Sprintf("An '%s' row with read/create/edit/delete is added at position %d, below the existing rows.",
					rightsDisplay(row.granteeName), row.position))
			}
		}
	}
	if plan.disables {
		text := "Operation permissions on " + label + " are turned OFF: it becomes available to ALL internal users. Every row is kept as " +
			"it is, operations included, and applies again if operation permissions are turned back on"
		if len(plan.before.roles) > 0 {
			text += ": " + rightsFormatRows(plan.before.roles) + "."
		} else {
			text += "; it has no stored rows."
		}
		facts = append(facts, text)
	}
	if change.request.switchOnly() {
		return facts
	}
	facts = append(facts, describeGranteeRow(change))
	if len(plan.rowsAboveGrantee) > 0 {
		facts = append(facts, "Rows above the grantee's row, which decide first for a user who is also in those roles: "+
			rightsFormatRows(plan.rowsAboveGrantee)+".")
	}
	return facts
}

func describeGranteeRow(change objectRightsChange) string {
	before, after := change.rowBefore(), change.rowAfter()
	if change.plan.addsGranteeRow && after != nil {
		return fmt.Sprintf("A row for the grantee is added at position %d, the lowest priority, with %s.", after.position, rightsFormatOperations(*after))
	}
	if change.rowChanges() && after != nil {
		now := rightsFormatOperations(*after)
		if change.request.revoke {
			return fmt.Sprintf("The grantee's row stays at position %d with [%s] cleared; it now has %s.", after.position, change.operations(), now)
		}
		return fmt.Sprintf("The grantee's row at position %d now has %s.", after.position, now)
	}
	if before == nil {
		return "The grantee has no row; no row changes."
	}
	return fmt.Sprintf("The grantee's row at position %d is unchanged (%s).", before.position, rightsFormatOperations(*before))
}

func objectRightsDoneLine(change objectRightsChange) string {
	if change.request.switchOnly() {
		state := "ON"
		if change.plan.disables {
			state = "OFF; every row is kept as it is"
		}
		return fmt.Sprintf("%s: operation permissions turned %s.", change.label(), state)
	}
	if change.rowChanges() {
		verb := "granted"
		if change.request.revoke {
			verb = "revoked"
		}
		return fmt.Sprintf("%s: %s [%s] for grantee %s.", change.label(), verb, change.operations(), change.granteeLabel)
	}
	granteeRow := fmt.Sprintf("the row of grantee %s is unchanged", change.granteeLabel)
	if change.rowAfter() == nil {
		granteeRow = fmt.Sprintf("grantee %s has no row", change.granteeLabel)
	}
	return fmt.Sprintf("%s: operation permissions turned ON; %s.", change.label(), granteeRow)
}

func reportObjectRightsSaved(change objectRightsChange, facts []string, actual objectRightsInfo, log *rightsLog) int {
	label := change.label()
	if !actual.isRead() {
		log.error("Error: %s: saved, but NOT verified — on the read-back %s. Check it with get-object-rights before retrying: the plan was:",
			label, actual.failureReason())
		writeRightsFacts(facts, log)
		return 1
	}
	critical, differences := compareReadBack(change.plan, change.request.grantee, actual.state())
	if len(critical) > 0 {
		log.error("Error: %s: the save reported success, but the object read back does not match the plan: %s. Check it with get-object-rights.",
			label, strings.Join(critical, "; "))
		return 1
	}
	reportObjectRightsDone(change, facts, actual.state(), differences, log)
	return 0
}

func reportObjectRightsFailedSave(change objectRightsChange, facts []string, save rightsSaveResult, actual objectRightsInfo, log *rightsLog) int {
	label := change.label()
	failure := fmt.Sprintf("the save failed (%s)", save.err)
	recheck := " Read the object with get-object-rights before retrying."
	if save.outcomeUnknown {
		failure = fmt.Sprintf("the save got no answer (%s) and may still be applied", save.err)
		recheck = " Re-read the object with get-object-rights before retrying or reporting a failure."
	}
	if !actual.isRead() {
		log.error("Error: %s: %s, and on the read-back %s.%s", label, failure, actual.failureReason(), recheck)
		return 1
	}
	critical, differences := compareReadBack(change.plan, change.request.grantee, actual.state())
	if len(critical) > 0 {
		tail := ""
		if save.outcomeUnknown {
			tail = recheck
		}
		log.error("Error: %s: %s. The object now: operation permissions %s; rows %s.%s", label, failure, rightsSwitch(actual.state()),
			rightsFormatRows(actual.roles), tail)
		return 1
	}
	matches := "matches the plan."
	if len(differences) > 0 {
		matches = "shows the switch and the rows this call writes as planned."
	}
	log.warn("%s: the save reported an error (%s), but the object read back %s", label, save.err, matches)
	reportObjectRightsDone(change, facts, actual.state(), differences, log)
	return 0
}

func reportObjectRightsDone(change objectRightsChange, facts []string, state objectRightsState, differences []string, log *rightsLog) {
	log.info(objectRightsDoneLine(change))
	writeRightsFacts(facts, log)
	log.info("  Rows now: %s.", rightsFormatRows(state.roles))
	for _, difference := range differences {
		log.warn("  Differs from the plan: %s — another client may have changed the object meanwhile.", difference)
	}
}

func writeRightsFacts(facts []string, log *rightsLog) {
	for _, fact := range facts {
		log.info("  " + fact)
	}
}
