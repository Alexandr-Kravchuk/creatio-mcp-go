package creatio

// trigger-smoke, ported from clio 8.1.0.142's TriggerSmokeRunner on top of the run verification run-process
// already uses (CreatedRecordWatcher, ProcessRunMonitor, RunRecordProvenance, CreatedRecordCleaner): create the
// triggering record, wait for the runs it starts (new SysProcessLog rows), tell the caller's runs from the
// others, report what appeared in the watched entities, and delete only what can be tied to the trigger.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/redact"
)

const (
	TriggerSmokeDefaultTimeout = 15
	TriggerSmokeMaxTimeout     = 30
	TriggerSmokeMaxWatched     = 5
	triggerSmokeQuiet          = 3 * time.Second
	triggerSmokePoll           = time.Second
	triggerSmokeRequest        = 10 * time.Second
	triggerSmokeCancelWait     = 10 * time.Second
	triggerSmokeTotalBudget    = 90 * time.Second
	triggerSmokeCaptureBudget  = 15 * time.Second
	triggerSmokeMinimumRequest = time.Second
	triggerSmokeMinimumWait    = 6 * time.Second
	triggerSmokeProcessLog     = "SysProcessLog"
	triggerSmokeUnknown        = "unknown"
	triggerSmokeRunning        = "running"
	triggerSmokeError          = "error"
	triggerSmokeCancelled      = "cancelled"
)

// triggerSmokeNow and triggerSmokeSleep are the clock and the pause of the wait (test seams, as clio's Clock/Sleep).
var (
	triggerSmokeNow   = time.Now
	triggerSmokeSleep = time.Sleep
)

// TriggerSmokeRequest is clio's TriggerSmokeRequest; Values keep the caller's JSON values.
type TriggerSmokeRequest struct {
	Entity         string
	Values         map[string]any
	Watch          []string
	TimeoutSeconds int
	Cleanup        bool
	Process        *string
}

// SmokeRunState is clio's SmokeRunState.
type SmokeRunState struct {
	Status    string           `json:"status"`
	WaitingOn []WaitingElement `json:"waitingOn"`
}

// UnrelatedRun is clio's UnrelatedRun.
type UnrelatedRun struct {
	ProcessID string  `json:"processId"`
	Name      *string `json:"name,omitempty"`
	Reason    string  `json:"reason"`
}

// SmokeProcessRun is clio's SmokeProcessRun.
type SmokeProcessRun struct {
	ProcessID         string           `json:"processId"`
	Name              *string          `json:"name,omitempty"`
	Status            string           `json:"status"`
	Error             *string          `json:"error,omitempty"`
	WaitingOn         []WaitingElement `json:"waitingOn"`
	StateBeforeCancel *SmokeRunState   `json:"stateBeforeCancel,omitempty"`
}

// TriggerSmokeResponse is clio's TriggerSmokeResponse (TriggerSmokeResponse.From over TriggerSmokeResult).
type TriggerSmokeResponse struct {
	Success       bool              `json:"success"`
	Error         *string           `json:"error,omitempty"`
	Trigger       *CreatedRecord    `json:"trigger,omitempty"`
	Processes     []SmokeProcessRun `json:"processes,omitempty"`
	Unrelated     []UnrelatedRun    `json:"unrelated,omitempty"`
	Created       []CreatedRecord   `json:"created,omitempty"`
	Cleanup       *CleanupReport    `json:"cleanup,omitempty"`
	Cancelled     []string          `json:"cancelled,omitempty"`
	Warnings      []string          `json:"warnings,omitempty"`
	CorrelationID string            `json:"correlation-id"`
}

