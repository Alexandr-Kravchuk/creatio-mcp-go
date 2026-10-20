package creatio

// run-process's run verification as clio 8.1.0.142 does it (ENG-103024): RunProcessCommand.Verify with its
// ProcessRunMonitor (poll a parked run, read what it waits on, cancel it), CreatedRecordWatcher (before/after
// diff of the watched entities), RunRecordProvenance (tie what appeared to the run) and CreatedRecordCleaner
// (delete it, children first).

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const (
	DataWriteDefaultWaitSeconds = 20
	dataWriteMaxWaitSeconds     = 300
	dataWriteMaxWatched         = 5
	dataWriteVerificationCall   = 10 * time.Second
	dataWritePollInterval       = time.Second
	dataWriteMaxFailedReads     = 3
	dataWriteCancelWait         = 10 * time.Second
	dataWriteMinimumDestructive = time.Second
	dataWriteCaptureBudget      = 12 * time.Second
	dataWriteWaitReserve        = 3 * time.Second
	dataWriteCollectReserve     = 6 * time.Second
	dataWriteCleanupReserve     = 15 * time.Second
	dataWriteRunningStatus      = 1
	dataWriteErrorStatus        = 3
	dataWriteCancelledStatus    = 4
	dataWriteCancelRoute        = "ServiceModel/ProcessEngineService.svc/CancelExecution"
	dataWriteParkedValuesNote   = "resultParameterValues are the values the run had at the launch, while it was "

	// DataWriteTimeWindowNote is RunProcessCommand.TimeWindowNote.
	DataWriteTimeWindowNote = "`created` is a before/after diff of the watched entities, so it also lists " +
		"records another session - another user's, or the same login's - created in the same window. cleanup deletes " +
		"only records tied to this run: one whose ProcessElementId names an element of the run (or of its " +
		"sub-processes), or one whose lookup points at such a record. Everything else, the run's own records with no " +
		"such tie included (for example one an Add data element wrote), is listed in cleanup.skipped and left in " +
		"place; check each before deleting it with odata-delete."
)

// dataWriteSleep is how the wait sleeps between polls (a test seam, as clio's Sleep).
var dataWriteSleep = time.Sleep

// WaitingElement is clio's WaitingElement: a process element a run is parked on.
type WaitingElement struct {
	Caption *string `json:"caption,omitempty"`
	Type    *string `json:"type,omitempty"`
}

// CreatedRecord is clio's CreatedRecord: a record that appeared in a watched entity while the run ran.
type CreatedRecord struct {
	Entity      string  `json:"entity"`
	ID          string  `json:"id"`
	Title       *string `json:"title,omitempty"`
	createdOn   string
	createdByID string
}

// CleanupFailure is clio's CleanupFailure: a record a cleanup could not delete, with the reason.
type CleanupFailure struct {
	Entity string `json:"entity"`
	ID     string `json:"id"`
	Error  string `json:"error"`
}

// CleanupReport is clio's CleanupReport.
type CleanupReport struct {
	Deleted []CreatedRecord  `json:"deleted"`
	Failed  []CleanupFailure `json:"failed"`
	Skipped []CreatedRecord  `json:"skipped"`
}

func newCleanupReport() *CleanupReport {
	return &CleanupReport{Deleted: []CreatedRecord{}, Failed: []CleanupFailure{}, Skipped: []CreatedRecord{}}
}

// DataWriteValidateVerification is RunProcessCommand.ValidateVerificationOptions: refused before anything is
// read or launched.
func DataWriteValidateVerification(request DataWriteProcessRequest) string {
	if request.WaitTimeoutSeconds < 0 || request.WaitTimeoutSeconds > dataWriteMaxWaitSeconds {
		return fmt.Sprintf("wait-timeout must be between 0 and %d seconds.", dataWriteMaxWaitSeconds)
	}
	if len(request.Watch) > dataWriteMaxWatched {
		return fmt.Sprintf("watch takes at most %d entities.", dataWriteMaxWatched)
	}
	for _, name := range request.Watch {
		if !recordDeletionEntityName.MatchString(name) {
			return fmt.Sprintf("watch must list entity schema names (letters, digits, underscore); '%s' is not one.", name)
		}
	}
	if request.Cleanup && len(request.Watch) == 0 {
		return "cleanup deletes only what the run created in the watched entities, so it needs watch: list the " +
			"entities the process creates records in (for example [\"Activity\"])."
	}
	return ""
}

// dataWriteRequestBudget is RequestBudget: a verification request may spend 10 s, never past the deadline.
func dataWriteRequestBudget(deadline time.Time) time.Duration {
	if deadline.IsZero() {
		return dataWriteVerificationCall
	}
	return min(max(time.Until(deadline), time.Second), dataWriteVerificationCall)
}

// dataWriteBudgetUntil is BudgetUntil: the time left before stopAt, at most 10 s; may be zero or negative.
func dataWriteBudgetUntil(stopAt time.Time) time.Duration {
	if stopAt.IsZero() {
		return dataWriteVerificationCall
	}
	return min(dataWriteVerificationCall, time.Until(stopAt))
}

// dataWriteWatchBudget is CreatedRecordWatcher.Budget: each request gets timeout, never past stopAt.
func dataWriteWatchBudget(timeout time.Duration, stopAt time.Time) time.Duration {
	bound := max(time.Millisecond, timeout)
	if stopAt.IsZero() {
		return bound
	}
	return min(max(time.Until(stopAt), time.Millisecond), bound)
}

func timeIsUp(stopAt time.Time) bool { return !stopAt.IsZero() && !time.Now().Before(stopAt) }

func fencedOr(err error, fallback string) string {
	if fenced := DataWriteUntrusted(clioConnectionText(err)); fenced != nil {
		return *fenced
	}
	return fallback
}

