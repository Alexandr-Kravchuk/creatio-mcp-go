package creatio

// clio's ObjectRightsPlanner, ObjectRightsReadBackVerifier and DefaultRecordRightsPlanner: pure policy, no I/O.

import (
	"fmt"
	"sort"
)

// objectRightsRequest is ObjectRightsChangeRequest.
type objectRightsRequest struct {
	grantee     string // emptyGUID or "" for a switch-only call
	granteeName string
	operations  []objectOperation
	revoke      bool
	enable      bool
	disable     bool
}

func (r objectRightsRequest) switchOnly() bool {
	return (r.grantee == "" || r.grantee == emptyGUID) && len(r.operations) == 0
}

type objectRightsRefusal int

const (
	refusalNone objectRightsRefusal = iota
	refusalEnableNotRequested
	refusalRevokeOnNotAdministered
	refusalLeavesNoGrantingRow
	refusalDuplicateGranteeRows
)

var objectRightsRefusalNames = []string{"None", "EnableNotRequested", "RevokeOnNotAdministered", "LeavesNoGrantingRow", "DuplicateGranteeRows"}

// objectRightsPlan is ObjectRightsPlan.
type objectRightsPlan struct {
	before, after         objectRightsState
	refusal               objectRightsRefusal
	enables, disables     bool
	addsGranteeRow        bool
	addsAllEmployeesRow   bool
	rowsBecomingEffective []roleRow
	rowsAboveGrantee      []roleRow
	duplicatePositions    []int
}

func (p objectRightsPlan) refused() bool { return p.refusal != refusalNone }
func (p objectRightsPlan) changes() bool { return !p.refused() && !p.after.sameAs(p.before) }

// planObjectRights is ObjectRightsPlanner.Plan. The callers validate the request shape first, as clio's
// command does, so the planner's argument exceptions cannot be reached.
func planObjectRights(before objectRightsState, request objectRightsRequest) objectRightsPlan {
	rows := append([]roleRow{}, before.roles...)
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].position < rows[j].position })
	ordered := objectRightsState{administered: before.administered, roles: rows}
	if request.switchOnly() {
		if request.disable {
			return planDisable(ordered)
		}
		return planEnable(ordered, rows)
	}
	granteeRows := []roleRow{}
	for _, row := range rows {
		if row.granteeID == request.grantee {
			granteeRows = append(granteeRows, row)
		}
	}
	if len(granteeRows) > 1 {
		positions := make([]int, len(granteeRows))
		for index, row := range granteeRows {
			positions[index] = row.position
		}
		return refuseObjectRights(ordered, refusalDuplicateGranteeRows, positions, nil, false)
	}
	var granteeRow *roleRow
	if len(granteeRows) == 1 {
		granteeRow = &granteeRows[0]
	}
	if request.revoke {
		return planRevoke(ordered, rows, granteeRow, request)
	}
	return planGrant(ordered, rows, granteeRow, request)
}

func planGrant(before objectRightsState, rows []roleRow, granteeRow *roleRow, request objectRightsRequest) objectRightsPlan {
	enabling := !before.administered
	after := append([]roleRow{}, rows...)
	next := nextPosition(rows)
	var allEmployees *roleRow
	if enabling && request.grantee != allEmployeesID {
		allEmployees = allEmployeesRowToAdd(before, next)
	}
	addsAllEmployees := allEmployees != nil
	if addsAllEmployees {
		after = append(after, *allEmployees)
		next++
	}
	var granted roleRow
	if granteeRow == nil {
		granted = roleRow{granteeID: request.grantee, granteeName: request.granteeName, position: next}.with(request.operations, true)
		after = append(after, granted)
	} else {
		granted = granteeRow.with(request.operations, true)
		for index := range after {
			if after[index] == *granteeRow {
				after[index] = granted
				break
			}
		}
	}
	if enabling && !request.enable {
		return refuseObjectRights(before, refusalEnableNotRequested, nil, otherRows(rows, request.grantee), addsAllEmployees)
	}
	effective := []roleRow{}
	if enabling {
		effective = otherRows(rows, request.grantee)
	}
	return objectRightsPlan{before: before, after: objectRightsState{administered: true, roles: after}, enables: enabling,
		addsGranteeRow: granteeRow == nil, addsAllEmployeesRow: addsAllEmployees, rowsBecomingEffective: effective,
		rowsAboveGrantee: rowsAbove(after, granted)}
}