// MarshalJSON writes the lists clio always fills (processes, created, cancelled when cleanup ran) even when empty.
func (r TriggerSmokeResponse) MarshalJSON() ([]byte, error) {
	type shape struct {
		Success       bool               `json:"success"`
		Error         *string            `json:"error,omitempty"`
		Trigger       *CreatedRecord     `json:"trigger,omitempty"`
		Processes     *[]SmokeProcessRun `json:"processes,omitempty"`
		Unrelated     []UnrelatedRun     `json:"unrelated,omitempty"`
		Created       *[]CreatedRecord   `json:"created,omitempty"`
		Cleanup       *CleanupReport     `json:"cleanup,omitempty"`
		Cancelled     *[]string          `json:"cancelled,omitempty"`
		Warnings      []string           `json:"warnings,omitempty"`
		CorrelationID string             `json:"correlation-id"`
	}
	out := shape{Success: r.Success, Error: r.Error, Trigger: r.Trigger, Unrelated: r.Unrelated, Cleanup: r.Cleanup,
		Warnings: r.Warnings, CorrelationID: r.CorrelationID}
	if r.Processes != nil {
		out.Processes = &r.Processes
	}
	if r.Created != nil {
		out.Created = &r.Created
	}
	if r.Cancelled != nil {
		out.Cancelled = &r.Cancelled
	}
	return json.Marshal(out)
}

// TriggerSmokeFailure is TriggerSmokeResponse.Failure.
func TriggerSmokeFailure(message string) TriggerSmokeResponse {
	return TriggerSmokeResponse{Error: &message, CorrelationID: dataWriteCorrelationID()}
}

type triggerSmokeAttribution struct {
	process        *string
	me             string
	facts          map[string][2]string // lower-case run id -> (code, root id)
	factsUnread    bool
	factsUnreadErr bool
}

type triggerSmokeRun struct {
	ctx       context.Context
	c         *Client
	request   TriggerSmokeRequest
	watcher   *createdRecordWatcher
	monitor   *processRunMonitor
	result    TriggerSmokeResponse
	unrelated []UnrelatedRun
}

func triggerSmokeBudget(stopAt time.Time, maximum time.Duration) time.Duration {
	left := stopAt.Sub(triggerSmokeNow())
	if left < time.Millisecond {
		return time.Millisecond
	}
	return min(left, maximum)
}

// TriggerSmoke is TriggerSmokeRunner.Run; a server-side refusal is reported, never raised.
func (c *Client) TriggerSmoke(ctx context.Context, request TriggerSmokeRequest) TriggerSmokeResponse {
	run := &triggerSmokeRun{ctx: ctx, c: c, request: request, watcher: &createdRecordWatcher{c: c, ctx: ctx},
		monitor: &processRunMonitor{c: c, ctx: ctx}}
	run.result = TriggerSmokeResponse{Processes: []SmokeProcessRun{}, Created: []CreatedRecord{}, Warnings: []string{}}
	run.execute()
	result := run.result
	if len(run.unrelated) > 0 {
		result.Unrelated = run.unrelated
	}
	if len(result.Warnings) == 0 {
		result.Warnings = nil
	}
	result.CorrelationID = dataWriteCorrelationID()
	return result
}

func (r *triggerSmokeRun) addWarnings(warnings []string) {
	for _, warning := range warnings {
		r.result.Warnings = append(r.result.Warnings, warning)
	}
}

// addNewWarnings is AddRange(x.Except(result.Warnings)): distinct, and only those not present yet.
func (r *triggerSmokeRun) addNewWarnings(warnings []string) {
	present := map[string]bool{}
	for _, warning := range r.result.Warnings {
		present[warning] = true
	}
	for _, warning := range warnings {
		if !present[warning] {
			present[warning] = true
			r.result.Warnings = append(r.result.Warnings, warning)
		}
	}
}

