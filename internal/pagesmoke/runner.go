package pagesmoke

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/redact"
)

// The page check itself (PageSmokeRunner): open the form page with the environment's session, wait for it to be
// ready, read its buttons, fields and errors, and optionally click one button and report what followed.

// Limits of clio's PageSmokeRunner.
const (
	DefaultTimeoutSeconds   = 30
	MaxTimeoutSeconds       = 45
	DefaultClickWaitSeconds = 8
	MaxClickWaitSeconds     = 20
	MaxWatchedEntities      = 5

	maxConsoleErrors   = 15
	maxReplyBytes      = 14_000
	envelopeReserve    = 64
	maxErrorChars      = 1_000
	fitWarningReserve  = 600
	totalBudget        = 100 * time.Second
	commandTimeout     = 10 * time.Second
	pollInterval       = 250 * time.Millisecond
	quietPeriod        = 1500 * time.Millisecond
	minimumSettle      = time.Second
	requestTimeout     = 10 * time.Second
	answerReserve      = 5 * time.Second
	worldName          = "clio-page-smoke"
	maxWorldRecreation = 2
	redactedFragment   = "#[redacted]"
	untrustedPrefix    = "[untrusted-source-text begin] "
	untrustedSuffix    = " [untrusted-source-text end]"
)

// Request is PageSmokeRequest.
type Request struct {
	Page             string
	RecordID         string // "" opens the page in add mode
	TimeoutSeconds   int
	Click            *string
	ClickWaitSeconds int
	Watch            []string
	BrowserPath      string
	BrowserNote      string
}

// ClickReport is PageClickReport: what happened after the click.
type ClickReport struct {
	Outcome            string                  `json:"outcome"`
	Name               *string                 `json:"name,omitzero"`
	Caption            *string                 `json:"caption,omitzero"`
	Candidates         []PageButton            `json:"candidates,omitzero"`
	URL                *string                 `json:"url,omitzero"`
	Navigations        []string                `json:"navigations,omitzero"`
	NavigationsOmitted *int                    `json:"navigationsOmitted,omitzero"`
	ConsoleErrors      []PageConsoleError      `json:"consoleErrors,omitzero"`
	Dialogs            []string                `json:"dialogs,omitzero"`
	Toasts             []string                `json:"toasts,omitzero"`
	InvalidFields      []PageField             `json:"invalidFields,omitzero"`
	Created            []creatio.CreatedRecord `json:"created,omitzero"`
	Settled            *bool                   `json:"settled,omitzero"`
}

// Result is PageSmokeResult.
type Result struct {
	Success        bool
	Error          *string
	URL            *string
	Mode           *string
	Ready          bool
	LoadMs         *int64
	Title          *string
	Buttons        []PageButton
	ButtonsOmitted int
	Fields         []PageField
	FieldsOmitted  int
	ConsoleErrors  []PageConsoleError
	Click          *ClickReport
	Warnings       []string
}

// Runner checks one page of one environment.
type Runner struct {
	client   *creatio.Client
	env      creatio.PageSmokeEnvironment
	clioHome string
	now      func() time.Time
	sleep    func(context.Context, time.Duration) error
}

// NewRunner checks pages of the environment client was resolved for; settingsPath is clio's appsettings.json,
// whose folder holds the browser sessions.
func NewRunner(client *creatio.Client, settingsPath string) *Runner {
	return &Runner{client: client, env: client.PageSmokeEnvironment(), clioHome: clioHomeFolder(settingsPath), now: time.Now,
		sleep: func(ctx context.Context, delay time.Duration) error {
			timer := time.NewTimer(delay)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
				return nil
			}
		}}
}

func text(value string) *string { return &value }

// Run is RunAsync. It never fails for a page or browser problem; that is reported. An error is returned only
// when the caller cancelled.
func (r *Runner) Run(ctx context.Context, request Request) (Result, error) {
	mode := "edit"
	if request.RecordID == "" {
		mode = "add"
	}
	result := Result{Mode: text(mode), ConsoleErrors: []PageConsoleError{}, Warnings: []string{}}
	if request.BrowserNote != "" {
		result.Warnings = append(result.Warnings, request.BrowserNote)
	}
	address := r.cardURL(request.Page, request.RecordID)
	result.URL = text(r.relativeURL(address))
	budget, cancel := context.WithTimeout(ctx, totalBudget)
	defer cancel()
	budgetEnd := r.now().Add(totalBudget)
	events := newEventCollector(r.relativeURL)
	err := r.check(budget, budgetEnd, request, address, events, &result)
	if err != nil {
		if ctx.Err() != nil {
			return Result{}, ctx.Err()
		}
		readLoadErrors(&result, events, everything)
		result.Success = false
		if result.Error == nil {
			if budget.Err() != nil {
				result.Error = text(fmt.Sprintf("The check ran out of its %d s budget; what was read before is reported.", int(totalBudget/time.Second)))
			} else {
				result.Error = text(redact.Text(err.Error()))
			}
		}
	}
	return fit(result), nil
}

