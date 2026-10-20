package creatio

// set-default-record-rights and apply-default-record-rights (clio's SetDefaultRecordRightsCommand and
// ApplyDefaultRecordRightsCommand with the MCP options of their tools).

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// RecordRightsSetRequest is SetDefaultRecordRightsArgs; a nil pointer is an argument that was not given.
type RecordRightsSetRequest struct {
	EntitySchemaName     string
	Author               *string
	Grantee              *string
	Operations           *string
	Level                *string
	DoNotApplyForManager *bool
	Revoke               bool
	Enable               bool
	Disable              bool
	Preview              bool

	clock *rightsClock
}

type recordRightsChange struct {
	schemaName string
	request    recordRightsRequest
	plan       recordRightsPlan
}

func (c recordRightsChange) pair() string {
	rule := c.request.rule
	return fmt.Sprintf("'%s' (%s) → '%s' (%s)", rightsDisplay(rule.authorName), rule.author, rightsDisplay(rule.granteeName), rule.grantee)
}

func (c recordRightsChange) summary() string {
	parts := []string{}
	if c.request.enable {
		parts = append(parts, "turn record permissions ON")
	}
	if rule := c.request.rule; rule != nil {
		if rule.revoke {
			parts = append(parts, fmt.Sprintf("revoke [%s] of rule %s", recordOperationList(rule.operations), c.pair()))
		} else {
			manager := ""
			if rule.doNotApplyForManager != nil {
				manager = ", do not apply for manager: " + rightsLower(*rule.doNotApplyForManager)
			}
			parts = append(parts, fmt.Sprintf("grant [%s] %s by rule %s%s", recordOperationList(rule.operations), recordLevelName(rule.level),
				c.pair(), manager))
		}
	}
	if c.request.disable {
		parts = append(parts, "turn record permissions OFF")
	}
	return fmt.Sprintf("On '%s': %s.", c.schemaName, strings.Join(parts, "; "))
}

type recordRuleInput struct {
	author, grantee string
	operations      []recordOperation
	level           int
}

// SetDefaultRecordRights runs set-default-record-rights: each request one attempt of at most 25 s, all within 100 s.
func (c *Client) SetDefaultRecordRights(ctx context.Context, input RecordRightsSetRequest) ObjectRightsResponse {
	log := &rightsLog{}
	clock := realRightsClock()
	if input.clock != nil {
		clock = *input.clock
	}
	request := rightsRequest{timeout: 25 * time.Second, clock: clock, deadline: newRightsDeadline(100*time.Second, clock)}
	return log.response(c.setDefaultRecordRights(ctx, input, request, log))
}