func (r *triggerSmokeRun) execute() {
	request := r.request
	stopAt := triggerSmokeNow().Add(triggerSmokeTotalBudget)
	captureStop := triggerSmokeNow().Add(triggerSmokeCaptureBudget)
	processes := r.watcher.capture([]string{triggerSmokeProcessLog}, triggerSmokeRequest, captureStop)
	for _, entity := range processes.entities {
		if entity.err != "" {
			message := "SysProcessLog could not be read before the trigger was created, so the runs it starts could not be seen: " +
				entity.err + ". Nothing was created; check the user's read access to the process log."
			r.result.Error = &message
			return
		}
	}
	entities := []string{request.Entity}
	for _, name := range request.Watch {
		if !strings.EqualFold(name, request.Entity) {
			entities = append(entities, name)
		}
	}
	records := r.watcher.capture(entities, triggerSmokeRequest, captureStop)
	me := records.currentUserContactID
	if me == "" {
		me = processes.currentUserContactID
	}
	triggerID, createError := r.createTrigger(triggerSmokeBudget(stopAt, 3*triggerSmokeRequest))
	if triggerID != "" {
		r.result.Trigger = &CreatedRecord{Entity: request.Entity, ID: triggerID, createdByID: me}
	}
	attribution := &triggerSmokeAttribution{process: request.Process, me: me, facts: map[string][2]string{}}
	timeout := request.TimeoutSeconds
	if triggerID == "" {
		timeout = 0
	}
	runs := r.waitForProcesses(processes, attribution, timeout, stopAt)
	r.readCreated(records, triggerID, stopAt)
	r.result.Success = r.judge(triggerID, createError)
	if request.Process == nil && len(r.result.Processes) > 0 {
		r.result.Warnings = append(r.result.Warnings, "processes lists every run the calling login started in the window, so a "+
			"scheduled or parallel run of the same login counts too; pass `process` to bind the verdict to the process under test.")
	}
	if request.Cleanup {
		r.cleanUp(records, runs, triggerID, stopAt)
	}
}

func (r *triggerSmokeRun) reasonUnrelated(run CreatedRecord, attribution *triggerSmokeAttribution) string {
	if attribution.me == "" {
		return "the calling user's contact could not be read, so no run can be shown to be the caller's"
	}
	if !strings.EqualFold(run.createdByID, attribution.me) {
		return "started by another user"
	}
	if attribution.process == nil || r.matchesProcess(run, attribution) {
		return ""
	}
	return fmt.Sprintf("not a run of '%s'", *attribution.process)
}

func (r *triggerSmokeRun) matchesProcess(run CreatedRecord, attribution *triggerSmokeAttribution) bool {
	process := *attribution.process
	if run.Title != nil && strings.EqualFold(*run.Title, process) {
		return true
	}
	facts, ok := attribution.facts[strings.ToLower(run.ID)]
	if !ok {
		return false
	}
	if strings.EqualFold(facts[0], process) {
		return true
	}
	if facts[1] == "" || strings.EqualFold(facts[1], run.ID) {
		return false
	}
	root, ok := attribution.facts[strings.ToLower(facts[1])]
	return ok && strings.EqualFold(root[0], process)
}

// readRunFacts is ReadRunFacts: the code and RootId of the runs not read yet, and of their roots.
func (r *triggerSmokeRun) readRunFacts(runs []CreatedRecord, attribution *triggerSmokeAttribution, stopAt time.Time) {
	if attribution.process == nil || attribution.factsUnread {
		return
	}
	var unread []string
	for _, run := range runs {
		if run.ID == "" {
			continue
		}
		if _, ok := attribution.facts[strings.ToLower(run.ID)]; !ok {
			unread = append(unread, run.ID)
		}
	}
	for round := 0; round < 2 && len(unread) > 0; round++ {
		rows, err := r.c.esqSelect(r.ctx, esqQuery(triggerSmokeProcessLog, map[string]any{"Id": esqColumn("Id"),
			"Code": esqColumn("SysSchema.Name"), "RootId": esqColumn("RootId")},
			esqGroup(0, map[string]any{"ids": inFilter("Id", unread, recordDeletionGUIDType)}), len(unread)),
			triggerSmokeBudget(stopAt, triggerSmokeRequest))
		if err != nil {
			attribution.factsUnread = true
			r.result.Warnings = append(r.result.Warnings, "The process code of the runs could not be read, so `process` is matched "+
				"against the run's caption only: "+fencedOr(err, "Exception"))
			return
		}
		for _, row := range rows {
			attribution.facts[strings.ToLower(rowText(row, "Id"))] = [2]string{rowText(row, "Code"), recordDeletionLookupValue(row["RootId"])}
		}
		unread = nil
		seen := map[string]bool{}
		for _, facts := range attribution.facts {
			root := facts[1]
			if root == "" || seen[strings.ToLower(root)] {
				continue
			}
			if _, ok := attribution.facts[strings.ToLower(root)]; !ok {
				seen[strings.ToLower(root)] = true
				unread = append(unread, root)
			}
		}
		sort.Strings(unread)
	}
}