func planRevoke(before objectRightsState, rows []roleRow, granteeRow *roleRow, request objectRightsRequest) objectRightsPlan {
	if !before.administered {
		return refuseObjectRights(before, refusalRevokeOnNotAdministered, nil, nil, false)
	}
	after := append([]roleRow{}, rows...)
	changedRows := false
	if granteeRow != nil {
		for index := range after {
			if after[index] == *granteeRow {
				after[index] = granteeRow.with(request.operations, false)
				break
			}
		}
		for index := range after {
			if after[index] != rows[index] {
				changedRows = true
			}
		}
	}
	if changedRows {
		granting := false
		for _, row := range after {
			if row.hasAny() {
				granting = true
			}
		}
		if !granting {
			return refuseObjectRights(before, refusalLeavesNoGrantingRow, nil, nil, false)
		}
	}
	above := []roleRow{}
	if granteeRow != nil {
		above = rowsAbove(after, *granteeRow)
	}
	return objectRightsPlan{before: before, after: objectRightsState{administered: before.administered, roles: after}, rowsAboveGrantee: above}
}

func planDisable(before objectRightsState) objectRightsPlan {
	if !before.administered {
		return objectRightsPlan{before: before, after: before}
	}
	return objectRightsPlan{before: before, after: objectRightsState{administered: false, roles: before.roles}, disables: true}
}

func planEnable(before objectRightsState, rows []roleRow) objectRightsPlan {
	if before.administered {
		return objectRightsPlan{before: before, after: before}
	}
	after := append([]roleRow{}, rows...)
	allEmployees := allEmployeesRowToAdd(before, nextPosition(rows))
	addsAllEmployees := allEmployees != nil
	if addsAllEmployees {
		after = append(after, *allEmployees)
	}
	granting := false
	for _, row := range after {
		if row.hasAny() {
			granting = true
		}
	}
	if !granting {
		return refuseObjectRights(before, refusalLeavesNoGrantingRow, nil, rows, addsAllEmployees)
	}
	return objectRightsPlan{before: before, after: objectRightsState{administered: true, roles: after}, enables: true,
		addsAllEmployeesRow: addsAllEmployees, rowsBecomingEffective: rows}
}

func allEmployeesRowToAdd(before objectRightsState, position int) *roleRow {
	if before.hasAllEmployeesRow() {
		return nil
	}
	return &roleRow{granteeID: allEmployeesID, granteeName: "All employees", position: position, ops: [4]bool{true, true, true, true}}
}

func rowsAbove(rows []roleRow, granteeRow roleRow) []roleRow {
	above := []roleRow{}
	for _, row := range rows {
		if row.position < granteeRow.position && row.granteeID != granteeRow.granteeID {
			above = append(above, row)
		}
	}
	return above
}

func refuseObjectRights(before objectRightsState, refusal objectRightsRefusal, duplicates []int, effective []roleRow, addsAllEmployees bool) objectRightsPlan {
	if effective == nil {
		effective = []roleRow{}
	}
	return objectRightsPlan{before: before, after: before, refusal: refusal, addsAllEmployeesRow: addsAllEmployees,
		rowsBecomingEffective: effective, duplicatePositions: duplicates}
}

func otherRows(rows []roleRow, grantee string) []roleRow {
	other := []roleRow{}
	for _, row := range rows {
		if row.granteeID != grantee {
			other = append(other, row)
		}
	}
	return other
}

func nextPosition(rows []roleRow) int {
	highest := -1
	for _, row := range rows {
		if row.position > highest {
			highest = row.position
		}
	}
	return highest + 1
}

// ---- read-back (ObjectRightsReadBackVerifier) ----

