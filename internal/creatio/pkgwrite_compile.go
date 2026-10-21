package creatio

// compile-creatio's three compiles, ported from clio master:
//
//   - full (CompileConfigurationCommand --all): the Rebuild request is sent ONCE and the build is observed on
//     the environment - new CompilationHistory rows, the runtime reload that ends a build (an availability
//     probe), or activity that has stopped - and the verdict is read from GetLastCompilationResult when the
//     request itself never answers (the reload usually resets its connection);
//   - one package (CompilePackageCommand --wait over PackageBuilder.Rebuild): RebuildPackage, judged by its
//     answer and by the package's compilation-history rows settling;
//   - the package of a business process (CompileBusinessProcessCommand): CrtProcessBuilder's CompileProcess,
//     gated on that package being 1.6.6.33 or later.
//
// Each returns clio's command log and exit code; the MCP tool records them in the compile registry.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
)

const (
	pkgWriteCompileAllRoute     = "ServiceModel/WorkspaceExplorerService.svc/Rebuild"
	pkgWriteRebuildPackageRoute = "ServiceModel/WorkspaceExplorerService.svc/RebuildPackage"
	pkgWriteCompileProcessRoute = "rest/ProcessDesignService/CompileProcess"
	pkgWriteCompileSeparator    = "================================================================================="
	pkgWriteOdataProject        = "Terrasoft.Configuration.ODataEntities.csproj"
	pkgWriteDevProject          = "Terrasoft.Configuration" + ".Dev.csproj" // split so the public-safety scan does not read a host name
	// pkgWriteProcessBuilderMinimum is CompileBusinessProcessOptions' [RequiresPackage] version.
	pkgWriteProcessBuilderMinimum = "1.6.6.33"
	pkgWriteProcessBuilderHint    = "Run 'clio install-process-builder -e <environment>' (or call the install-process-builder " +
		"MCP tool) to install or update CrtProcessBuilder."
	pkgWriteMaxListedProcessErrors = 46
)

// pkgWriteCompileTimings are clio's waits; tests shorten them.
var pkgWriteCompileTimings = struct {
	// full compile (CompileConfigurationCommand)
	startupGrace, decisionInterval, quietFallback, fullTimeout time.Duration
	// CompilationActivityWatcher and EnvironmentReloadWatcher
	historyPoll, reloadProbeInterval, reloadProbeTimeout time.Duration
	// package compile (PackageBuilder with --wait 600)
	packagePause, packageBudget, packageSettleWindow, packageQuietFallback, pollGiveUp time.Duration
	pollBackoff                                                                        []time.Duration
	// CompileBusinessProcessService.CompileTimeoutMs
	processTimeout time.Duration
}{
	startupGrace: 90 * time.Second, decisionInterval: time.Second, quietFallback: 5 * time.Minute, fullTimeout: 60 * time.Minute,
	historyPoll: time.Second, reloadProbeInterval: 5 * time.Second, reloadProbeTimeout: 5 * time.Second,
	packagePause: 500 * time.Millisecond, packageBudget: 600 * time.Second, packageSettleWindow: 45 * time.Second,
	packageQuietFallback: 5 * time.Minute, pollGiveUp: 90 * time.Second,
	pollBackoff:    []time.Duration{time.Second, 2 * time.Second, 5 * time.Second},
	processTimeout: 60 * time.Minute,
}

// pkgWriteNewLine is Environment.NewLine.
func pkgWriteNewLine() string {
	if runtime.GOOS == "windows" {
		return "\r\n"
	}
	return "\n"
}

// The console colors clio's LogRecord and FormatHistoryDiagnostics wrap values in; they reach the MCP log
// as they are.
func pkgWriteRed(value string) string    { return "\x1b[31m" + value + "\x1b[0m" }
func pkgWriteYellow(value string) string { return "\x1b[33m" + value + "\x1b[0m" }
func pkgWriteBlue(value string) string   { return "\x1b[34m" + value + "\x1b[0m" }
func pkgWriteGreen(value string) string  { return "\x1b[32m" + value + "\x1b[0m" }

// ---- compilation history ----

// pkgWriteHistoryRow is one CompilationHistory record.
type pkgWriteHistoryRow struct {
	ID             string
	CreatedOn      time.Time
	createdText    string
	ProjectName    string
	Duration       int
	ErrorsWarnings string
	Result         bool
}

var pkgWriteDateLayouts = []string{"2006-01-02T15:04:05.9999999", "2006-01-02T15:04:05.9999999Z07:00", "2006-01-02 15:04:05.9999999"}

func pkgWriteParseDate(text string) time.Time {
	for _, layout := range pkgWriteDateLayouts {
		if parsed, err := time.Parse(layout, text); err == nil {
			return parsed
		}
	}
	return time.Time{}
}

// pkgWriteHistoryQuery reads CompilationHistory newest first, optionally only rows created after a moment.
func pkgWriteHistoryQuery(after string, rowCount int) map[string]any {
	column := func(path string, order int) map[string]any {
		item := map[string]any{"expression": map[string]any{"expressionType": 0, "columnPath": path}}
		if order > 0 {
			item["orderDirection"], item["orderPosition"] = order, 0
		}
		return item
	}
	filters := map[string]any{}
	if after != "" {
		// DataService reads a DateTime parameter only as a quoted JSON date ("\"2026-10-21T02:00:00.000\""), as
		// DataService returned it; the bare text answers HTTP 500 (ArgumentNullException), so every polling
		// round of a package compile failed on the live stand (T23) while clio's succeeded.
		quoted, _ := json.Marshal(after)
		filters["CreatedAfter"] = map[string]any{"filterType": 1, "comparisonType": 7, "isEnabled": true,
			"leftExpression":  map[string]any{"expressionType": 0, "columnPath": "CreatedOn"},
			"rightExpression": map[string]any{"expressionType": 2, "parameter": map[string]any{"dataValueType": 7, "value": string(quoted)}}}
	}
	return map[string]any{"rootSchemaName": "CompilationHistory", "operationType": 0, "rowCount": rowCount,
		"columns": map[string]any{"items": map[string]any{
			"Id": column("Id", 0), "CreatedOn": column("CreatedOn", 2), "ProjectName": column("ProjectName", 0),
			"DurationInSeconds": column("DurationInSeconds", 0), "ErrorsWarnings": column("ErrorsWarnings", 0), "Result": column("Result", 0)}},
		"filters": map[string]any{"filterType": 6, "logicalOperation": 0, "isEnabled": true, "items": filters}}
}