// readCreated is ReadCreated: the watched entities after the wait, the trigger split off.
func (r *triggerSmokeRun) readCreated(records *watchBaseline, triggerID string, stopAt time.Time) {
	outcome := r.watcher.collect(records, triggerSmokeRequest, stopAt)
	r.addNewWarnings(outcome.warnings)
	triggerIndex := -1
	if triggerID != "" {
		for index, record := range outcome.created {
			if strings.EqualFold(record.Entity, r.request.Entity) && strings.EqualFold(record.ID, triggerID) {
				triggerIndex = index
				found := record
				r.result.Trigger = &found
				break
			}
		}
	}
	reportTriggerEntity := triggerID == ""
	for _, name := range r.request.Watch {
		if strings.EqualFold(name, r.request.Entity) {
			reportTriggerEntity = true
		}
	}
	created := []CreatedRecord{}
	for index, record := range outcome.created {
		if index == triggerIndex {
			continue
		}
		if reportTriggerEntity || !strings.EqualFold(record.Entity, r.request.Entity) {
			created = append(created, record)
		}
	}
	r.result.Created = created
}

func (r *triggerSmokeRun) judge(triggerID, createError string) bool {
	fail := func(message string) bool {
		r.result.Error = &message
		return false
	}
	if triggerID == "" {
		return fail(createError)
	}
	if len(r.result.Processes) == 0 {
		message := fmt.Sprintf("No run of this call started within %d s of creating the record. Check that the process is active, "+
			"that its signal is 'record added' on this entity, and that its filter matches the values; a process that runs in "+
			"the background may also start later than the wait.", r.request.TimeoutSeconds)
		if len(r.unrelated) > 0 {
			message += fmt.Sprintf(" %d other run(s) started in the window; `unrelated` lists them, and they do not count.", len(r.unrelated))
		}
		return fail(message)
	}
	failed, unknown := 0, 0
	for _, run := range r.result.Processes {
		if run.Status == triggerSmokeError {
			failed++
		}
		if run.Status == "" || strings.HasPrefix(run.Status, triggerSmokeUnknown) {
			unknown++
		}
	}
	if failed > 0 {
		return fail(fmt.Sprintf("%d started process run(s) ended in error; processes[].error names each.", failed))
	}
	if unknown > 0 {
		return fail(fmt.Sprintf("Not verified: the state of %d run(s) could not be read before the wait ended, so they may have "+
			"failed; processes[].error says why. Read SysProcessLog for them, or run again with a longer timeout.", unknown))
	}
	return true
}

