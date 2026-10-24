package creatio

// clio 8.1.0.142's EnvironmentRuntimeDetectionService, which reg-web-app runs when a registration names a
// uri but not is-net-core: three probes per runtime (the health check, an authenticated SelectQuery when
// credentials exist, the login page), decided by the same rules and refused with the same diagnostics.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// runtimeProbeTimeout is both the probe HttpClient's timeout and the SelectQuery probe's.
const runtimeProbeTimeout = 10 * time.Second

// runtimeProbeQuery is the SelectQuery body clio's probe sends (System.Text.Json of its anonymous object).
const runtimeProbeQuery = `{"rootSchemaName":"SysAdminUnit","operationType":0,"allColumns":false,"isDistinct":false,"ignoreDisplayValues":false,"rowCount":1,"rowsOffset":-1,"isPageable":false,"conditionalValues":null,"isHierarchical":false,"hierarchicalMaxDepth":0,"hierarchicalColumnFiltersValue":{"filterType":6,"isEnabled":true,"items":{},"logicalOperation":0,"trimDateTimeParameterToDate":false},"hierarchicalColumnName":null,"hierarchicalColumnValue":null,"hierarchicalFullDataLoad":false,"useLocalization":true,"useRecordDeactivation":false,"columns":{"items":{"Id":{"expression":{"expressionType":0,"columnPath":"Id"},"orderDirection":0,"orderPosition":-1,"isVisible":true}}},"filters":{"filterType":6,"isEnabled":true,"trimDateTimeParameterToDate":false,"logicalOperation":0,"items":{}},"__type":"Terrasoft.Nui.ServiceModel.DataContract.SelectQuery","queryKind":0,"serverESQCacheParameters":{"cacheLevel":0,"cacheGroup":"","cacheItemName":""},"queryOptimize":false,"useMetrics":false,"querySource":0}`

type probeAttempt struct {
	succeeded bool
	message   string
	status    int // 0: no HTTP answer
}

type runtimeProbe struct {
	healthURL, serviceURL, uiURL string
	health, service, ui          probeAttempt
}

// DetectRuntime reports whether the site runs on .NET Core / NET8. The error is clio's refusal text.
func DetectRuntime(ctx context.Context, target RegistrationTarget) (bool, error) {
	if strings.TrimSpace(target.URI) == "" {
		return false, errors.New("Cannot auto-detect the Creatio runtime because the environment URI is missing. Rerun reg-web-app with --IsNetCore true or --IsNetCore false.")
	}
	target.URI = strings.TrimRight(target.URI, "/")
	canAuthenticate := (strings.TrimSpace(target.Login) != "" && strings.TrimSpace(target.Password) != "") ||
		(strings.TrimSpace(target.ClientID) != "" && strings.TrimSpace(target.ClientSecret) != "" && strings.TrimSpace(target.AuthAppURI) != "")
	core := probeRuntime(ctx, target, true, canAuthenticate)
	framework := probeRuntime(ctx, target, false, canAuthenticate)
	if !canAuthenticate {
		if resolved, ok := resolveByMarkers(core, framework); ok {
			return resolved, nil
		}
		if core.health.succeeded != framework.health.succeeded {
			return core.health.succeeded, nil
		}
		if message, ok := siteUnavailable(target.URI, core, framework); ok {
			return false, errors.New(message)
		}
		return false, errors.New("Unable to auto-detect the Creatio runtime because no credentials were provided and the unauthenticated probes were inconclusive. " +
			probeSummary(core, framework) + " Provide -l/-p or OAuth settings to enable the authenticated SelectQuery probe, or rerun reg-web-app with --IsNetCore true or --IsNetCore false.")
	}
	switch {
	case core.service.succeeded && !framework.service.succeeded:
		return true, nil
	case framework.service.succeeded && !core.service.succeeded:
		return false, nil
	case core.service.succeeded && framework.service.succeeded:
		if resolved, ok := resolveByMarkers(core, framework); ok {
			return resolved, nil
		}
		return false, errors.New("Unable to auto-detect the Creatio runtime because both .NET Core / NET8 and .NET Framework service probes succeeded. " +
			probeSummary(core, framework) + " Rerun reg-web-app with --IsNetCore true or --IsNetCore false to override detection.")
	}
	if resolved, ok := resolveByMarkers(core, framework); ok {
		return resolved, nil
	}
	if message, ok := siteUnavailable(target.URI, core, framework); ok {
		return false, errors.New(message)
	}
	return false, errors.New("Unable to auto-detect the Creatio runtime. " + probeSummary(core, framework) +
		" Rerun reg-web-app with --IsNetCore true or --IsNetCore false to override detection.")
}