// dataWriteVerify is RunProcessCommand.Verify: waits for a parked run, reports what it created in the watched
// entities, and cleans up.
func (c *Client) dataWriteVerify(ctx context.Context, request DataWriteProcessRequest, response *DataWriteProcessResult, baseline *watchBaseline) {
	if response.Status == "" || response.Status == DataWriteNotStartedStatus {
		return
	}
	work := context.WithoutCancel(ctx)
	processID, _ := parseGUID(response.ProcessID)
	if processID == "" {
		processID = emptyGUID
	}
	monitor := &processRunMonitor{c: c, ctx: work}
	if request.Wait && processID != emptyGUID && response.Status == dataWriteProcessStatuses[dataWriteRunningStatus] {
		dataWriteWaitForRun(request, response, processID, monitor)
	}
	if baseline == nil {
		return
	}
	if response.Status == dataWriteQueuedStatus {
		// A queued run has not executed yet, so a diff now would read as "the run created nothing".
		response.Warnings = append(response.Warnings, "`created` is not reported and nothing was cleaned up: the platform queued "+
			"the run, so it had not executed yet when clio could look. Read the watched entities later, or launch "+
			"with result-parameters, which makes the run synchronous.")
		return
	}
	if request.callerGone() {
		return
	}
	watcher := &createdRecordWatcher{c: c, ctx: work}
	outcome := watcher.collect(baseline, dataWriteRequestBudget(request.Deadline), dataWriteStopAt(request.Deadline))
	response.Created = outcome.created
	response.Warnings = append(response.Warnings, outcome.warnings...)
	if request.Cleanup {
		c.dataWriteCleanUp(work, request, response, processID, baseline, monitor)
	}
}

// dataWriteStopAt is the deadline less the wait reserve; zero when no deadline applies.
func dataWriteStopAt(deadline time.Time) time.Time {
	if deadline.IsZero() {
		return time.Time{}
	}
	return deadline.Add(-dataWriteWaitReserve)
}

func dataWriteWaitForRun(request DataWriteProcessRequest, response *DataWriteProcessResult, processID string, monitor *processRunMonitor) {
	reserve := dataWriteWaitReserve
	if len(request.Watch) > 0 {
		reserve += dataWriteCollectReserve
	}
	if request.Cleanup {
		reserve += dataWriteCleanupReserve
	}
	started := time.Now()
	until := started.Add(time.Duration(request.WaitTimeoutSeconds) * time.Second)
	cut := false
	if !request.Deadline.IsZero() && request.Deadline.Add(-reserve).Before(until) {
		until = request.Deadline.Add(-reserve)
		cut = true
	}
	state, readError := dataWritePollUntilEnd(request, monitor, processID, until)
	if request.callerGone() {
		return
	}
	if state == nil && readError != "" {
		response.Warnings = append(response.Warnings, "The run is still running and its state could not be read while waiting: "+
			readError+fmt.Sprintf(". Read SysProcessLog row %s for the outcome.", processID))
		return
	}
	if readError != "" {
		response.Warnings = append(response.Warnings, "The last read of the run's state failed ("+readError+"); the state "+
			"reported is the one read before it.")
	}
	waited := int(math.RoundToEven(time.Since(started).Seconds()))
	var requested *int
	if cut {
		seconds := request.WaitTimeoutSeconds
		requested = &seconds
	}
	DataWriteApplyRunState(response, state, waited, requested)
}

// dataWritePollUntilEnd reads the run until it ends, until passes, the caller goes or reads keep failing; the
// last state read in full is kept.
func dataWritePollUntilEnd(request DataWriteProcessRequest, monitor *processRunMonitor, processID string, until time.Time) (*processRunState, string) {
	var state *processRunState
	readError := ""
	failed := 0
	for !request.callerGone() {
		readTimeout := min(max(time.Until(until)+dataWriteWaitReserve, time.Second), dataWriteVerificationCall)
		read, err := monitor.read(processID, readTimeout)
		if err != nil {
			readError = fencedOr(err, "Exception")
			failed++
		} else {
			state, readError, failed = read, "", 0
		}
		left := time.Until(until)
		if (readError == "" && state != nil && state.terminal()) || left <= 0 || failed >= dataWriteMaxFailedReads {
			break
		}
		dataWriteSleep(min(dataWritePollInterval, left))
	}
	return state, readError
}

// DataWriteApplyRunState is RunProcessCommand.ApplyRunState, for a run the launch answered `running`.
func DataWriteApplyRunState(response *DataWriteProcessResult, state *processRunState, waitedSeconds int, requestedSeconds *int) {
	if state == nil || !state.found || state.status == nil {
		response.Warnings = append(response.Warnings, fmt.Sprintf("The run answered `running`, but its SysProcessLog row "+
			"%s could not be read while waiting, so no later status is known. Read that row later instead of re-running the process.",
			response.ProcessID))
		return
	}
	if state.note != "" {
		response.Warnings = append(response.Warnings, state.note)
	}
	response.Status = dataWriteStatusName(*state.status)
	dataWriteDropStaleResultValues(response, state.terminal())
	if *state.status == dataWriteErrorStatus {
		response.Error = dataWriteDescribeRunFailure("", state.errorSummary, response.VersionName)
		return
	}
	if *state.status == dataWriteCancelledStatus {
		response.Error = "The run was cancelled while clio waited for it - by a user or by another process - so it " +
			"did not finish. What it did before the cancel stays in place."
		return
	}
	if state.terminal() {
		return
	}
	if len(state.waitingOn) > 0 {
		response.WaitingOn = state.waitingOn
	} else {
		response.WaitingOn = nil
	}
	elements := "no element is logged as running yet"
	if len(state.waitingOn) > 0 {
		described := make([]string, len(state.waitingOn))
		for i, element := range state.waitingOn {
			described[i] = dataWriteDescribeWaitingElement(element)
		}
		elements = "it is parked on " + strings.Join(described, ", ")
	}
	waited := fmt.Sprintf("%d s", waitedSeconds)
	if requestedSeconds != nil {
		waited = fmt.Sprintf("%d s (the response deadline cut the requested %d s wait short)", waitedSeconds, *requestedSeconds)
	}
	response.Warnings = append(response.Warnings, fmt.Sprintf("The run is still running after clio waited %s: %s. ", waited, elements)+
		"This is NOT a failure. A user task (for example a Perform task that created an activity) waits for a "+
		"person to complete it and never ends on its own, so judge the run by `created` and `waitingOn` rather "+
		"than by waiting longer. A timer or background element ends by itself: read SysProcessLog row "+
		fmt.Sprintf("%s later instead of re-running the process.", response.ProcessID))
}

func dataWriteStatusName(status int) string {
	if name, ok := dataWriteProcessStatuses[status]; ok {
		return name
	}
	return fmt.Sprintf("unknown-status-%d", status)
}