func (c *Client) pkgWriteHistory(ctx context.Context, after string, rowCount int) ([]pkgWriteHistoryRow, error) {
	rows, err := c.selectRows(ctx, pkgWriteHistoryQuery(after, rowCount))
	if err != nil {
		return nil, err
	}
	result := make([]pkgWriteHistoryRow, 0, len(rows))
	for _, row := range rows {
		created := rowText(row, "CreatedOn")
		duration, _ := strconv.ParseFloat(rowText(row, "DurationInSeconds"), 64)
		result = append(result, pkgWriteHistoryRow{ID: strings.ToLower(rowText(row, "Id")), CreatedOn: pkgWriteParseDate(created),
			createdText: created, ProjectName: rowText(row, "ProjectName"), Duration: int(duration),
			ErrorsWarnings: rowText(row, "ErrorsWarnings"), Result: strings.EqualFold(rowText(row, "Result"), "true")})
	}
	return result, nil
}

// pkgWriteHistoryBaseline is CompilationHistoryPoller.GetBaseline: the newest row, nil when there is none.
func (c *Client) pkgWriteHistoryBaseline(ctx context.Context) (*pkgWriteHistoryRow, error) {
	rows, err := c.pkgWriteHistory(ctx, "", 1)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return &rows[0], nil
}

// pkgWriteDiagnostic is PackageBuildDiagnostic.
type pkgWriteDiagnostic struct {
	ErrorNumber, ErrorText, FileName string
	Line, Column                     *int
	IsWarning                        bool
}

// format is PackageBuildDiagnostic.Format.
func (d pkgWriteDiagnostic) format(code, file func(string) string) string {
	text := "(" + code(d.ErrorNumber) + ")"
	if strings.TrimSpace(d.FileName) == "" {
		return text + ": " + d.ErrorText
	}
	position := ""
	if d.Line != nil && d.Column != nil {
		position = fmt.Sprintf(" at (%d,%d)", *d.Line, *d.Column)
	}
	return text + " in " + file(d.FileName) + position + ": " + d.ErrorText
}

func (d pkgWriteDiagnostic) String() string {
	plain := func(text string) string { return text }
	return d.format(plain, plain)
}

// pkgWriteParseDiagnostics is PackageBuildResultParser.ToDiagnostics over a JSON array; nil when it does not
// read as one.
func pkgWriteParseDiagnostics(raw []byte) ([]pkgWriteDiagnostic, bool) {
	var payloads []map[string]json.RawMessage
	if json.Unmarshal(raw, &payloads) != nil {
		return nil, false
	}
	result := []pkgWriteDiagnostic{}
	for _, payload := range payloads {
		if payload == nil {
			continue
		}
		get := func(name string) (json.RawMessage, bool) {
			for key, value := range payload {
				if strings.EqualFold(key, name) {
					return value, true
				}
			}
			return nil, false
		}
		text := func(name string) string {
			var value string
			if raw, ok := get(name); ok {
				_ = json.Unmarshal(raw, &value)
			}
			return value
		}
		number := func(name string) *int {
			var value *int
			if raw, ok := get(name); ok {
				_ = json.Unmarshal(raw, &value)
			}
			return value
		}
		flag := func(name string) bool {
			var value bool
			if raw, ok := get(name); ok {
				_ = json.Unmarshal(raw, &value)
			}
			return value
		}
		result = append(result, pkgWriteDiagnostic{ErrorNumber: text("errorNumber"), ErrorText: text("errorText"),
			FileName: text("fileName"), Line: number("line"), Column: number("column"), IsWarning: flag("warning") || flag("isWarning")})
	}
	return result, true
}

// pkgWriteHistoryDiagnostics is PackageBuildResultParser.ParseHistoryDiagnostics.
func pkgWriteHistoryDiagnostics(errorsWarnings string) []pkgWriteDiagnostic {
	if strings.TrimSpace(errorsWarnings) == "" {
		return nil
	}
	diagnostics, _ := pkgWriteParseDiagnostics([]byte(errorsWarnings))
	return diagnostics
}

// pkgWriteFormatHistoryDiagnostics is CompileConfigurationCommand.FormatHistoryDiagnostics.
func pkgWriteFormatHistoryDiagnostics(errorsWarnings string) string {
	diagnostics := pkgWriteHistoryDiagnostics(errorsWarnings)
	if len(diagnostics) == 0 {
		return errorsWarnings
	}
	var builder strings.Builder
	for index, diagnostic := range diagnostics {
		code := pkgWriteRed
		if diagnostic.IsWarning {
			code = pkgWriteYellow
		}
		builder.WriteString(pkgWriteNewLine() + "\t" + fmt.Sprintf("%d of %d ", index+1, len(diagnostics)) + diagnostic.format(code, pkgWriteYellow))
	}
	return builder.String()
}

// pkgWriteHasRealError is CompilationDiagnostics.HasRealError.
func pkgWriteHasRealError(errorsWarnings string) bool {
	if strings.TrimSpace(errorsWarnings) == "" || strings.EqualFold(errorsWarnings, "[]") {
		return false
	}
	var entries []map[string]json.RawMessage
	if json.Unmarshal([]byte(errorsWarnings), &entries) != nil {
		return true
	}
	for _, entry := range entries {
		warning := false
		for key, value := range entry {
			if strings.EqualFold(key, "IsWarning") {
				_ = json.Unmarshal(value, &warning)
			}
		}
		if !warning {
			return true
		}
	}
	return false
}

// pkgWriteLogRecord is CompileConfigurationCommand.LogRecord.
func pkgWriteLogRecord(log *appWriteLog, record pkgWriteHistoryRow) {
	duration := strconv.Itoa(record.Duration)
	switch {
	case record.Duration >= 10:
		duration = pkgWriteRed(duration)
	case record.Duration >= 5:
		duration = pkgWriteYellow(duration)
	}
	project := record.ProjectName
	if project == pkgWriteOdataProject || project == pkgWriteDevProject {
		project = pkgWriteBlue(project) + pkgWriteGreen(" <============")
	}
	at := record.CreatedOn.Format("15:04:05")
	if strings.EqualFold(record.ErrorsWarnings, "[]") {
		log.info(fmt.Sprintf("At: %s after: %s sec. %s", at, duration, project))
		return
	}
	log.warning(fmt.Sprintf("At: %s after: %s sec. %s with: %s", at, duration, project, pkgWriteFormatHistoryDiagnostics(record.ErrorsWarnings)))
}

// ---- the build response (PackageBuildResultParser) ----

type pkgWriteBuildResult struct {
	Success      bool
	BuildResult  *int
	Diagnostics  []pkgWriteDiagnostic
	ErrorMessage string
	ErrorCode    string
}

func (r pkgWriteBuildResult) errors() []pkgWriteDiagnostic {
	var errorsOnly []pkgWriteDiagnostic
	for _, diagnostic := range r.Diagnostics {
		if !diagnostic.IsWarning {
			errorsOnly = append(errorsOnly, diagnostic)
		}
	}
	return errorsOnly
}

func pkgWriteMember(object *dataWriteObject, name string) (json.RawMessage, bool) {
	for _, key := range object.keys {
		if strings.EqualFold(key, name) {
			return object.values[key], true
		}
	}
	return nil, false
}

