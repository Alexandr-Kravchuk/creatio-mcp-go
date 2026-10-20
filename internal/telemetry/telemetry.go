// Package telemetry ports clio 8.1.0.142's product telemetry store (Clio.Common.Telemetry): the consent file,
// the local event outbox, per-run session state and the background upload. It writes the same files in the
// same place (<clio home>/telemetry, or CLIO_TELEMETRY_HOME), so clio and this server share one consent
// decision and one outbox.
package telemetry

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

// Status and consent tokens.
const (
	ConsentGranted  = "granted"
	ConsentDenied   = "denied"
	unknown         = "unknown"
	StatusRecorded  = "recorded"
	schemaVersion   = "3"
	floorWorkflow   = "unattributed"
	maxSessionID    = 128
	maxFieldLength  = 64
	telemetryHome   = "CLIO_TELEMETRY_HOME"
	clioHomeVar     = "CLIO_HOME"
	eventsDirName   = "events"
	sessionsDirName = "sessions"
)

// The canonical stage names and the deprecated app-creation names clio still accepts.
var (
	canonicalEvents = []string{"workflow_started", "clarification_requested", "user_input_received", "plan_presented",
		"plan_skipped", "plan_blocked", "plan_changes_requested", "plan_approved", "build_started", "work_item_completed",
		"workflow_completed", "workflow_failed", "changes_requested", "changes_applied", "session_usage"}
	legacyEvents = []string{"session_started", "pre_plan_clarification_requested", "pre_plan_user_input_received",
		"business_plan_generated", "business_plan_generation_skipped", "business_plan_feedback_received",
		"business_plan_regenerated", "business_plan_approved", "implementation_started", "implementation_user_input_received",
		"implementation_completed", "implementation_changes_requested", "implementation_changes_applied", "implementation_failed"}
	allowedEvents = func() map[string]bool {
		set := map[string]bool{}
		for _, name := range append(append([]string{}, legacyEvents...), canonicalEvents...) {
			set[name] = true
		}
		return set
	}()
)

// syncRoot is clio's static SyncRoot: one writer at a time inside the process.
var syncRoot sync.Mutex

// Request is clio's TelemetryEventRequest; Extra holds members clio's binder parks in ExtensionData.
type Request struct {
	SessionID         string
	EventName         string
	CodingAgent       string
	PluginVersion     string
	DurationMs        *int64
	TelemetryConsent  string
	Workflow          string
	Variant           string
	Model             string
	InputTokens       *int64
	OutputTokens      *int64
	CachedInputTokens *int64
	Extra             []string
}

// Error is clio's TelemetryError.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// EventResult is clio's TelemetryEventResult.
type EventResult struct {
	Success bool   `json:"success"`
	Status  string `json:"status"`
	EventID string `json:"event_id,omitempty"`
	Error   *Error `json:"error,omitempty"`
}

// ConsentResult is clio's TelemetryConsentResult.
type ConsentResult struct {
	Success          bool   `json:"success"`
	Status           string `json:"status"`
	TelemetryConsent string `json:"telemetry_consent"`
}

// WithdrawalResult is clio's TelemetryConsentWithdrawalResult.
type WithdrawalResult struct {
	Success          bool   `json:"success"`
	Status           string `json:"status"`
	TelemetryConsent string `json:"telemetry_consent"`
	EventsPurged     int    `json:"events_purged"`
}

// Service is clio's TelemetryService over one telemetry root.
type Service struct {
	// Root is the telemetry directory; empty means Root().
	Root string
	// Version is reported as clio_version.
	Version string
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// Root is clio's TelemetryStoragePaths.ResolveRoot: CLIO_TELEMETRY_HOME, else <clio home>/telemetry.
func Root() string {
	if override := os.Getenv(telemetryHome); strings.TrimSpace(override) != "" {
		return override
	}
	home := os.Getenv(clioHomeVar)
	if strings.TrimSpace(home) == "" {
		variable := "HOME"
		if runtime.GOOS == "windows" {
			variable = "LOCALAPPDATA"
		}
		home = filepath.Join(os.Getenv(variable), "creatio", "clio")
	}
	return filepath.Join(home, "telemetry")
}

func (s *Service) root() string {
	if s.Root != "" {
		return s.Root
	}
	return Root()
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s *Service) consentPath() string      { return filepath.Join(s.root(), "consent.json") }
func (s *Service) installationPath() string { return filepath.Join(s.root(), "installation-id.txt") }
func (s *Service) eventsDir() string        { return filepath.Join(s.root(), eventsDirName) }
func (s *Service) sessionsDir() string      { return filepath.Join(s.root(), sessionsDirName) }

type consentState struct {
	TelemetryConsent string `json:"telemetry_consent"`
	UpdatedAt        string `json:"updated_at"`
}

// dotnetTime is System.Text.Json's DateTimeOffset form (trailing zeros of the fraction trimmed).
func dotnetTime(t time.Time) string {
	return t.UTC().Truncate(100 * time.Nanosecond).Format("2006-01-02T15:04:05.9999999-07:00")
}

// roundTrip is DateTimeOffset.ToString("O"): always seven fraction digits.
func roundTrip(t time.Time) string {
	return t.UTC().Truncate(100 * time.Nanosecond).Format("2006-01-02T15:04:05.0000000-07:00")
}

func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return hex.EncodeToString(b[:])
}