// compareReadBack is ObjectRightsReadBackVerifier.Compare: critical differences (the call's own change did not
// land) and other differences (another client may have changed the object).
func compareReadBack(plan objectRightsPlan, grantee string, actual objectRightsState) (critical, differences []string) {
	planned := plan.after
	written := rowsWritten(plan)
	critical, differences = []string{}, []string{}
	if actual.administered != planned.administered {
		critical = append(critical, fmt.Sprintf("operation permissions are %s, the plan turned them %s", rightsSwitch(actual), rightsSwitch(planned)))
	}
	missing, extraAll := planned.diffRows(actual)
	extra := []roleRow{}
	for _, row := range extraAll {
		if !isSynthesizedAfterDisable(plan, actual, row) {
			extra = append(extra, row)
		}
	}
	for _, row := range missing {
		var read *roleRow
		for index := range extra {
			if extra[index].granteeID == row.granteeID && extra[index].position == row.position {
				found := extra[index]
				read = &found
				extra = append(extra[:index], extra[index+1:]...)
				break
			}
		}
		text := rightsFormatRow(row, false) + " is missing"
		if read != nil {
			text = rightsFormatRow(*read, false) + ", the plan wrote " + rightsFormatOperations(row)
		}
		claimed := row.granteeID == grantee
		for _, writtenRow := range written {
			if writtenRow.sameRowAs(&row) {
				claimed = true
			}
		}
		if claimed {
			critical = append(critical, text)
		} else {
			differences = append(differences, text)
		}
	}
	for _, row := range extra {
		text := rightsFormatRow(row, false) + " is not in the plan"
		if row.granteeID == grantee {
			critical = append(critical, text)
		} else {
			differences = append(differences, text)
		}
	}
	return critical, differences
}

func isSynthesizedAfterDisable(plan objectRightsPlan, actual objectRightsState, row roleRow) bool {
	return plan.disables && len(plan.after.roles) == 0 && !actual.administered && len(actual.roles) == 1 &&
		row.granteeID == allEmployeesID && row.position == 0 && row.ops == [4]bool{true, true, true, true}
}

func rowsWritten(plan objectRightsPlan) []roleRow {
	_, written := plan.before.diffRows(plan.after)
	contains := func(row roleRow) bool {
		for _, item := range written {
			if item == row {
				return true
			}
		}
		return false
	}
	if plan.enables {
		for _, row := range plan.after.roles {
			if row.granteeID == allEmployeesID && !contains(row) {
				written = append(written, row)
			}
		}
	}
	if plan.disables {
		for _, row := range plan.after.roles {
			if !contains(row) {
				written = append(written, row)
			}
		}
	}
	return written
}

// ---- default record rules (DefaultRecordRightsPlanner) ----

// recordRuleChange is DefaultRecordRuleChange.
type recordRuleChange struct {
	author, authorName   string
	grantee, granteeName string
	operations           []recordOperation
	level                int
	doNotApplyForManager *bool
	revoke               bool
}

// recordRightsRequest is DefaultRecordRightsChangeRequest.
type recordRightsRequest struct {
	rule    *recordRuleChange
	enable  bool
	disable bool
}

type recordRefusal int

const (
	recordRefusalNone recordRefusal = iota
	recordRefusalEnableNotRequested
	recordRefusalDuplicatePairs
	recordRefusalInvalidStoredLevel
)

var recordRefusalNames = []string{"None", "EnableNotRequested", "DuplicatePairs", "InvalidStoredLevel"}

// recordRightsPlan is DefaultRecordRightsPlan.
type recordRightsPlan struct {
	before, after          recordState
	refusal                recordRefusal
	ruleBefore             *recordRule
	ruleAfter              *recordRule
	storedComingIntoEffect []recordRule
	problemRules           []recordRule
}

func (p recordRightsPlan) refused() bool { return p.refusal != recordRefusalNone }
func (p recordRightsPlan) changes() bool { return !p.refused() && len(p.before.diff(p.after)) > 0 }
func (p recordRightsPlan) enables() bool {
	return !p.refused() && !p.before.administered && p.after.administered
}
func (p recordRightsPlan) disables() bool {
	return !p.refused() && p.before.administered && !p.after.administered
}
func (p recordRightsPlan) changesRules() bool {
	return !p.refused() && p.before.rulesDifferFrom(p.after)
}

