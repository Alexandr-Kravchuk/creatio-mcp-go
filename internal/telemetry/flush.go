package telemetry

// clio's TelemetryFlushService and TelemetryFlushScheduler: after an event is recorded, the outbox is pruned
// and uploaded in the background, at most one run at a time, as OTLP/JSON in batches of 50, only while
// consent is granted and an endpoint is configured; delivered or permanently rejected batches are deleted.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	endpointVariable  = "CLIO_TELEMETRY_ENDPOINT"
	ingestKeyVariable = "CLIO_TELEMETRY_INGEST_KEY"
	enabledVariable   = "CLIO_TELEMETRY_ENABLED"
	// ProductionEndpoint is clio's default collector.
	ProductionEndpoint = "https://caadt-telemetry.creatio.com/v1/logs"
	maxBatchSize       = 50
	maxSpoolFiles      = 500
	maxSpoolAge        = 30 * 24 * time.Hour
	maxTmpAge          = 24 * time.Hour
	postAttempts       = 3
	postTimeout        = 30 * time.Second
	flushRunTimeout    = 2 * time.Minute
)

// Settings is clio's "telemetry" settings section.
type Settings struct {
	Enabled   *bool  `json:"enabled"`
	Endpoint  string `json:"endpoint"`
	IngestKey string `json:"ingest-key"`
}

// Options is clio's TelemetryFlushOptions.
type Options struct {
	Endpoint  string
	IngestKey string
}

// ReadSettings reads the telemetry section of a clio appsettings.json; a missing or broken file has none.
func ReadSettings(settingsPath string) Settings {
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		return Settings{}
	}
	var file struct {
		Telemetry *Settings `json:"telemetry"`
	}
	if json.Unmarshal(bytes.TrimPrefix(data, []byte("\xEF\xBB\xBF")), &file) != nil || file.Telemetry == nil {
		return Settings{}
	}
	return *file.Telemetry
}

// ResolveOptions is clio's TelemetryFlushOptionsProvider.Resolve.
func ResolveOptions(settings Settings) Options {
	if value := os.Getenv(enabledVariable); strings.TrimSpace(value) != "" {
		if enabled, err := strconv.ParseBool(strings.TrimSpace(value)); err == nil {
			if !enabled {
				return Options{}
			}
		} else if settings.Enabled != nil && !*settings.Enabled {
			return Options{}
		}
	} else if settings.Enabled != nil && !*settings.Enabled {
		return Options{}
	}
	endpoint := firstNonBlank(os.Getenv(endpointVariable), settings.Endpoint, ProductionEndpoint)
	ingestKey := firstNonBlank(os.Getenv(ingestKeyVariable), settings.IngestKey)
	if endpoint != "" && !validEndpoint(endpoint) {
		endpoint = ""
	}
	return Options{Endpoint: endpoint, IngestKey: ingestKey}
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if !blank(value) {
			return value
		}
	}
	return ""
}

func validEndpoint(endpoint string) bool {
	parsed, err := url.Parse(endpoint)
	if err != nil || !parsed.IsAbs() || parsed.Host == "" {
		return false
	}
	if parsed.Scheme == "https" {
		return true
	}
	host := strings.Trim(parsed.Hostname(), "[]")
	ip := net.ParseIP(host)
	return parsed.Scheme == "http" && (strings.EqualFold(host, "localhost") || (ip != nil && ip.IsLoopback()))
}

// Flusher is clio's TelemetryFlushScheduler plus TelemetryFlushService.
type Flusher struct {
	Service *Service
	// Options resolves the endpoint at flush time.
	Options func() Options
	// Client posts batches; nil means a client with clio's 30 s timeout.
	Client *http.Client
	// Delay waits between retries; nil means a real sleep.
	Delay func(context.Context, time.Duration) bool

	gate    sync.Mutex
	running bool
	pending sync.WaitGroup
}

// TryScheduleFlush starts one background flush unless one is already running (clio's gate.Wait(0)).
func (f *Flusher) TryScheduleFlush() {
	f.gate.Lock()
	if f.running {
		f.gate.Unlock()
		return
	}
	f.running = true
	f.pending.Add(1)
	f.gate.Unlock()
	go func() {
		defer func() {
			f.gate.Lock()
			f.running = false
			f.gate.Unlock()
			f.pending.Done()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), flushRunTimeout)
		defer cancel()
		f.Flush(ctx)
	}()
}