func (c *Client) setDefaultRecordRights(ctx context.Context, input RecordRightsSetRequest, request rightsRequest, log *rightsLog) int {
	schemaName, rule, ok := parseRecordRightsInputs(input, log)
	if !ok {
		return 1
	}
	reader := c.newObjectRightsReader()
	var ruleChange *recordRuleChange
	if rule != nil {
		authorName, ok := rightsResolveUnitName(ctx, reader, rule.author, "author", request, log)
		if !ok {
			return 1
		}
		granteeName, ok := rightsResolveUnitName(ctx, reader, rule.grantee, "grantee", request, log)
		if !ok {
			return 1
		}
		ruleChange = &recordRuleChange{author: rule.author, authorName: authorName, grantee: rule.grantee, granteeName: granteeName,
			operations: rule.operations, level: rule.level, doNotApplyForManager: input.DoNotApplyForManager, revoke: input.Revoke}
	}
	before := reader.getObjectRights(ctx, schemaName, request)
	if !before.isRead() {
		log.error("Error: '%s': %s. Nothing was changed.", schemaName, before.failureReason())
		return 1
	}
	change := recordRightsChange{schemaName: schemaName, request: recordRightsRequest{rule: ruleChange, enable: input.Enable, disable: input.Disable}}
	change.plan = planRecordRights(before.recordState(), change.request)
	plan := change.plan
	if plan.refused() {
		log.error("Error: %s Nothing was changed.", recordRightsRefusalMessage(change))
		return 1
	}
	if !plan.changes() {
		log.info("'%s': %s (no change).", schemaName, recordRightsNoChangeReason(change))
		return 0
	}
	facts := describeRecordRightsPlan(change)
	facts = append(facts, fmt.Sprintf("Planned: record permissions %s; rules: %s.", recordSwitch(plan.after), recordRulesText(plan.after.rules)))
	if plan.after.administered && (plan.enables() || plan.changesRules()) {
		fact, _ := rightsDescribeRecordCount(ctx, reader, schemaName, request)
		facts = append(facts, fmt.Sprintf("'%s' has %s. Applying the rules to existing records is a separate, heavy step "+
			"(apply-default-record-rights): ask the user whether and when to run it.", schemaName, fact))
	}
	if input.Preview {
		log.info("PREVIEW — nothing was changed. %s", change.summary())
		writeRightsFacts(facts, log)
		return 0
	}
	if !rightsHasTimeFor(request, 2, schemaName, "save", "the save and the read-back need", "nothing was changed", log) {
		return 1
	}
	save := reader.saveRecords(ctx, before.snapshot, plan.after, request)
	actual := reader.getObjectRights(ctx, schemaName, request)
	if save.succeeded() {
		if !actual.isRead() {
			log.error("Error: '%s': saved, but NOT verified — on the read-back %s. Check it with get-object-rights before retrying: the plan was:",
				schemaName, actual.failureReason())
			writeRightsFacts(facts, log)
			return 1
		}
		if differences := plan.after.diff(actual.recordState()); len(differences) > 0 {
			log.error("Error: '%s': saved, but NOT verified — the object read back does not match the plan: %s. Check it with get-object-rights.",
				schemaName, strings.Join(differences, "; "))
			return 1
		}
		reportRecordRightsDone(change, facts, actual.recordState(), log)
		return 0
	}
	failure := fmt.Sprintf("the save failed (%s)", save.err)
	recheck := " Read the object with get-object-rights before retrying."
	if save.outcomeUnknown {
		failure = fmt.Sprintf("the save got no answer (%s) and may still be applied", save.err)
		recheck = " Re-read the object with get-object-rights before retrying or reporting a failure."
	}
	if !actual.isRead() {
		log.error("Error: '%s': %s, and on the read-back %s.%s", schemaName, failure, actual.failureReason(), recheck)
		return 1
	}
	if len(plan.after.diff(actual.recordState())) > 0 {
		tail := ""
		if save.outcomeUnknown {
			tail = recheck
		}
		log.error("Error: '%s': %s. The object now: record permissions %s; rules %s.%s", schemaName, failure,
			recordSwitch(actual.recordState()), recordRulesText(actual.rules), tail)
		return 1
	}
	log.warn("'%s': the save reported an error (%s), but the object read back matches the plan.", schemaName, save.err)
	reportRecordRightsDone(change, facts, actual.recordState(), log)
	return 0
}

func reportRecordRightsDone(change recordRightsChange, facts []string, state recordState, log *rightsLog) {
	log.info("Done. %s", change.summary())
	writeRightsFacts(facts, log)
	log.info("  Record permissions now: %s; rules: %s.", recordSwitch(state), recordRulesText(state.rules))
}

