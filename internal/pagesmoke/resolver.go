package pagesmoke

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Why there is no browser on a call (PageSmokeBrowserResolver's codes).
const (
	CodeConsentRequired = "browser-consent-required"
	CodeDeclined        = "browser-declined"
	CodeReady           = "browser-ready"
	CodeNotFound        = "browser-not-found"
	CodeDownloadFailed  = "browser-download-failed"
	CodeDownloading     = "browser-downloading"
)

const (
	consentWhy = "page-smoke opens the page in a headless Chromium-based browser on the machine clio runs on, " +
		"to report its buttons, fields and console errors (and what a click did)."
	relayRule = "This refusal is not the user's answer: put browserConsent.question to the user verbatim " +
		"and never choose for them, then call page-smoke again with the arguments of the choice they picked " +
		"(browserConsent.choices). The answer is remembered, so the user is asked once."
)

// BrowserConsent is the question page-smoke needs the user to answer (PageSmokeBrowserConsent).
type BrowserConsent struct {
	Why      string          `json:"why"`
	Question string          `json:"question"`
	Choices  []BrowserChoice `json:"choices"`
}

// BrowserChoice is one answer: the arguments to call page-smoke again with (PageSmokeBrowserChoice).
type BrowserChoice struct {
	Browser     string  `json:"browser"`
	BrowserPath *string `json:"browser-path,omitempty"`
	Description string  `json:"description"`
}

// BrowserDownload is how far a resumable download got (PageSmokeBrowserDownload).
type BrowserDownload struct {
	DownloadedBytes int64 `json:"downloadedBytes"`
	TotalBytes      int64 `json:"totalBytes"`
}

// Resolution is PageSmokeBrowserResolution: the browser to run, or why there is none on this call.
type Resolution struct {
	BrowserPath string
	Code        string
	Message     string
	Consent     *BrowserConsent
	Note        string
	Download    *BrowserDownload
}

// Resolver is clio's PageSmokeBrowserResolver: CHROME_PATH wins without asking; otherwise the remembered answer;
// otherwise a browser-consent-required refusal listing the browsers already on the machine, a download of the
// pinned headless shell, and "no".
type Resolver struct {
	chromium  chromiumLocator
	cache     cacheLocator
	installer *headlessShellInstaller
	settings  settingsStore
	now       func() time.Time
	windows   bool
}

// NewResolver resolves against this machine, clio's appsettings.json at settingsPath and the process environment.
func NewResolver(settingsPath string, env func(string) string) *Resolver {
	layout := currentLayout()
	return &Resolver{
		chromium:  chromiumLocator{env: env, layout: layout},
		cache:     cacheLocator{env: env, layout: layout},
		installer: newHeadlessShellInstaller(clioHomeFolder(settingsPath), env),
		settings:  clioSettingsStore{path: settingsPath},
		now:       time.Now,
		windows:   runtime.GOOS == "windows",
	}
}

// Resolve is ResolveAsync: choice and browserPath are the call's browser and browser-path ("" when absent).
func (r *Resolver) Resolve(ctx context.Context, choice *string, browserPath *string) (Resolution, error) {
	// The user's "no" - given now or remembered - wins even over CHROME_PATH.
	if choice != nil && *choice == ChoiceNone {
		if err := r.remember(ChoiceNone, ""); err != nil {
			return Resolution{}, err
		}
		return Resolution{Code: CodeDeclined, Message: "Remembered: page-smoke will not use or download a browser, so the check " +
			"was not run; continue without a UI check."}, nil
	}
	var stored *storedChoice
	if choice == nil {
		stored = r.settings.load()
	}
	if stored != nil && stored.Choice == ChoiceNone {
		return Resolution{Code: CodeDeclined, Message: "The user chose earlier that page-smoke must not use or download a browser, " +
			"so the check was not run; continue without a UI check. Only if the user now asks for it, put browserConsent's " +
			"question to them again by calling page-smoke with browser=existing or browser=download."}, nil
	}
	if choice != nil && *choice == ChoiceDownload {
		return r.chooseDownload(ctx)
	}
	if choice != nil {
		path := ""
		if browserPath != nil {
			path = *browserPath
		}
		return r.chooseExisting(path)
	}
	if overridden := r.chromium.chromePathOverride(); overridden != "" {
		note := "Used the browser CHROME_PATH names, without asking: CHROME_PATH is set where clio runs."
		if stored != nil && stored.Choice != "" {
			note = "Used the browser CHROME_PATH names instead of the user's remembered answer (" + stored.Choice + "): " +
				"CHROME_PATH is set where clio runs, and it overrides a remembered answer (not one given on the call)."
		}
		return Resolution{BrowserPath: overridden, Note: note}, nil
	}
	switch {
	case stored != nil && stored.Choice == ChoiceDownload:
		if downloaded := r.installer.findInstalled(); downloaded != "" {
			return ready(downloaded), nil
		}
		return r.download(ctx)
	case stored != nil && stored.Choice == ChoiceExisting:
		// Re-checked against what is offered now, so an edited setting cannot start an arbitrary file.
		if r.isOffered(stored.Path) && fileExists(stored.Path) {
			return ready(stored.Path), nil
		}
		return r.askUser("The browser the user chose earlier (" + stored.Path + ") is no longer offered. "), nil
	default:
		return r.askUser(""), nil
	}
}

func ready(path string) Resolution { return Resolution{BrowserPath: path} }

func (r *Resolver) chooseDownload(ctx context.Context) (Resolution, error) {
	if _, ok := r.installer.downloadArchive(); !ok {
		return Resolution{Code: CodeNotFound, Message: "There is no " + headlessShellDisplayName + " build for this machine, so " +
			"browser=download cannot be used and nothing was remembered. " + relayRule, Consent: r.buildConsent(r.findExisting())}, nil
	}
	if err := r.remember(ChoiceDownload, ""); err != nil {
		return Resolution{}, err
	}
	if downloaded := r.installer.findInstalled(); downloaded != "" {
		return ready(downloaded), nil
	}
	return r.download(ctx)
}

func (r *Resolver) isOffered(path string) bool {
	if path == "" {
		return false
	}
	for _, browser := range r.findExisting() {
		if r.samePath(browser.Path, path) {
			return true
		}
	}
	return false
}

func (r *Resolver) chooseExisting(browserPath string) (Resolution, error) {
	existing := r.findExisting()
	if len(existing) == 0 {
		return Resolution{Code: CodeNotFound, Message: "No Chromium-based browser was found on the machine clio runs on, so " +
			"browser=existing cannot be used. " + relayRule, Consent: r.buildConsent(existing)}, nil
	}
	for _, browser := range existing {
		if r.samePath(browser.Path, browserPath) {
			if err := r.remember(ChoiceExisting, browser.Path); err != nil {
				return Resolution{}, err
			}
			return ready(browser.Path), nil
		}
	}
	return Resolution{Code: CodeNotFound, Message: "browser-path must be one of the browsers page-smoke offers (browserConsent.choices); " +
		"an arbitrary executable is never started. " + relayRule, Consent: r.buildConsent(existing)}, nil
}

func (r *Resolver) download(ctx context.Context) (Resolution, error) {
	executable, err := r.installer.install(ctx)
	if err == nil {
		return Resolution{Code: CodeReady, Message: fmt.Sprintf("Downloaded %s %s to %s. The page was not checked on this call, "+
			"so the download stays inside the call's time; call page-smoke again with the same arguments, without "+
			"browser, to run the check.", headlessShellDisplayName, headlessShellVersion, executable)}, nil
	}
	var install *installError
	if !errors.As(err, &install) {
		return Resolution{}, err
	}
	if install.incomplete {
		return Resolution{Code: CodeDownloading, Message: install.message,
			Download: &BrowserDownload{DownloadedBytes: install.downloaded, TotalBytes: install.total}}, nil
	}
	return Resolution{Code: CodeDownloadFailed, Message: install.message + " The user's choice (download) is kept, so calling " +
		"page-smoke again retries; ask the user before switching to browser=existing or browser=none."}, nil
}

func (r *Resolver) askUser(prefix string) Resolution {
	return Resolution{Code: CodeConsentRequired, Message: prefix + "page-smoke needs a browser and the user has not chosen one " +
		"yet, so the check was not run. " + relayRule, Consent: r.buildConsent(r.findExisting())}
}

func (r *Resolver) buildConsent(existing []InstalledBrowser) *BrowserConsent {
	choices := []BrowserChoice{}
	for _, browser := range existing {
		path := browser.Path
		choices = append(choices, BrowserChoice{Browser: ChoiceExisting, BrowserPath: &path,
			Description: "Use " + describeBrowser(browser) + ", already on this machine."})
	}
	if archive, ok := r.installer.downloadArchive(); ok {
		choices = append(choices, BrowserChoice{Browser: ChoiceDownload,
			Description: fmt.Sprintf("Download %s %s (about %d MB, once) into %s.", headlessShellDisplayName, headlessShellVersion,
				megabytesRounded(archive.Size), r.installer.installDirectory())})
	}
	choices = append(choices, BrowserChoice{Browser: ChoiceNone,
		Description: "No: page-smoke stays off and the work continues without a UI check."})
	var question strings.Builder
	question.WriteString("page-smoke needs a Chromium-based browser to open the page headless. Which should it use?")
	for index, choice := range choices {
		question.WriteString(" " + strconv.Itoa(index+1) + ") " + choice.Description)
	}
	question.WriteString(" The answer is remembered; to change it later, delete page-smoke-browser from clio's settings or ask again.")
	return &BrowserConsent{Why: consentWhy, Question: question.String(), Choices: choices}
}

// findExisting lists the headless shells other tools already downloaded first (the recommended choice), then
// installed browsers.
func (r *Resolver) findExisting() []InstalledBrowser {
	cached := r.cache.findCached()
	found := append([]InstalledBrowser{}, cached...)
	for _, installed := range r.chromium.findInstalled() {
		duplicate := false
		for _, browser := range cached {
			if r.samePath(browser.Path, installed.Path) {
				duplicate = true
				break
			}
		}
		if !duplicate {
			found = append(found, installed)
		}
	}
	return found
}

func describeBrowser(browser InstalledBrowser) string {
	if browser.Version == "" {
		return browser.Name + " at " + browser.Path
	}
	return browser.Name + " " + browser.Version + " at " + browser.Path
}

func megabytesRounded(bytes int64) int64 { return (bytes + 512*1024) / (1024 * 1024) }

// remember writes the answer; a failed write fails the call, as clio's settings exception does.
func (r *Resolver) remember(choice, path string) error {
	return r.settings.save(choice, path, r.now())
}

func (r *Resolver) samePath(left, right string) bool {
	if r.windows {
		return strings.EqualFold(left, right)
	}
	return left == right
}