// dataWriteDropStaleResultValues: values returned at the launch of a parked run are not what it ended with.
func dataWriteDropStaleResultValues(response *DataWriteProcessResult, ended bool) {
	var values map[string]json.RawMessage
	if json.Unmarshal(response.ResultParameterValues, &values) != nil || len(values) == 0 {
		return
	}
	if ended {
		response.ResultParameterValues = nil
		response.Warnings = append(response.Warnings, "resultParameterValues are not reported: the platform returned them at the "+
			"launch, while the run was still running, so they are not what it ended with. Judge the run by its "+
			"effects and `created` instead of re-running it.")
		return
	}
	response.Warnings = append(response.Warnings, dataWriteParkedValuesNote+"parked; it is still running, so they are not its results.")
}

func dataWriteDescribeWaitingElement(element WaitingElement) string {
	caption := "an unnamed element"
	if element.Caption != nil {
		if fenced := DataWriteUntrusted(*element.Caption); fenced != nil {
			caption = *fenced
		}
	}
	if element.Type == nil {
		return caption
	}
	return fmt.Sprintf("%s (%s)", caption, *element.Type)
}

// dataWriteCleanUp is RunProcessCommand.CleanUp: deletes only the caller's records the run provably holds.
func (c *Client) dataWriteCleanUp(ctx context.Context, request DataWriteProcessRequest, response *DataWriteProcessResult, processID string,
	baseline *watchBaseline, monitor *processRunMonitor) {
	created := response.Created
	me := baseline.currentUserContactID
	mine := []CreatedRecord{}
	if me != "" {
		for _, record := range created {
			if strings.EqualFold(record.createdByID, me) {
				mine = append(mine, record)
			}
		}
	}
	stopAt := dataWriteStopAt(request.Deadline)
	if request.callerGone() {
		return
	}
	response.Warnings = append(response.Warnings, DataWriteTimeWindowNote)
	if timeIsUp(stopAt) {
		report := newCleanupReport()
		report.Skipped = append(report.Skipped, created...)
		response.Cleanup = report
		if len(created) > 0 {
			response.Warnings = append(response.Warnings, "cleanup did nothing: too little of the response time was left to tie the records "+
				"to the run, cancel it and delete safely. cleanup.skipped lists every record in `created`.")
		}
		return
	}
	// Read while the run is still parked: its element data rows, which tie a record to it, go when it stops.
	tied := c.dataWriteTieToRun(ctx, mine, processID, stopAt, response)
	ours := []CreatedRecord{}
	for _, record := range mine {
		if tied[strings.ToLower(record.ID)] {
			ours = append(ours, record)
		}
	}
	if len(ours) > 0 && processID != emptyGUID && response.Status == dataWriteProcessStatuses[dataWriteRunningStatus] {
		dataWriteCancelRun(request, response, processID, stopAt, monitor)
	}
	references := map[string][]string{}
	for _, watch := range baseline.entities {
		if watch.err == "" {
			if _, ok := references[strings.ToLower(watch.entity)]; !ok {
				references[strings.ToLower(watch.entity)] = watch.references
			}
		}
	}
	cleaner := &createdRecordCleaner{service: newRecordDeletionService(ctx, c)}
	report := cleaner.delete(ours, references, dataWriteRequestBudget(request.Deadline), stopAt, request.callerGone)
	oursIDs := map[string]bool{}
	for _, record := range ours {
		oursIDs[record.key()] = true
	}
	for _, record := range created {
		if !oursIDs[record.key()] {
			report.Skipped = append(report.Skipped, record)
		}
	}
	response.Cleanup = report
	if len(report.Skipped) > 0 {
		if me == "" {
			response.Warnings = append(response.Warnings, "cleanup deleted nothing: the calling user's contact could not be read, so no record could be shown "+
				"to be the caller's own. cleanup.skipped lists them.")
		} else {
			response.Warnings = append(response.Warnings, fmt.Sprintf("cleanup left %d record(s) it cannot tie to this run; cleanup.skipped lists them.",
				len(report.Skipped)))
		}
	}
	if len(report.Failed) > 0 {
		response.Warnings = append(response.Warnings, fmt.Sprintf("cleanup left %d record(s) in place; cleanup.failed names each with the reason.",
			len(report.Failed)))
	}
}

func (r CreatedRecord) key() string {
	return strings.Join([]string{r.Entity, r.ID, r.createdOn, r.createdByID}, "\x00")
}

// dataWriteTieToRun: the ids of the caller's records the run provably holds, or that point at such a record.
func (c *Client) dataWriteTieToRun(ctx context.Context, mine []CreatedRecord, processID string, stopAt time.Time, response *DataWriteProcessResult) map[string]bool {
	if len(mine) == 0 || processID == emptyGUID {
		return map[string]bool{}
	}
	provenance := (&runRecordProvenance{c: c, ctx: ctx}).resolve(mine, []string{processID}, max(time.Millisecond, dataWriteBudgetUntil(stopAt)), stopAt)
	response.Warnings = append(response.Warnings, provenance.warnings...)
	return provenance.tied
}