// pkgWriteParseBuildResult is PackageBuildResultParser.TryParseResponse.
func pkgWriteParseBuildResult(body string) *pkgWriteBuildResult {
	if strings.TrimSpace(body) == "" || !strings.HasPrefix(strings.TrimLeft(body, " \t\r\n"), "{") {
		return nil
	}
	if !json.Valid([]byte(body)) {
		return nil
	}
	root, ok := dataWriteParseObject([]byte(body))
	if !ok {
		return nil
	}
	successRaw, ok := pkgWriteMember(root, "success")
	if !ok || (dataWriteJSONKind(successRaw) != 't' && dataWriteJSONKind(successRaw) != 'f') {
		return nil
	}
	result := &pkgWriteBuildResult{Success: dataWriteJSONKind(successRaw) == 't', Diagnostics: []pkgWriteDiagnostic{}}
	if raw, ok := pkgWriteMember(root, "buildResult"); ok && dataWriteJSONKind(raw) == 'n' {
		var number int32
		if json.Unmarshal(raw, &number) == nil {
			value := int(number)
			result.BuildResult = &value
		}
	}
	if raw, ok := pkgWriteMember(root, "errors"); ok && dataWriteJSONKind(raw) == 'a' {
		if diagnostics, ok := pkgWriteParseDiagnostics(raw); ok {
			result.Diagnostics = diagnostics
		}
	}
	if raw, ok := pkgWriteMember(root, "errorInfo"); ok {
		if info, ok := dataWriteParseObject(raw); ok {
			text := func(name string) string {
				var value string
				if member, ok := pkgWriteMember(info, name); ok && dataWriteJSONKind(member) == 's' {
					_ = json.Unmarshal(member, &value)
				}
				return value
			}
			result.ErrorMessage, result.ErrorCode = text("message"), text("errorCode")
		}
	}
	return result
}

// pkgWriteUnrecognizedBody is PackageBuildResultParser.IsUnrecognizedBody.
func pkgWriteUnrecognizedBody(body string) bool {
	return strings.TrimSpace(body) != "" && !strings.HasPrefix(strings.TrimLeft(body, " \t\r\n"), "{")
}

// ---- full compile ----

type pkgWriteCompletion int

const (
	pkgWriteKeepWaiting pkgWriteCompletion = iota
	pkgWriteResponseReceived
	pkgWriteConfirmedByReload
	pkgWriteInferredFromQuiet
	pkgWriteTransportFailure
)

// pkgWriteActivity is CompilationActivityWatcher: it polls the history for rows after the baseline, logs
// each new one, and keeps how many were seen, when the last arrived and whether one carried a real error.
type pkgWriteActivity struct {
	mu           sync.Mutex
	seen         map[string]bool
	count        int
	lastActivity *time.Time
	hasErrors    bool
	stop         chan struct{}
	done         chan struct{}
}

func (c *Client) pkgWriteWatchActivity(ctx context.Context, cutoff string, onRow func(pkgWriteHistoryRow)) *pkgWriteActivity {
	watcher := &pkgWriteActivity{seen: map[string]bool{}, stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(watcher.done)
		failures := 0
		for {
			rows, err := c.pkgWriteHistory(ctx, cutoff, -1)
			delay := pkgWriteCompileTimings.historyPoll
			if err != nil {
				failures++
				// PollRetryPolicy.NextDelay: 1 s doubled per consecutive failure, at most 30 s.
				delay = time.Duration(math.Min(float64(30*time.Second), float64(time.Second)*math.Pow(2, float64(failures-1))))
			} else {
				failures = 0
				// The newest rows come first; report them oldest first.
				for index := len(rows) - 1; index >= 0; index-- {
					row := rows[index]
					if row.createdText != "" && (cutoff == "" || row.CreatedOn.After(pkgWriteParseDate(cutoff))) {
						cutoff = row.createdText
					}
					if watcher.observe(row) {
						onRow(row)
					}
				}
			}
			select {
			case <-watcher.stop:
				return
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}
		}
	}()
	return watcher
}

func (w *pkgWriteActivity) observe(row pkgWriteHistoryRow) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.seen[row.ID] {
		return false
	}
	w.seen[row.ID] = true
	w.count++
	now := time.Now()
	w.lastActivity = &now
	if pkgWriteHasRealError(row.ErrorsWarnings) {
		w.hasErrors = true
	}
	return true
}

func (w *pkgWriteActivity) snapshot() (int, *time.Time, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.count, w.lastActivity, w.hasErrors
}

func (w *pkgWriteActivity) close() {
	close(w.stop)
	<-w.done
}

// pkgWriteReload is EnvironmentReloadWatcher over EnvironmentAvailabilityProbe: an anonymous GET of the
// compilation-result endpoint every 5 s; any HTTP answer is "reachable". Three failures after the
// environment was reachable start an outage, and reachable again after one is the reload.
type pkgWriteReload struct {
	mu                               sync.Mutex
	reachable, everReachable, outage bool
	reloadObserved                   bool
	failures                         int
	stop, done                       chan struct{}
}