const everything = int(^uint(0) >> 1)

func (r *Runner) check(ctx context.Context, budgetEnd time.Time, request Request, address string, events *eventCollector, result *Result) error {
	cookies, err := r.readSessionCookies(ctx)
	if err != nil {
		return err
	}
	browser, err := launchBrowser(ctx, request.BrowserPath)
	if err != nil {
		return err
	}
	defer browser.dispose()
	page := browser.page
	page.onEvent(events.onEvent)
	events.dialogOpened = func() { go dismissNativeDialog(page) }
	frameID, err := r.openPage(ctx, page, cookies, address)
	if err != nil {
		return err
	}
	scripts := &scriptRunner{page: page, frameID: frameID}
	navigatedAt := r.now()
	readyWait := clampDuration(budgetEnd.Sub(r.now())-answerReserve, time.Duration(request.TimeoutSeconds)*time.Second)
	state, err := r.waitForReady(ctx, scripts, events, readyWait)
	if err != nil {
		return err
	}
	if state.State != "ready" {
		readLoadErrors(result, events, everything)
		result.Error = text(explainNotReady(state, request))
		return nil
	}
	result.Ready = true
	loadMs := r.now().Sub(navigatedAt).Milliseconds()
	result.LoadMs = &loadMs
	var inventory pageInventory
	if err := scripts.evaluate(ctx, inventoryScript, &inventory); err != nil {
		return err
	}
	applyInventory(result, inventory)
	result.Success = true
	if request.Click == nil {
		readLoadErrors(result, events, everything)
		return nil
	}
	return r.click(ctx, scripts, events, request, result, budgetEnd)
}

func clampDuration(value, upper time.Duration) time.Duration {
	if value < 0 {
		return 0
	}
	if value > upper {
		return upper
	}
	return value
}

// cardURL is BuildCardUrl: the Shell's #Card/{page}/edit/{record} route, or /add without a record.
func (r *Runner) cardURL(page, recordID string) string {
	suffix := "/add"
	if recordID != "" {
		suffix = "/edit/" + recordID
	}
	return shellURL(r.env) + "#Card/" + page + suffix
}

// relativeURL is the page address without the environment's own prefix, never with a query string or a
// key=value fragment (either can carry a token), cut to 120 characters.
func (r *Runner) relativeURL(address string) string {
	if address == "" {
		return address
	}
	base, fragment := address, ""
	if hash := strings.IndexByte(address, '#'); hash >= 0 {
		base, fragment = address[:hash], stripQuery(address[hash:])
	}
	if strings.Contains(fragment, "=") {
		fragment = redactedFragment
	}
	address = stripQuery(base) + fragment
	prefix := strings.TrimRight(r.env.URI, "/")
	if prefix != "" && len(address) >= len(prefix) && strings.EqualFold(address[:len(prefix)], prefix) {
		address = address[len(prefix):]
	}
	return capURL(address)
}

func stripQuery(part string) string {
	if query := strings.IndexByte(part, '?'); query >= 0 {
		return part[:query]
	}
	return part
}

// readLoadErrors reads the load's errors once, up to the click (or to the end when there is none).
func readLoadErrors(result *Result, events *eventCollector, upTo int) {
	if len(result.ConsoleErrors) > 0 || (result.Click != nil && upTo == everything) {
		return
	}
	errors, omitted := events.errorsBetween(0, upTo, maxConsoleErrors)
	result.ConsoleErrors = errors
	noteOmitted(result, omitted, "load")
}

func noteOmitted(result *Result, omitted int, phase string) {
	if omitted > 0 {
		result.Warnings = append(result.Warnings, fmt.Sprintf("%d more distinct console error(s) during %s not listed.", omitted, phase))
	}
}

// dismissNativeDialog answers an alert/confirm/prompt/beforeunload with "no" without waiting on the receive
// loop: page-smoke never confirms anything.
func dismissNativeDialog(page *cdpSession) {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	_, _ = page.send(ctx, "Page.handleJavaScriptDialog", map[string]any{"accept": false})
}

func (r *Runner) readSessionCookies(ctx context.Context) ([]BrowserCookie, error) {
	service := sessionService{env: r.env, transport: r.client.PageSmokeTransport(), clioHome: r.clioHome, now: r.now}
	path, err := service.sessionPath(ctx)
	if err != nil {
		return nil, err
	}
	data, err := readFileText(path)
	if err != nil {
		return nil, err
	}
	cookies := parseStorageCookies(data)
	if len(cookies) == 0 {
		return nil, errors.New("The environment's browser session holds no cookie, so the page would open on the login form. " +
			"Check the environment's credentials with ping-app.")
	}
	return cookies, nil
}

