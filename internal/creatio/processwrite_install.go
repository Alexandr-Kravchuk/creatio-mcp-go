package creatio

// install-process-builder: clio's InstallProcessBuilderCommand (InstallBundledPackageCommand for
// CrtProcessBuilder). The archive is the one the installed clio carries. The install refuses a downgrade and
// a suffixed bundled version, installs through the same installer install-application uses (backup on,
// developer mode off), waits for the platform to come back after its restart, then asks the package's own
// ungated Ping whether it is compiled and serving.

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"
)

// processWriteInstallTimings are the waits of the install; tests shorten them.
var processWriteInstallTimings = struct {
	initialDelay, pollInterval, readyTimeout, probeTimeout, pingTimeout, pingDelay, versionTimeout time.Duration
	pingAttempts                                                                                   int
}{initialDelay: 10 * time.Second, pollInterval: 5 * time.Second, readyTimeout: 600 * time.Second, probeTimeout: 100 * time.Second,
	pingTimeout: 15 * time.Second, pingDelay: 5 * time.Second, versionTimeout: 60 * time.Second, pingAttempts: 3}

const (
	processWriteInstallVerb     = "install-process-builder"
	processWriteInstallPingPath = "rest/ProcessDesignService/Ping"
	processWriteOverrideHint    = "A command-line override exists for the case where the rollback is what you want; see 'clio " + processWriteInstallVerb + " -H'."
)

// InstallProcessBuilder is InstallBundledPackageCommand.Execute for CrtProcessBuilder (no --force over MCP).
func (c *Client) InstallProcessBuilder(ctx context.Context) CommandResult {
	log := &appWriteLog{}
	archive := ProcessWriteArchivePath()
	if archive == "" {
		log.error(fmt.Sprintf("The bundled %s package was not found at '%s'. This clio installation does not carry the package archive, so retrying "+
			"will not help — reinstall or update clio itself.", processBuilderPackageName, processWriteExpectedArchive()))
		return log.result(1)
	}
	if refusal := c.processWriteInstallRefusal(ctx, archive, log); refusal != "" {
		log.error(refusal)
		return log.result(1)
	}
	success, err := c.appWriteInstall(ctx, AppInstallRequest{Name: archive, EnvironmentURI: c.config.BaseURL}, log)
	if err != nil {
		log.error(err.Error())
		return log.result(1)
	}
	if !success {
		log.error(fmt.Sprintf("Failed to install the bundled %s package.", processBuilderPackageName))
		return log.result(1)
	}
	if !c.processWriteWaitForReady(ctx, log) {
		log.error(fmt.Sprintf("%s was installed, but the environment did not "+
			"become ready within the timeout after the platform's post-install restart. Check the "+
			"instance, then verify with 'clio call-service --service-path %s "+
			"-m POST -b {} -e <environment>'.", processBuilderPackageName, processWriteInstallPingPath))
		return log.result(1)
	}
	if ok, diagnosis := c.processWritePing(ctx, log); !ok {
		if diagnosis == "" {
			diagnosis = fmt.Sprintf("%s was installed, but its Ping route (%s) "+
				"does not answer, which means the environment did not compile the package. Check the "+
				"environment's configuration build log, and verify the bundled archive still contains "+
				"its Source Code schema — without it the package installs but is never compiled.", processBuilderPackageName, processWriteInstallPingPath)
		}
		log.error(diagnosis)
		return log.result(1)
	}
	log.line("Done")
	return log.result(0)
}

// processWriteExpectedArchive is where clio expects its archive, relative to its tool directory.
func processWriteExpectedArchive() string {
	return filepath.Join("<clio tool directory>", processBuilderPackageName, processBuilderPackageName+".gz")
}