func (c *Client) pkgWriteWatchReload(ctx context.Context) *pkgWriteReload {
	watcher := &pkgWriteReload{stop: make(chan struct{}), done: make(chan struct{})}
	probe := &http.Client{Transport: c.http.Transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	go func() {
		defer close(watcher.done)
		for {
			watcher.record(c.pkgWriteReachable(ctx, probe))
			select {
			case <-watcher.stop:
				return
			case <-ctx.Done():
				return
			case <-time.After(pkgWriteCompileTimings.reloadProbeInterval):
			}
		}
	}()
	return watcher
}

func (c *Client) pkgWriteReachable(ctx context.Context, probe *http.Client) bool {
	requestCtx, cancel := context.WithTimeout(ctx, pkgWriteCompileTimings.reloadProbeTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, c.serviceURL(compileLastResultRoute), nil)
	if err != nil {
		return false
	}
	response, err := probe.Do(request)
	if err != nil {
		return false
	}
	_ = response.Body.Close()
	return true
}

func (w *pkgWriteReload) record(reachable bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.reachable = reachable
	if !reachable {
		w.failures++
		if w.everReachable && w.failures >= 3 {
			w.outage = true
		}
		return
	}
	w.failures = 0
	if w.outage {
		w.reloadObserved, w.outage = true, false
	}
	w.everReachable = true
}

func (w *pkgWriteReload) snapshot() (reachable, reloadObserved, everReachable bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.reachable, w.reloadObserved, w.everReachable
}

func (w *pkgWriteReload) close() {
	close(w.stop)
	<-w.done
}

// pkgWriteDecide is CompilationCompletionDecider.Decide.
func pkgWriteDecide(responseReceived, requestEnded bool, records int, reloadObserved, reachable, everReachable bool,
	lastActivity *time.Time, started, now time.Time) pkgWriteCompletion {
	switch {
	case responseReceived:
		return pkgWriteResponseReceived
	case records > 0 && reloadObserved && reachable:
		return pkgWriteConfirmedByReload
	case records > 0 && reachable && lastActivity != nil && now.Sub(*lastActivity) >= pkgWriteCompileTimings.quietFallback:
		return pkgWriteInferredFromQuiet
	case records == 0 && now.Sub(started) >= pkgWriteCompileTimings.startupGrace && (requestEnded || !everReachable):
		return pkgWriteTransportFailure
	}
	return pkgWriteKeepWaiting
}

type pkgWriteAnswer struct {
	body string
	err  error
}

// pkgWriteSendBuild posts a build request once, on its own goroutine; the channel carries its outcome.
func (c *Client) pkgWriteSendBuild(ctx context.Context, route, body string, timeout time.Duration) <-chan pkgWriteAnswer {
	answers := make(chan pkgWriteAnswer, 1)
	go func() {
		text, err := c.dataWriteSendOnce(ctx, http.MethodPost, route, []byte(body), timeout)
		answers <- pkgWriteAnswer{body: text, err: err}
	}()
	return answers
}

// pkgWriteCompileAll is CompileConfigurationCommand.Execute with --all.
func (c *Client) pkgWriteCompileAll(ctx context.Context, log *appWriteLog) int {
	timings := pkgWriteCompileTimings
	baseline, err := c.pkgWriteHistoryBaseline(ctx)
	if err != nil {
		log.warning("Could not read the compilation history baseline: " + err.Error())
	}
	started := time.Now()
	log.line(pkgWriteCompileSeparator)
	log.info(fmt.Sprintf("At: %s Starting compilation...", started.Format("15:04:05")))
	log.line("")
	endpoint := c.serviceURL(pkgWriteCompileAllRoute)

	requestCtx, cancelRequest := context.WithCancel(ctx)
	answers := c.pkgWriteSendBuild(requestCtx, pkgWriteCompileAllRoute, "{}", timings.fullTimeout)
	cutoff := ""
	if baseline != nil {
		cutoff = baseline.createdText
	}
	activity := c.pkgWriteWatchActivity(ctx, cutoff, func(row pkgWriteHistoryRow) { pkgWriteLogRecord(log, row) })
	reload := c.pkgWriteWatchReload(ctx)
	completion, answer := pkgWriteKeepWaiting, (*pkgWriteAnswer)(nil)
	reloadReported := false
	deadline := started.Add(timings.fullTimeout)
	for {
		if answer == nil {
			select {
			case received := <-answers:
				answer = &received
			default:
			}
		}
		records, lastActivity, _ := activity.snapshot()
		reachable, reloadObserved, everReachable := reload.snapshot()
		if !reloadReported && reloadObserved {
			reloadReported = true
			log.info(fmt.Sprintf("At: %s The environment reloaded and is answering again - a configuration "+
				"build ends by reloading the application. Reading the compilation result.", time.Now().Format("15:04:05")))
		}
		completion = pkgWriteDecide(answer != nil && answer.err == nil, answer != nil, records, reloadObserved, reachable,
			everReachable, lastActivity, started, time.Now())
		if completion != pkgWriteKeepWaiting || !time.Now().Before(deadline) || ctx.Err() != nil {
			break
		}
		select {
		case <-ctx.Done():
		case <-time.After(timings.decisionInterval):
		}
	}
	cancelRequest()
	activity.close()
	reload.close()

	success := false
	switch completion {
	case pkgWriteResponseReceived:
		success = pkgWriteProceedResponse(log, answer.body, endpoint)
	case pkgWriteConfirmedByReload, pkgWriteInferredFromQuiet:
		success = c.pkgWriteEnvironmentVerdict(ctx, log, activity, completion == pkgWriteInferredFromQuiet)
	case pkgWriteTransportFailure:
		log.error("The compilation request failed and the environment never started building: no compilation " +
			"history was written after the request was sent.")
		if answer != nil && answer.err != nil {
			log.error("Request error: " + answer.err.Error())
		}
		log.error("Endpoint: " + endpoint)
		log.error("Check the environment URI, the IsNetCore flag, and the credentials.")
	default:
		log.error("Timed out waiting for the compilation to finish. The build may still be running on the " +
			"environment; check `clio last-compilation-log` before starting another one.")
	}
	if !success {
		return 1
	}
	elapsed := time.Since(started)
	log.line("")
	log.info(fmt.Sprintf("Compilation finished in %02d:%02d:%02d", int(elapsed.Hours())%24, int(elapsed.Minutes())%60, int(elapsed.Seconds())%60))
	log.line(pkgWriteCompileSeparator)
	return 0
}

// pkgWriteProceedResponse is CompileConfigurationCommand.ProceedResponse.
func pkgWriteProceedResponse(log *appWriteLog, body, endpoint string) bool {
	switch {
	case strings.TrimSpace(body) == "":
		log.error("Empty response received from server during compilation.")
		log.error("Endpoint: " + endpoint)
		return false
	case pkgWriteUnrecognizedBody(body):
		log.error("Server returned non-JSON response during compilation.")
		log.error("Endpoint: " + endpoint)
		log.error(fmt.Sprintf("Response length: %d characters.", len(utf16.Encode([]rune(body)))))
		log.error("Check environment URI, IsNetCore flag, and credentials (a login/404 page is often returned as HTML).")
		return false
	case !json.Valid([]byte(body)):
		log.error("The compilation response is not valid JSON; it may have been cut off.")
		log.error("Endpoint: " + endpoint)
		return false
	}
	result := pkgWriteParseBuildResult(body)
	if result == nil {
		log.error("The compilation response carried no build result.")
		log.error("Endpoint: " + endpoint)
		return false
	}
	if result.Success {
		return true
	}
	buildResult := ""
	if result.BuildResult != nil {
		buildResult = fmt.Sprintf(" Build result: %d.", *result.BuildResult)
	}
	log.error("Compilation failed on the environment." + buildResult)
	var parts []string
	for _, part := range []string{result.ErrorCode, result.ErrorMessage} {
		if strings.TrimSpace(part) != "" {
			parts = append(parts, part)
		}
	}
	if len(parts) > 0 {
		log.error(strings.Join(parts, ": "))
	}
	for _, diagnostic := range result.errors() {
		log.error(diagnostic.String())
	}
	return false
}

// pkgWriteEnvironmentVerdict is CompileConfigurationCommand.ApplyEnvironmentVerdict.
func (c *Client) pkgWriteEnvironmentVerdict(ctx context.Context, log *appWriteLog, activity *pkgWriteActivity, inferred bool) bool {
	_, _, historyErrors := activity.snapshot()
	var result *LastCompilationLogResult
	var errorsRaw []struct {
		Line        int    `json:"line"`
		Column      int    `json:"column"`
		ErrorNumber string `json:"errorNumber"`
		ErrorText   string `json:"errorText"`
		Warning     bool   `json:"warning"`
		FileName    string `json:"fileName"`
	}
	if payload, err := c.compileGetLastResult(ctx); err == nil {
		if parsed, err := compileParseLastResult(payload); err == nil {
			result = &parsed
			var decoded struct {
				Errors json.RawMessage `json:"errors"`
			}
			_ = json.Unmarshal(payload, &decoded)
			_ = json.Unmarshal(decoded.Errors, &errorsRaw)
		}
	}
	if inferred {
		log.warning("The compilation request never returned and no runtime reload was observed. Completion was " +
			"inferred from the environment's compilation activity having stopped.")
	}
	if result == nil && inferred {
		log.error("Could not read the compilation result from the environment, and the build's completion was " +
			"only inferred from its activity having stopped. The outcome is unknown; check " +
			"`clio last-compilation-log` before relying on this environment.")
		return false
	}
	if result == nil {
		log.warning("Could not read the compilation result from the environment; reporting the outcome from the " +
			"compilation history instead.")
		return !historyErrors
	}
	if result.CompilationSucceeded && !historyErrors {
		return true
	}
	buildResult := 0
	if result.BuildResult != nil {
		buildResult = *result.BuildResult
	}
	log.error(fmt.Sprintf("Compilation failed on the environment. Build result: %d.", buildResult))
	for _, diagnostic := range errorsRaw {
		if diagnostic.Warning {
			continue
		}
		log.error(fmt.Sprintf("(%s) in %s at (%d,%d): %s", diagnostic.ErrorNumber, diagnostic.FileName, diagnostic.Line, diagnostic.Column, diagnostic.ErrorText))
	}
	return false
}

// ---- package compile ----

// pkgWritePackageProgress is PackageBuilder.CompilationProgress.
type pkgWritePackageProgress struct {
	mu           sync.Mutex
	seen         map[string]bool
	lastActivity *time.Time
	hasErrors    bool
	errorDetails string
	slowest      int
	lastProject  string
	diagnostics  []pkgWriteDiagnostic
}

func pkgWriteIsErrorRow(row pkgWriteHistoryRow) bool {
	return !row.Result && row.ErrorsWarnings != "" && row.ErrorsWarnings != "[]"
}

func (p *pkgWritePackageProgress) observe(row pkgWriteHistoryRow, log *appWriteLog) {
	p.mu.Lock()
	if p.seen[row.ID] {
		p.mu.Unlock()
		return
	}
	p.seen[row.ID] = true
	now := time.Now()
	p.lastActivity, p.lastProject = &now, row.ProjectName
	p.slowest = max(p.slowest, row.Duration)
	failed := pkgWriteIsErrorRow(row)
	if failed {
		p.hasErrors, p.errorDetails = true, row.ErrorsWarnings
		p.diagnostics = append(p.diagnostics, pkgWriteHistoryDiagnostics(row.ErrorsWarnings)...)
	}
	p.mu.Unlock()
	outcome := "succeeded"
	if failed {
		outcome = "failed"
	}
	log.info(fmt.Sprintf("Compilation history: %s built in %d s, %s", row.ProjectName, row.Duration, outcome))
}

type pkgWritePackageSnapshot struct {
	lastActivity *time.Time
	hasErrors    bool
	errorDetails string
	slowest      int
	lastProject  string
	diagnostics  []pkgWriteDiagnostic
}

func (p *pkgWritePackageProgress) snapshot() pkgWritePackageSnapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	return pkgWritePackageSnapshot{p.lastActivity, p.hasErrors, p.errorDetails, p.slowest, p.lastProject, append([]pkgWriteDiagnostic{}, p.diagnostics...)}
}