// send runs one command with clio's per-command limit.
func send(ctx context.Context, page *cdpSession, method string, params any) (json.RawMessage, error) {
	commandCtx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	result, err := page.send(commandCtx, method, params)
	if err != nil && ctx.Err() == nil && commandCtx.Err() != nil {
		return nil, fmt.Errorf("The browser did not answer %s within %d s.", method, int(commandTimeout/time.Second))
	}
	return result, err
}

// openPage enables what the check reads, sets the session cookies (Network only for them), and navigates.
// It returns the main frame's id, which the page scripts' isolated world is created in.
func (r *Runner) openPage(ctx context.Context, page *cdpSession, cookies []BrowserCookie, address string) (string, error) {
	for _, method := range []string{"Runtime.enable", "Log.enable", "Page.enable", "Network.enable"} {
		if _, err := send(ctx, page, method, nil); err != nil {
			return "", err
		}
	}
	for _, cookie := range cookies {
		if _, err := send(ctx, page, "Network.setCookie", setCookieParams(cookie, r.env.URI)); err != nil {
			return "", err
		}
	}
	if _, err := send(ctx, page, "Network.disable", nil); err != nil {
		return "", err
	}
	navigation, err := send(ctx, page, "Page.navigate", map[string]any{"url": address})
	if err != nil {
		return "", err
	}
	reply := objectOf(navigation)
	if errorText := reply.text("errorText"); errorText != nil && !blank(*errorText) {
		return "", fmt.Errorf("The browser could not open the environment: %s. Check that the environment answers (ping-app).", *errorText)
	}
	if frameID := reply.text("frameId"); frameID != nil {
		return *frameID, nil
	}
	return "", errors.New("The browser did not name the page's frame, so the page cannot be read.")
}

// setCookieParams is BuildSetCookieParams: CDP accepts only Strict, Lax or None for sameSite.
func setCookieParams(cookie BrowserCookie, fallbackURL string) map[string]any {
	path := cookie.Path
	if path == "" {
		path = "/"
	}
	sameSite := "Lax"
	switch strings.ToLower(cookie.SameSite) {
	case "strict":
		sameSite = "Strict"
	case "none":
		sameSite = "None"
	}
	params := map[string]any{"name": cookie.Name, "value": cookie.Value, "path": path, "httpOnly": cookie.HTTPOnly,
		"secure": cookie.Secure, "sameSite": sameSite}
	if cookie.Domain != "" {
		params["domain"] = cookie.Domain
	} else {
		params["url"] = fallbackURL
	}
	if cookie.Expires > 0 {
		params["expires"] = cookie.Expires
	}
	return params
}

// waitForReady polls the ready script until the page is ready and quiet, lands on the login form or a Classic UI
// page twice in a row, or the wait ends. A script that fails while the page navigates counts as still loading.
func (r *Runner) waitForReady(ctx context.Context, scripts *scriptRunner, events *eventCollector, wait time.Duration) (pageReadyState, error) {
	deadline := r.now().Add(wait)
	last := pageReadyState{State: "loading", Why: text("the page has not answered yet")}
	tracker := readyTracker{}
	for {
		var state pageReadyState
		ok, err := scripts.tryEvaluate(ctx, readyScript, &state)
		if err != nil {
			return pageReadyState{}, err
		}
		if !ok {
			state = last
			state.State = "loading"
			state.Why = text("the page is navigating")
		}
		if tracker.isDone(state, events.navigationTotal(), r.now()) {
			return state, nil
		}
		last = state
		if !r.now().Before(deadline) {
			if last.State == "ready" {
				last.State = "unsettled"
			}
			return last, nil
		}
		if err := r.sleep(ctx, pollInterval); err != nil {
			return pageReadyState{}, err
		}
	}
}

type readyTracker struct {
	signature   string
	hasSig      bool
	stableSince time.Time
	terminal    string
}

func (t *readyTracker) isDone(state pageReadyState, navigationCount int, now time.Time) bool {
	if state.State == "login" || state.State == "classic" {
		repeated := t.terminal == state.State
		t.terminal = state.State
		return repeated
	}
	t.terminal = ""
	if state.State != "ready" {
		t.hasSig = false
		return false
	}
	current := deref(state.Signature) + "|" + fmt.Sprint(navigationCount)
	if !t.hasSig || current != t.signature {
		t.signature, t.hasSig, t.stableSince = current, true, now
		return false
	}
	return now.Sub(t.stableSince) >= quietPeriod
}