// processWriteInstallRefusal is ShouldRefuseInstall: a suffixed bundled version, or an environment that
// carries a numerically newer version, refuses; a version that cannot be read is a warning and installs.
func (c *Client) processWriteInstallRefusal(ctx context.Context, archive string, log *appWriteLog) string {
	shipped, diagnosis := processWriteArchiveVersion(archive)
	if diagnosis != "" {
		log.warning(diagnosis + " Installing anyway: without a version to compare, a downgrade cannot be ruled in " +
			"or out, and refusing would block the install over clio's own defect.")
		return ""
	}
	if shipped.suffix != "" {
		return fmt.Sprintf("Refusing: this clio distribution declares %s %s, but a bundled package version "+
			"must be a plain four-part number with no pre-release suffix — clio compares bundled versions "+
			"numerically, and installing a suffixed one could move the target environment's recorded "+
			"version backwards for everyone using it without being detected. Reinstall or update clio "+
			"itself, or re-run the rebundle with a four-part version if you produced this archive. %s", processBuilderPackageName, shipped.text, processWriteOverrideHint)
	}
	readCtx, cancel := context.WithTimeout(ctx, processWriteInstallTimings.versionTimeout)
	defer cancel()
	installed, err := c.processWriteInstalledVersion(readCtx, processBuilderPackageName)
	if err != nil {
		if errors.Is(readCtx.Err(), context.DeadlineExceeded) {
			log.warning("Timed out reading the version currently installed in the environment. Installing anyway; " +
				"if the environment is genuinely unreachable the install itself will say so.")
			return ""
		}
		log.warning(fmt.Sprintf("Could not read the version currently installed in the environment (%s). Installing anyway; if the environment is "+
			"genuinely unreachable the install itself will say so.", processWriteDisplay(err.Error(), 300)))
		return ""
	}
	numbers := func(version processWriteVersion) processWriteVersion { version.suffix = ""; return version }
	if installed == nil || numbers(*installed).compare(numbers(shipped)) <= 0 {
		return ""
	}
	return fmt.Sprintf("Refusing: the environment carries %s %s, and this clio ships %s — installing would move that "+
		"environment's recorded version BACKWARDS for everyone using it, and nothing downstream would "+
		"report it: the gate would see a present package and the convergence check compares the recorded "+
		"version, which the rollback has just rewritten. Update clio instead. %s", processBuilderPackageName, installed.text, shipped.text, processWriteOverrideHint)
}

// processWriteArchiveVersion is BundledPackageCatalog.TryGetVersion with its diagnosis texts.
func processWriteArchiveVersion(archive string) (processWriteVersion, string) {
	content, found, err := processWriteReadArchiveEntry(archive, "descriptor.json")
	if err != nil {
		return processWriteVersion{}, fmt.Sprintf("The bundled %s archive at '%s' could not be read (%s). Reinstall or update clio itself.",
			processBuilderPackageName, archive, strings.NewReplacer("\r", " ", "\n", " ").Replace(err.Error()))
	}
	if !found {
		return processWriteVersion{}, fmt.Sprintf("The bundled %s archive at '%s' contains no descriptor.json, so "+
			"the version it carries cannot be determined. Reinstall or update clio itself.", processBuilderPackageName, archive)
	}
	var descriptor struct {
		Descriptor *struct {
			PackageVersion *string `json:"PackageVersion"`
		} `json:"Descriptor"`
	}
	if err := json.Unmarshal(processWriteStripBOM(content), &descriptor); err != nil {
		return processWriteVersion{}, fmt.Sprintf("The bundled %s archive at '%s' has a malformed descriptor.json (%s). Reinstall or update clio itself.",
			processBuilderPackageName, archive, err.Error())
	}
	if descriptor.Descriptor != nil && descriptor.Descriptor.PackageVersion != nil {
		if version, ok := processWriteParseVersion(*descriptor.Descriptor.PackageVersion); ok {
			return version, ""
		}
	}
	return processWriteVersion{}, fmt.Sprintf("The bundled %s archive at '%s' has a descriptor.json without a "+
		"readable Descriptor.PackageVersion. Reinstall or update clio itself.", processBuilderPackageName, archive)
}

// processWriteDisplay caps a quoted text on one line (TextUtilities.SanitizeForDisplay, without its
// control-character rules).
func processWriteDisplay(text string, limit int) string {
	flat := strings.Join(strings.Fields(text), " ")
	if runes := []rune(flat); len(runes) > limit {
		return string(runes[:limit]) + "…"
	}
	return flat
}

// processWriteWaitForReady is ServerReadinessWaiter.WaitForReady with liveness only: the health check of
// the web host (.NET Framework) or the web app loader (.NET), polled until the deadline.
func (c *Client) processWriteWaitForReady(ctx context.Context, log *appWriteLog) bool {
	timings := processWriteInstallTimings
	log.info(fmt.Sprintf("Waiting %.0f seconds for server to start...", timings.initialDelay.Seconds()))
	if !processWriteSleep(ctx, timings.initialDelay) {
		return false
	}
	deadline := time.Now().Add(timings.readyTimeout)
	for attempt := 1; ; attempt++ {
		if c.processWriteHealthCheck(ctx, log) {
			log.info(fmt.Sprintf("Server is ready after %d attempt(s).", attempt))
			return true
		}
		if !time.Now().Before(deadline) {
			break
		}
		log.info(fmt.Sprintf("Waiting for server to become ready... (attempt %d). Next check in %.0f seconds.", attempt, timings.pollInterval.Seconds()))
		if !processWriteSleep(ctx, timings.pollInterval) || !time.Now().Before(deadline) {
			break
		}
	}
	log.warning(fmt.Sprintf("Server did not become ready within %.0f seconds.", timings.readyTimeout.Seconds()))
	return false
}