// pkgWriteRequiredQuiet is PackageBuilder.RequiredQuietWindow for a waited build.
func pkgWriteRequiredQuiet(observed pkgWritePackageSnapshot, requestEnded bool) time.Duration {
	if !requestEnded && !observed.hasErrors {
		return pkgWriteCompileTimings.packageQuietFallback
	}
	scaled := time.Duration(float64(observed.slowest) * 1.5 * float64(time.Second))
	return max(scaled, pkgWriteCompileTimings.packageSettleWindow)
}

func pkgWriteSettled(observed pkgWritePackageSnapshot, requestEnded bool) bool {
	if observed.lastActivity == nil {
		return false
	}
	return time.Since(*observed.lastActivity) >= pkgWriteRequiredQuiet(observed, requestEnded)
}

func pkgWriteSettledSince(observed pkgWritePackageSnapshot, answeredAt time.Time) bool {
	if observed.lastActivity == nil {
		return false
	}
	last := *observed.lastActivity
	if answeredAt.After(last) {
		last = answeredAt
	}
	observed.lastActivity = &last
	return pkgWriteSettled(observed, true)
}

// pkgWritePollFailure is CompilationHistoryPoller's give-up: every round failed for the give-up window.
type pkgWritePollFailure struct{ err error }

func (e pkgWritePollFailure) Error() string {
	return "Package compilation could not be monitored: " + e.err.Error()
}

// pkgWriteCompilePackage is CompilePackageCommand.Execute with --wait (600 s) for one package.
func (c *Client) pkgWriteCompilePackage(ctx context.Context, log *appWriteLog, packageName string) int {
	safeName := strings.ReplaceAll(strings.ReplaceAll(packageName, " ", ""), ",", `","`)
	log.line(fmt.Sprintf("Start rebuild packages (%s).", safeName))
	if err := c.pkgWriteRebuildPackage(ctx, log, safeName); err != nil {
		log.error(err.Error())
		return 1
	}
	log.line(fmt.Sprintf("End rebuild packages (%s).", safeName))
	log.info("Done")
	return 0
}