// createTrigger is CreateTrigger: the OData insert, sent once.
func (r *triggerSmokeRun) createTrigger(timeout time.Duration) (string, string) {
	entity := r.request.Entity
	keys := make([]string, 0, len(r.request.Values))
	for key := range r.request.Values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	fields := orderedFields{}
	for _, key := range keys {
		fields = append(fields, field{key, r.request.Values[key]})
	}
	body := toJNode(fields).stjJSON()
	response, err := r.c.dataWriteSendOnce(r.ctx, "POST", "odata/"+dataWriteTrim(entity), body, timeout)
	if err != nil {
		return "", fmt.Sprintf("The triggering %s record could not be created: %s. The request may still have reached the server; "+
			"check with odata-read before creating it again.", entity, redact.Text(r.c.clioRequestFailureText(err)))
	}
	text := response
	if strings.TrimSpace(text) == "" {
		text = "{}"
	}
	var probe any
	if err := json.Unmarshal([]byte(text), &probe); err != nil {
		return "", fmt.Sprintf("The triggering %s record could not be created: %s. The request may still have reached the server; "+
			"check with odata-read before creating it again.", entity, redact.Text(dataWriteSTJParseError(text)))
	}
	if serverError, ok := dataWriteDetectError(json.RawMessage(text), dataWriteODataContext); ok {
		message := fmt.Sprintf("The triggering %s record was not created: %s", entity,
			dataWriteAppendForeignKeyHint(redact.Text(serverError), json.RawMessage(text)))
		if inner := recordDeletionInnerMessage(response); inner != "" {
			message += fmt.Sprintf(" The server's own reason: %s.", inner)
		}
		return "", message
	}
	if object, ok := probe.(map[string]any); ok {
		if id, ok := object["Id"].(string); ok {
			return id, ""
		}
	}
	return "", fmt.Sprintf("The triggering %s record was not confirmed: the create answered without an Id. Check with odata-read "+
		"before creating it again.", entity)
}

// waitForProcesses is WaitForProcesses: poll the new SysProcessLog rows and the states of this call's runs until
// they all finished or parked, nothing changed for 3 s and the minimum wait passed, or the timeout.
func (r *triggerSmokeRun) waitForProcesses(baseline *watchBaseline, attribution *triggerSmokeAttribution, timeoutSeconds int,
	stopAt time.Time) []CreatedRecord {
	started := triggerSmokeNow()
	until := started.Add(time.Duration(timeoutSeconds) * time.Second)
	if until.After(stopAt) {
		until = stopAt
	}
	quietSince := started
	lastSignature := "\x00"
	final := map[string]SmokeProcessRun{}
	last := map[string]SmokeProcessRun{}
	for {
		outcome := r.watcher.collect(baseline, triggerSmokeRequest, stopAt)
		r.readRunFacts(outcome.created, attribution, stopAt)
		runs := r.attribute(outcome.created, attribution)
		processes := make([]SmokeProcessRun, 0, len(runs))
		for _, run := range runs {
			processes = append(processes, r.currentState(run, final, last, until, stopAt))
		}
		r.result.Processes = processes
		for _, run := range processes {
			last[strings.ToLower(run.ProcessID)] = run
			if triggerSmokeIsFinal(run) {
				final[strings.ToLower(run.ProcessID)] = run
			}
		}
		parts := make([]string, 0, len(processes))
		for _, run := range processes {
			parts = append(parts, fmt.Sprintf("%s:%s:%s", run.ProcessID, run.Status, dotnetBool(triggerSmokeIsParked(run))))
		}
		signature := strings.Join(parts, "|")
		if signature != lastSignature {
			lastSignature = signature
			quietSince = triggerSmokeNow()
		}
		now := triggerSmokeNow()
		settled := triggerSmokeSettled(processes, now.Sub(quietSince), now.Sub(started), timeoutSeconds)
		left := until.Sub(now)
		if settled || left.Milliseconds() <= 0 {
			r.addWarnings(outcome.warnings)
			return runs
		}
		triggerSmokeSleep(min(triggerSmokePoll, left))
	}
}

// clioConnectionText is an exception message as clio's client words it: a refused connection is the socket's
// "Connection refused (host:port)" (measured against clio 8.1.0.142), a connect fault already worded as
// Creatio.Client raises it keeps that text, anything else is the error's own text.
func clioConnectionText(err error) string {
	if fault, ok := asConnectFault(err); ok {
		return fault.Error()
	}
	var urlErr *url.Error
	if isConnectionRefused(err) && errors.As(err, &urlErr) {
		return connectSentence(err, runtime.GOOS) + " (" + rightsHostPort(urlErr.URL) + ")"
	}
	return err.Error()
}

