package pagesmoke

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// The headless launcher (HeadlessBrowserLauncher): a headless Chromium with a throw-away profile, driven over a
// private DevTools pipe; no debugging port is opened, so no other local process can attach to the browser and
// read the session it is given.

const (
	profilePrefix          = "clio-headless-profile-"
	startupTimeout         = 30 * time.Second
	pageTargetPollAttempts = 16
	pollDelay              = 250 * time.Millisecond
	staleProfileAge        = 10 * time.Minute
	exitWait               = 5 * time.Second
	profileDeleteAttempts  = 5
	profileDeleteRetry     = 200 * time.Millisecond

	windowsVersionHint = "On Windows the browser must be Chromium 117 or later (Chrome, Edge), which can take its DevTools pipe from clio."
	linuxLibrariesHint = "On Linux a browser that never starts usually lacks system libraries (for example libnss3, libatk-bridge2.0-0, " +
		"libgbm1, libxkbcommon0, libasound2); install them with the distribution's package manager, or run the browser once by " +
		"hand to see which one is missing."
)

// launchError is the launcher's InvalidOperationException: its message is what the check reports.
type launchError struct{ message string }

func (e *launchError) Error() string { return e.message }

// headlessBrowser is a running browser: its page session, process and profile. dispose never fails.
type headlessBrowser struct {
	process *pipeProcess
	page    *cdpSession
	profile string
	once    bool
}

// browserArguments is HeadlessBrowserLauncher.BuildArguments: no --remote-debugging-port (the spawner adds the
// pipe), a new private profile, a mock keychain so a cookie write never waits on the OS keychain.
func browserArguments(userDataDir string, noSandbox bool) []string {
	arguments := []string{
		"--headless=new",
		"--user-data-dir=" + userDataDir,
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-extensions",
		"--disable-background-networking",
		"--disable-sync",
		"--mute-audio",
		"--use-mock-keychain",
		"--password-store=basic",
		"--window-size=1440,900",
	}
	if noSandbox {
		// Chromium refuses to start as root with its sandbox on; a CI agent running as root needs this.
		arguments = append(arguments, "--no-sandbox")
	}
	return append(arguments, "about:blank")
}

func runsAsRootOnLinux() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	current, err := user.Current()
	return err == nil && current.Username == "root"
}

// launchBrowser starts the browser and attaches a CDP session to its page.
func launchBrowser(ctx context.Context, browserPath string) (*headlessBrowser, error) {
	deleteStaleProfiles()
	userDataDir := filepath.Join(os.TempDir(), profilePrefix+newHexID())
	// Owner-only before the browser starts: the profile holds the session cookies.
	if err := os.Mkdir(userDataDir, 0o700); err != nil {
		return nil, err
	}
	process, err := startPipeBrowser(browserPath, browserArguments(userDataDir, runsAsRootOnLinux()))
	if err != nil {
		deleteProfile(userDataDir)
		return nil, &launchError{message: "Error: failed to launch the browser at '" + browserPath + "' with a private DevTools pipe (" +
			err.Error() + "). page-smoke never falls back to an open debugging port."}
	}
	browser := &headlessBrowser{process: process, page: newCdpSession(process.fromBrowser, process.toBrowser), profile: userDataDir}
	if err := attachToPage(ctx, browser.page); err != nil {
		browser.dispose()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &launchError{message: explainNoAnswer(browserPath, err)}
	}
	return browser, nil
}

// attachToPage finds the page started with about:blank among the browser's targets and attaches it as a flat
// session the CDP session routes every command to.
func attachToPage(ctx context.Context, session *cdpSession) error {
	startup, cancel := context.WithTimeout(ctx, startupTimeout)
	defer cancel()
	for attempt := 0; attempt < pageTargetPollAttempts; attempt++ {
		targets, err := session.send(startup, "Target.getTargets", nil)
		if err != nil {
			return err
		}
		if targetID := findPageTargetID(targets); targetID != "" {
			attached, err := session.send(startup, "Target.attachToTarget", map[string]any{"targetId": targetID, "flatten": true})
			if err != nil {
				return err
			}
			var reply struct {
				SessionID string `json:"sessionId"`
			}
			if err := json.Unmarshal(attached, &reply); err != nil || reply.SessionID == "" {
				return errors.New("Error: the browser did not name the page's session.")
			}
			session.routeToSession(reply.SessionID)
			return nil
		}
		timer := time.NewTimer(pollDelay)
		select {
		case <-startup.Done():
			timer.Stop()
			return startup.Err()
		case <-timer.C:
		}
	}
	return errors.New("Error: the browser opened no page to drive.")
}

// findPageTargetID is the targetId of the first page target in a Target.getTargets result, or "".
func findPageTargetID(result json.RawMessage) string {
	var reply struct {
		TargetInfos []struct {
			Type     *string `json:"type"`
			TargetID *string `json:"targetId"`
		} `json:"targetInfos"`
	}
	if json.Unmarshal(result, &reply) != nil {
		return ""
	}
	for _, target := range reply.TargetInfos {
		if target.Type != nil && *target.Type == "page" && target.TargetID != nil && *target.TargetID != "" {
			return *target.TargetID
		}
	}
	return ""
}

func explainNoAnswer(browserPath string, cause error) string {
	var closed *cdpClosedError
	noAnswer := errors.Is(cause, context.DeadlineExceeded) || errors.As(cause, &closed)
	var why string
	switch {
	case errors.Is(cause, context.DeadlineExceeded):
		why = "it did not answer over its DevTools pipe in time"
	case errors.As(cause, &closed):
		why = "it exited, or closed its DevTools pipe, before it answered"
	default:
		why = strings.TrimRight(strings.TrimPrefix(cause.Error(), "Error: "), ".")
	}
	message := "Error: the browser at '" + browserPath + "' could not be driven: " + why + "."
	if noAnswer {
		switch runtime.GOOS {
		case "windows":
			message += " " + windowsVersionHint
		case "linux":
			message += " " + linuxLibrariesHint
		}
	}
	return message
}

// dispose closes the session (the pipe closing is Chromium's signal to shut down), ends the process tree, waits
// for the exit and deletes the profile, which the browser releases a moment after it exits.
func (b *headlessBrowser) dispose() {
	if b.once {
		return
	}
	b.once = true
	b.page.dispose()
	b.process.lifetime.terminate()
	b.process.lifetime.waitForExit(exitWait)
	b.process.dispose()
	for attempt := 0; attempt < profileDeleteAttempts; attempt++ {
		if deleteProfile(b.profile) {
			return
		}
		time.Sleep(profileDeleteRetry)
	}
}

func deleteProfile(directory string) bool {
	if !directoryExists(directory) {
		return true
	}
	return os.RemoveAll(directory) == nil
}

// deleteStaleProfiles removes profiles a killed run left: older than any running check can be.
func deleteStaleProfiles() {
	entries, err := os.ReadDir(os.TempDir())
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-staleProfileAge)
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), profilePrefix) {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.ModTime().Before(cutoff) {
			continue
		}
		deleteProfile(filepath.Join(os.TempDir(), entry.Name()))
	}
}