// dataWriteCancelRun: every request is bounded by stopAt, none starts with less than a second left or after the
// caller cancelled the call.
func dataWriteCancelRun(request DataWriteProcessRequest, response *DataWriteProcessResult, processID string, stopAt time.Time, monitor *processRunMonitor) {
	budget := dataWriteBudgetUntil(stopAt)
	if budget < dataWriteMinimumDestructive {
		response.Warnings = append(response.Warnings, "The still-running run was not cancelled before cleanup: too little of "+
			"the response time was left.")
		return
	}
	if request.callerGone() {
		return
	}
	message, err := monitor.cancel(processID, budget)
	if err != nil {
		response.Warnings = append(response.Warnings, "The still-running run could not be cancelled before cleanup: "+fencedOr(err, "Exception")+".")
		return
	}
	if message != "" {
		response.Warnings = append(response.Warnings, "The still-running run was not cancelled before cleanup: "+message+".")
		return
	}
	until := time.Now().Add(dataWriteCancelWait)
	if !stopAt.IsZero() && stopAt.Before(until) {
		until = stopAt
	}
	for time.Now().Before(until) && !request.callerGone() {
		// The cancel was accepted; one slow read of its effect is retried, not reported as a failed cancel.
		state, err := monitor.read(processID, max(time.Millisecond, min(dataWriteVerificationCall, time.Until(until))))
		if err == nil && state != nil && state.terminal() {
			status := dataWriteErrorStatus
			if state.status != nil {
				status = *state.status
			}
			response.Status = dataWriteStatusName(status)
			response.WaitingOn = nil
			kept := response.Warnings[:0]
			for _, warning := range response.Warnings {
				if !strings.HasPrefix(warning, dataWriteParkedValuesNote) {
					kept = append(kept, warning)
				}
			}
			response.Warnings = kept
			dataWriteDropStaleResultValues(response, true)
			response.Warnings = append(response.Warnings, "cleanup cancelled the still-running run before deleting what it created.")
			return
		}
		left := time.Until(until)
		if left <= 0 {
			break
		}
		dataWriteSleep(min(dataWritePollInterval, left))
	}
	response.Warnings = append(response.Warnings, "cleanup asked the platform to cancel the still-running run, but it had not "+
		"stopped in the time left; records it still uses may refuse deletion.")
}

// ---- ProcessRunMonitor ----

type processRunState struct {
	found        bool
	status       *int
	errorSummary string
	waitingOn    []WaitingElement
	note         string
}

func (s *processRunState) terminal() bool {
	return s.status != nil && (*s.status == 2 || *s.status == 3 || *s.status == 4)
}

type processRunMonitor struct {
	c   *Client
	ctx context.Context
}

// read is ProcessRunMonitor.Read: the run's status, the first line of its error (or of a failed
// sub-process's), and the elements it is parked on.
func (m *processRunMonitor) read(processID string, timeout time.Duration) (*processRunState, error) {
	readUntil := time.Now().Add(max(time.Millisecond, timeout))
	left := func() time.Duration { return max(time.Millisecond, time.Until(readUntil)) }
	rows, err := m.c.esqSelect(m.ctx, esqQuery("SysProcessLog",
		map[string]any{"StatusValue": esqColumn("Status.Value"), "ErrorDescription": esqColumn("ErrorDescription")},
		esqGroup(0, map[string]any{"byId": esqEq("Id", processID, recordDeletionGUIDType)}), 1), timeout)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return &processRunState{waitingOn: []WaitingElement{}}, nil
	}
	root := rows[0]
	var status *int
	if raw, ok := root["StatusValue"]; ok {
		var number json.Number
		decoder := json.NewDecoder(strings.NewReader(string(raw)))
		decoder.UseNumber()
		if decoder.Decode(&number) == nil {
			if value, err := strconv.Atoi(number.String()); err == nil {
				status = &value
			}
		}
	}
	errorSummary := recordDeletionFirstLine(rowText(root, "ErrorDescription"))
	if errorSummary == "" && status != nil && *status == dataWriteErrorStatus {
		errorSummary = m.readSubProcessError(processID, left())
	}
	state := &processRunState{found: true, status: status, errorSummary: errorSummary, waitingOn: []WaitingElement{}}
	if status == nil || *status != dataWriteRunningStatus {
		return state, nil
	}
	remaining := left()
	if remaining < 500*time.Millisecond {
		state.note = "The elements the run waits on were not read: too little of the read's time was left."
		return state, nil
	}
	waiting, err := m.readWaitingElements(processID, remaining)
	if err != nil {
		state.note = "The elements the run waits on could not be read: " + fencedOr(err, "Exception") + "."
		return state, nil
	}
	state.waitingOn = waiting
	return state, nil
}

// readSubProcessError: a sub-process failure leaves the root's own ErrorDescription empty.
func (m *processRunMonitor) readSubProcessError(rootID string, timeout time.Duration) string {
	rows, err := m.c.esqSelect(m.ctx, esqQuery("SysProcessLog", map[string]any{"ErrorDescription": esqColumn("ErrorDescription")},
		esqGroup(0, map[string]any{"byRoot": esqEq("RootId", rootID, recordDeletionGUIDType),
			"failed": esqEq("Status.Value", strconv.Itoa(dataWriteErrorStatus), 4)}), 5), timeout)
	if err != nil {
		return ""
	}
	for _, row := range rows {
		if line := recordDeletionFirstLine(rowText(row, "ErrorDescription")); line != "" {
			return line
		}
	}
	return ""
}

func (m *processRunMonitor) readWaitingElements(processID string, timeout time.Duration) ([]WaitingElement, error) {
	rows, err := m.c.esqSelect(m.ctx, esqQuery("SysProcessElementLog", map[string]any{"Caption": esqColumn("Caption"), "Type": esqColumn("Type")},
		esqGroup(0, map[string]any{"byProcess": esqEq("SysProcess", processID, recordDeletionGUIDType),
			"running": esqEq("Status.Value", strconv.Itoa(dataWriteRunningStatus), 4)}), 20), timeout)
	if err != nil {
		return nil, err
	}
	elements := []WaitingElement{}
	for _, row := range rows {
		element := WaitingElement{}
		if _, ok := row["Caption"]; ok {
			caption := rowText(row, "Caption")
			element.Caption = &caption
		}
		if kind := strings.TrimSpace(rowText(row, "Type")); kind != "" {
			short := rowText(row, "Type")
			if dot := strings.LastIndex(short, "."); dot >= 0 {
				short = short[dot+1:]
			}
			element.Type = &short
		}
		elements = append(elements, element)
	}
	return elements, nil
}

// cancel asks the platform to cancel the run: "" when the request was accepted, else the error.
func (m *processRunMonitor) cancel(processID string, timeout time.Duration) (string, error) {
	body, _ := json.Marshal(map[string]string{"processDataIds": processID})
	response, err := m.c.dataWriteSendOnce(m.ctx, http.MethodPost, dataWriteCancelRoute, body, max(time.Millisecond, timeout))
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(response) == "" {
		return "", nil
	}
	if units := []rune(response); len(units) > 300 {
		response = string(units[:300])
	}
	detail := "no detail"
	if fenced := DataWriteUntrusted(response); fenced != nil {
		detail = *fenced
	}
	return "the cancel request was answered with an error: " + detail, nil
}

// ---- CreatedRecordWatcher ----

