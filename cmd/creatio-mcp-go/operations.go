package main

// Long-running operations, the way clio runs compile-creatio and the restart tools:
//
//   - progress: notifications/progress on the caller's progress token, progress = a running sequence number
//     and message = a stage line, plus a heartbeat "<name> is still running… (~Ns elapsed)" every 15 s
//     (clio's McpProgressHeartbeat; CLIO_MCP_HEARTBEAT_INTERVAL_SECONDS overrides it);
//   - response deadline: when the work outlives 150 s (CLIO_MCP_RESPONSE_DEADLINE_SECONDS) the call answers
//     an in-progress notice and the work goes on in the background; the caller polls the status tool;
//   - cancellation: a notifications/cancelled for the call before the deadline cancels the work's context;
//   - operation registry: compile-status and restart-status read clio's CompileOperationRegistry and
//     RestartOperationRegistry records (same states, same fields), kept per environment for 5 minutes after
//     finishing, at most 50 records, a running one never evicted.
//
// A write tool does:
//
//	op := compileOperations.begin(envs.tenantKey(name), name, operationDetails{PackageName: pkg})
//	result, finished, err := runLongOperation(ctx, "compile-creatio", 0, func(ctx context.Context, stage func(string)) creatio.CommandResult {
//		stage("compiling")
//		result := client.Compile(ctx, ...)
//		compileOperations.finish(op.ID, result.ExitCode, result.Messages)
//		return result
//	})
//	if !finished { return creatio.CommandInfo(clio's in-progress text naming op.ID) }

import (
	"context"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/redact"
)

// progressKey carries the caller's progress reporter on the context of a hidden-tool call.
type progressKey struct{}

type progressFunc func(progress, total float64, message string) error

func withProgress(ctx context.Context, report func(float64, float64, string) error) context.Context {
	if report == nil {
		return ctx
	}
	return context.WithValue(ctx, progressKey{}, progressFunc(report))
}

// progressFrom returns the caller's progress reporter, or nil when the call carries no progress token.
func progressFrom(ctx context.Context) progressFunc {
	report, _ := ctx.Value(progressKey{}).(progressFunc)
	return report
}

const (
	defaultHeartbeatInterval = 15 * time.Second
	defaultResponseDeadline  = 150 * time.Second
	operationIdleTTL         = 5 * time.Minute
	maxOperationRecords      = 50
	// messageTailCap is CompileOperationRegistry.MessageTailCap.
	messageTailCap = 50
)

// durationFromEnv reads clio's override variables: seconds, more than 0 and at most 600.
func durationFromEnv(name string, fallback time.Duration) time.Duration {
	seconds, err := strconv.ParseFloat(strings.TrimSpace(os.Getenv(name)), 64)
	if err != nil || seconds <= 0 || seconds > 600 {
		return fallback
	}
	return time.Duration(seconds * float64(time.Second))
}

// progressChannel numbers the notifications of one call; sends are serialized and a failed send is ignored,
// as clio's ProgressChannel does.
type progressChannel struct {
	mu       sync.Mutex
	report   progressFunc
	sequence int
}

func (c *progressChannel) send(message string) {
	if c == nil || c.report == nil || strings.TrimSpace(message) == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sequence++
	_ = c.report(float64(c.sequence), 0, message)
}

// runLongOperation runs work with stage progress and a heartbeat. finished is false when deadline (0: the
// default) passed first: the work keeps running on a context of its own and the caller answers in-progress.
// err is the caller's context error when the call was cancelled first; the work's context is then cancelled.
func runLongOperation[T any](ctx context.Context, name string, deadline time.Duration,
	work func(ctx context.Context, stage func(string)) T) (result T, finished bool, err error) {
	if deadline <= 0 {
		deadline = durationFromEnv("CLIO_MCP_RESPONSE_DEADLINE_SECONDS", defaultResponseDeadline)
	}
	channel := &progressChannel{report: progressFrom(ctx)}
	// The work outlives the call when the deadline passes, so it must not inherit the call's cancellation;
	// a cancellation that arrives while the call still waits is forwarded below.
	workCtx, cancelWork := context.WithCancel(context.WithoutCancel(ctx))
	done := make(chan T, 1)
	go func() {
		defer cancelWork()
		done <- work(workCtx, channel.send)
	}()
	stopHeartbeat := make(chan struct{})
	defer close(stopHeartbeat)
	if channel.report != nil {
		go heartbeat(channel, name, durationFromEnv("CLIO_MCP_HEARTBEAT_INTERVAL_SECONDS", defaultHeartbeatInterval), stopHeartbeat)
	}
	timer := time.NewTimer(deadline)
	defer timer.Stop()
	select {
	case result = <-done:
		return result, true, nil
	case <-timer.C:
		return result, false, nil
	case <-ctx.Done():
		cancelWork()
		return result, false, ctx.Err()
	}
}

func heartbeat(channel *progressChannel, name string, interval time.Duration, stop <-chan struct{}) {
	if name == "" {
		name = "operation"
	}
	seconds := int(math.Max(1, math.Round(interval.Seconds())))
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for tick := 1; ; tick++ {
		select {
		case <-stop:
			return
		case <-ticker.C:
			channel.send(fmt.Sprintf("%s is still running… (~%ds elapsed)", name, tick*seconds))
		}
	}
}