func explainNotReady(state pageReadyState, request Request) string {
	switch state.State {
	case "login":
		return "The browser landed on the login form: the environment did not accept the session. Run get-browser-session " +
			"with force-refresh=true, or check the credentials with ping-app."
	case "classic":
		return "'" + request.Page + "' opened as a Classic UI page; page-smoke reads Freedom UI pages only."
	case "unsettled":
		return fmt.Sprintf("The page kept changing for the whole %d s wait; raise timeout, or read consoleErrors for a loop.", request.TimeoutSeconds)
	}
	return fmt.Sprintf("The page was not ready after %d s (%s). Check that '%s' is a Freedom UI form page (list-pages), that the "+
		"record exists, and read consoleErrors.", request.TimeoutSeconds, deref(state.Why), request.Page)
}

func applyInventory(result *Result, inventory pageInventory) {
	result.Title = inventory.Title
	result.Buttons = inventory.Buttons
	if result.Buttons == nil {
		result.Buttons = []PageButton{}
	}
	result.Fields = inventory.Fields
	if result.Fields == nil {
		result.Fields = []PageField{}
	}
	result.ButtonsOmitted = max(0, inventory.TotalButtons-len(result.Buttons))
	result.FieldsOmitted = max(0, inventory.TotalFields-len(result.Fields))
	if deref(inventory.Scope) != "page" {
		result.Warnings = append(result.Warnings, "No page outlet (crt-navigation-schema-outlet) was found, so buttons and fields "+
			"were read from the whole document, the Shell's own included.")
	}
}

func (r *Runner) watchStop(budgetEnd time.Time) time.Time {
	byRequest := r.now().Add(2 * requestTimeout)
	byBudget := budgetEnd.Add(-answerReserve)
	if byRequest.Before(byBudget) {
		return byRequest
	}
	return byBudget
}

func untrusted(value string) string {
	if fenced := creatio.DataWriteUntrusted(value); fenced != nil {
		return *fenced
	}
	return value
}

func (r *Runner) click(ctx context.Context, scripts *scriptRunner, events *eventCollector, request Request, result *Result,
	budgetEnd time.Time) (err error) {
	var watch *creatio.PageSmokeWatch
	if len(request.Watch) > 0 {
		watch = r.client.PageSmokeCaptureWatch(ctx, request.Watch, requestTimeout, r.watchStop(budgetEnd))
	}
	mark := events.mark()
	readLoadErrors(result, events, mark)
	report := &ClickReport{Outcome: "unknown"}
	result.Click = report
	defer func() {
		// Whatever happened after the press, the records it may have created are read - unless it was never pressed.
		if watch != nil && (report.Outcome == "clicked" || report.Outcome == "unknown") {
			created, warnings := watch.Collect(requestTimeout, r.watchStop(budgetEnd))
			report.Created = created
			result.Warnings = append(result.Warnings, warnings...)
		}
	}()
	var outcome pageClickOutcome
	if evaluateErr := scripts.evaluate(ctx, clickScript(*request.Click), &outcome); evaluateErr != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// The reply can be lost after the button was pressed, so what followed is still reported.
		result.Success = false
		result.Error = text("The click script did not answer (" + redact.Text(evaluateErr.Error()) +
			"); the button may have been pressed, so read click.created and the record before retrying.")
		reportWhatFollowed(report, events, mark, result)
		return nil
	}
	report.Outcome, report.Name, report.Caption = outcome.Outcome, outcome.Name, outcome.Caption
	if outcome.Outcome != "clicked" {
		report.Candidates = outcome.Candidates
		result.Success = false
		switch outcome.Outcome {
		case "ambiguous":
			result.Error = text("Several visible buttons match '" + *request.Click + "'; click.candidates lists them. Pass an element " +
				"name instead of the caption.")
		case "disabled":
			result.Error = text("The button '" + *request.Click + "' is disabled, so it was not clicked.")
		default:
			result.Error = text("No visible button matches '" + *request.Click + "' by caption or element name; click.candidates " +
				"lists the page's buttons.")
		}
		return nil
	}
	settle := clampDuration(budgetEnd.Sub(r.now())-answerReserve, time.Duration(request.ClickWaitSeconds)*time.Second)
	if err := r.readWhatFollowed(ctx, scripts, events, report, result, mark, settle); err != nil {
		return err
	}
	if report.Settled != nil && !*report.Settled {
		result.Warnings = append(result.Warnings, fmt.Sprintf("The page was still changing when click-wait (%d s) ended; what "+
			"followed may be incomplete.", request.ClickWaitSeconds))
	}
	return nil
}

