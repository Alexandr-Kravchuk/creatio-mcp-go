package main

// The environment registry: every call names its target with environment-name, resolved against clio's
// appsettings.json the way clio resolves it, with one authenticated client kept per target.

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
)

// missingTargetMessage is clio's ToolCommandResolver text for a call that names neither an environment nor
// a uri. Here it is reached only when no CREATIO_* default and no active clio environment exist either.
const missingTargetMessage = "Either a configured environment name or an explicit URI is required for MCP command execution. " +
	"Prefer a registered environment name; use explicit URI credentials only as a bootstrap or emergency fallback."

// maxCachedClients bounds the client cache. Registered environments need one entry each; the rest are
// one-off uri/login/password targets, which must not grow the cache without limit.
const maxCachedClients = 64

// environmentError is a caller-actionable resolution failure: an unknown name, a missing target, an
// unusable registration (clio's EnvironmentResolutionException, exit code 1), or a Safe environment that
// needs a confirmation an MCP call cannot give (clio's SafeEnvironmentConfirmationRequiredException).
type environmentError struct {
	message string
	safe    bool
}

func (e *environmentError) Error() string { return e.message }

func (e *environmentError) exceptionType() string {
	if e.safe {
		return "SafeEnvironmentConfirmationRequiredException"
	}
	return "EnvironmentResolutionException"
}

// exitCode is what clio's command envelope reports: 1 for a resolution failure, -1 for the Safe refusal,
// which clio does not classify as a resolution failure.
func (e *environmentError) exitCode() int {
	if e.safe {
		return -1
	}
	return 1
}

func safeEnvironmentError(uri string) error {
	return &environmentError{safe: true, message: fmt.Sprintf(
		"Safe environment confirmation required but it was declined or the context is non-interactive. Environment: '%s'.", uri)}
}

func isEnvironmentError(err error) bool {
	var target *environmentError
	return errors.As(err, &target)
}

type settingsStamp struct {
	modTime time.Time
	size    int64
}

// environments resolves a call's target to an authenticated client. It is safe for concurrent use.
type environments struct {
	path string
	// fallback serves calls that name no environment when CREATIO_* variables are set; fallbackErr is the
	// reason those variables could not be used.
	fallback    *creatio.Client
	fallbackErr error

	mu       sync.Mutex
	loaded   bool
	stamp    settingsStamp
	settings creatio.ClioSettings
	warning  string
	clients  map[creatio.Config]*creatio.Client
}

// newEnvironments reads CREATIO_* as the unnamed default (when present) and clio's settings file at path.
func newEnvironments(path string) *environments {
	registry := &environments{path: path, clients: map[creatio.Config]*creatio.Client{}}
	if creatio.EnvConfigPresent() {
		config, err := creatio.LoadConfig()
		if err != nil {
			registry.fallbackErr = err
		} else {
			registry.fallback, _ = creatio.NewClient(config)
		}
	}
	return registry
}

// staticEnvironments serves every unnamed call with one client and has no registered environments. Tests
// and the single-environment entry points use it.
func staticEnvironments(client *creatio.Client) *environments {
	return &environments{path: "", fallback: client, clients: map[creatio.Config]*creatio.Client{}}
}

// snapshot re-reads the settings file when it changed since the last read, as clio re-reads it before
// every resolution. A file that cannot be read keeps the previous snapshot and records a warning.
func (e *environments) snapshot() (creatio.ClioSettings, string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.snapshotLocked()
}

func (e *environments) snapshotLocked() (creatio.ClioSettings, string) {
	if e.path == "" {
		return e.settings, ""
	}
	info, err := os.Stat(e.path)
	if err != nil {
		if !e.loaded {
			// Nothing was ever read: the file is absent, so no environment is registered.
			e.warning = ""
			return e.settings, ""
		}
		e.warning = fmt.Sprintf("Could not re-read %s: appsettings.json could not be read. The previously loaded settings are still in use.", e.path)
		return e.settings, e.warning
	}
	stamp := settingsStamp{modTime: info.ModTime(), size: info.Size()}
	if e.loaded && stamp == e.stamp && e.warning == "" {
		return e.settings, ""
	}
	settings, err := creatio.LoadClioSettings(e.path)
	if err != nil {
		e.warning = fmt.Sprintf("Could not re-read %s: %s The previously loaded settings are still in use.", e.path, strings.TrimSuffix(err.Error(), ".")+".")
		return e.settings, e.warning
	}
	e.settings, e.stamp, e.loaded, e.warning = settings, stamp, true, ""
	return e.settings, ""
}

