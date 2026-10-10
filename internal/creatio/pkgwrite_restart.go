package creatio

// The restart request and the readiness wait behind restart-by-environment-name and restart-by-credentials:
// clio's RestartCommand (a RemoteCommand posting "{}" to AppInstallerService UnloadAppDomain, or RestartApp
// on .NET Core) and ServerReadinessWaiter with RequireAuthenticatedReadiness: after an initial delay the
// health-check ping is probed, and once it answers, an authenticated DataService round-trip must return a
// genuine answer before the application counts as serving.

import (
	"context"
	"encoding/json"
	"fmt"
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

// WaitForReadiness is ServerReadinessWaiter.WaitForReady with RequireAuthenticatedReadiness, as the restart
// tools use it. timeout is clamped to 1..3600 s, as RestartCommand.WaitForReadiness clamps it. The waiter's
// log lines are not part of the restart answer (clio writes them outside the captured command).
func (c *Client) WaitForReadiness(ctx context.Context, timeout time.Duration) bool {
	return c.pkgWriteWaitReady(ctx, min(max(timeout, time.Second), time.Hour), true, nil)
}

// pkgWriteWaitReady is ServerReadinessWaiter.WaitForReady. authenticated adds the DataService round-trip
// after the liveness probe; log, when given, receives the waiter's and the health check's lines.
func (c *Client) pkgWriteWaitReady(ctx context.Context, timeout time.Duration, authenticated bool, log *appWriteLog) bool {
	if log != nil {
		log.info(fmt.Sprintf("Waiting %.0f seconds for server to start...", pkgWriteReadinessInitialDelay.Seconds()))
	}
	if !pkgWriteSleep(ctx, pkgWriteReadinessInitialDelay) {
		return false
	}
	deadline := time.Now().Add(timeout)
	rejections := 0
	for attempt := 1; ; attempt++ {
		probeTimeout := pkgWriteProbeTimeout(deadline)
		if c.pkgWriteHealthCheck(ctx, probeTimeout, log) {
			if !authenticated {
				if log != nil {
					log.info(fmt.Sprintf("Server is ready after %d attempt(s).", attempt))
				}
				return true
			}
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
			break
		}
		if log != nil {
			log.info(fmt.Sprintf("Waiting for server to become ready... (attempt %d). Next check in %.0f seconds.",
				attempt, pkgWriteReadinessPollInterval.Seconds()))
		}
		if !pkgWriteSleep(ctx, pkgWriteReadinessPollInterval) {
			return false
		}
		if !time.Now().Before(deadline) {
			break
		}
	}
	if log != nil {
		log.warning(fmt.Sprintf("Server did not become ready within %.0f seconds.", timeout.Seconds()))
	}
	return false
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
// (/0/api/HealthCheck/Ping "WebHost" on .NET Framework, /api/HealthCheck/Ping "WebAppLoader" on .NET Core),
// redirects not followed, healthy only on a 2xx answer. log, when given, receives the command's lines.
func (c *Client) pkgWriteHealthCheck(ctx context.Context, timeout time.Duration, log *appWriteLog) bool {
	name, path := "WebHost", "/0/api/HealthCheck/Ping"
	if c.config.IsNetCore {
		name, path = "WebAppLoader", "/api/HealthCheck/Ping"
	}
	uri := strings.TrimRight(c.config.BaseURL, "/") + path
	if log != nil {
		log.info(fmt.Sprintf("Checking %s %s ...", name, uri))
	}
	failure := func(message string) bool {
		if log != nil {
			log.error("\tError: " + message)
		}
		return false
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, uri, nil)
	if err != nil {
		return failure(err.Error())
	}
	probe := &http.Client{Transport: c.http.Transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := probe.Do(request)
	if err != nil {
		if requestCtx.Err() != nil {
			return failure(fmt.Sprintf("request timed out after %d ms", timeout.Milliseconds()))
		}
		return failure(err.Error())
	}
	_ = response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		status := fmt.Sprintf("%d %s", response.StatusCode, http.StatusText(response.StatusCode))
		if response.StatusCode >= 300 && response.StatusCode < 400 {
			target := ""
			if location := response.Header.Get("Location"); location != "" {
				target = " to " + location
			}
			status += " — the health-check endpoint answered a redirect" + target + " instead of a health " +
				"response (the application is not serving its own health endpoint; commonly a login " +
				"redirect or a scheme/host mismatch in the registered environment uri)"
		}
		return failure(status)
	}
	if log != nil {
		log.info("\t" + name + " - OK")
	}
	return true
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