const (
	watchMaxCreated       = 100
	watchMaxKnownIDs      = 500
	watchCreatedByAlias   = "ClioCreatedBy"
	watchCanonicalRows    = 5000
	watchCasePrefixLength = 3
)

type entityWatch struct {
	entity      string
	titleColumn string
	references  []string
	baseline    *string
	knownIDs    map[string]bool
	err         string
}

type watchBaseline struct {
	entities             []entityWatch
	currentUserContactID string
}

type watchOutcome struct {
	created  []CreatedRecord
	warnings []string
}

type createdRecordWatcher struct {
	c   *Client
	ctx context.Context
}

// capture is CreatedRecordWatcher.Capture: per entity, the newest CreatedOn and the ids at that instant.
func (w *createdRecordWatcher) capture(entities []string, timeout time.Duration, stopAt time.Time) *watchBaseline {
	baseline := &watchBaseline{currentUserContactID: w.readCurrentUserContactID(dataWriteWatchBudget(timeout, stopAt))}
	seen := map[string]bool{}
	for _, entity := range entities {
		if seen[strings.ToLower(entity)] {
			continue
		}
		seen[strings.ToLower(entity)] = true
		if timeIsUp(stopAt) {
			baseline.entities = append(baseline.entities, entityWatch{entity: entity, err: "the baseline read ran out of time before the launch"})
			continue
		}
		baseline.entities = append(baseline.entities, w.captureOne(entity, timeout, stopAt))
	}
	return baseline
}

func (w *createdRecordWatcher) captureOne(entity string, timeout time.Duration, stopAt time.Time) entityWatch {
	if !recordDeletionEntityName.MatchString(entity) {
		return entityWatch{entity: entity, err: "not a plain entity schema name"}
	}
	name := entity
	schema, err := w.readSchema(entity, timeout, stopAt)
	if err != nil {
		return entityWatch{entity: name, err: fencedOr(err, "Exception")}
	}
	if recordDeletionEntityName.MatchString(schema.Name) {
		name = schema.Name
	}
	titleColumn := schema.primaryDisplayColumn()
	references := []string{}
	referenceSeen := map[string]bool{}
	for _, column := range schema.Columns.Items {
		if column.ReferenceSchemaName != nil && strings.TrimSpace(*column.ReferenceSchemaName) != "" && !referenceSeen[strings.ToLower(*column.ReferenceSchemaName)] {
			referenceSeen[strings.ToLower(*column.ReferenceSchemaName)] = true
			references = append(references, *column.ReferenceSchemaName)
		}
	}
	rows, err := w.c.esqSelect(w.ctx, watchNewestQuery(name), dataWriteWatchBudget(timeout, stopAt))
	if err != nil {
		return entityWatch{entity: name, err: fencedOr(err, "Exception")}
	}
	var newest *string
	if len(rows) > 0 {
		if value := rowText(rows[0], "CreatedOn"); value != "" {
			newest = &value
		}
	}
	known := map[string]bool{}
	if newest != nil {
		atNewest := entityWatch{entity: name, titleColumn: titleColumn, baseline: newest}
		rows, err := w.c.esqSelect(w.ctx, watchRowsQuery(atNewest, watchMaxKnownIDs+1, false), dataWriteWatchBudget(timeout, stopAt))
		if err != nil {
			return entityWatch{entity: name, err: fencedOr(err, "Exception")}
		}
		if len(rows) > watchMaxKnownIDs {
			return entityWatch{entity: name, err: fmt.Sprintf("more than %d existing rows share its newest CreatedOn, so new rows cannot be "+
				"told apart from them", watchMaxKnownIDs)}
		}
		for _, row := range rows {
			known[strings.ToLower(rowText(row, "Id"))] = true
		}
	}
	return entityWatch{entity: name, titleColumn: titleColumn, references: references, baseline: newest, knownIDs: known}
}

// readSchema: the schema reader matches the name exactly; on a miss the platform's spelling is looked up.
func (w *createdRecordWatcher) readSchema(entity string, timeout time.Duration, stopAt time.Time) (*richRuntimeSchema, error) {
	schema, err := w.c.watchRuntimeSchema(w.ctx, entity, dataWriteWatchBudget(timeout, stopAt))
	if err == nil {
		return schema, nil
	}
	canonical := w.findCanonicalName(entity, timeout, stopAt)
	if canonical == "" {
		return nil, err
	}
	return w.c.watchRuntimeSchema(w.ctx, canonical, dataWriteWatchBudget(timeout, stopAt))
}

func (c *Client) watchRuntimeSchema(ctx context.Context, name string, timeout time.Duration) (*richRuntimeSchema, error) {
	readCtx, cancel := context.WithTimeout(ctx, max(time.Millisecond, timeout))
	defer cancel()
	return c.readRichRuntimeSchema(readCtx, map[string]string{"Name": name}, fmt.Sprintf("Runtime schema '%s' was not returned by Creatio.", name))
}

func (w *createdRecordWatcher) findCanonicalName(entity string, timeout time.Duration, stopAt time.Time) string {
	variants := map[string]any{}
	for i, prefix := range watchCasePrefixes(entity) {
		variants[fmt.Sprintf("prefix%d", i)] = map[string]any{"filterType": 1, "comparisonType": 9, "isEnabled": true,
			"leftExpression":  map[string]any{"expressionType": 0, "columnPath": "Name"},
			"rightExpression": map[string]any{"expressionType": 2, "parameter": map[string]any{"dataValueType": recordDeletionTextType, "value": prefix}}}
	}
	rows, err := w.c.esqSelect(w.ctx, esqQuery("SysSchema",
		map[string]any{"Name": map[string]any{"expression": map[string]any{"expressionType": 0, "columnPath": "Name"}, "orderDirection": 1, "orderPosition": 0}},
		esqGroup(0, map[string]any{"byPrefix": esqGroup(1, variants), "byManager": esqEq("ManagerName", "EntitySchemaManager", recordDeletionTextType)}),
		watchCanonicalRows), dataWriteWatchBudget(timeout, stopAt))
	if err != nil {
		return ""
	}
	for _, row := range rows {
		name := rowText(row, "Name")
		if strings.EqualFold(name, entity) && name != entity && recordDeletionEntityName.MatchString(name) {
			return name
		}
	}
	return ""
}