func indented(value any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buffer.Bytes(), "\n"), nil
}

// writeJSON is clio's WriteJson: a temporary file beside the target, moved over it. The temporary name is
// unique per write, so two processes writing the same file cannot interleave into one temporary file.
func writeJSON(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o777); err != nil {
		return err
	}
	data, err := indented(value)
	if err != nil {
		return err
	}
	temporary := path + "." + newID() + ".tmp"
	defer func() { _ = os.Remove(temporary) }()
	if err := os.WriteFile(temporary, data, 0o666); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

func (s *Service) readConsent() consentState {
	data, err := os.ReadFile(s.consentPath())
	if err != nil {
		return consentState{TelemetryConsent: unknown}
	}
	var state consentState
	if json.Unmarshal(bytes.TrimPrefix(data, []byte("\xEF\xBB\xBF")), &state) != nil {
		return consentState{TelemetryConsent: unknown}
	}
	return state
}

// Consent is clio's GetConsentStatus.
func (s *Service) Consent() ConsentResult {
	switch consent := s.readConsent().TelemetryConsent; consent {
	case ConsentGranted, ConsentDenied:
		return ConsentResult{Success: true, Status: "known", TelemetryConsent: consent}
	}
	return ConsentResult{Success: true, Status: unknown, TelemetryConsent: unknown}
}

// Withdraw is clio's WithdrawConsent: consent becomes denied and the local outbox and session state go.
func (s *Service) Withdraw() WithdrawalResult {
	syncRoot.Lock()
	defer syncRoot.Unlock()
	if err := writeJSON(s.consentPath(), consentState{TelemetryConsent: ConsentDenied, UpdatedAt: dotnetTime(s.now())}); err != nil {
		return WithdrawalResult{Status: "withdraw-failed", TelemetryConsent: s.readConsent().TelemetryConsent}
	}
	purged := 0
	if entries, err := os.ReadDir(s.eventsDir()); err == nil {
		for _, entry := range entries {
			if entry.Type().IsRegular() && os.Remove(filepath.Join(s.eventsDir(), entry.Name())) == nil &&
				strings.HasSuffix(strings.ToLower(entry.Name()), ".json") {
				purged++
			}
		}
	}
	if entries, err := os.ReadDir(s.sessionsDir()); err == nil {
		for _, entry := range entries {
			if entry.Type().IsRegular() {
				_ = os.Remove(filepath.Join(s.sessionsDir(), entry.Name()))
			}
		}
	}
	return WithdrawalResult{Success: true, Status: "withdrawn", TelemetryConsent: ConsentDenied, EventsPurged: purged}
}

func invalid(code, message string) EventResult {
	return EventResult{Status: "rejected", Error: &Error{Code: code, Message: message}}
}

func blank(value string) bool { return strings.TrimFunc(value, unicode.IsSpace) == "" }