// parseRecordRightsInputs is TryParseInputs of set-default-record-rights.
func parseRecordRightsInputs(input RecordRightsSetRequest, log *rightsLog) (string, *recordRuleInput, bool) {
	schemaName, ok := rightsReadSchemaName(input.EntitySchemaName, log)
	if !ok {
		return "", nil, false
	}
	if input.Enable && input.Disable {
		log.error("Error: --enable-record-permissions and --disable-record-permissions contradict each other; pass one of them.")
		return "", nil, false
	}
	if input.Author == nil && input.Grantee == nil && input.Operations == nil {
		if input.Revoke || input.Level != nil || input.DoNotApplyForManager != nil {
			log.error("Error: --revoke, --level and --do-not-apply-for-manager need a rule: pass --author, --grantee and --operations.")
			return "", nil, false
		}
		if !input.Enable && !input.Disable {
			log.error("Error: nothing to change: pass a rule (--author, --grantee, --operations) and/or --enable-record-permissions or " +
				"--disable-record-permissions.")
			return "", nil, false
		}
		return schemaName, nil, true
	}
	if input.Author == nil || input.Grantee == nil || input.Operations == nil {
		log.error("Error: a rule change needs --author, --grantee and --operations together (the rule is the author → grantee pair, and " +
			"no operation is granted by default). To change only the switch, pass none of them and --enable-record-permissions or " +
			"--disable-record-permissions.")
		return "", nil, false
	}
	author, ok := DataWriteParseGUID(*input.Author)
	if !ok || author == emptyGUID {
		log.error("Error: --author must be a SysAdminUnit id (GUID).")
		return "", nil, false
	}
	grantee, ok := DataWriteParseGUID(*input.Grantee)
	if !ok || grantee == emptyGUID {
		log.error("Error: --grantee must be a SysAdminUnit id (GUID).")
		return "", nil, false
	}
	operations, message := parseRecordOperations(*input.Operations)
	if message != "" {
		log.error(message)
		return "", nil, false
	}
	if input.Disable {
		log.error("Error: --disable-record-permissions is a call of its own: pass only --entity-schema-name and the flag. It keeps every rule as it is.")
		return "", nil, false
	}
	if input.Revoke && input.Enable {
		log.error("Error: a revoke never changes the switch. Turn record permissions on in a call of its own (--enable-record-permissions), or with a grant.")
		return "", nil, false
	}
	if input.Revoke && (input.Level != nil || input.DoNotApplyForManager != nil) {
		log.error("Error: --level and --do-not-apply-for-manager apply to a grant; a revoke sets the named operations to 'not set'.")
		return "", nil, false
	}
	level := 1
	if input.Level != nil {
		switch strings.ToLower(strings.TrimSpace(*input.Level)) {
		case "granted":
		case "delegated":
			level = 2
		default:
			log.error("Error: --level: unknown level '%s'. Use granted, delegated.", rightsDisplay(*input.Level))
			return "", nil, false
		}
	}
	return schemaName, &recordRuleInput{author: author, grantee: grantee, operations: operations, level: level}, true
}