func dotnetBool(value bool) string {
	if value {
		return "True"
	}
	return "False"
}

func (r *triggerSmokeRun) attribute(created []CreatedRecord, attribution *triggerSmokeAttribution) []CreatedRecord {
	runs := []CreatedRecord{}
	r.unrelated = []UnrelatedRun{}
	for _, run := range created {
		if reason := r.reasonUnrelated(run, attribution); reason != "" {
			r.unrelated = append(r.unrelated, UnrelatedRun{ProcessID: run.ID, Name: run.Title, Reason: reason})
			continue
		}
		runs = append(runs, run)
	}
	return runs
}

func (r *triggerSmokeRun) currentState(run CreatedRecord, final, last map[string]SmokeProcessRun, until, stopAt time.Time) SmokeProcessRun {
	if done, ok := final[strings.ToLower(run.ID)]; ok {
		return done
	}
	if triggerSmokeNow().Before(until) {
		return r.readRun(run, triggerSmokeBudget(stopAt, triggerSmokeRequest))
	}
	if seen, ok := last[strings.ToLower(run.ID)]; ok {
		return seen
	}
	message := "not read: the wait ended first"
	return SmokeProcessRun{ProcessID: run.ID, Name: run.Title, Status: triggerSmokeUnknown, Error: &message, WaitingOn: []WaitingElement{}}
}

// triggerSmokeSettled is IsSettled.
func triggerSmokeSettled(processes []SmokeProcessRun, quiet, waited time.Duration, timeoutSeconds int) bool {
	if len(processes) == 0 {
		return false
	}
	for _, run := range processes {
		if run.Status == triggerSmokeRunning && !triggerSmokeIsParked(run) {
			return false
		}
	}
	return quiet >= triggerSmokeQuiet && waited >= min(triggerSmokeMinimumWait, time.Duration(timeoutSeconds)*time.Second)
}

// triggerSmokeIsParked is IsParked: running on an element that waits for something outside the run.
func triggerSmokeIsParked(run SmokeProcessRun) bool {
	if run.Status != triggerSmokeRunning {
		return false
	}
	for _, element := range run.WaitingOn {
		if element.Type == nil {
			continue
		}
		kind := *element.Type
		if strings.HasSuffix(kind, "UserTask") || strings.Contains(kind, "IntermediateCatch") || strings.Contains(kind, "EventBasedGateway") ||
			strings.Contains(kind, "ReceiveTask") {
			return true
		}
	}
	return false
}

func triggerSmokeIsFinal(run SmokeProcessRun) bool {
	return run.Status == "completed" || run.Status == triggerSmokeError || run.Status == triggerSmokeCancelled
}

// readRun is ReadRun.
func (r *triggerSmokeRun) readRun(run CreatedRecord, timeout time.Duration) SmokeProcessRun {
	state, err := r.monitor.read(run.ID, timeout)
	if err != nil {
		message := "its state could not be read: " + fencedOr(err, "Exception")
		return SmokeProcessRun{ProcessID: run.ID, Name: run.Title, Status: triggerSmokeUnknown, Error: &message, WaitingOn: []WaitingElement{}}
	}
	status := triggerSmokeUnknown
	if state.status != nil {
		switch *state.status {
		case 0:
			status = "inactive"
		case 1:
			status = triggerSmokeRunning
		case 2:
			status = "completed"
		case 3:
			status = triggerSmokeError
		case 4:
			status = triggerSmokeCancelled
		case 5:
			status = "cancelling"
		default:
			status = fmt.Sprintf("%s-status-%d", triggerSmokeUnknown, *state.status)
		}
	}
	result := SmokeProcessRun{ProcessID: run.ID, Name: run.Title, Status: status, WaitingOn: state.waitingOn}
	if result.WaitingOn == nil {
		result.WaitingOn = []WaitingElement{}
	}
	if state.status != nil && *state.status == 3 {
		message := "no error text was logged"
		if fenced := DataWriteUntrusted(state.errorSummary); fenced != nil {
			message = *fenced
		}
		result.Error = &message
	}
	return result
}