func probeRuntime(ctx context.Context, target RegistrationTarget, isNetCore, canAuthenticate bool) runtimeProbe {
	base := strings.TrimRight(target.URI, "/")
	prefix := "/0"
	if isNetCore {
		prefix = ""
	}
	probe := runtimeProbe{healthURL: base + prefix + "/api/HealthCheck/Ping", serviceURL: base + prefix + "/DataService/json/SyncReply/SelectQuery"}
	if isNetCore {
		probe.uiURL = base + "/Login/Login.html"
	} else {
		probe.uiURL = base + "/0/Login/NuiLogin.aspx"
	}
	probe.health = httpGetProbe(ctx, probe.healthURL)
	if canAuthenticate {
		probed := target
		probed.URI, probed.IsNetCore = base, isNetCore
		probe.service = serviceProbe(ctx, probed, probe.serviceURL)
	} else {
		probe.service = probeAttempt{message: "skipped because credentials are missing"}
	}
	probe.ui = httpGetProbe(ctx, probe.uiURL)
	return probe
}

// httpGetProbe is one unauthenticated GET that follows no redirect: 2xx and 3xx mean the route is served.
func httpGetProbe(ctx context.Context, target string) probeAttempt {
	client := &http.Client{Transport: RegistrationTransport, Timeout: runtimeProbeTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return probeAttempt{message: err.Error()}
	}
	response, err := client.Do(request)
	if err != nil {
		return probeAttempt{message: socketSentence(err)}
	}
	_ = response.Body.Close()
	if response.StatusCode >= 200 && response.StatusCode < 400 {
		return probeAttempt{succeeded: true, status: response.StatusCode}
	}
	return probeAttempt{status: response.StatusCode,
		message: fmt.Sprintf("The remote server returned an error: (%d) %s.", response.StatusCode, reasonPhrase(response))}
}

// serviceProbe logs in and sends the SelectQuery once; a usable answer is success:true.
func serviceProbe(ctx context.Context, target RegistrationTarget, serviceURL string) probeAttempt {
	session, err := registrationLogin(ctx, target, runtimeProbeTimeout)
	if err != nil {
		return probeAttempt{message: BaseMessage(err)}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, serviceURL, bytes.NewReader([]byte(runtimeProbeQuery)))
	if err != nil {
		return probeAttempt{message: err.Error()}
	}
	request.Header.Set("Content-Type", "application/json")
	if session.csrf != "" {
		request.Header.Set("BPMCSRF", session.csrf)
	}
	if session.bearer != "" {
		request.Header.Set("Authorization", "Bearer "+session.bearer)
	}
	response, err := session.client.Do(request)
	if err != nil {
		return probeAttempt{message: socketSentence(err)}
	}
	payload, readErr := readResponseLimit(response, maxResponseBytes)
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return probeAttempt{message: fmt.Sprintf("The remote server returned an error: (%d) %s.", response.StatusCode, reasonPhrase(response))}
	}
	if readErr != nil {
		return probeAttempt{message: readErr.Error()}
	}
	if strings.TrimSpace(string(payload)) == "" {
		return probeAttempt{message: "SelectQuery returned an empty response."}
	}
	var answer map[string]json.RawMessage
	if err := json.Unmarshal(payload, &answer); err != nil {
		return probeAttempt{message: err.Error()}
	}
	var success bool
	for key, value := range answer {
		if strings.EqualFold(key, "success") {
			_ = json.Unmarshal(value, &success)
		}
	}
	if success {
		return probeAttempt{succeeded: true}
	}
	message := "SelectQuery failed."
	for key, value := range answer {
		if !strings.EqualFold(key, "errorInfo") {
			continue
		}
		var info map[string]json.RawMessage
		if json.Unmarshal(value, &info) == nil {
			for name, raw := range info {
				var text *string
				if strings.EqualFold(name, "message") && json.Unmarshal(raw, &text) == nil && text != nil {
					message = *text
				}
			}
		}
	}
	return probeAttempt{message: message}
}

