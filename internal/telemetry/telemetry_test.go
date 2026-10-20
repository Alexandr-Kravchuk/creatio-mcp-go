package telemetry

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func int64p(value int64) *int64 { return &value }

func TestSendValidatesInClioOrder(t *testing.T) {
	service := &Service{Root: t.TempDir(), Version: "test"}
	cases := []struct {
		request Request
		code    string
		message string
	}{
		{Request{SessionID: "s", EventName: "workflow_started", Extra: []string{"zzz", "abc"}}, "unsupported-fields", "Unsupported telemetry fields: abc, zzz."},
		{Request{EventName: "workflow_started"}, "missing-required-field", "Telemetry field 'session_id' is required."},
		{Request{SessionID: "bad id", EventName: "workflow_started"}, "invalid-session-id", "session_id must be 1-128 characters of letters, digits, '.', '_', ':' or '-'."},
		{Request{SessionID: "s", EventName: "workflow_started", CodingAgent: strings.Repeat("a", 65)}, "field-too-long", "Telemetry field 'coding_agent' exceeds 64 characters."},
		{Request{SessionID: "s", EventName: "workflow_started", Workflow: "Bad Flow"}, "invalid-token", "Telemetry field 'workflow' must be 1-64 characters of lowercase letters, digits, '.', '_' or '-'."},
		{Request{SessionID: "s", EventName: "nope"}, "unknown-event-name", "Unsupported event_name 'nope'."},
		{Request{SessionID: "s", EventName: "workflow_started", TelemetryConsent: "maybe"}, "unknown-consent", "Unsupported telemetry_consent 'maybe'."},
		{Request{SessionID: "s", EventName: "workflow_started", DurationMs: int64p(-1)}, "invalid-duration", "duration_ms must be a non-negative value when supplied."},
		{Request{SessionID: "s", EventName: "workflow_started", OutputTokens: int64p(-1)}, "invalid-token-count", "output_tokens must be a non-negative value when supplied."},
		{Request{SessionID: "s", EventName: "workflow_started"}, "telemetry-consent-required", "Telemetry consent is required before telemetry events can be stored."},
	}
	for _, tc := range cases {
		result := service.Send(tc.request)
		if result.Success || result.Status != "rejected" || result.Error == nil || result.Error.Code != tc.code || !strings.HasPrefix(result.Error.Message, tc.message) {
			t.Errorf("%s: %+v %+v", tc.code, result, result.Error)
		}
	}
	if _, err := os.Stat(filepath.Join(service.Root, "consent.json")); err == nil {
		t.Fatal("a rejected request must not store consent")
	}
}

func TestSendStoresClioShapedEventsAndRunState(t *testing.T) {
	root := t.TempDir()
	clock := time.Date(2026, 10, 20, 12, 0, 0, 123456700, time.UTC)
	service := &Service{Root: root, Version: "creatio-mcp-go/test", Now: func() time.Time { return clock }}
	first := service.Send(Request{SessionID: "run-1", EventName: "workflow_started", TelemetryConsent: "granted", Workflow: "branding",
		CodingAgent: "Claude Code", InputTokens: int64p(5)})
	if !first.Success || first.Status != "recorded" || len(first.EventID) != 32 {
		t.Fatalf("first: %+v", first)
	}
	clock = clock.Add(1500 * time.Millisecond)
	second := service.Send(Request{SessionID: "run-1", EventName: "plan_presented", Workflow: "branding"})
	if !second.Success {
		t.Fatalf("second: %+v", second)
	}
	if consent := service.Consent(); consent.Status != "known" || consent.TelemetryConsent != "granted" {
		t.Fatalf("consent %+v", consent)
	}
	files, _ := filepath.Glob(filepath.Join(root, "events", "*.json"))
	if len(files) != 2 || !strings.HasPrefix(filepath.Base(files[0]), "20261020T120000123Z_") {
		t.Fatalf("event files %v", files)
	}
	var event logEvent
	data, _ := os.ReadFile(files[1])
	if err := json.Unmarshal(data, &event); err != nil {
		t.Fatal(err)
	}
	attributes := map[string]attributeValue{}
	for _, attribute := range event.Attributes {
		attributes[attribute.Key] = attribute.Value
	}
	if event.EventName != "plan_presented" || *attributes["duration_ms"].IntValue != 1500 || *attributes["duration_since_session_start_ms"].IntValue != 1500 ||
		*attributes["workflow"].StringValue != "branding" || *attributes["clio_version"].StringValue != "creatio-mcp-go/test" ||
		*attributes["event_timestamp"].StringValue != "2026-10-20T12:00:01.6234567+00:00" {
		t.Fatalf("event %s", data)
	}
	firstData, _ := os.ReadFile(files[0])
	if !strings.Contains(string(firstData), `"key": "coding_agent",`) || !strings.Contains(string(firstData), `"string_value": "claude-code"`) {
		t.Fatalf("first event %s", firstData)
	}
	sessions, _ := filepath.Glob(filepath.Join(root, "sessions", "*.json"))
	if len(sessions) != 1 {
		t.Fatalf("sessions %v", sessions)
	}
	withdrawn := service.Withdraw()
	if !withdrawn.Success || withdrawn.EventsPurged != 2 || withdrawn.TelemetryConsent != "denied" {
		t.Fatalf("withdraw %+v", withdrawn)
	}
	if denied := service.Send(Request{SessionID: "run-1", EventName: "workflow_completed", TelemetryConsent: "granted"}); !denied.Success || denied.Status != "consent-denied" {
		t.Fatalf("after withdrawal %+v", denied)
	}
}