// client returns the authenticated client for a call. name is the environment-name argument; overrides are
// clio's direct-connection arguments, honoured only by tools whose clio counterpart accepts them.
func (e *environments) client(name string, overrides creatio.ConnectionOverrides) (*creatio.Client, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	name = strings.TrimSpace(name)
	var target creatio.ClioEnvironment
	switch {
	case name != "":
		settings, _ := e.snapshotLocked()
		found, ok := settings.Find(name)
		if !ok {
			return nil, &environmentError{message: environmentNotFoundMessage(name, settings)}
		}
		if found.Safe != nil && *found.Safe {
			// clio asks for confirmation before acting on a Safe environment; an MCP call cannot answer.
			return nil, safeEnvironmentError(found.URI)
		}
		target = found.Fill(overrides)
	case overrides.URI != "":
		target = creatio.ClioEnvironment{}.Fill(overrides)
	case e.fallback != nil:
		return e.fallback, nil
	case e.fallbackErr != nil:
		return nil, &environmentError{message: e.fallbackErr.Error()}
	default:
		settings, _ := e.snapshotLocked()
		active, ok := settings.Active()
		if !ok {
			return nil, &environmentError{message: missingTargetMessage}
		}
		if active.Safe != nil && *active.Safe {
			return nil, safeEnvironmentError(active.URI)
		}
		target = active.Fill(overrides)
	}
	config, err := target.Config()
	if err != nil {
		return nil, &environmentError{message: err.Error()}
	}
	if client, ok := e.clients[config]; ok {
		return client, nil
	}
	if len(e.clients) >= maxCachedClients {
		clear(e.clients)
	}
	client, err := creatio.NewClient(config)
	if err != nil {
		return nil, err
	}
	e.clients[config] = client
	return client, nil
}

// environmentNotFoundMessage is clio's EnvironmentNotFoundError.Build text for an MCP session, left
// unredacted: the tools that report it the way clio redacts it pass it through redacted().
func environmentNotFoundMessage(name string, settings creatio.ClioSettings) string {
	names := make([]string, 0, len(settings.Environments))
	for _, environment := range settings.Environments {
		if trimmed := strings.TrimSpace(environment.Name); trimmed != "" {
			names = append(names, trimmed)
		}
	}
	sortOrdinalIgnoreCase(names)
	hint := " No environments are registered."
	if len(names) > 0 {
		hint = " Available environments: " + strings.Join(names, ", ") + " (use `list-environments` to inspect them)."
	}
	return "Environment with key '" + name + "' not found." + hint + " " +
		"To register it from this MCP session, call the clio-run tool with " +
		`{"command":"reg-web-app","args":{"environment-name":"` + name + `","uri":"<url>",` +
		`"login":"<login>","password":"<password>"}} — that writes appsettings.json and updates ` +
		"this running server in one step. " +
		"This server holds the environment list it loaded from appsettings.json at start; " +
		"`list-environments` and environment resolution re-read that file at call time, but tools bound " +
		"at server start still answer from the loaded copy, so an edit made outside this process (Bash, " +
		"or `clio reg-web-app` in another process) is not guaranteed to be seen before a restart."
}

// sortOrdinalIgnoreCase orders names as .NET's StringComparer.OrdinalIgnoreCase does: by upper-cased code
// unit, so "applicants1" sorts before "applicant_0919".
func sortOrdinalIgnoreCase(names []string) {
	sort.SliceStable(names, func(i, j int) bool {
		return strings.ToUpper(names[i]) < strings.ToUpper(names[j])
	})
}
