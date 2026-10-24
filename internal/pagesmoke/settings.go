// Package pagesmoke is a port of clio 8.1.0.142's page-smoke: it opens a Freedom UI form page in a headless
// Chromium driven over a private DevTools pipe, reports what the page shows and logs, and optionally clicks one
// button. The pieces follow clio's: Common/BrowserSession (browser discovery, the headless-shell download, the
// consent store, the pipe spawn, the CDP session, the browser session cache) and Command/RuntimeVerification
// (PageSmokeRunner, FreedomUiPageScripts, PageEventCollector).
package pagesmoke

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/knowledge"
)

// The user's answers to page-smoke's browser question (PageSmokeBrowserSettings).
const (
	// ChoiceExisting uses a browser already on the machine.
	ChoiceExisting = "existing"
	// ChoiceDownload downloads the pinned headless shell into the browsers folder.
	ChoiceDownload = "download"
	// ChoiceNone keeps page-smoke off.
	ChoiceNone = "none"

	settingsKey = "page-smoke-browser"
)

// IsKnownChoice is PageSmokeBrowserSettings.IsKnown: an exact, case-sensitive match.
func IsKnownChoice(choice string) bool {
	return choice == ChoiceExisting || choice == ChoiceDownload || choice == ChoiceNone
}

// storedChoice is the remembered answer: the choice, and the browser with existing.
type storedChoice struct {
	Choice string
	Path   string
}

// settingsStore reads and writes the remembered answer (clio's ISettingsRepository.Get/SetPageSmokeBrowser).
type settingsStore interface {
	load() *storedChoice
	save(choice, path string, at time.Time) error
}

// clioSettingsStore keeps the answer in clio's appsettings.json under page-smoke-browser, written under clio's
// settings lock, so clio and this server share it.
type clioSettingsStore struct{ path string }

func (s clioSettingsStore) load() *storedChoice {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return nil
	}
	root, err := knowledge.ParseJSONObject(data)
	if err != nil {
		return nil
	}
	key, ok := root.FindFold(settingsKey)
	if !ok {
		return nil
	}
	section := root.Object(key)
	if section == nil {
		return nil
	}
	stored := &storedChoice{Choice: textMember(section, "choice"), Path: textMember(section, "path")}
	return stored
}

// textMember reads a string member case-insensitively, as Json.NET binds clio's settings.
func textMember(object *knowledge.JSONObject, name string) string {
	key, ok := object.FindFold(name)
	if !ok {
		return ""
	}
	value, _ := object.Get(key)
	text, _ := value.(string)
	return text
}

func (s clioSettingsStore) save(choice, path string, at time.Time) error {
	return knowledge.UpdateSettings(s.path, func(root *knowledge.JSONObject) (bool, error) {
		section := knowledge.NewJSONObject()
		section.Set("choice", choice)
		if path != "" {
			section.Set("path", path)
		}
		section.Set("decided-at", newtonsoftTimestamp(at))
		key := settingsKey
		if existing, ok := root.FindFold(settingsKey); ok {
			key = existing
		}
		root.Set(key, section)
		return true, nil
	})
}

// newtonsoftTimestamp is how Json.NET writes a DateTimeOffset in UTC: seconds with up to seven fractional digits,
// trailing zeros dropped, and the +00:00 offset.
func newtonsoftTimestamp(at time.Time) string {
	at = at.UTC()
	text := at.Format("2006-01-02T15:04:05")
	fraction := strings.TrimRight(fmt.Sprintf("%07d", at.Nanosecond()/100), "0")
	if fraction != "" {
		text += "." + fraction
	}
	return text + "+00:00"
}

// clioHomeFolder is clio's ClioRuntimePaths.Home: the folder of its appsettings.json.
func clioHomeFolder(settingsPath string) string { return filepath.Dir(settingsPath) }