// operationDetails are the per-kind fields of a record (compile: package or process).
type operationDetails struct {
	PackageName string
	ProcessName string
}

// operationRecord is one tracked operation (CompileOperationRecord / RestartOperationRecord).
type operationRecord struct {
	ID              string
	TenantKey       string
	EnvironmentName string
	Details         operationDetails
	Status          string
	StartedUTC      time.Time
	FinishedUTC     *time.Time
	ExitCode        *int
	MessageTail     []string
}

// operationStore is clio's BoundedOperationStore plus the status names of one registry.
type operationStore struct {
	mu             sync.Mutex
	byID           map[string]*operationRecord
	latestByTenant map[string]string
	succeeded      string
	failed         string
	keepTail       bool
	now            func() time.Time
}

// compileOperations and restartOperations are what compile-status and restart-status read.
var (
	compileOperations = newOperationStore("succeeded", "failed", true)
	restartOperations = newOperationStore("ready", "timedout", false)
)

func newOperationStore(succeeded, failed string, keepTail bool) *operationStore {
	return &operationStore{byID: map[string]*operationRecord{}, latestByTenant: map[string]string{},
		succeeded: succeeded, failed: failed, keepTail: keepTail,
		now: func() time.Time { return time.Now().UTC().Truncate(100 * time.Nanosecond) }}
}

// begin records a running operation and makes it the tenant's latest.
func (s *operationStore) begin(tenantKey, environmentName string, details operationDetails) operationRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.evictIdle(now)
	record := &operationRecord{ID: newOperationID(), TenantKey: tenantKey, EnvironmentName: environmentName,
		Details: details, Status: "running", StartedUTC: now}
	if s.keepTail {
		record.MessageTail = []string{}
	}
	s.byID[record.ID] = record
	s.latestByTenant[tenantKey] = record.ID
	s.evictOverCapacity(record.ID)
	return *record
}

// finish closes an operation: exit code 0 is success, anything else failure. The message tail keeps the last
// 50 messages, redacted first, as compile output carries paths and hosts.
func (s *operationStore) finish(id string, exitCode int, messages []creatio.LogMessage) operationRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	record, ok := s.byID[id]
	if !ok {
		record = &operationRecord{ID: id, StartedUTC: now}
		s.byID[id] = record
	}
	record.Status = s.failed
	if exitCode == 0 {
		record.Status = s.succeeded
	}
	record.FinishedUTC, record.ExitCode = &now, &exitCode
	if s.keepTail {
		if len(messages) > messageTailCap {
			messages = messages[len(messages)-messageTailCap:]
		}
		tail := make([]string, len(messages))
		for i, message := range messages {
			tail[i] = message.Value
		}
		record.MessageTail = redact.All(tail)
	}
	return *record
}

// lookup returns the tenant's latest operation, or the one with the given id when it belongs to the tenant.
func (s *operationStore) lookup(tenantKey, operationID string) (operationRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := strings.TrimSpace(operationID)
	if id == "" {
		id = s.latestByTenant[tenantKey]
	}
	record, ok := s.byID[id]
	if !ok || record.TenantKey != tenantKey {
		return operationRecord{}, false
	}
	return *record, true
}

func (s *operationStore) running(record *operationRecord) bool { return record.Status == "running" }

func lastActivity(record *operationRecord) time.Time {
	if record.FinishedUTC != nil {
		return *record.FinishedUTC
	}
	return record.StartedUTC
}

func (s *operationStore) evictIdle(now time.Time) {
	for id, record := range s.byID {
		if !s.running(record) && now.Sub(lastActivity(record)) > operationIdleTTL {
			s.remove(id)
		}
	}
}

// evictOverCapacity drops the least recently active finished records; running ones stay even over the cap.
func (s *operationStore) evictOverCapacity(justAdded string) {
	for len(s.byID) > maxOperationRecords {
		victim := ""
		var oldest time.Time
		for id, record := range s.byID {
			if s.running(record) || id == justAdded {
				continue
			}
			if victim == "" || lastActivity(record).Before(oldest) {
				victim, oldest = id, lastActivity(record)
			}
		}
		if victim == "" {
			return
		}
		s.remove(victim)
	}
}

func (s *operationStore) remove(id string) {
	delete(s.byID, id)
	for tenant, latest := range s.latestByTenant {
		if latest == id {
			delete(s.latestByTenant, tenant)
		}
	}
}

// utcTime is a timestamp in System.Text.Json's DateTime form (UTC, up to 7 fractional digits, trailing zeros
// dropped), which clio's status responses carry.
type utcTime time.Time

func (t utcTime) MarshalJSON() ([]byte, error) {
	return []byte(`"` + time.Time(t).UTC().Format("2006-01-02T15:04:05.9999999Z") + `"`), nil
}

func utcTimePointer(t *time.Time) *utcTime {
	if t == nil {
		return nil
	}
	converted := utcTime(*t)
	return &converted
}