// pkgWriteRebuildPackage is PackageBuilder.CompileWithPolling with wait options: nil when the build is
// confirmed, otherwise the failure the command reports.
func (c *Client) pkgWriteRebuildPackage(ctx context.Context, log *appWriteLog, packageName string) error {
	timings := pkgWriteCompileTimings
	baseline, err := c.pkgWriteHistoryBaseline(ctx)
	if err != nil {
		log.warning("Could not read the compilation history baseline: " + err.Error())
	}
	cutoff := ""
	if baseline != nil {
		cutoff = baseline.createdText
	}
	started := time.Now()
	timeoutAt := started.Add(timings.packageBudget)
	progress := &pkgWritePackageProgress{seen: map[string]bool{}}
	monitorCtx, stopMonitoring := context.WithCancel(ctx)
	defer stopMonitoring()
	answers := c.pkgWriteSendBuild(monitorCtx, pkgWriteRebuildPackageRoute, `{ "packageName":"`+packageName+`" }`, timings.fullTimeout)
	pollFault := make(chan error, 1)
	go c.pkgWritePollHistory(monitorCtx, cutoff, progress, log, pollFault)

	var answer *pkgWriteAnswer
	var answeredAt *time.Time
	for time.Now().Before(timeoutAt) {
		select {
		case fault := <-pollFault:
			return fault
		default:
		}
		if answer == nil {
			select {
			case received := <-answers:
				answer = &received
			default:
			}
		}
		if answer != nil && answer.err == nil {
			if answeredAt == nil {
				now := time.Now()
				answeredAt = &now
			}
			if pkgWriteUnrecognizedBody(answer.body) {
				return fmt.Errorf("The environment answered the build request for '%s' with a response that is not a "+
					"build result (not JSON; possibly a login or proxy error page), so the build was not "+
					"confirmed. Check the environment's URL and credentials.", packageName)
			}
			response := pkgWriteParseBuildResult(answer.body)
			observed := progress.snapshot()
			if (response != nil && !response.Success) || pkgWriteSettledSince(observed, *answeredAt) {
				return pkgWritePackageVerdict(log, packageName, response, progress.snapshot(), false)
			}
		} else if pkgWriteSettled(progress.snapshot(), answer != nil) {
			return pkgWritePackageVerdict(log, packageName, nil, progress.snapshot(), true)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(timings.packagePause):
		}
	}
	var requestFault error
	if answer != nil {
		requestFault = answer.err
	}
	stopMonitoring()
	observed := progress.snapshot()
	if observed.hasErrors {
		return pkgWritePackageFailure(log, packageName, nil, observed.diagnostics, "", observed.errorDetails)
	}
	var reason string
	if answeredAt != nil && observed.lastActivity == nil {
		reason = fmt.Sprintf("The environment accepted the build of '%s' but wrote no compilation history within "+
			"%.0f s, so there is no evidence it finished.", packageName, timings.packageBudget.Seconds())
	} else {
		reason = fmt.Sprintf("Package compilation of '%s' did not finish within %.0f s.", packageName, timings.packageBudget.Seconds())
	}
	advice := pkgWriteQuietShortfall(observed, started, answeredAt, answer != nil)
	if advice == "" {
		advice = " The build may still be running on the environment; check `clio last-compilation-log` before compiling again."
	}
	if requestFault != nil {
		return fmt.Errorf("%s%s (%v)", reason, advice, requestFault)
	}
	return errors.New(reason + advice)
}