// tokenAllowed is clio's IsAllowedToken.
func tokenAllowed(value string) bool {
	if blank(value) || len(value) > maxFieldLength {
		return false
	}
	for _, r := range value {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r == '.' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

func sessionIDAllowed(value string) bool {
	for _, r := range value {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || strings.ContainsRune("._:-", r)) {
			return false
		}
	}
	return true
}

func utf16Length(value string) int {
	length := 0
	for _, r := range value {
		if r > 0xFFFF {
			length += 2
		} else {
			length++
		}
	}
	return length
}

// validate is clio's ValidateRequest, in clio's order.
func validate(request Request) *EventResult {
	if len(request.Extra) > 0 {
		names := append([]string{}, request.Extra...)
		sort.Strings(names)
		result := invalid("unsupported-fields", "Unsupported telemetry fields: "+strings.Join(names, ", ")+".")
		return &result
	}
	for _, field := range []struct{ name, value string }{{"session_id", request.SessionID}, {"event_name", request.EventName}} {
		if blank(field.value) {
			result := invalid("missing-required-field", fmt.Sprintf("Telemetry field '%s' is required.", field.name))
			return &result
		}
	}
	if utf16Length(request.SessionID) > maxSessionID || !sessionIDAllowed(request.SessionID) {
		result := invalid("invalid-session-id", fmt.Sprintf("session_id must be 1-%d characters of letters, digits, '.', '_', ':' or '-'.", maxSessionID))
		return &result
	}
	for _, field := range []struct{ name, value string }{{"coding_agent", request.CodingAgent}, {"plugin_version", request.PluginVersion}} {
		if utf16Length(field.value) > maxFieldLength {
			result := invalid("field-too-long", fmt.Sprintf("Telemetry field '%s' exceeds %d characters.", field.name, maxFieldLength))
			return &result
		}
	}
	for _, field := range []struct{ name, value string }{{"workflow", request.Workflow}, {"variant", request.Variant}, {"model", request.Model}} {
		if !blank(field.value) && !tokenAllowed(field.value) {
			result := invalid("invalid-token", fmt.Sprintf("Telemetry field '%s' must be 1-%d characters of lowercase letters, digits, '.', '_' or '-'.", field.name, maxFieldLength))
			return &result
		}
	}
	if !allowedEvents[request.EventName] {
		result := invalid("unknown-event-name", fmt.Sprintf("Unsupported event_name '%s'.", request.EventName))
		return &result
	}
	if !blank(request.TelemetryConsent) && request.TelemetryConsent != ConsentGranted && request.TelemetryConsent != ConsentDenied {
		result := invalid("unknown-consent", fmt.Sprintf("Unsupported telemetry_consent '%s'.", request.TelemetryConsent))
		return &result
	}
	if request.DurationMs != nil && *request.DurationMs < 0 {
		result := invalid("invalid-duration", "duration_ms must be a non-negative value when supplied.")
		return &result
	}
	for _, field := range []struct {
		name  string
		value *int64
	}{{"input_tokens", request.InputTokens}, {"output_tokens", request.OutputTokens}, {"cached_input_tokens", request.CachedInputTokens}} {
		if field.value != nil && *field.value < 0 {
			result := invalid("invalid-token-count", fmt.Sprintf("%s must be a non-negative value when supplied.", field.name))
			return &result
		}
	}
	return nil
}

type sessionState struct {
	SessionID string            `json:"session_id"`
	Events    map[string]string `json:"events"`
	Workflow  string            `json:"workflow,omitempty"`
	times     map[string]time.Time
}

func (s *Service) sessionPath(sessionID, workflow string) string {
	sum := sha256.Sum256([]byte(sessionID + "\x1f" + workflow))
	return filepath.Join(s.sessionsDir(), hex.EncodeToString(sum[:])+".json")
}

func (s *Service) readSession(sessionID, workflow string) *sessionState {
	fresh := &sessionState{SessionID: sessionID, Events: map[string]string{}, Workflow: workflow, times: map[string]time.Time{}}
	data, err := os.ReadFile(s.sessionPath(sessionID, workflow))
	if err != nil {
		return fresh
	}
	var state sessionState
	if json.Unmarshal(data, &state) != nil || state.Events == nil {
		return fresh
	}
	state.times = map[string]time.Time{}
	for name, value := range state.Events {
		parsed, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			return fresh
		}
		state.times[name] = parsed
	}
	return &state
}

func isSessionStart(event string) bool {
	return event == "workflow_started" || event == "session_started"
}

func isTerminal(event string) bool {
	switch event {
	case "workflow_completed", "workflow_failed", "plan_blocked", "implementation_completed", "implementation_failed":
		return true
	}
	return false
}

func (state *sessionState) firstKnown(names ...string) string {
	best, bestTime := "", time.Time{}
	for _, name := range names {
		if at, ok := state.times[name]; ok && (best == "" || at.Before(bestTime)) {
			best, bestTime = name, at
		}
	}
	return best
}