// readWhatFollowed: the settle wait, then the page address, overlays and invalid fields. An oversized reply costs
// only its own part of the report.
func (r *Runner) readWhatFollowed(ctx context.Context, scripts *scriptRunner, events *eventCollector, report *ClickReport,
	result *Result, mark int, settle time.Duration) error {
	followedReported := false
	err := func() error {
		settled, err := r.waitToSettle(ctx, scripts, events, settle)
		if err != nil {
			return err
		}
		report.Settled = &settled
		reportWhatFollowed(report, events, mark, result)
		followedReported = true
		var after pageReadyState
		ok, err := scripts.tryEvaluate(ctx, readyScript, &after)
		if err != nil {
			return err
		}
		if ok && after.Href != nil {
			report.URL = text(r.relativeURL(*after.Href))
		} else {
			report.URL = nil
		}
		var overlays pageOverlays
		ok, err = scripts.tryEvaluate(ctx, overlayScript, &overlays)
		if err != nil {
			return err
		}
		if ok && len(overlays.Dialogs) > 0 {
			dialogs := append([]string{}, report.Dialogs...)
			for _, dialog := range overlays.Dialogs {
				dialogs = append(dialogs, untrusted(dialog))
			}
			report.Dialogs = dialogs
		}
		report.Toasts = nil
		if ok && len(overlays.Toasts) > 0 {
			for _, toast := range overlays.Toasts {
				report.Toasts = append(report.Toasts, untrusted(toast))
			}
		}
		var inventory pageInventory
		ok, err = scripts.tryEvaluate(ctx, inventoryScript, &inventory)
		if err != nil {
			return err
		}
		report.InvalidFields = nil
		if ok {
			for _, field := range inventory.Fields {
				if field.Error != nil {
					report.InvalidFields = append(report.InvalidFields, field)
				}
			}
		}
		return nil
	}()
	var tooLarge *cdpTooLargeError
	if errors.As(err, &tooLarge) {
		if !followedReported {
			reportWhatFollowed(report, events, mark, result)
		}
		result.Warnings = append(result.Warnings, tooLarge.Error()+" What followed the click may be incomplete.")
		return nil
	}
	return err
}

func reportWhatFollowed(report *ClickReport, events *eventCollector, mark int, result *Result) {
	navigations, navigationsOmitted := events.navigationsSince(mark)
	report.Navigations = nil
	if len(navigations) > 0 {
		report.Navigations = navigations
	}
	report.NavigationsOmitted = nil
	if navigationsOmitted > 0 {
		report.NavigationsOmitted = &navigationsOmitted
	}
	consoleErrors, omitted := events.errorsBetween(mark, everything, maxConsoleErrors)
	report.ConsoleErrors = nil
	if len(consoleErrors) > 0 {
		report.ConsoleErrors = consoleErrors
	}
	noteOmitted(result, omitted, "click")
	report.Dialogs = nil
	for _, dialog := range events.nativeDialogsSince(mark) {
		report.Dialogs = append(report.Dialogs, untrusted(dialog))
	}
}

// waitToSettle: the page has settled when nothing masks it and neither its address, its named elements nor its
// overlays changed for the quiet period; a click that navigates resets the wait.
func (r *Runner) waitToSettle(ctx context.Context, scripts *scriptRunner, events *eventCollector, wait time.Duration) (bool, error) {
	start := r.now()
	deadline := start.Add(wait)
	signature, hasSignature := "", false
	stableSince := start
	for {
		var state pageReadyState
		ok, err := scripts.tryEvaluate(ctx, readyScript, &state)
		if err != nil {
			return false, err
		}
		current, hasCurrent := "", false
		if ok && state.State != "loading" {
			current, hasCurrent = state.State+"|"+deref(state.Signature)+"|"+fmt.Sprint(events.navigationTotal()), true
		}
		if !hasCurrent || !hasSignature || current != signature {
			signature, hasSignature = current, hasCurrent
			stableSince = r.now()
		} else if r.now().Sub(stableSince) >= quietPeriod && r.now().Sub(start) >= minimumSettle {
			return true, nil
		}
		if !r.now().Before(deadline) {
			return false, nil
		}
		if err := r.sleep(ctx, pollInterval); err != nil {
			return false, err
		}
	}
}

// scriptRunner evaluates the page scripts in an ISOLATED world of the main frame: they share the DOM but none of
// the page's JavaScript, so a page that overrides a global can neither make them throw text of its choosing nor
// steer which button the click script matches. A world lives as long as its document.
type scriptRunner struct {
	page      *cdpSession
	frameID   string
	contextID *int
}

func (s *scriptRunner) evaluate(ctx context.Context, script string, target any) error {
	answer, err := s.evaluateInWorld(ctx, script)
	if err != nil {
		return err
	}
	reply := objectOf(answer)
	if details, ok := reply["exceptionDetails"]; ok {
		return errors.New("The page script failed: " + describeScriptError(objectOf(details)))
	}
	result := objectOf(reply["result"])
	value, ok := result["value"]
	if !ok {
		return errors.New("The page script returned no value.")
	}
	if err := json.Unmarshal(value, target); err != nil {
		return &scriptValueError{err.Error()}
	}
	return nil
}