// watchCasePrefixes is CasePrefixes: every case variant of the first three letters.
func watchCasePrefixes(entity string) []string {
	head := []rune(entity)
	if len(head) > watchCasePrefixLength {
		head = head[:watchCasePrefixLength]
	}
	prefixes := []string{""}
	for _, letter := range head {
		lower, upper := unicode.ToLower(letter), unicode.ToUpper(letter)
		next := []string{}
		for _, prefix := range prefixes {
			if lower == upper {
				next = append(next, prefix+string(letter))
			} else {
				next = append(next, prefix+string(lower), prefix+string(upper))
			}
		}
		prefixes = next
	}
	return prefixes
}

func watchCreatedOnIsNotNull() map[string]any {
	return map[string]any{"filterType": 2, "comparisonType": 2, "isNull": false, "isEnabled": true,
		"leftExpression": map[string]any{"expressionType": 0, "columnPath": "CreatedOn"}}
}

func watchNewestQuery(entity string) map[string]any {
	return esqQuery(entity, map[string]any{"CreatedOn": map[string]any{"expression": map[string]any{"expressionType": 0, "columnPath": "CreatedOn"},
		"orderDirection": 2, "orderPosition": 0}}, esqGroup(0, map[string]any{"hasCreatedOn": watchCreatedOnIsNotNull()}), 1)
}

// watchRowsQuery is BuildRowsQuery: rows at or after the baseline (every row when the table was empty), oldest
// first. The baseline goes back as a quoted JSON date, as DataService returned it.
func watchRowsQuery(watch entityWatch, rowCount int, withTitle bool) map[string]any {
	columns := map[string]any{"Id": esqColumn("Id"),
		"CreatedOn": map[string]any{"expression": map[string]any{"expressionType": 0, "columnPath": "CreatedOn"}, "orderDirection": 1, "orderPosition": 0}}
	if withTitle {
		columns[watchCreatedByAlias] = esqColumn("CreatedBy")
	}
	if withTitle && strings.TrimSpace(watch.titleColumn) != "" && watch.titleColumn != "Id" && watch.titleColumn != "CreatedOn" {
		columns[watch.titleColumn] = esqColumn(watch.titleColumn)
	}
	items := map[string]any{"hasCreatedOn": watchCreatedOnIsNotNull()}
	if watch.baseline != nil {
		quoted, _ := json.Marshal(*watch.baseline)
		items["sinceBaseline"] = map[string]any{"filterType": 1, "comparisonType": 8, "isEnabled": true,
			"leftExpression":  map[string]any{"expressionType": 0, "columnPath": "CreatedOn"},
			"rightExpression": map[string]any{"expressionType": 2, "parameter": map[string]any{"dataValueType": 7, "value": string(quoted)}}}
	}
	return esqQuery(watch.entity, columns, esqGroup(0, items), rowCount)
}

// readCurrentUserContactID is best effort: without it a cleanup deletes nothing, which is the safe failure.
func (w *createdRecordWatcher) readCurrentUserContactID(timeout time.Duration) string {
	filter := map[string]any{"filterType": 1, "comparisonType": 3, "isEnabled": true,
		"leftExpression":  map[string]any{"expressionType": 0, "columnPath": "Id"},
		"rightExpression": map[string]any{"expressionType": 1, "functionType": 1, "macrosType": 2}}
	rows, err := w.c.esqSelect(w.ctx, esqQuery("Contact", map[string]any{"Id": esqColumn("Id")}, esqGroup(0, map[string]any{"me": filter}), 1), timeout)
	if err != nil || len(rows) == 0 {
		return ""
	}
	return rowText(rows[0], "Id")
}

// collect is CreatedRecordWatcher.Collect: the records created after the baseline.
func (w *createdRecordWatcher) collect(baseline *watchBaseline, timeout time.Duration, stopAt time.Time) watchOutcome {
	outcome := watchOutcome{created: []CreatedRecord{}}
	for _, watch := range baseline.entities {
		if watch.err != "" {
			outcome.warnings = append(outcome.warnings, fmt.Sprintf("'%s' was not watched: %s", watch.entity, watch.err))
			continue
		}
		if timeIsUp(stopAt) {
			outcome.warnings = append(outcome.warnings, fmt.Sprintf("'%s' was not read after the run: the response deadline was reached, "+
				"so what the run created in it is not reported.", watch.entity))
			continue
		}
		rows, err := w.c.esqSelect(w.ctx, watchRowsQuery(watch, watchMaxCreated+len(watch.knownIDs)+1, true), dataWriteWatchBudget(timeout, stopAt))
		if err != nil {
			outcome.warnings = append(outcome.warnings, fmt.Sprintf("'%s' could not be read after the run: %s", watch.entity, fencedOr(err, "Exception")))
			continue
		}
		created := []CreatedRecord{}
		for _, row := range rows {
			id := rowText(row, "Id")
			if watch.knownIDs[strings.ToLower(id)] {
				continue
			}
			record := CreatedRecord{Entity: watch.entity, ID: id, Title: watchReadTitle(row, watch.titleColumn), createdOn: rowText(row, "CreatedOn")}
			record.createdByID = watchReadLookupID(row[watchCreatedByAlias])
			created = append(created, record)
		}
		if len(created) > watchMaxCreated {
			outcome.warnings = append(outcome.warnings, fmt.Sprintf("'%s' had more than %d new records; only the first %d are listed.",
				watch.entity, watchMaxCreated, watchMaxCreated))
			created = created[:watchMaxCreated]
		}
		outcome.created = append(outcome.created, created...)
	}
	return outcome
}

func watchReadLookupID(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var lookup map[string]json.RawMessage
	if json.Unmarshal(raw, &lookup) == nil && lookup != nil {
		return rowText(lookup, "value")
	}
	return rowText(map[string]json.RawMessage{"v": raw}, "v")
}

func watchReadTitle(row map[string]json.RawMessage, titleColumn string) *string {
	if strings.TrimSpace(titleColumn) == "" {
		return nil
	}
	raw, ok := row[titleColumn]
	if !ok {
		return nil
	}
	var lookup map[string]json.RawMessage
	if json.Unmarshal(raw, &lookup) == nil && lookup != nil {
		if _, has := lookup["displayValue"]; !has {
			return nil
		}
		title := rowText(lookup, "displayValue")
		return &title
	}
	title := rowText(row, titleColumn)
	return &title
}