func (state *sessionState) preferredKnown(names ...string) string {
	for _, name := range names {
		if _, ok := state.times[name]; ok {
			return name
		}
	}
	return ""
}

// inferDuration is clio's InferDurationMs.
func (state *sessionState) inferDuration(event string, at time.Time) *int64 {
	start := ""
	switch event {
	case "business_plan_generated":
		start = state.firstKnown("session_started", "workflow_started")
	case "business_plan_approved":
		start = state.firstKnown("business_plan_generated")
	case "implementation_completed", "implementation_failed":
		start = "implementation_started"
	case "implementation_changes_applied":
		start = "implementation_changes_requested"
	case "plan_presented", "plan_blocked":
		start = state.firstKnown("workflow_started", "session_started")
	case "plan_approved":
		start = state.firstKnown("plan_presented")
	case "build_started":
		start = state.firstKnown("plan_approved")
	case "workflow_completed", "workflow_failed":
		start = state.preferredKnown("build_started", "plan_approved", "workflow_started", "session_started")
	case "changes_applied":
		start = state.firstKnown("changes_requested")
	}
	started, ok := state.times[start]
	if start == "" || !ok {
		return nil
	}
	elapsed := max(int64(0), at.Sub(started).Milliseconds())
	return &elapsed
}

func (state *sessionState) sinceStart(event string, at time.Time) *int64 {
	if isSessionStart(event) {
		return nil
	}
	anchor := ""
	var anchorTime time.Time
	for name, value := range state.times {
		if isSessionStart(name) && (anchor == "" || value.Before(anchorTime)) {
			anchor, anchorTime = name, value
		}
	}
	if anchor == "" {
		return nil
	}
	elapsed := max(int64(0), at.Sub(anchorTime).Milliseconds())
	return &elapsed
}

type attributeValue struct {
	StringValue *string `json:"string_value,omitempty"`
	IntValue    *int64  `json:"int_value,omitempty"`
}

type attribute struct {
	Key   string         `json:"key"`
	Value attributeValue `json:"value"`
}

type logEvent struct {
	TimeUnixNano int64       `json:"time_unix_nano"`
	SeverityText string      `json:"severity_text"`
	Attributes   []attribute `json:"attributes"`
	EventName    string      `json:"event_name"`
}

func stringAttribute(key, value string) attribute {
	return attribute{Key: key, Value: attributeValue{StringValue: &value}}
}

func intAttribute(key string, value int64) attribute {
	return attribute{Key: key, Value: attributeValue{IntValue: &value}}
}

func platform() string {
	switch runtime.GOOS {
	case "windows":
		return "windows"
	case "darwin":
		return "macos"
	case "linux":
		return "linux"
	}
	return unknown
}

// normalizeCodingAgent is clio's NormalizeCodingAgent.
func normalizeCodingAgent(value string) string {
	if blank(value) {
		return value
	}
	var slug strings.Builder
	for _, r := range strings.TrimFunc(value, unicode.IsSpace) {
		if r < 0x80 && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
			slug.WriteRune(unicode.ToLower(r))
		} else if strings.ContainsRune("._- ", r) && slug.Len() > 0 && !strings.HasSuffix(slug.String(), "-") {
			slug.WriteByte('-')
		}
	}
	if canonical := strings.TrimRight(slug.String(), "-"); canonical != "" {
		return canonical
	}
	var kept strings.Builder
	for _, r := range strings.ToLower(strings.TrimFunc(value, unicode.IsSpace)) {
		if !unicode.IsControl(r) && !unicode.Is(unicode.Cf, r) {
			kept.WriteRune(r)
		}
	}
	return strings.TrimFunc(kept.String(), unicode.IsSpace)
}

func (s *Service) installationID() string {
	if data, err := os.ReadFile(s.installationPath()); err == nil {
		if existing := strings.TrimSpace(strings.TrimPrefix(string(data), "\xEF\xBB\xBF")); existing != "" {
			return existing
		}
	}
	id := newID()
	temporary := s.installationPath() + "." + id + ".tmp"
	if err := os.WriteFile(temporary, []byte(id), 0o666); err != nil {
		return id
	}
	// First writer wins: a hard link fails when another process already published its identifier.
	if _, err := os.Stat(s.installationPath()); err == nil {
		_ = os.Remove(temporary)
	} else if linkErr := os.Link(temporary, s.installationPath()); linkErr != nil {
		_ = os.Rename(temporary, s.installationPath())
	} else {
		_ = os.Remove(temporary)
	}
	if data, err := os.ReadFile(s.installationPath()); err == nil && strings.TrimSpace(string(data)) != "" {
		return strings.TrimSpace(string(data))
	}
	return id
}