// Drain waits for a running flush (tests and shutdown).
func (f *Flusher) Drain() { f.pending.Wait() }

func (f *Flusher) client() *http.Client {
	if f.Client != nil {
		return f.Client
	}
	return &http.Client{Timeout: postTimeout}
}

// Flush is clio's FlushCoreAsync; it never fails.
func (f *Flusher) Flush(ctx context.Context) {
	service := f.Service
	pruneSessions(service.sessionsDir(), service.now())
	events := service.eventsDir()
	if _, err := os.Stat(events); err != nil {
		return
	}
	pruneTemporary(events, service.now())
	spool := pruneSpool(listSpool(events), service.now())
	if len(spool) == 0 {
		return
	}
	options := f.Options()
	if options.Endpoint == "" || service.Consent().TelemetryConsent != ConsentGranted {
		return
	}
	for offset := 0; offset < len(spool) && ctx.Err() == nil; {
		if service.Consent().TelemetryConsent != ConsentGranted {
			return
		}
		end := min(offset+maxBatchSize, len(spool))
		files := spool[offset:end]
		offset = end
		var paths []string
		var batch []logEvent
		for _, path := range files {
			data, err := os.ReadFile(path)
			var event logEvent
			if err != nil || len(bytes.TrimSpace(data)) == 0 || json.Unmarshal(data, &event) != nil {
				_ = os.Remove(path)
				continue
			}
			paths = append(paths, path)
			batch = append(batch, event)
		}
		if len(batch) == 0 {
			continue
		}
		switch f.postWithRetry(ctx, options, otlpPayload(batch)) {
		case delivered, permanentlyRejected:
			for _, path := range paths {
				_ = os.Remove(path)
			}
		default:
			return
		}
	}
}

type postOutcome int

const (
	delivered postOutcome = iota
	transientFailure
	permanentlyRejected
)

func (f *Flusher) postWithRetry(ctx context.Context, options Options, payload []byte) postOutcome {
	for attempt := 1; attempt <= postAttempts; attempt++ {
		outcome := f.postOnce(ctx, options, payload)
		if outcome != transientFailure || attempt == postAttempts {
			return outcome
		}
		backoff := time.Duration(1<<(attempt-1))*time.Second + time.Duration(rand.Intn(500))*time.Millisecond
		if !f.delay(ctx, backoff) {
			return transientFailure
		}
	}
	return transientFailure
}

func (f *Flusher) delay(ctx context.Context, wait time.Duration) bool {
	if f.Delay != nil {
		return f.Delay(ctx, wait)
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (f *Flusher) postOnce(ctx context.Context, options Options, payload []byte) postOutcome {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, options.Endpoint, bytes.NewReader(payload))
	if err != nil {
		return permanentlyRejected
	}
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	if !blank(options.IngestKey) {
		request.Header.Set("X-Ingest-Key", options.IngestKey)
	}
	response, err := f.client().Do(request)
	if err != nil {
		return transientFailure
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64*1024))
	switch {
	case response.StatusCode >= 200 && response.StatusCode <= 299:
		return delivered
	case response.StatusCode >= 500 || response.StatusCode == http.StatusRequestTimeout || response.StatusCode == http.StatusTooManyRequests:
		return transientFailure
	}
	return permanentlyRejected
}