// TestFlushUploadsToTheConfiguredEndpointOnly never reaches a real collector: the endpoint is local.
func TestFlushUploadsToTheConfiguredEndpointOnly(t *testing.T) {
	var mu sync.Mutex
	var bodies []map[string]any
	status := http.StatusOK
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("X-Ingest-Key") != "key-1" || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			t.Errorf("headers %v", r.Header)
		}
		data, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(data, &body)
		bodies = append(bodies, body)
		w.WriteHeader(status)
	}))
	defer collector.Close()
	t.Setenv("CLIO_TELEMETRY_ENDPOINT", collector.URL+"/v1/logs")
	t.Setenv("CLIO_TELEMETRY_INGEST_KEY", "key-1")
	t.Setenv("CLIO_TELEMETRY_ENABLED", "")
	service := &Service{Root: t.TempDir(), Version: "test"}
	flusher := &Flusher{Service: service, Options: func() Options { return ResolveOptions(Settings{}) },
		Delay: func(context.Context, time.Duration) bool { return true }}
	service.Send(Request{SessionID: "s", EventName: "workflow_started", TelemetryConsent: "granted"})
	flusher.Flush(context.Background())
	files, _ := filepath.Glob(filepath.Join(service.Root, "events", "*.json"))
	if len(bodies) != 1 || len(files) != 0 {
		t.Fatalf("delivered %d batches, %d files left", len(bodies), len(files))
	}
	records := bodies[0]["resourceLogs"].([]any)[0].(map[string]any)["scopeLogs"].([]any)[0].(map[string]any)["logRecords"].([]any)
	if record := records[0].(map[string]any); record["eventName"] != "workflow_started" || record["severityNumber"] != float64(9) {
		t.Fatalf("record %v", record)
	}
	status = http.StatusServiceUnavailable
	service.Send(Request{SessionID: "s", EventName: "plan_presented"})
	flusher.Flush(context.Background())
	if files, _ := filepath.Glob(filepath.Join(service.Root, "events", "*.json")); len(files) != 1 || len(bodies) != 1+postAttempts {
		t.Fatalf("a transient failure must keep the event (%d files, %d posts)", len(files), len(bodies))
	}
	status = http.StatusBadRequest
	flusher.Flush(context.Background())
	if files, _ := filepath.Glob(filepath.Join(service.Root, "events", "*.json")); len(files) != 0 {
		t.Fatal("a permanent rejection drops the batch")
	}
	t.Setenv("CLIO_TELEMETRY_ENABLED", "false")
	if options := ResolveOptions(Settings{}); options.Endpoint != "" {
		t.Fatalf("disabled uploading still has an endpoint: %+v", options)
	}
	t.Setenv("CLIO_TELEMETRY_ENABLED", "")
	t.Setenv("CLIO_TELEMETRY_ENDPOINT", "http://collector.example/v1/logs")
	if options := ResolveOptions(Settings{}); options.Endpoint != "" {
		t.Fatalf("a non-loopback HTTP endpoint must be refused: %+v", options)
	}
	disabled := false
	t.Setenv("CLIO_TELEMETRY_ENDPOINT", "")
	if options := ResolveOptions(Settings{Enabled: &disabled}); options.Endpoint != "" {
		t.Fatalf("settings telemetry.enabled=false must switch uploading off: %+v", options)
	}
}