// cleanUp is CleanUp: only what can be tied to the trigger is cancelled or deleted.
func (r *triggerSmokeRun) cleanUp(records *watchBaseline, runs []CreatedRecord, triggerID string, stopAt time.Time) {
	r.result.Cancelled = []string{}
	if triggerID == "" {
		report := newCleanupReport()
		report.Skipped = append(report.Skipped, r.result.Created...)
		r.result.Cleanup = report
		if len(r.result.Created) > 0 {
			r.result.Warnings = append(r.result.Warnings, "cleanup did nothing: the trigger was not confirmed, so nothing in the window "+
				"can be shown to come from this call. cleanup.skipped lists the records; delete them with odata-delete after checking each.")
		}
		return
	}
	me := records.currentUserContactID
	provenance := r.tie(triggerID, me, stopAt)
	if r.cancelHoldingRuns(provenance, runs, me, stopAt) {
		r.readCreated(records, triggerID, stopAt)
		provenance = r.tie(triggerID, me, stopAt)
	}
	all := append(append([]CreatedRecord{}, r.result.Created...), *r.result.Trigger)
	mine := []CreatedRecord{}
	for _, record := range all {
		if provenance.tied[strings.ToLower(record.ID)] {
			mine = append(mine, record)
		}
	}
	references := map[string][]string{}
	for _, watch := range records.entities {
		if watch.err == "" {
			if _, ok := references[strings.ToLower(watch.entity)]; !ok {
				references[strings.ToLower(watch.entity)] = watch.references
			}
		}
	}
	cleaner := &createdRecordCleaner{service: newRecordDeletionService(r.ctx, r.c)}
	report := cleaner.delete(mine, references, triggerSmokeRequest, stopAt, func() bool { return r.ctx.Err() != nil })
	if report == nil {
		report = newCleanupReport()
	}
	// all.Except(mine): distinct, by value.
	ours := map[string]bool{}
	for _, record := range mine {
		ours[record.key()] = true
	}
	report.Skipped = []CreatedRecord{}
	seen := map[string]bool{}
	for _, record := range all {
		if ours[record.key()] || seen[record.key()] {
			continue
		}
		seen[record.key()] = true
		report.Skipped = append(report.Skipped, record)
	}
	r.result.Cleanup = report
	if len(report.Skipped) > 0 {
		if me == "" {
			r.result.Warnings = append(r.result.Warnings, "cleanup deleted only the trigger: the calling user's contact could not be read. "+
				"cleanup.skipped lists the other records.")
		} else {
			r.result.Warnings = append(r.result.Warnings, fmt.Sprintf("cleanup left %d record(s) it cannot tie to the trigger - created by "+
				"another user, or by the same login with no lookup to the trigger; cleanup.skipped lists them. Delete them with "+
				"odata-delete only after checking each is a test record.", len(report.Skipped)))
		}
	}
	if len(report.Failed) > 0 {
		r.result.Warnings = append(r.result.Warnings, fmt.Sprintf("cleanup left %d record(s) in place; cleanup.failed names each with "+
			"the reason. Delete them with odata-delete, which explains the refusal.", len(report.Failed)))
	}
}

// tie is Tie: the trigger, then the caller's new records whose lookup points at a tied record.
func (r *triggerSmokeRun) tie(triggerID, me string, stopAt time.Time) recordProvenance {
	candidates := []CreatedRecord{}
	if me != "" {
		for _, record := range r.result.Created {
			if record.ID != "" && strings.EqualFold(record.createdByID, me) {
				candidates = append(candidates, record)
			}
		}
	}
	candidates = append(candidates, *r.result.Trigger)
	provenance := (&runRecordProvenance{c: r.c, ctx: r.ctx}).resolveSeeded(candidates, []string{triggerID}, nil, triggerSmokeRequest, stopAt)
	r.addNewWarnings(provenance.warnings)
	return provenance
}

