package creatio

// The restart request and the readiness wait behind restart-by-environment-name and restart-by-credentials:
// clio's RestartCommand (a RemoteCommand posting "{}" to AppInstallerService UnloadAppDomain, or RestartApp
// on .NET Core) and ServerReadinessWaiter with RequireAuthenticatedReadiness: after an initial delay the
// health-check ping is probed, and once it answers, an authenticated DataService round-trip must return a
// genuine answer before the application counts as serving.

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"strings"
	"time"
)

const (
	pkgWriteRestartFrameworkRoute = "ServiceModel/AppInstallerService.svc/UnloadAppDomain"
	pkgWriteRestartNetCoreRoute   = "ServiceModel/AppInstallerService.svc/RestartApp"
	// pkgWriteRestartTimeout is the RestartOptions TimeOut the MCP tools set (30 000 ms).
	pkgWriteRestartTimeout = 30 * time.Second
	// pkgWriteReadinessQuery is ServerReadinessWaiter.AuthenticatedReadinessQuery.
	pkgWriteReadinessQuery = `{"rootSchemaName":"SysSettings","operationType":0,` +
		`"columns":{"items":{"Id":{"expression":{"expressionType":0,"columnPath":"Id"}}}},"rowCount":1}`
	pkgWriteMinProbeTimeout     = time.Second
	pkgWriteDefaultProbeTimeout = 100 * time.Second
	pkgWriteMaxAuthRejections   = 2
)

// The waits clio uses; tests shorten them. pkgWriteRestartAttempts and pkgWriteRestartRetryDelay are
// RemoteCommandOptions' MaxAttempts (3) and RetryDelay (1 s); the readiness delays are
// ServerReadinessOptions' InitialDelay (10 s) and PollInterval (5 s).
var (
	pkgWriteRestartAttempts       = 3
	pkgWriteRestartRetryDelay     = time.Second
	pkgWriteReadinessInitialDelay = 10 * time.Second
	pkgWriteReadinessPollInterval = 5 * time.Second
)

// pkgWriteRestartDone is RemoteCommand's success line for the restart-web-app verb.
const pkgWriteRestartDone = "Done restart-web-app"

// RestartApplication is RestartCommand.Execute with WaitReady off: the restart request alone.
func (c *Client) RestartApplication(ctx context.Context) CommandResult {
	route := pkgWriteRestartFrameworkRoute
	if c.config.IsNetCore {
		route = pkgWriteRestartNetCoreRoute
	}
	var err error
	for attempt := 1; attempt <= pkgWriteRestartAttempts; attempt++ {
		if _, err = c.callService(ctx, serviceCall{Route: route, Body: []byte("{}"), Timeout: pkgWriteRestartTimeout}); err == nil {
			return NewCommandResult(0, "Info", pkgWriteRestartDone)
		}
		if attempt == pkgWriteRestartAttempts || ctx.Err() != nil {
			break
		}
		select {
		case <-ctx.Done():
		case <-time.After(pkgWriteRestartRetryDelay):
		}
	}
	return CommandFailure(err.Error())
}

type pkgWriteReadiness int

const (
	pkgWriteNotReady pkgWriteReadiness = iota
	pkgWriteReady
	pkgWriteAuthRejected
)

// WaitForReadiness is ServerReadinessWaiter.WaitForReady with RequireAuthenticatedReadiness. timeout is
// clamped to 1..3600 s, as RestartCommand.WaitForReadiness clamps it. The waiter's log lines are not part of
// any answer (clio writes them outside the captured command), so only the verdict is returned.
func (c *Client) WaitForReadiness(ctx context.Context, timeout time.Duration) bool {
	timeout = min(max(timeout, time.Second), time.Hour)
	if !pkgWriteSleep(ctx, pkgWriteReadinessInitialDelay) {
		return false
	}
	deadline := time.Now().Add(timeout)
	rejections := 0
	for {
		probeTimeout := pkgWriteProbeTimeout(deadline)
		if c.pkgWriteHealthCheck(ctx, probeTimeout) {
			switch c.pkgWriteApplicationLayer(ctx, probeTimeout) {
			case pkgWriteReady:
				return true
			case pkgWriteAuthRejected:
				rejections++
				if rejections >= pkgWriteMaxAuthRejections {
					return false
				}
			default:
				rejections = 0
			}
		}
		if !time.Now().Before(deadline) {
			return false
		}
		if !pkgWriteSleep(ctx, pkgWriteReadinessPollInterval) || !time.Now().Before(deadline) {
			return false
		}
	}
}

func pkgWriteSleep(ctx context.Context, wait time.Duration) bool {
	if wait <= 0 {
		return ctx.Err() == nil
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

// pkgWriteProbeTimeout bounds each probe by what is left of the budget, between 1 s and 100 s.
func pkgWriteProbeTimeout(deadline time.Time) time.Duration {
	remaining := time.Until(deadline)
	return time.Duration(math.Max(float64(pkgWriteMinProbeTimeout), math.Min(float64(remaining), float64(pkgWriteDefaultProbeTimeout))))
}

// pkgWriteHealthCheck is HealthCheckCommand's single probe: an anonymous GET of the ping endpoint
// (/0/api/HealthCheck/Ping on .NET Framework, /api/HealthCheck/Ping on .NET Core), redirects not followed,
// healthy only on a 2xx answer.
func (c *Client) pkgWriteHealthCheck(ctx context.Context, timeout time.Duration) bool {
	path := "/0/api/HealthCheck/Ping"
	if c.config.IsNetCore {
		path = "/api/HealthCheck/Ping"
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, strings.TrimRight(c.config.BaseURL, "/")+path, nil)
	if err != nil {
		return false
	}
	probe := &http.Client{Transport: c.http.Transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := probe.Do(request)
	if err != nil {
		return false
	}
	_ = response.Body.Close()
	return response.StatusCode >= 200 && response.StatusCode < 300
}

// pkgWriteApplicationLayer is ExecuteAuthenticatedRoundTrip: one authenticated SelectQuery that must come back
// as a genuine DataService answer. A rejected login is told apart from a warm-up stall.
func (c *Client) pkgWriteApplicationLayer(ctx context.Context, timeout time.Duration) pkgWriteReadiness {
	payload, err := c.callService(ctx, serviceCall{Route: "DataService/json/SyncReply/SelectQuery", Body: []byte(pkgWriteReadinessQuery), Timeout: timeout})
	if err != nil {
		if pkgWriteIsAuthRejection(err) {
			return pkgWriteAuthRejected
		}
		return pkgWriteNotReady
	}
	if pkgWriteGenuineAnswer(payload) {
		return pkgWriteReady
	}
	return pkgWriteNotReady
}

// pkgWriteIsAuthRejection recognizes a login the application answered and refused.
func pkgWriteIsAuthRejection(err error) bool {
	text := err.Error()
	return strings.Contains(text, "rejected credentials") || strings.Contains(text, "OAuth token endpoint returned HTTP 4")
}

// pkgWriteGenuineAnswer is IsGenuineAuthenticatedJsonAnswer: a JSON object carrying one of DataService's
// answer members (rows, rowsAffected, success, errorInfo; names compared ignoring case).
func pkgWriteGenuineAnswer(payload []byte) bool {
	var answer map[string]json.RawMessage
	if json.Unmarshal(payload, &answer) != nil {
		return false
	}
	for name := range answer {
		switch strings.ToLower(name) {
		case "rows", "rowsaffected", "success", "errorinfo":
			return true
		}
	}
	return false
}