// ---- RunRecordProvenance ----

type recordProvenance struct {
	tied       map[string]bool // lower-cased ids
	holdingRun map[string]string
	warnings   []string
}

type runRecordProvenance struct {
	c   *Client
	ctx context.Context
}

// resolve is RunRecordProvenance.Resolve: which candidates provably belong to the run.
func (p *runRecordProvenance) resolve(candidates []CreatedRecord, runIDs []string, timeout time.Duration, stopAt time.Time) recordProvenance {
	return p.resolveSeeded(candidates, nil, runIDs, timeout, stopAt)
}

// resolveSeeded is Resolve with seed ids: records tied from the start (trigger-smoke seeds its trigger).
func (p *runRecordProvenance) resolveSeeded(candidates []CreatedRecord, seeds, runIDs []string, timeout time.Duration, stopAt time.Time) recordProvenance {
	provenance := recordProvenance{tied: map[string]bool{}, holdingRun: map[string]string{}, warnings: []string{}}
	for _, seed := range seeds {
		provenance.tied[strings.ToLower(seed)] = true
	}
	pointsAt := map[string]map[string]bool{}
	elementOf := map[string]string{}
	elementOrder := []string{}
	groups := []string{}
	byEntity := map[string][]string{}
	for _, record := range candidates {
		key := strings.ToLower(record.Entity)
		if _, ok := byEntity[key]; !ok {
			groups = append(groups, record.Entity)
		}
		byEntity[key] = append(byEntity[key], record.ID)
	}
	for _, entity := range groups {
		p.readEntity(entity, byEntity[strings.ToLower(entity)], timeout, stopAt, pointsAt, elementOf, &elementOrder, &provenance)
	}
	p.readHoldingRuns(elementOf, elementOrder, timeout, stopAt, &provenance)
	runs := p.expandRuns(runIDs, timeout, stopAt, &provenance)
	for record, run := range provenance.holdingRun {
		if runs[run] {
			provenance.tied[record] = true
		}
	}
	for {
		linked := []string{}
		for _, record := range candidates {
			id := strings.ToLower(record.ID)
			if provenance.tied[id] {
				continue
			}
			for value := range pointsAt[id] {
				if provenance.tied[value] {
					linked = append(linked, id)
					break
				}
			}
		}
		for _, id := range linked {
			provenance.tied[id] = true
		}
		if len(linked) == 0 {
			return provenance
		}
	}
}

func (p *runRecordProvenance) readEntity(entity string, ids []string, timeout time.Duration, stopAt time.Time,
	pointsAt map[string]map[string]bool, elementOf map[string]string, elementOrder *[]string, provenance *recordProvenance) {
	tooLate := fmt.Sprintf("The new %s records could not be tied to the run: the time ran out first.", entity)
	if timeIsUp(stopAt) {
		provenance.warnings = append(provenance.warnings, tooLate)
		return
	}
	schema, err := p.c.watchRuntimeSchema(p.ctx, entity, dataWriteWatchBudget(timeout, stopAt))
	if err != nil {
		provenance.warnings = append(provenance.warnings, fmt.Sprintf("The new %s records could not be tied to the run: %s", entity, fencedOr(err, "Exception")))
		return
	}
	lookups := []string{}
	hasElement := false
	for _, column := range schema.Columns.Items {
		if column.Name == recordDeletionProcessColumn {
			hasElement = true
		}
		if column.ReferenceSchemaName != nil && strings.TrimSpace(*column.ReferenceSchemaName) != "" && !recordDeletionAuditColumns[column.Name] &&
			recordDeletionEntityName.MatchString(column.Name) {
			lookups = append(lookups, column.Name)
		}
	}
	sort.Strings(lookups)
	columns := append([]string{}, lookups...)
	if hasElement {
		columns = append(columns, recordDeletionProcessColumn)
	}
	if len(columns) == 0 {
		return
	}
	if timeIsUp(stopAt) {
		provenance.warnings = append(provenance.warnings, tooLate)
		return
	}
	rows, err := p.selectByIDs(entity, columns, ids, timeout, stopAt)
	if err != nil {
		provenance.warnings = append(provenance.warnings, fmt.Sprintf("The new %s records could not be tied to the run: %s", entity, fencedOr(err, "Exception")))
		return
	}
	for _, row := range rows {
		id := strings.ToLower(rowText(row, "Id"))
		values := map[string]bool{}
		for _, column := range lookups {
			if value := recordDeletionLookupValue(row[column]); value != "" {
				values[value] = true
			}
		}
		pointsAt[id] = values
		if hasElement {
			if element := recordDeletionLookupValue(row[recordDeletionProcessColumn]); element != "" {
				if _, ok := elementOf[id]; !ok {
					*elementOrder = append(*elementOrder, id)
				}
				elementOf[id] = element
			}
		}
	}
}

func (p *runRecordProvenance) readHoldingRuns(elementOf map[string]string, order []string, timeout time.Duration, stopAt time.Time, provenance *recordProvenance) {
	if len(elementOf) == 0 {
		return
	}
	if timeIsUp(stopAt) {
		provenance.warnings = append(provenance.warnings, "The runs holding the new records could not be read: the time ran out first.")
		return
	}
	elements := []string{}
	seen := map[string]bool{}
	for _, record := range order {
		if element := elementOf[record]; !seen[element] {
			seen[element] = true
			elements = append(elements, element)
		}
	}
	rows, err := p.selectByIDs("SysProcessElementData", []string{"SysProcess"}, elements, timeout, stopAt)
	if err != nil {
		provenance.warnings = append(provenance.warnings, "The runs holding the new records could not be read: "+fencedOr(err, "Exception"))
		return
	}
	runOf := map[string]string{}
	for _, row := range rows {
		element, run := strings.ToLower(rowText(row, "Id")), recordDeletionLookupValue(row["SysProcess"])
		if element == "" || run == "" {
			continue
		}
		if _, ok := runOf[element]; !ok {
			runOf[element] = run
		}
	}
	for record, element := range elementOf {
		if run, ok := runOf[strings.ToLower(element)]; ok {
			provenance.holdingRun[record] = run
		}
	}
}