func processWriteSleep(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// processWriteHealthCheck is HealthCheckCommand with no web-app/web-host flag: one anonymous GET of the
// health route, no redirects followed.
func (c *Client) processWriteHealthCheck(ctx context.Context, log *appWriteLog) bool {
	name, path := "WebHost", "/0/api/HealthCheck/Ping"
	if c.config.IsNetCore {
		name, path = "WebAppLoader", "/api/HealthCheck/Ping"
	}
	uri := strings.TrimRight(c.config.BaseURL, "/") + path
	log.info(fmt.Sprintf("Checking %s %s ...", name, uri))
	probeCtx, cancel := context.WithTimeout(ctx, processWriteInstallTimings.probeTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(probeCtx, http.MethodGet, uri, nil)
	if err != nil {
		log.error("\tError: " + err.Error())
		return false
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		message := err.Error()
		if errors.Is(probeCtx.Err(), context.DeadlineExceeded) {
			message = fmt.Sprintf("request timed out after %d ms", processWriteInstallTimings.probeTimeout.Milliseconds())
		}
		log.error("\tError: " + message)
		return false
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message := fmt.Sprintf("%d %s", response.StatusCode, http.StatusText(response.StatusCode))
		if response.StatusCode >= 300 && response.StatusCode < 400 {
			target := ""
			if location := response.Header.Get("Location"); location != "" {
				target = " to " + location
			}
			message += " — the health-check endpoint answered a redirect" + target + " instead of a health " +
				"response (the application is not serving its own health endpoint; commonly a login " +
				"redirect or a scheme/host mismatch in the registered environment uri)"
		}
		log.error("\tError: " + message)
		return false
	}
	log.info(fmt.Sprintf("\t%s - OK", name))
	return true
}

// processWritePing is BundledPackagePingOutcomeVerifier.IsPackageOperational: the ungated Ping must answer
// {"PingResult":{"success":true}}.
func (c *Client) processWritePing(ctx context.Context, log *appWriteLog) (bool, string) {
	url := strings.TrimRight(c.serviceURL(processWriteInstallPingPath), "/")
	var payload []byte
	var err error
	for attempt := 1; attempt <= processWriteInstallTimings.pingAttempts; attempt++ {
		payload, err = c.processWritePost(ctx, serviceCall{Route: processWriteInstallPingPath, Body: []byte("{}"), Timeout: processWriteInstallTimings.pingTimeout, Label: "ProcessDesignService"})
		if err == nil || attempt == processWriteInstallTimings.pingAttempts || !processWriteSleep(ctx, processWriteInstallTimings.pingDelay) {
			break
		}
	}
	if err != nil {
		log.error(fmt.Sprintf("%s Ping did not answer: %s", processBuilderPackageName, err.Error()))
		return false, ""
	}
	if looksLikeHTML(payload) {
		return false, fmt.Sprintf("%s was installed, but %s answered with a login page: the session expired and "+
			"automatic re-authentication did not restore it. The configuration build is NOT implicated "+
			"and the package's state is UNKNOWN — check the environment's credentials, then verify "+
			"with 'clio call-service --service-path %s -m POST -b {} -e <environment>'.", processBuilderPackageName, url, processWriteInstallPingPath)
	}
	root, parseErr := parseJNode(payload)
	if parseErr != nil {
		log.error(fmt.Sprintf("%s Ping did not answer: %s", processBuilderPackageName, processWriteJSONError(payload, parseErr)))
		return false, ""
	}
	result := root.get("PingResult")
	if result == nil {
		return false, fmt.Sprintf("%s was installed, but %s answered with a JSON body carrying no PingResult, "+
			"so something other than this package is serving that route — a reverse proxy, an "+
			"authenticating gateway or an expired-session redirect. The configuration build is NOT "+
			"implicated; check what sits in front of the environment. First 200 characters of the "+
			"answer: %s", processBuilderPackageName, url, processWriteTruncate(string(payload)))
	}
	if success := result.get("success"); success == nil || success.kind != jkBool || !success.flag {
		return false, fmt.Sprintf("%s's Ping route answered, but not with success — so the package is serving and "+
			"is reporting a problem, which the shipped build cannot do (its Ping returns a constant). "+
			"Either the serving build is not the one clio ships, or something is answering in this "+
			"package's envelope. First 200 characters of the answer: %s", processBuilderPackageName, processWriteTruncate(string(payload)))
	}
	return true, ""
}

func processWriteTruncate(text string) string {
	if text == "" {
		return "(empty)"
	}
	return processWriteDisplay(text, 200)
}