func provesAbsence(attempt probeAttempt) bool { return attempt.status == 404 || attempt.status == 410 }
func didNotAnswer(attempt probeAttempt) bool  { return !attempt.succeeded && attempt.status == 0 }
func serverError(attempt probeAttempt) bool   { return attempt.status >= 500 }
func wasNotServed(attempt probeAttempt) bool {
	return !attempt.succeeded && (attempt.status == 0 || attempt.status >= 500 || provesAbsence(attempt))
}

// resolveByMarkers: one answering login page names the runtime; a 404/410 page decides only when the other
// runtime's page got no HTTP answer at all.
func resolveByMarkers(core, framework runtimeProbe) (bool, bool) {
	if core.ui.succeeded != framework.ui.succeeded {
		return core.ui.succeeded, true
	}
	if provesAbsence(core.ui) && didNotAnswer(framework.ui) {
		return false, true
	}
	if provesAbsence(framework.ui) && didNotAnswer(core.ui) {
		return true, true
	}
	return false, false
}

// siteUnavailable is the diagnostic for a site that served none of the direct probes.
func siteUnavailable(baseURI string, core, framework runtimeProbe) (string, bool) {
	attempts := []probeAttempt{core.health, core.ui, framework.health, framework.ui}
	for _, attempt := range attempts {
		if !wasNotServed(attempt) {
			return "", false
		}
	}
	authority := baseURI
	if parsed, err := url.Parse(baseURI); err == nil && parsed.IsAbs() && parsed.Host != "" {
		authority = parsed.Host
	}
	summary := probeSummary(core, framework)
	all := func(test func(probeAttempt) bool) bool {
		for _, attempt := range attempts {
			if !test(attempt) {
				return false
			}
		}
		return true
	}
	switch {
	case all(func(attempt probeAttempt) bool { return attempt.status == 0 }):
		return "Unable to auto-detect the Creatio runtime because the host '" + authority + "' could not be reached from this machine. " +
			"Verify the URL, DNS/VPN connectivity, and that the site is accessible, then rerun reg-web-app. " + summary, true
	case all(serverError):
		return "Unable to auto-detect the Creatio runtime because the site at '" + authority + "' is not serving requests: every probe answered an HTTP server error. " +
			"Verify that the Creatio application is started and finished warming up, then rerun reg-web-app. " + summary, true
	case all(provesAbsence):
		absence := "every probe answered 404"
		if !all(func(attempt probeAttempt) bool { return attempt.status == 404 }) {
			absence = "every probe reported the route as absent (answered " + observedOutcomes(attempts) + ")"
		}
		return "Unable to auto-detect the Creatio runtime because nothing at '" + baseURI + "' serves a Creatio route: " + absence +
			". Verify the application path in the URL and that the application is still deployed, then rerun reg-web-app. " + summary, true
	}
	return "Unable to auto-detect the Creatio runtime because the site at '" + authority + "' served none of the probed routes (observed: " +
		observedOutcomes(attempts) + "). Verify the application path in the URL and that the Creatio application is started and finished warming up, then rerun reg-web-app. " + summary, true
}

func observedOutcomes(attempts []probeAttempt) string {
	seen := map[int]bool{}
	var codes []int
	noResponse := false
	for _, attempt := range attempts {
		if attempt.status == 0 {
			noResponse = true
		} else if !seen[attempt.status] {
			seen[attempt.status] = true
			codes = append(codes, attempt.status)
		}
	}
	sort.Ints(codes)
	var outcomes []string
	for _, code := range codes {
		outcomes = append(outcomes, fmt.Sprintf("HTTP %d", code))
	}
	if noResponse {
		outcomes = append(outcomes, "no response")
	}
	return strings.Join(outcomes, ", ")
}

func describeProbe(attempt probeAttempt) string {
	if attempt.succeeded {
		return "success"
	}
	if attempt.message == "" {
		return "failed"
	}
	return attempt.message
}

func probeSummary(core, framework runtimeProbe) string {
	return ".NET Core / NET8: health " + core.healthURL + " => " + describeProbe(core.health) +
		", service " + core.serviceURL + " => " + describeProbe(core.service) +
		", UI marker " + core.uiURL + " => " + describeProbe(core.ui) +
		". .NET Framework: health " + framework.healthURL + " => " + describeProbe(framework.health) +
		", service " + framework.serviceURL + " => " + describeProbe(framework.service) +
		", UI marker " + framework.uiURL + " => " + describeProbe(framework.ui) + "."
}