func parseRecordOperations(raw string) ([]recordOperation, string) {
	seen := map[recordOperation]bool{}
	for _, token := range rightsSplitTokens(raw) {
		found := false
		for index, name := range recordOperationNames {
			if strings.EqualFold(name, token) {
				seen[recordOperation(index)] = true
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Sprintf("Error: --operations: unknown operation '%s'. Use %s.", rightsDisplay(token), recordOperationsAccepted)
		}
	}
	if len(seen) == 0 {
		return nil, "Error: --operations: no operation given. Use " + recordOperationsAccepted + "."
	}
	operations := []recordOperation{}
	for operation := range seen {
		operations = append(operations, operation)
	}
	sort.Slice(operations, func(i, j int) bool { return operations[i] < operations[j] })
	return operations, ""
}

func recordRightsRefusalMessage(change recordRightsChange) string {
	schema := change.schemaName
	plan := change.plan
	switch plan.refusal {
	case recordRefusalEnableNotRequested:
		text := "record permissions on '" + schema + "' are OFF, so this rule would be stored but give nobody anything. Re-run with " +
			"--enable-record-permissions to turn them ON"
		if len(plan.storedComingIntoEffect) > 0 {
			text += "; these stored rules would then come into effect too: " + recordRulesText(plan.storedComingIntoEffect)
		}
		return text + "."
	case recordRefusalDuplicatePairs:
		return "the stored rules of '" + schema + "' have more than one rule for one author → grantee pair (" + recordRulesText(plan.problemRules) +
			"). The save replaces the whole rule list and the server keeps only the last rule of a pair, so the tool does not save it. Remove " +
			"the duplicates in the Object permissions designer, then re-run."
	case recordRefusalInvalidStoredLevel:
		return "stored rules of '" + schema + "' have a level that is not 'not set', 'granted' or 'delegated' (" + recordRulesText(plan.problemRules) +
			"). The save would send them back as they are, so the tool does not save. Fix them in the Object permissions designer, then re-run."
	}
	return fmt.Sprintf("the change on '%s' is refused (%s).", schema, recordRefusalNames[plan.refusal])
}

func recordRightsNoChangeReason(change recordRightsChange) string {
	reasons := []string{}
	if change.request.enable || change.request.disable {
		reasons = append(reasons, "record permissions are already "+recordSwitch(change.plan.before))
	}
	if change.request.rule != nil {
		if change.plan.ruleBefore == nil {
			reasons = append(reasons, fmt.Sprintf("there is no rule %s, so there is nothing to revoke", change.pair()))
		} else {
			reasons = append(reasons, fmt.Sprintf("the rule %s already has %s", change.pair(), recordLevels(*change.plan.ruleBefore)))
		}
	}
	return strings.Join(reasons, ", and ")
}

func describeRecordRightsPlan(change recordRightsChange) []string {
	plan := change.plan
	schema := change.schemaName
	facts := []string{}
	if plan.enables() {
		facts = append(facts, fmt.Sprintf("Record permissions on '%s' are turned ON: from then on a record is reachable only through its record rights.", schema))
		if len(plan.storedComingIntoEffect) == 0 {
			facts = append(facts, fmt.Sprintf("'%s' has NO default record rules, so the built-in default applies: every user sees only the "+
				"records they create (and their managers and holders of 'view any data' see them too).", schema))
		} else {
			facts = append(facts, "Rules that apply to records created from now on: "+recordRulesText(plan.storedComingIntoEffect)+".")
		}
		facts = append(facts, "Existing records keep the record rights they have: a record that had record rights before record "+
			"permissions were turned off gets them back. A record created while they were off — and every record on a first enable — has "+
			"none until apply-default-record-rights runs for '"+schema+"': it is reachable only by its owner/author, their managers and "+
			"holders of 'view any data'.")
	}
	if change.request.rule != nil {
		facts = append(facts, describeRecordRule(change))
		if plan.changesRules() && !plan.after.administered {
			facts = append(facts, fmt.Sprintf("Record permissions on '%s' are OFF, so the stored rule changes but access does not change now; "+
				"the rules come into effect when record permissions are turned on.", schema))
		} else if plan.changesRules() && !plan.enables() {
			facts = append(facts, "The change applies to records created from now on; existing records keep their record rights until "+
				"apply-default-record-rights runs.")
		}
	}
	if plan.disables() {
		facts = append(facts, fmt.Sprintf("Record permissions on '%s' are turned OFF: every user with read operation rights reaches every "+
			"record. The rules and the records' rights are kept and come back into effect when record permissions are turned on again; a "+
			"record created while they are off gets no record rights until apply-default-record-rights runs.", schema))
	}
	return facts
}

func describeRecordRule(change recordRightsChange) string {
	before, after := change.plan.ruleBefore, change.plan.ruleAfter
	switch {
	case before == nil && after == nil:
		return fmt.Sprintf("There is no rule %s; no rule changes.", change.pair())
	case before == nil:
		return fmt.Sprintf("A rule is added: %s: %s.", change.pair(), recordLevels(*after))
	case after == nil:
		return fmt.Sprintf("The rule %s is removed: it has no right left.", change.pair())
	case before.sameRuleAs(after):
		return fmt.Sprintf("The rule %s is unchanged (%s).", change.pair(), recordLevels(*before))
	}
	levels := []string{}
	for operation := range recordOperationNames {
		if before.levels[operation] != after.levels[operation] {
			levels = append(levels, fmt.Sprintf("%s %s → %s", recordOperationNames[operation], recordLevelName(before.levels[operation]),
				recordLevelName(after.levels[operation])))
		}
	}
	if before.doNotApplyForManager != after.doNotApplyForManager {
		levels = append(levels, fmt.Sprintf("do not apply for manager %s → %s", rightsLower(before.doNotApplyForManager), rightsLower(after.doNotApplyForManager)))
	}
	return fmt.Sprintf("The rule %s changes: %s. It now has %s.", change.pair(), strings.Join(levels, ", "), recordLevels(*after))
}

// ---- apply-default-record-rights ----

const (
	actualizationProcessName = "ObjectRecordRightsActualizationProcess"
	processStatusRunning     = "ed2ae277-b6e2-df11-971b-001d60e938c6"
	processStatusCompleted   = "815c9586-b6e2-df11-971b-001d60e938c6"
	processStatusError       = "f942c08d-b6e2-df11-971b-001d60e938c6"
	processStatusCanceled    = "1be78f3e-234d-4d6a-869a-dc07253fd2f3"
	actualizationUnknown     = "outcome-unknown"
)

// RecordRightsApplyRequest is ApplyDefaultRecordRightsArgs.
type RecordRightsApplyRequest struct {
	EntitySchemaName string
	Wait             *bool
	TimeoutSeconds   *int

	clock *rightsClock
}

// ApplyDefaultRecordRights runs apply-default-record-rights: the read, the launch and the wait all within 100 s.
func (c *Client) ApplyDefaultRecordRights(ctx context.Context, input RecordRightsApplyRequest) ObjectRightsResponse {
	log := &rightsLog{}
	clock := realRightsClock()
	if input.clock != nil {
		clock = *input.clock
	}
	request := rightsRequest{timeout: 25 * time.Second, clock: clock, deadline: newRightsDeadline(100*time.Second, clock)}
	return log.response(c.applyDefaultRecordRights(ctx, input, request, log))
}

func (c *Client) applyDefaultRecordRights(ctx context.Context, input RecordRightsApplyRequest, request rightsRequest, log *rightsLog) int {
	schemaName, ok := rightsReadSchemaName(input.EntitySchemaName, log)
	if !ok {
		return 1
	}
	timeoutSeconds := 60
	if input.TimeoutSeconds != nil {
		timeoutSeconds = *input.TimeoutSeconds
	}
	if timeoutSeconds <= 0 {
		log.error("Error: --timeout-seconds must be a positive number of seconds.")
		return 1
	}
	reader := c.newObjectRightsReader()
	info := reader.getObjectRights(ctx, schemaName, request)
	switch {
	case !info.isRead():
		log.error("Error: '%s': %s. Nothing was started.", schemaName, info.failureReason())
		return 1
	case !info.administeredRecords:
		log.error("Error: record permissions on '%s' are OFF, so its record rights are not evaluated and there is nothing to apply. Turn them "+
			"on first with set-default-record-rights --enable-record-permissions. Nothing was started.", schemaName)
		return 1
	case info.schemaUID == emptyGUID:
		log.error("Error: '%s': the service did not return the object's schema UId. Nothing was started.", schemaName)
		return 1
	}
	c.warnIfAnUpdateIsRunning(ctx, reader, schemaName, request, log)
	if !rightsHasTimeFor(request, 1, schemaName, "launch", "the launch needs", "nothing was started", log) {
		return 1
	}
	started, err := c.startActualization(ctx, info.schemaUID, request)
	if err != nil {
		if rightsLeavesOutcomeUnknown(err) {
			return reportLaunchOutcomeUnknown(schemaName, "no answer: "+rightsDisplayFailure(err), log)
		}
		log.error("Error: '%s': the record-rights update was not started (%s).", schemaName, rightsDisplayFailure(err))
		return 1
	}
	if started.Error != "" {
		if started.Status == actualizationUnknown {
			return reportLaunchOutcomeUnknown(schemaName, started.Error, log)
		}
		log.error("Error: '%s': %s", schemaName, rightsDisplayError(started.Error))
		return 1
	}
	processID, ok := DataWriteParseGUID(started.ProcessID)
	if !ok || processID == emptyGUID {
		log.info("'%s': the record-rights update was queued; the platform returned no process id to follow. Check the records' rights with "+
			"get-record-rights later.", schemaName)
		return 0
	}
	log.info("'%s': the record-rights update started, process %s.", schemaName, processID)
	if input.Wait != nil && !*input.Wait {
		return reportStillRunning(schemaName, processID, false, log)
	}
	return c.waitForActualization(ctx, reader, schemaName, processID, timeoutSeconds, request, log)
}

func reportLaunchOutcomeUnknown(schemaName, reason string, log *rightsLog) int {
	log.error("Error: '%s': the launch of the record-rights update got no usable answer (%s), so the run MAY already be going. Do NOT start "+
		"it again: check the newest %s row in SysProcessLog, or the records' rights with get-record-rights.", schemaName,
		rightsDisplayError(reason), actualizationProcessName)
	return 1
}

func reportStillRunning(schemaName, processID string, waited bool, log *rightsLog) int {
	still := ""
	if waited {
		still = "still "
	}
	log.info("'%s': the record-rights update is %srunning (process %s). Do NOT start it again: check its status in SysProcessLog (Id = %s).",
		schemaName, still, processID, processID)
	return 0
}

func (c *Client) warnIfAnUpdateIsRunning(ctx context.Context, reader *objectRightsReader, schemaName string, request rightsRequest, log *rightsLog) {
	query := rightsSelectQuery("SysProcessLog",
		[]rightsColumn{{path: "Id", alias: "Id"}, {path: "StartDate", alias: "StartDate", orderDirection: 2, orderPosition: 0}},
		[]rightsFilter{{"SysSchema.Name", actualizationProcessName, rightsTextType}, {"Status", processStatusRunning, rightsGUIDType}}, 3, false)
	rows, err := reader.client.rightsSelect(ctx, query, request)
	if err != nil {
		log.warn("'%s': could not check whether a record-rights update is already running (%s).", schemaName, rightsDisplayFailure(err))
		return
	}
	if len(rows) == 0 {
		return
	}
	id, _ := DataWriteParseGUID(rightsStr(processDescribeProperty(rows[0], "Id")))
	if id == "" {
		id = emptyGUID
	}
	more := ""
	if len(rows) > 1 {
		more = fmt.Sprintf(", and %d more", len(rows)-1)
	}
	log.warn("'%s': a record-rights update is already running on this environment (process %s, started %s in the calling user's time zone%s). "+
		"The log does not say which object it is for: if it is this one, wait for it to end (check SysProcessLog, Id = that process) instead "+
		"of starting another — two runs at once double the load.", schemaName, id,
		rightsDisplay(rightsStr(processDescribeProperty(rows[0], "StartDate"))), more)
}

// startActualization is RecordRightsActualizationClient.Start: RunProcess sent once.
func (c *Client) startActualization(ctx context.Context, schemaUID string, request rightsRequest) (DataWriteProcessResult, error) {
	body, _ := json.Marshal(map[string]any{"schemaName": actualizationProcessName,
		"parameterValues": []map[string]string{{"name": "EntitySchemaUId", "value": schemaUID}}, "resultParameterNames": nil})
	text, err := c.rightsPost(ctx, "ServiceModel/ProcessEngineService.svc/RunProcess", body, request)
	if err != nil {
		return DataWriteProcessResult{}, err
	}
	if strings.TrimSpace(text) == "" {
		return DataWriteProcessResult{Status: actualizationUnknown, Error: "RunProcess returned an empty response"}, nil
	}
	if strings.TrimSpace(text) == "null" {
		return DataWriteProcessResult{Error: "The launch returned an empty response"}, nil
	}
	native, parseErr := dataWriteParseRunResponse([]byte(text))
	if parseErr != nil {
		return DataWriteProcessResult{Status: actualizationUnknown, Error: "RunProcess returned a response clio could not read: " +
			rightsJSONStartError(text, parseErr)}, nil
	}
	return dataWriteBuildRunResponse(native, actualizationProcessName), nil
}

// rightsJSONStartError words a body that does not start like JSON (a proxy's HTML page) as System.Text.Json
// does; any other parse failure keeps the Go parser's text.
func rightsJSONStartError(text string, parseErr error) string {
	line, column := 0, 0
	for _, r := range text {
		switch r {
		case ' ', '\t', '\r':
			column++
			continue
		case '\n':
			line++
			column = 0
			continue
		}
		if strings.ContainsRune("{[\"-0123456789tfn", r) {
			break
		}
		return fmt.Sprintf("'%c' is an invalid start of a value. Path: $ | LineNumber: %d | BytePositionInLine: %d.", r, line, column)
	}
	return parseErr.Error()
}

// waitForActualization is WaitForEnd: polls the run's SysProcessLog row every 2 s until it ends or the wait is over.
func (c *Client) waitForActualization(ctx context.Context, reader *objectRightsReader, schemaName, processID string, timeoutSeconds int,
	request rightsRequest, log *rightsLog) int {
	waitFor := time.Duration(timeoutSeconds) * time.Second
	if request.deadline != nil && request.deadline.remaining() < waitFor {
		waitFor = request.deadline.remaining()
	}
	wait := newRightsDeadline(waitFor, request.clock)
	poll := request
	poll.deadline = wait
	statusSeen := false
	reads := 0
	lastReadError := ""
	for !wait.spent() {
		reads++
		statusID, statusName, found, err := c.readActualizationStatus(ctx, reader, processID, poll)
		if err != nil {
			lastReadError = rightsDisplayFailure(err)
		}
		if found && (statusID == processStatusCompleted || statusID == processStatusError || statusID == processStatusCanceled) {
			if statusID == processStatusCompleted {
				log.info("'%s': the record-rights update completed (process %s). Existing records now have the rights of the current default "+
					"rules; rights granted by hand were kept.", schemaName, processID)
				return 0
			}
			log.error("Error: '%s': the record-rights update ended as '%s' (process %s). Its log is in the process log (SysProcessLog) of the "+
				"environment.", schemaName, rightsDisplay(statusName), processID)
			return 1
		}
		statusSeen = statusSeen || found
		if wait.spent() || ctx.Err() != nil {
			break
		}
		pause := 2 * time.Second
		if left := wait.remaining(); left < pause {
			pause = left
		}
		request.clock.sleep(ctx, pause)
	}
	if statusSeen {
		return reportStillRunning(schemaName, processID, true, log)
	}
	reason := "the wait ended before the status could be read"
	if reads > 0 {
		reason = "no SysProcessLog row was found"
		if lastReadError != "" {
			reason = lastReadError
		}
	}
	log.warn("'%s': the record-rights update was started (process %s), but its status could not be read (%s). Do NOT start it again: check "+
		"SysProcessLog (Id = %s) for its outcome.", schemaName, processID, reason, processID)
	return 0
}

// readActualizationStatus is ReadStatus: the Status lookup of the run's SysProcessLog row.
func (c *Client) readActualizationStatus(ctx context.Context, reader *objectRightsReader, processID string, request rightsRequest) (string, string, bool, error) {
	query := rightsSelectQuery("SysProcessLog", []rightsColumn{{path: "Status", alias: "Status"}},
		[]rightsFilter{{"Id", processID, rightsGUIDType}}, 1, false)
	rows, err := reader.client.rightsSelect(ctx, query, request)
	if err != nil {
		return "", "", false, err
	}
	if len(rows) == 0 {
		return "", "", false, nil
	}
	status := processDescribeProperty(rows[0], "Status")
	if !status.isObject() {
		return "", "", false, nil
	}
	id, ok := DataWriteParseGUID(rightsStr(processDescribeProperty(status, "value")))
	if !ok {
		id = emptyGUID
	}
	return id, rightsStr(processDescribeProperty(status, "displayValue")), true, nil
}