// planRecordRights is DefaultRecordRightsPlanner.Plan (the command validates the request shape first).
func planRecordRights(before recordState, request recordRightsRequest) recordRightsPlan {
	change := request.rule
	var ruleBefore, ruleAfter *recordRule
	if change != nil {
		ruleBefore = before.ruleFor(change.author, change.grantee)
		ruleAfter = applyRuleChange(*change, ruleBefore)
	}
	rules := replaceRule(before.rules, ruleBefore, ruleAfter)
	switchAfter := request.enable || (!request.disable && before.administered)
	after := recordState{administered: switchAfter, rules: rules}
	grants := change != nil && !change.revoke
	if grants && !switchAfter {
		return recordRightsPlan{before: before, after: before, refusal: recordRefusalEnableNotRequested, ruleBefore: ruleBefore,
			ruleAfter: ruleAfter, storedComingIntoEffect: before.rules, problemRules: []recordRule{}}
	}
	if before.rulesDifferFrom(after) {
		type pair struct{ author, grantee string }
		counts := map[pair]int{}
		order := []pair{}
		for _, rule := range before.rules {
			key := pair{rule.authorID, rule.granteeID}
			if counts[key] == 0 {
				order = append(order, key)
			}
			counts[key]++
		}
		duplicates := []recordRule{}
		for _, key := range order {
			if counts[key] < 2 {
				continue
			}
			for _, rule := range before.rules {
				if rule.authorID == key.author && rule.granteeID == key.grantee {
					duplicates = append(duplicates, rule)
				}
			}
		}
		if len(duplicates) > 0 {
			return recordRightsPlan{before: before, after: before, refusal: recordRefusalDuplicatePairs, ruleBefore: ruleBefore,
				ruleAfter: ruleAfter, storedComingIntoEffect: []recordRule{}, problemRules: duplicates}
		}
		invalid := []recordRule{}
		for _, rule := range before.rules {
			if rule.hasInvalidLevel() {
				invalid = append(invalid, rule)
			}
		}
		if len(invalid) > 0 {
			return recordRightsPlan{before: before, after: before, refusal: recordRefusalInvalidStoredLevel, ruleBefore: ruleBefore,
				ruleAfter: ruleAfter, storedComingIntoEffect: []recordRule{}, problemRules: invalid}
		}
	}
	effective := []recordRule{}
	if !before.administered && switchAfter {
		effective = after.rules
	}
	return recordRightsPlan{before: before, after: after, ruleBefore: ruleBefore, ruleAfter: ruleAfter,
		storedComingIntoEffect: effective, problemRules: []recordRule{}}
}

func applyRuleChange(change recordRuleChange, ruleBefore *recordRule) *recordRule {
	if change.revoke {
		if ruleBefore == nil {
			return nil
		}
		revoked := ruleBefore.with(change.operations, 0)
		if revoked.isEmpty() {
			return nil
		}
		return &revoked
	}
	base := recordRule{authorID: change.author, authorName: change.authorName, granteeID: change.grantee, granteeName: change.granteeName}
	if ruleBefore != nil {
		base = *ruleBefore
	}
	granted := base.with(change.operations, change.level)
	if change.doNotApplyForManager != nil {
		granted.doNotApplyForManager = *change.doNotApplyForManager
	}
	return &granted
}

func replaceRule(rules []recordRule, ruleBefore, ruleAfter *recordRule) []recordRule {
	result := append([]recordRule{}, rules...)
	index := -1
	if ruleBefore != nil {
		for position := range result {
			if result[position] == *ruleBefore {
				index = position
				break
			}
		}
	}
	switch {
	case index >= 0 && ruleAfter == nil:
		result = append(result[:index], result[index+1:]...)
	case index >= 0:
		result[index] = *ruleAfter
	case ruleAfter != nil:
		result = append(result, *ruleAfter)
	}
	return result
}