// scriptValueError is a JsonException: the value did not read as the script's shape.
type scriptValueError struct{ message string }

func (e *scriptValueError) Error() string { return e.message }

// tryEvaluate is TryEvaluateAsync: while the page navigates its document is replaced and an evaluation in flight
// fails; that is "not yet" (ok false), not an error. A closed session, an oversized reply and a cancelled call
// are errors.
func (s *scriptRunner) tryEvaluate(ctx context.Context, script string, target any) (bool, error) {
	err := s.evaluate(ctx, script, target)
	if err == nil {
		return true, nil
	}
	var closed *cdpClosedError
	var tooLarge *cdpTooLargeError
	if ctx.Err() != nil || errors.As(err, &closed) || errors.As(err, &tooLarge) {
		return false, err
	}
	return false, nil
}

// evaluateInWorld sends the script again in a new world when the old one's document is gone ("Cannot find
// context"), twice at most; any other failure keeps the world and is not retried - for the click that would risk
// a second press.
func (s *scriptRunner) evaluateInWorld(ctx context.Context, script string) (json.RawMessage, error) {
	recreations := 0
	for {
		if s.contextID == nil {
			id, err := s.createWorld(ctx)
			if err != nil {
				return nil, err
			}
			s.contextID = &id
		}
		answer, err := send(ctx, s.page, "Runtime.evaluate", map[string]any{"expression": script, "contextId": *s.contextID,
			"returnByValue": true})
		var command *cdpCommandError
		if err != nil && errors.As(err, &command) && strings.Contains(strings.ToLower(command.Error()), "cannot find context") &&
			recreations < maxWorldRecreation {
			s.contextID = nil
			recreations++
			continue
		}
		return answer, err
	}
}

func (s *scriptRunner) createWorld(ctx context.Context) (int, error) {
	world, err := send(ctx, s.page, "Page.createIsolatedWorld", map[string]any{"frameId": s.frameID, "worldName": worldName})
	if err != nil {
		return 0, err
	}
	var reply struct {
		ExecutionContextID *int `json:"executionContextId"`
	}
	if json.Unmarshal(world, &reply) != nil || reply.ExecutionContextID == nil {
		return 0, errors.New("The browser did not create the page scripts' isolated world.")
	}
	return *reply.ExecutionContextID, nil
}

// describeScriptError is the page's own exception text: page-authored, so scrubbed, flattened, capped and fenced.
func describeScriptError(details eventObject) string {
	var message *string
	if exception := objectOf(details["exception"]); exception != nil {
		if _, ok := exception["description"]; ok {
			message = exception.text("description")
		} else {
			message = details.text("text")
		}
	} else {
		message = details.text("text")
	}
	if message != nil {
		if fenced := creatio.DataWriteUntrusted(*message); fenced != nil {
			return *fenced
		}
	}
	return "unknown error"
}

// ---- Fit: the reply stays under 14 KB ----