// expandRuns: the runs and their sub-processes, since a Perform task inside a sub-process names its own run.
func (p *runRecordProvenance) expandRuns(runIDs []string, timeout time.Duration, stopAt time.Time, provenance *recordProvenance) map[string]bool {
	runs := map[string]bool{}
	ordered := []string{}
	for _, id := range runIDs {
		if parsed, ok := parseGUID(id); ok && parsed != emptyGUID && !runs[parsed] {
			runs[parsed] = true
			ordered = append(ordered, parsed)
		}
	}
	allHeld := true
	for _, run := range provenance.holdingRun {
		if !runs[run] {
			allHeld = false
		}
	}
	if len(runs) == 0 || len(provenance.holdingRun) == 0 || allHeld || timeIsUp(stopAt) {
		return runs
	}
	rows, err := p.c.esqSelect(p.ctx, esqQuery("SysProcessLog", map[string]any{"Id": esqColumn("Id")},
		esqGroup(0, map[string]any{"byRoot": inFilter("RootId", ordered, recordDeletionGUIDType)}), 100), dataWriteWatchBudget(timeout, stopAt))
	if err != nil {
		provenance.warnings = append(provenance.warnings, "The run's sub-processes could not be read, so a record a sub-process holds is not tied "+
			"to the run: "+fencedOr(err, "Exception"))
		return runs
	}
	for _, row := range rows {
		if id, ok := parseGUID(rowText(row, "Id")); ok {
			runs[id] = true
		}
	}
	return runs
}

func (p *runRecordProvenance) selectByIDs(entity string, columns, ids []string, timeout time.Duration, stopAt time.Time) ([]map[string]json.RawMessage, error) {
	selected := map[string]any{"Id": esqColumn("Id")}
	for _, column := range columns {
		selected[column] = esqColumn(column)
	}
	return p.c.esqSelect(p.ctx, esqQuery(entity, selected, esqGroup(0, map[string]any{"ids": inFilter("Id", ids, recordDeletionGUIDType)}), len(ids)),
		dataWriteWatchBudget(timeout, stopAt))
}

// ---- CreatedRecordCleaner ----

type createdRecordCleaner struct {
	service *recordDeletionService
}

const cleanerNotAttemptedCancelled = "not attempted: the call was cancelled"

// delete is CreatedRecordCleaner.Delete: children first, newest first within an entity, refused records retried
// while a pass still deletes something.
func (cl *createdRecordCleaner) delete(records []CreatedRecord, references map[string][]string, timeout time.Duration, stopAt time.Time,
	cancelled func() bool) *CleanupReport {
	report := newCleanupReport()
	pending := cleanerOrder(records, references)
	lastError := map[int]string{}
	indexes := make([]int, len(pending))
	for i := range pending {
		indexes[i] = i
	}
	for pass := 0; pass < 3 && len(indexes) > 0; pass++ {
		refused := []int{}
		for _, index := range indexes {
			record := pending[index]
			var message string
			if cancelled != nil && cancelled() {
				if previous, ok := lastError[index]; ok {
					message = previous
				} else {
					message = cleanerNotAttemptedCancelled
				}
			} else {
				message = cl.deleteBeforeStop(record, timeout, stopAt)
			}
			if message == "" {
				report.Deleted = append(report.Deleted, record)
			} else {
				lastError[index] = message
				refused = append(refused, index)
			}
		}
		progressed := len(refused) < len(indexes)
		indexes = refused
		if !progressed {
			break
		}
	}
	for _, index := range indexes {
		record := pending[index]
		report.Failed = append(report.Failed, CleanupFailure{Entity: record.Entity, ID: record.ID, Error: lastError[index]})
	}
	return report
}

func (cl *createdRecordCleaner) deleteBeforeStop(record CreatedRecord, timeout time.Duration, stopAt time.Time) string {
	if !stopAt.IsZero() {
		left := time.Until(stopAt)
		if left < time.Second {
			return "not attempted: the response deadline was reached"
		}
		timeout = min(timeout, left)
	}
	outcome := cl.service.delete(record.Entity, record.ID, timeout)
	if outcome.success {
		return ""
	}
	if outcome.serverDetail == "" {
		return outcome.err
	}
	return fmt.Sprintf("%s The server's own reason: %s.", outcome.err, outcome.serverDetail)
}

// cleanerOrder is OrderForDeletion: an entity that references another entity of the set precedes it; within
// an entity, newest first (CreatedOn compared ordinally, as DataService wrote it).
func cleanerOrder(records []CreatedRecord, references map[string][]string) []CreatedRecord {
	entities := []string{}
	present := map[string]bool{}
	for _, record := range records {
		if key := strings.ToLower(record.Entity); !present[key] {
			present[key] = true
			entities = append(entities, record.Entity)
		}
	}
	parentsOf := map[string]map[string]bool{}
	for _, entity := range entities {
		parents := map[string]bool{}
		for _, parent := range references[strings.ToLower(entity)] {
			if present[strings.ToLower(parent)] && !strings.EqualFold(parent, entity) {
				parents[strings.ToLower(parent)] = true
			}
		}
		parentsOf[strings.ToLower(entity)] = parents
	}
	ordered := []string{}
	placed := map[string]bool{}
	for len(placed) < len(entities) {
		next := ""
		for _, entity := range entities {
			if placed[strings.ToLower(entity)] {
				continue
			}
			referenced := false
			for _, other := range entities {
				if !placed[strings.ToLower(other)] && !strings.EqualFold(other, entity) && parentsOf[strings.ToLower(other)][strings.ToLower(entity)] {
					referenced = true
					break
				}
			}
			if !referenced {
				next = entity
				break
			}
		}
		if next == "" {
			for _, entity := range entities {
				if !placed[strings.ToLower(entity)] {
					next = entity
					break
				}
			}
		}
		ordered = append(ordered, next)
		placed[strings.ToLower(next)] = true
	}
	result := []CreatedRecord{}
	for _, entity := range ordered {
		group := []CreatedRecord{}
		for _, record := range records {
			if strings.EqualFold(record.Entity, entity) {
				group = append(group, record)
			}
		}
		sort.SliceStable(group, func(i, j int) bool { return group[i].createdOn > group[j].createdOn })
		result = append(result, group...)
	}
	return result
}