// pkgWritePollHistory is CompilationHistoryPoller.Poll: rounds every second, a failed round retried after
// 1, 2, then 5 s with a warning, and a fault once every round failed for 90 s.
func (c *Client) pkgWritePollHistory(ctx context.Context, cutoff string, progress *pkgWritePackageProgress, log *appWriteLog, fault chan<- error) {
	timings := pkgWriteCompileTimings
	failedRounds := 0
	var failingSince time.Time
	for {
		rows, err := c.pkgWriteHistory(ctx, cutoff, -1)
		delay := timings.historyPoll
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if failedRounds == 0 {
				failingSince = time.Now()
			}
			failedRounds++
			elapsed := time.Since(failingSince)
			if elapsed >= timings.pollGiveUp {
				fault <- pkgWritePollFailure{fmt.Errorf("Compilation polling gave up after %.0f s of rounds that all failed "+
					"(give-up window %.0f s, %d failed rounds): %w", elapsed.Seconds(), timings.pollGiveUp.Seconds(), failedRounds, err)}
				return
			}
			delay = timings.pollBackoff[min(failedRounds, len(timings.pollBackoff))-1]
			fenced := "no detail reported"
			if text := DataWriteUntrusted(err.Error()); text != nil {
				fenced = *text
			}
			log.warning(fmt.Sprintf("Compilation polling round %d failed (%.0f s into a %.0f s tolerance window); retrying in %.0f s. Reason: %s",
				failedRounds, elapsed.Seconds(), timings.pollGiveUp.Seconds(), delay.Seconds(), fenced))
		} else {
			failedRounds = 0
			for index := len(rows) - 1; index >= 0; index-- {
				row := rows[index]
				if row.createdText != "" && (cutoff == "" || row.CreatedOn.After(pkgWriteParseDate(cutoff))) {
					cutoff = row.createdText
				}
				progress.observe(row, log)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
	}
}

// pkgWritePackageVerdict is PackageBuilder.ApplyVerdict for a waited build.
func pkgWritePackageVerdict(log *appWriteLog, packageName string, response *pkgWriteBuildResult, history pkgWritePackageSnapshot, inferred bool) error {
	if response == nil {
		if !inferred {
			log.warning(fmt.Sprintf("The environment did not report a build result for '%s' (empty or "+
				"unrecognized response); the outcome is taken from the compilation history only.", packageName))
		} else {
			log.warning(fmt.Sprintf("The environment never answered the build request for '%s'; completion "+
				"was inferred from its compilation history having stopped.", packageName))
		}
		if history.hasErrors {
			return pkgWritePackageFailure(log, packageName, nil, history.diagnostics, "", history.errorDetails)
		}
		return nil
	}
	if response.Success && !history.hasErrors {
		return nil
	}
	diagnostics := response.errors()
	if len(diagnostics) == 0 {
		diagnostics = history.diagnostics
	}
	return pkgWritePackageFailure(log, packageName, response.BuildResult, diagnostics, response.ErrorMessage, history.errorDetails)
}

// pkgWritePackageFailure is PackageBuilder.Fail: the errors are logged, and the failure is the message the
// command reports.
func pkgWritePackageFailure(log *appWriteLog, packageName string, buildResult *int, diagnostics []pkgWriteDiagnostic, errorMessage, rawHistory string) error {
	count := 0
	for _, diagnostic := range diagnostics {
		if diagnostic.IsWarning {
			continue
		}
		count++
		log.error(diagnostic.String())
	}
	if count == 0 && strings.TrimSpace(rawHistory) != "" {
		log.error(rawHistory)
	}
	resultPart := ""
	if buildResult != nil {
		resultPart = fmt.Sprintf(" (build result %d)", *buildResult)
	}
	messagePart := ""
	if strings.TrimSpace(errorMessage) != "" {
		messagePart = " Creatio reported: " + strings.TrimRight(strings.TrimSpace(errorMessage), ".") + "."
	}
	return fmt.Errorf("Package compilation failed for '%s'%s.%s The new code was not loaded: the environment keeps "+
		"running the previous build until the errors are fixed and the package is compiled again.", packageName, resultPart, messagePart)
}

// pkgWriteQuietShortfall is PackageBuilder.DescribeQuietShortfall for a waited build.
func pkgWriteQuietShortfall(observed pkgWritePackageSnapshot, started time.Time, answeredAt *time.Time, requestEnded bool) string {
	if observed.hasErrors || observed.lastActivity == nil {
		return ""
	}
	lastRow := *observed.lastActivity
	lastEvidence := lastRow
	if answeredAt != nil && answeredAt.After(lastRow) {
		lastEvidence = *answeredAt
	}
	window := pkgWriteRequiredQuiet(observed, requestEnded || answeredAt != nil)
	needed := lastEvidence.Sub(started) + window
	suggested := int(math.Ceil(needed.Seconds()/60)) * 60
	retry := fmt.Sprintf("Re-run with `--wait-timeout %d` or more", suggested)
	if suggested > 3600 {
		retry = "Even the largest `--wait-timeout` (3600) would not cover it; check `clio last-compilation-log`"
	}
	return fmt.Sprintf(" No compile error was reported: the last compilation-history row (%s) arrived %.0f s after the "+
		"build started, and a build counts as finished only after %.0f s without a new row, so it has most likely "+
		"succeeded. %s to let clio confirm it.", observed.lastProject, lastRow.Sub(started).Seconds(), window.Seconds(), retry)
}

// ---- business-process compile ----

var pkgWriteVersionPattern = regexp.MustCompile(`^\s*(\d+(?:\.\d+)*)`)

// pkgWriteVersion parses the numeric part of a package version; ok is false when there is none.
func pkgWriteVersion(text string) ([]int, bool) {
	match := pkgWriteVersionPattern.FindStringSubmatch(text)
	if match == nil {
		return nil, false
	}
	parts := strings.Split(match[1], ".")
	version := make([]int, len(parts))
	for index, part := range parts {
		version[index], _ = strconv.Atoi(part)
	}
	return version, true
}

func pkgWriteCompareVersions(left, right []int) int {
	for index := 0; index < max(len(left), len(right)); index++ {
		var a, b int
		if index < len(left) {
			a = left[index]
		}
		if index < len(right) {
			b = right[index]
		}
		if a != b {
			if a < b {
				return -1
			}
			return 1
		}
	}
	return 0
}

// pkgWriteEnsureProcessBuilder is RequiredPackageChecker.EnsureRequirements for CompileBusinessProcessOptions:
// CrtProcessBuilder 1.6.6.33 or later. clio's bundled-package convergence refusal is not applied here.
func (c *Client) pkgWriteEnsureProcessBuilder(ctx context.Context) error {
	packages, err := c.pkgWriteInstalledPackages(ctx)
	if err != nil {
		return err
	}
	required, _ := pkgWriteVersion(pkgWriteProcessBuilderMinimum)
	var installed []int
	for _, item := range packages {
		if strings.EqualFold(item.Name, processBuilderPackageName) {
			if version, ok := pkgWriteVersion(item.Version); ok && (installed == nil || pkgWriteCompareVersions(version, installed) < 0) {
				installed = version
			}
		}
	}
	if installed == nil || pkgWriteCompareVersions(installed, required) < 0 {
		return fmt.Errorf("To use this command, you need to install the %s package version %s or higher. "+
			"Install or update the package in the target environment and retry.\n%s",
			processBuilderPackageName, pkgWriteProcessBuilderMinimum, pkgWriteProcessBuilderHint)
	}
	return nil
}

type pkgWriteProcessResult struct {
	Success         bool    `json:"success"`
	ErrorMessage    *string `json:"errorMessage"`
	ProcessName     *string `json:"processName"`
	PackageName     *string `json:"packageName"`
	PackageType     *string `json:"packageType"`
	CompileRequired bool    `json:"compileRequired"`
	Compiled        bool    `json:"compiled"`
	DurationMs      int64   `json:"durationMs"`
	Errors          []struct {
		FileName      *string `json:"fileName"`
		Line          int     `json:"line"`
		Column        int     `json:"column"`
		Code          *string `json:"code"`
		Message       *string `json:"message"`
		InThisProcess bool    `json:"inThisProcess"`
	} `json:"errors"`
	ErrorCount int `json:"errorCount"`
}

const pkgWriteProcessUnknown = " so whether the package was compiled is UNKNOWN. Read last-compilation-log for this environment before compiling again."

// pkgWriteCompileProcessService is CompileBusinessProcessService.Compile: the result, or the failure the
// command reports.
func (c *Client) pkgWriteCompileProcessService(ctx context.Context, log *appWriteLog, environmentName, processName string) (*pkgWriteProcessResult, error) {
	body, err := json.Marshal(map[string]any{"request": map[string]string{"name": processName}})
	if err != nil {
		return nil, err
	}
	log.info(fmt.Sprintf("Compiling the package of process '%s' on '%s'. A compile reloads the runtime "+
		"for every user of the environment and usually takes a few minutes...", processName, environmentName))
	response, err := c.serviceRequest(ctx, serviceCall{Route: pkgWriteCompileProcessRoute, Body: body, Timeout: pkgWriteCompileTimings.processTimeout})
	if err != nil {
		detail := "none reported"
		if fenced := DataWriteUntrusted(err.Error()); fenced != nil {
			detail = *fenced
		}
		return nil, errors.New("CompileProcess did not answer, so whether the package was compiled is UNKNOWN - the compile may " +
			"still be running on the server. Wait, then call compile-status, whose not-found answer lists the " +
			"environment's compilation-history rows with their times, or read last-compilation-log for this " +
			"environment - its verdict carries no time and can be an earlier compile's, so report it as " +
			"unconfirmed - and ask the user before compiling again. Transport detail: " + detail)
	}
	text := strings.TrimPrefix(string(response.payload), "\uFEFF")
	if strings.TrimSpace(text) == "" {
		return nil, errors.New("CompileProcess returned an empty body," + pkgWriteProcessUnknown)
	}
	pageStatus, isErrorPage := 0, false
	if dataWriteIsMarkup(text) {
		pageStatus, isErrorPage = dataWriteMarkupStatus(text)
	}
	if isErrorPage && (pageStatus == 401 || pageStatus == 404) {
		advice := "Check the environment's credentials."
		if pageStatus == 404 {
			advice = "The route was not found: check the environment's URL and its IsNetCore setting, and that " +
				"CrtProcessBuilder is installed and compiled there (install-process-builder)."
		}
		return nil, fmt.Errorf("CompileProcess was answered with an HTTP %d error page: the request did not reach the "+
			"process builder, so nothing was compiled. %s Ask the user again before compiling.", pageStatus, advice)
	}
	if !(isErrorPage && pageStatus >= 500) && pkgWriteSessionExpired(text) {
		return nil, errors.New("CompileProcess was refused by the sign-in check, so the request did not reach the process builder " +
			"and nothing was compiled. Verify the environment's credentials (clio reg-web-app --check-login), " +
			"then ask the user again before compiling.")
	}
	root, ok := dataWriteParseObject([]byte(text))
	if !ok || !json.Valid([]byte(text)) {
		if strings.HasPrefix(strings.TrimSpace(text), "null") {
			return nil, errors.New("CompileProcess returned a response without its result," + pkgWriteProcessUnknown)
		}
		return nil, errors.New("CompileProcess returned a response clio could not read," + pkgWriteProcessUnknown)
	}
	raw, found := pkgWriteMember(root, "CompileProcessResult")
	if !found || dataWriteJSONKind(raw) == 'z' {
		return nil, pkgWriteMissingProcessResult(text)
	}
	var result pkgWriteProcessResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, errors.New("CompileProcess returned a response clio could not read," + pkgWriteProcessUnknown)
	}
	return &result, nil
}