// fit keeps the reply under the bound. The diagnostics give way first: field values, then fields, buttons, the
// load errors and the click's candidates, invalid fields and errors, all halved together each round. click.created
// is touched only when the reply is still too big with every diagnostic list empty, and then titles go (last
// record first) before any record does.
func fit(result Result) Result {
	result.Error = clampPreservingFence(result.Error, maxErrorChars)
	const limit = maxReplyBytes - envelopeReserve
	if replySize(result) <= limit {
		return result
	}
	const target = limit - fitWarningReserve
	if len(result.Fields) > 0 {
		fields := make([]PageField, len(result.Fields))
		for index, field := range result.Fields {
			field.Value = nil
			fields[index] = field
		}
		result.Fields = fields
		result.Warnings = append(result.Warnings, "Field values were dropped to keep the reply small.")
	}
	click := result.Click
	type diagnostic struct {
		name  string
		count func() int
		keep  func(int)
	}
	diagnostics := []diagnostic{
		{"fields", func() int { return len(result.Fields) }, func(n int) { result.Fields = result.Fields[:n:n] }},
		{"buttons", func() int { return len(result.Buttons) }, func(n int) { result.Buttons = result.Buttons[:n:n] }},
		{"consoleErrors", func() int { return len(result.ConsoleErrors) }, func(n int) { result.ConsoleErrors = result.ConsoleErrors[:n:n] }},
		{"click.candidates", func() int { return clickLen(click, func(c *ClickReport) int { return len(c.Candidates) }) },
			func(n int) { click.Candidates = click.Candidates[:n:n] }},
		{"click.invalidFields", func() int { return clickLen(click, func(c *ClickReport) int { return len(c.InvalidFields) }) },
			func(n int) { click.InvalidFields = click.InvalidFields[:n:n] }},
		{"click.consoleErrors", func() int { return clickLen(click, func(c *ClickReport) int { return len(c.ConsoleErrors) }) },
			func(n int) { click.ConsoleErrors = click.ConsoleErrors[:n:n] }},
		{"click.navigations", func() int { return clickLen(click, func(c *ClickReport) int { return len(c.Navigations) }) },
			func(n int) {
				omitted := len(click.Navigations) - n
				if click.NavigationsOmitted != nil {
					omitted += *click.NavigationsOmitted
				}
				click.NavigationsOmitted = &omitted
				kept := append([]string{}, click.Navigations[:n-n/2]...)
				click.Navigations = append(kept, click.Navigations[len(click.Navigations)-n/2:]...)
			}},
	}
	before := make([]int, len(diagnostics))
	for index, list := range diagnostics {
		before[index] = list.count()
	}
	anyLeft := func() bool {
		for _, list := range diagnostics {
			if list.count() > 0 {
				return true
			}
		}
		return false
	}
	for replySize(result) > target && anyLeft() {
		for _, list := range diagnostics {
			if count := list.count(); count > 0 {
				list.keep(count / 2)
			}
		}
	}
	result.FieldsOmitted += before[0] - diagnostics[0].count()
	result.ButtonsOmitted += before[1] - diagnostics[1].count()
	var cut []string
	for index, list := range diagnostics {
		if removed := before[index] - list.count(); removed > 0 {
			cut = append(cut, fmt.Sprintf("%s -%d", list.name, removed))
		}
	}
	if len(cut) > 0 {
		result.Warnings = append(result.Warnings, fmt.Sprintf("Lists cut to keep the reply under %d KB: %s.", maxReplyBytes/1000,
			strings.Join(cut, ", ")))
	}
	if click != nil && len(click.Created) > 0 && replySize(result) > target {
		fitCreated(&result, click, target)
	}
	return result
}

func clickLen(click *ClickReport, length func(*ClickReport) int) int {
	if click == nil {
		return 0
	}
	return length(click)
}

func fitCreated(result *Result, click *ClickReport, target int) {
	created := append([]creatio.CreatedRecord{}, click.Created...)
	click.Created = created
	untitled := 0
	for index := len(created) - 1; index >= 0 && replySize(*result) > target; index-- {
		if created[index].Title != nil {
			created[index].Title = nil
			untitled++
		}
	}
	total := len(created)
	for len(click.Created) > 0 && replySize(*result) > target {
		click.Created = click.Created[:len(click.Created)-1]
	}
	if removed := total - len(click.Created); removed > 0 {
		result.Warnings = append(result.Warnings, fmt.Sprintf("click.created was cut by %d record(s), and %d title(s) were dropped, to "+
			"keep the reply small: the click created more records than the reply can list, so read the watched entities to find all of them.",
			removed, untitled))
	} else {
		result.Warnings = append(result.Warnings, fmt.Sprintf("click.created lost %d title(s) to keep the reply small; every record is "+
			"still listed by entity and id.", untitled))
	}
}

// clampPreservingFence is SensitiveErrorTextRedactor.ClampPreservingFence: a fenced payload is what gets cut, the
// framing around it is kept whole.
func clampPreservingFence(value *string, maxLength int) *string {
	if value == nil {
		return nil
	}
	units := utf16.Encode([]rune(*value))
	if len(units) <= maxLength {
		return value
	}
	const ellipsis = "..."
	opener := strings.Index(*value, untrustedPrefix)
	if opener < 0 || !strings.HasSuffix(*value, untrustedSuffix) {
		return text(truncateUTF16(*value, maxLength) + ellipsis)
	}
	openerUnits := len(utf16.Encode([]rune((*value)[:opener])))
	budget := maxLength - (openerUnits + len(untrustedPrefix) + len(untrustedSuffix) + len(ellipsis))
	if budget <= 0 {
		return text(truncateUTF16(*value, maxLength) + ellipsis)
	}
	payload := (*value)[opener+len(untrustedPrefix) : len(*value)-len(untrustedSuffix)]
	return text((*value)[:opener+len(untrustedPrefix)] + truncateUTF16(payload, budget) + ellipsis + untrustedSuffix)
}

// truncateUTF16 is TextUtilities.TruncateWithoutSplittingSurrogatePair over UTF-16 units.
func truncateUTF16(value string, maxLength int) string {
	units := utf16.Encode([]rune(value))
	if len(units) <= maxLength {
		return value
	}
	if maxLength <= 0 {
		return ""
	}
	units = units[:maxLength]
	if last := units[len(units)-1]; last >= 0xD800 && last <= 0xDBFF {
		units = units[:len(units)-1]
	}
	return string(utf16.Decode(units))
}