func listSpool(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, entry := range entries {
		if entry.Type().IsRegular() && strings.HasSuffix(entry.Name(), ".json") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	paths := make([]string, len(names))
	for i, name := range names {
		paths[i] = filepath.Join(dir, name)
	}
	return paths
}

// fileTimestamp parses clio's yyyyMMddTHHmmssfffZ file-name prefix.
func fileTimestamp(name string) (time.Time, bool) {
	if len(name) <= 18 || name[18] != 'Z' {
		return time.Time{}, false
	}
	parsed, err := time.Parse("20060102T150405", name[:15])
	if err != nil {
		return time.Time{}, false
	}
	milliseconds, err := strconv.Atoi(name[15:18])
	if err != nil {
		return time.Time{}, false
	}
	return parsed.Add(time.Duration(milliseconds) * time.Millisecond), true
}

func pruneSpool(spool []string, now time.Time) []string {
	cutoff := now.Add(-maxSpoolAge)
	var remaining []string
	for _, path := range spool {
		written, ok := fileTimestamp(filepath.Base(path))
		if !ok || written.Before(cutoff) {
			_ = os.Remove(path)
			continue
		}
		remaining = append(remaining, path)
	}
	if excess := len(remaining) - maxSpoolFiles; excess > 0 {
		for _, path := range remaining[:excess] {
			_ = os.Remove(path)
		}
		remaining = remaining[excess:]
	}
	return remaining
}

func pruneTemporary(dir string, now time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := now.Add(-maxTmpAge)
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".tmp") {
			continue
		}
		if written, ok := fileTimestamp(entry.Name()); !ok || written.Before(cutoff) {
			_ = os.Remove(filepath.Join(dir, entry.Name()))
		}
	}
}

func pruneSessions(dir string, now time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := now.Add(-maxSpoolAge)
	type session struct {
		path    string
		written time.Time
	}
	var live []session
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		info, err := entry.Info()
		if err != nil || info.ModTime().Before(cutoff) {
			_ = os.Remove(path)
			continue
		}
		live = append(live, session{path, info.ModTime()})
	}
	if excess := len(live) - maxSpoolFiles; excess > 0 {
		sort.Slice(live, func(i, j int) bool { return live[i].written.Before(live[j].written) })
		for _, item := range live[:excess] {
			_ = os.Remove(item.path)
		}
	}
}

type otlpValue struct {
	StringValue *string `json:"stringValue,omitempty"`
	IntValue    *string `json:"intValue,omitempty"`
}

type otlpKeyValue struct {
	Key   string    `json:"key"`
	Value otlpValue `json:"value"`
}

// otlpPayload is clio's BuildOtlpPayload (camelCase, nulls omitted, service.name "clio").
func otlpPayload(events []logEvent) []byte {
	type record struct {
		TimeUnixNano   string         `json:"timeUnixNano"`
		SeverityNumber *int           `json:"severityNumber,omitempty"`
		SeverityText   string         `json:"severityText"`
		Attributes     []otlpKeyValue `json:"attributes"`
		EventName      string         `json:"eventName"`
	}
	records := make([]record, 0, len(events))
	for _, event := range events {
		var severity *int
		if event.SeverityText == "INFO" {
			nine := 9
			severity = &nine
		}
		attributes := make([]otlpKeyValue, 0, len(event.Attributes))
		for _, item := range event.Attributes {
			value := otlpValue{StringValue: item.Value.StringValue}
			if item.Value.IntValue != nil {
				text := strconv.FormatInt(*item.Value.IntValue, 10)
				value.IntValue = &text
			}
			attributes = append(attributes, otlpKeyValue{Key: item.Key, Value: value})
		}
		records = append(records, record{TimeUnixNano: strconv.FormatInt(event.TimeUnixNano, 10), SeverityNumber: severity,
			SeverityText: event.SeverityText, Attributes: attributes, EventName: event.EventName})
	}
	type scope struct {
		Name string `json:"name"`
	}
	type scopeLogs struct {
		Scope      scope    `json:"scope"`
		LogRecords []record `json:"logRecords"`
	}
	type resource struct {
		Attributes []otlpKeyValue `json:"attributes"`
	}
	type resourceLogs struct {
		Resource  resource    `json:"resource"`
		ScopeLogs []scopeLogs `json:"scopeLogs"`
	}
	service := "clio"
	data, _ := json.Marshal(struct {
		ResourceLogs []resourceLogs `json:"resourceLogs"`
	}{[]resourceLogs{{Resource: resource{[]otlpKeyValue{{Key: "service.name", Value: otlpValue{StringValue: &service}}}},
		ScopeLogs: []scopeLogs{{Scope: scope{service}, LogRecords: records}}}}})
	return data
}