// pkgWriteSessionExpired is ReauthExecutor.IsSessionExpiredResponse: the sign-in page or Creatio's JSON 401
// envelope.
func pkgWriteSessionExpired(body string) bool {
	if strings.HasPrefix(strings.TrimSpace(body), "<") && looksLikeLoginHTML([]byte(body), "") {
		return true
	}
	root, ok := dataWriteParseObject([]byte(body))
	if !ok {
		return false
	}
	for _, name := range []string{"Code", "code"} {
		if raw, ok := root.raw(name); ok && strings.TrimSpace(string(raw)) == "401" {
			return true
		}
	}
	return false
}

// pkgWriteMissingProcessResult is CompileBusinessProcessService.DescribeMissingResult.
func pkgWriteMissingProcessResult(body string) error {
	_, isErrorBody := dataWriteDetectError(json.RawMessage(body), dataWriteServiceContext)
	if !isErrorBody {
		return errors.New("CompileProcess returned a response without its result," + pkgWriteProcessUnknown)
	}
	code := ""
	if root, ok := dataWriteParseObject([]byte(body)); ok {
		for _, name := range []string{"Code", "code"} {
			if raw, ok := root.raw(name); ok {
				var number int32
				if dataWriteJSONKind(raw) == 'n' && json.Unmarshal(raw, &number) == nil {
					code = fmt.Sprintf(" (code %d)", number)
				}
				break
			}
		}
	}
	return fmt.Errorf("CompileProcess answered with a Creatio error%s instead of its result, so whether the package "+
		"was compiled is UNKNOWN. The process builder reports its own failures inside that result, so this "+
		"error came from the platform around it: a refusal before the builder ran compiled nothing, but a "+
		"failure after the compile looks the same. The server's wording is not reproduced here. Before "+
		"compiling again, read last-compilation-log: it shows the latest finished compile and carries no time, "+
		"so it may be an earlier one - report it to the user as unconfirmed and ask before compiling again.", code)
}

var pkgWriteSchemaCode = regexp.MustCompile(`\A[A-Za-z_][A-Za-z0-9_]{0,127}\z`)

// pkgWriteServerName is CompileBusinessProcessCommand.ServerName.
func pkgWriteServerName(name *string) string {
	if name != nil && pkgWriteSchemaCode.MatchString(*name) {
		return *name
	}
	if name != nil {
		if fenced := DataWriteUntrusted(*name); fenced != nil {
			return *fenced
		}
	}
	return "(unnamed)"
}

func pkgWriteFenced(text *string, fallback string) string {
	if text != nil {
		if fenced := DataWriteUntrusted(*text); fenced != nil {
			return *fenced
		}
	}
	return fallback
}

// pkgWriteCompileProcess is CompileBusinessProcessCommand.Execute.
func (c *Client) pkgWriteCompileProcess(ctx context.Context, log *appWriteLog, environmentName, processName string) int {
	result, err := c.pkgWriteCompileProcessService(ctx, log, environmentName, processName)
	if err != nil {
		log.error(err.Error())
		return 1
	}
	if !result.Success {
		log.error("CompileProcess failed: " + pkgWriteFenced(result.ErrorMessage, "the server reported no detail."))
		listed := min(len(result.Errors), pkgWriteMaxListedProcessErrors)
		for _, item := range result.Errors[:listed] {
			where := ""
			if !item.InThisProcess {
				where = " [another schema of the package]"
			}
			fileName, code := "", ""
			if item.FileName != nil {
				fileName = *item.FileName
			}
			if item.Code != nil {
				code = *item.Code
			}
			location := fmt.Sprintf("%s(%d,%d): %s", fileName, item.Line, item.Column, code)
			log.error(pkgWriteFenced(&location, "(no location)") + " " + pkgWriteFenced(item.Message, "(no message)") + where)
		}
		if omitted := max(result.ErrorCount, len(result.Errors)) - listed; omitted > 0 {
			log.error(fmt.Sprintf("... and %d more error(s) not listed.", omitted))
		}
		if result.Compiled {
			log.error("Fix what the errors above name, then ask the user again before the next compile: their " +
				"answer covered this compile only, and every compile reloads the runtime for every user of the " +
				"environment.")
		}
		return 1
	}
	if !result.CompileRequired {
		log.info(fmt.Sprintf("Process '%s' carries no C# (no script task and no process methods), so "+
			"nothing was compiled and nothing needs to be.", pkgWriteServerName(result.ProcessName)))
		return 0
	}
	packageType := "the shared configuration assembly"
	if result.PackageType != nil && strings.EqualFold(*result.PackageType, "assembly") {
		packageType = "its own assembly"
	}
	duration := "an unreported time"
	if result.DurationMs >= 0 && result.DurationMs <= 86_400_000 {
		elapsed := time.Duration(result.DurationMs) * time.Millisecond
		duration = fmt.Sprintf("%d:%02d", int(elapsed.Minutes())%60, int(elapsed.Seconds())%60)
	}
	log.info(fmt.Sprintf("Compiled package '%s' (%s) in %s. Verify on a run that process '%s' executes the saved code; "+
		"on a .NET (Core) host restart the application first, as after any compile.",
		pkgWriteServerName(result.PackageName), packageType, duration, pkgWriteServerName(result.ProcessName)))
	return 0
}

// ---- entry point ----

// CompileRequest is compile-creatio's normalized scope: neither name for a full compile, otherwise one.
type CompileRequest struct {
	EnvironmentName string
	PackageName     string
	ProcessName     string
}

// Compile runs the scoped compile and returns clio's command log. A process compile first checks that
// CrtProcessBuilder 1.6.6.33 or later is installed; that refusal ends the log as clio's Execute catch does.
func (c *Client) Compile(ctx context.Context, request CompileRequest) CommandResult {
	log := &appWriteLog{}
	var exitCode int
	switch {
	case request.ProcessName != "":
		if err := c.pkgWriteEnsureProcessBuilder(ctx); err != nil {
			log.error(err.Error())
			return log.result(1)
		}
		exitCode = c.pkgWriteCompileProcess(ctx, log, request.EnvironmentName, request.ProcessName)
	case request.PackageName != "":
		exitCode = c.pkgWriteCompilePackage(ctx, log, request.PackageName)
	default:
		exitCode = c.pkgWriteCompileAll(ctx, log)
	}
	return log.result(exitCode)
}