// replySize is JsonSerializer.SerializeToUtf8Bytes(result).Length: the result as System.Text.Json writes it with
// its default options, where a null member without an ignore condition is written as null.
func replySize(result Result) int {
	return creatio.ClioDefaultJSONLength(sizeViewOf(result))
}

type sizeButton struct {
	Name     *string `json:"name"`
	Caption  *string `json:"caption"`
	Disabled bool    `json:"disabled,omitzero"`
}

type sizeField struct {
	Name     *string `json:"name"`
	Type     *string `json:"type"`
	Caption  *string `json:"caption"`
	Required bool    `json:"required,omitzero"`
	Readonly bool    `json:"readonly,omitzero"`
	Value    *string `json:"value,omitzero"`
	Error    *string `json:"error,omitzero"`
}

type sizeCreated struct {
	Entity string  `json:"entity"`
	ID     string  `json:"id"`
	Title  *string `json:"title"`
}

type sizeClick struct {
	Outcome            string             `json:"outcome"`
	Name               *string            `json:"name,omitzero"`
	Caption            *string            `json:"caption,omitzero"`
	Candidates         []sizeButton       `json:"candidates,omitzero"`
	URL                *string            `json:"url,omitzero"`
	Navigations        []string           `json:"navigations,omitzero"`
	NavigationsOmitted *int               `json:"navigationsOmitted,omitzero"`
	ConsoleErrors      []PageConsoleError `json:"consoleErrors,omitzero"`
	Dialogs            []string           `json:"dialogs,omitzero"`
	Toasts             []string           `json:"toasts,omitzero"`
	InvalidFields      []sizeField        `json:"invalidFields,omitzero"`
	Created            []sizeCreated      `json:"created,omitzero"`
	Settled            *bool              `json:"settled,omitzero"`
}

type sizeResult struct {
	Success        bool               `json:"success"`
	Error          *string            `json:"error,omitzero"`
	URL            *string            `json:"url,omitzero"`
	Mode           *string            `json:"mode,omitzero"`
	Ready          bool               `json:"ready"`
	LoadMs         *int64             `json:"loadMs,omitzero"`
	Title          *string            `json:"title,omitzero"`
	Buttons        []sizeButton       `json:"buttons,omitzero"`
	ButtonsOmitted int                `json:"buttonsOmitted,omitzero"`
	Fields         []sizeField        `json:"fields,omitzero"`
	FieldsOmitted  int                `json:"fieldsOmitted,omitzero"`
	ConsoleErrors  []PageConsoleError `json:"consoleErrors"`
	Click          *sizeClick         `json:"click,omitzero"`
	Warnings       []string           `json:"warnings"`
}

func sizeButtons(buttons []PageButton) []sizeButton {
	if buttons == nil {
		return nil
	}
	view := make([]sizeButton, len(buttons))
	for index, button := range buttons {
		view[index] = sizeButton(button)
	}
	return view
}

func sizeFields(fields []PageField) []sizeField {
	if fields == nil {
		return nil
	}
	view := make([]sizeField, len(fields))
	for index, field := range fields {
		view[index] = sizeField(field)
	}
	return view
}

func sizeViewOf(result Result) sizeResult {
	view := sizeResult{Success: result.Success, Error: result.Error, URL: result.URL, Mode: result.Mode, Ready: result.Ready,
		LoadMs: result.LoadMs, Title: result.Title, Buttons: sizeButtons(result.Buttons), ButtonsOmitted: result.ButtonsOmitted,
		Fields: sizeFields(result.Fields), FieldsOmitted: result.FieldsOmitted, ConsoleErrors: result.ConsoleErrors,
		Warnings: result.Warnings}
	if view.ConsoleErrors == nil {
		view.ConsoleErrors = []PageConsoleError{}
	}
	if view.Warnings == nil {
		view.Warnings = []string{}
	}
	if click := result.Click; click != nil {
		sized := &sizeClick{Outcome: click.Outcome, Name: click.Name, Caption: click.Caption, Candidates: sizeButtons(click.Candidates),
			URL: click.URL, Navigations: click.Navigations, NavigationsOmitted: click.NavigationsOmitted,
			ConsoleErrors: click.ConsoleErrors, Dialogs: click.Dialogs, Toasts: click.Toasts,
			InvalidFields: sizeFields(click.InvalidFields), Settled: click.Settled}
		if click.Created != nil {
			sized.Created = make([]sizeCreated, len(click.Created))
			for index, record := range click.Created {
				sized.Created[index] = sizeCreated{Entity: record.Entity, ID: record.ID, Title: record.Title}
			}
		}
		view.Click = sized
	}
	return view
}