// Send is clio's TelemetryService.Send: validation, consent, then one event file and the run's state.
func (s *Service) Send(request Request) EventResult {
	if result := validate(request); result != nil {
		return *result
	}
	syncRoot.Lock()
	defer syncRoot.Unlock()
	result, err := s.record(request)
	if err != nil {
		return EventResult{Status: "record-failed", Error: &Error{Code: "record-unavailable",
			Message: "clio could not record the telemetry event; it was not retained."}}
	}
	return result
}

func (s *Service) record(request Request) (EventResult, error) {
	for _, dir := range []string{s.root(), s.eventsDir(), s.sessionsDir()} {
		if err := os.MkdirAll(dir, 0o777); err != nil {
			return EventResult{}, err
		}
	}
	consent := s.readConsent()
	if !blank(request.TelemetryConsent) && consent.TelemetryConsent != ConsentGranted && consent.TelemetryConsent != ConsentDenied {
		consent = consentState{TelemetryConsent: request.TelemetryConsent, UpdatedAt: dotnetTime(s.now())}
		if err := writeJSON(s.consentPath(), consent); err != nil {
			return EventResult{}, err
		}
	}
	if consent.TelemetryConsent == ConsentDenied {
		return EventResult{Success: true, Status: "consent-denied"}, nil
	}
	if consent.TelemetryConsent != ConsentGranted {
		return invalid("telemetry-consent-required", "Telemetry consent is required before telemetry events can be stored. Ask the user and retry with telemetry_consent set to granted or denied."), nil
	}
	eventID := newID()
	at := s.now()
	if blank(request.Workflow) {
		request.Workflow = floorWorkflow
	}
	session := s.readSession(request.SessionID, request.Workflow)
	if isSessionStart(request.EventName) {
		for name := range session.times {
			if isTerminal(name) {
				session = &sessionState{SessionID: session.SessionID, Events: map[string]string{}, Workflow: session.Workflow, times: map[string]time.Time{}}
				break
			}
		}
	}
	duration := request.DurationMs
	if duration == nil {
		duration = session.inferDuration(request.EventName, at)
	}
	attributes := []attribute{stringAttribute("schema_version", schemaVersion), stringAttribute("session_id", request.SessionID),
		stringAttribute("event_timestamp", roundTrip(at)), stringAttribute("platform", platform()),
		stringAttribute("clio_version", s.Version), stringAttribute("installation_id", s.installationID()),
		stringAttribute("event_id", eventID)}
	if !blank(request.CodingAgent) {
		attributes = append(attributes, stringAttribute("coding_agent", normalizeCodingAgent(request.CodingAgent)))
	}
	for _, field := range []struct{ key, value string }{{"plugin_version", request.PluginVersion}, {"workflow", request.Workflow},
		{"variant", request.Variant}, {"model", request.Model}} {
		if !blank(field.value) {
			attributes = append(attributes, stringAttribute(field.key, field.value))
		}
	}
	for _, field := range []struct {
		key   string
		value *int64
	}{{"input_tokens", request.InputTokens}, {"output_tokens", request.OutputTokens}, {"cached_input_tokens", request.CachedInputTokens}} {
		if field.value != nil {
			attributes = append(attributes, intAttribute(field.key, *field.value))
		}
	}
	if duration != nil {
		attributes = append(attributes, intAttribute("duration_ms", *duration))
	}
	if since := session.sinceStart(request.EventName, at); since != nil {
		attributes = append(attributes, intAttribute("duration_since_session_start_ms", *since))
	}
	event := logEvent{TimeUnixNano: at.UnixMilli() * 1_000_000, SeverityText: "INFO", Attributes: attributes, EventName: request.EventName}
	name := at.Format("20060102T150405.000") + "Z_" + eventID + ".json"
	name = strings.Replace(name, ".", "", 1)
	if err := writeJSON(filepath.Join(s.eventsDir(), name), event); err != nil {
		return EventResult{}, err
	}
	session.Events[request.EventName] = dotnetTime(at)
	if err := writeJSON(s.sessionPath(session.SessionID, session.Workflow), session); err != nil {
		return EventResult{}, err
	}
	return EventResult{Success: true, Status: StatusRecorded, EventID: eventID}, nil
}