// cancelHoldingRuns is CancelHoldingRuns: only a running run of the caller that holds a tied record is cancelled.
func (r *triggerSmokeRun) cancelHoldingRuns(provenance recordProvenance, runs []CreatedRecord, me string, stopAt time.Time) bool {
	running := []SmokeProcessRun{}
	for _, run := range r.result.Processes {
		if run.Status != triggerSmokeRunning || me == "" {
			continue
		}
		for _, record := range runs {
			if record.ID == run.ProcessID {
				if strings.EqualFold(record.createdByID, me) {
					running = append(running, run)
				}
				break
			}
		}
	}
	if len(running) == 0 {
		return false
	}
	holding := map[string]bool{}
	for id := range provenance.tied {
		if run, ok := provenance.holdingRun[id]; ok && run != "" {
			holding[strings.ToLower(run)] = true
		}
	}
	cancelledAny := false
	for _, run := range running {
		processID := run.ProcessID
		if !holding[strings.ToLower(processID)] {
			continue
		}
		if triggerSmokeBudget(stopAt, triggerSmokeRequest) < triggerSmokeMinimumRequest {
			r.result.Warnings = append(r.result.Warnings, fmt.Sprintf("Run %s was not cancelled: the call's time budget ran out.", processID))
			continue
		}
		if r.cancelAndWait(processID, stopAt) {
			cancelledAny = true
			r.result.Cancelled = append(r.result.Cancelled, processID)
			for index, process := range r.result.Processes {
				if process.ProcessID == processID {
					before := &SmokeRunState{Status: process.Status, WaitingOn: process.WaitingOn}
					process.Status, process.WaitingOn, process.StateBeforeCancel = triggerSmokeCancelled, []WaitingElement{}, before
					r.result.Processes[index] = process
				}
			}
		}
	}
	for _, run := range running {
		if !holding[strings.ToLower(run.ProcessID)] {
			r.result.Warnings = append(r.result.Warnings, "Runs still running that hold none of the records cleanup deletes were left "+
				"running: nothing ties them to the trigger.")
			break
		}
	}
	return cancelledAny
}

// cancelAndWait is CancelAndWait.
func (r *triggerSmokeRun) cancelAndWait(processID string, stopAt time.Time) bool {
	normalized := normalizeGUID(processID)
	if normalized == "" {
		normalized = processID
	}
	message, err := r.monitor.cancel(normalized, triggerSmokeBudget(stopAt, triggerSmokeRequest))
	if err != nil {
		r.result.Warnings = append(r.result.Warnings, fmt.Sprintf("Run %s could not be cancelled: %s.", normalized, fencedOr(err, "Exception")))
		return false
	}
	if message != "" {
		r.result.Warnings = append(r.result.Warnings, fmt.Sprintf("Run %s was not cancelled: %s.", normalized, message))
		return false
	}
	until := triggerSmokeNow().Add(triggerSmokeCancelWait)
	if until.After(stopAt) {
		until = stopAt
	}
	for triggerSmokeNow().Before(until) {
		state, err := r.monitor.read(normalized, triggerSmokeBudget(until, triggerSmokeRequest))
		if err != nil {
			r.result.Warnings = append(r.result.Warnings, fmt.Sprintf("Run %s could not be cancelled: %s.", normalized, fencedOr(err, "Exception")))
			return false
		}
		if state.terminal() {
			return true
		}
		triggerSmokeSleep(max(time.Millisecond, min(triggerSmokePoll, until.Sub(triggerSmokeNow()))))
	}
	r.result.Warnings = append(r.result.Warnings, fmt.Sprintf("Run %s had not stopped after the cancel in the time left; records it uses "+
		"may refuse deletion.", normalized))
	return false
}
