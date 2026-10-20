// Package toolkit ports clio 8.1.0.142's toolkit installer (Clio.Common.Skills): install-toolkit,
// update-toolkit and delete-toolkit put the Creatio AI App Development Toolkit into every detected coding
// agent through the agent's own CLI (claude, codex, copilot) or, for Cursor, by copying the plugin runtime
// from a toolkit checkout; the agents' config files are edited the way clio edits them and clio's
// ~/.clio/managed-skills.json manifest is kept.
package toolkit

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// clio's ToolkitDistribution.
const (
	PluginName        = "creatio-ai-app-development-toolkit"
	MarketplaceName   = "creatio"
	MarketplaceGitURL = "https://github.com/Creatio-Platform/creatio-ai-app-development-toolkit.git"
	ReleaseRef        = "release"
	PluginSource      = PluginName + "@" + MarketplaceName
	SkillName         = "creatio-app-orchestrator"
)

// Operation is clio's SkillOperationKind.
type Operation int

// The operations.
const (
	Install Operation = iota
	Update
	Delete
)

func (o Operation) verb() string {
	return [...]string{"install", "update", "delete"}[o]
}

func (o Operation) pastVerb() string {
	return [...]string{"installed", "updated", "removed"}[o]
}

// OutcomeStatus is clio's AgentOutcomeStatus.
type OutcomeStatus int

// The outcome states.
const (
	Skipped OutcomeStatus = iota
	Succeeded
	Failed
)

// Outcome is clio's AgentOutcome.
type Outcome struct {
	AgentID string
	Status  OutcomeStatus
	Message string
}

// Result is clio's SkillCommandResult.
type Result struct {
	ExitCode int
	Outcomes []Outcome
	Summary  string
}

// Environment is what the installer reads from the machine; tests point it at temporary directories.
type Environment struct {
	// Home is the user's home (HOME, USERPROFILE on Windows).
	Home string
	// ClioHome is clio's settings directory: CLI working directory and Cursor's toolkit checkout cache.
	ClioHome string
	// Path is the PATH searched for agent CLIs.
	Path string
}

// CurrentEnvironment is the process environment as clio's UserHomeProvider and AgentCliRunner read it.
func CurrentEnvironment(clioHome string) Environment {
	home := os.Getenv("HOME")
	if runtime.GOOS == "windows" {
		home = os.Getenv("USERPROFILE")
	}
	if strings.TrimSpace(home) == "" {
		home, _ = os.UserHomeDir()
	}
	if absolute, err := filepath.Abs(home); err == nil {
		home = absolute
	}
	return Environment{Home: home, ClioHome: clioHome, Path: os.Getenv("PATH")}
}

func (e Environment) agentHome(id string) string { return filepath.Join(e.Home, "."+id) }
func (e Environment) agentsDir() string          { return filepath.Join(e.Home, ".agents") }
func (e Environment) clioDir() string            { return filepath.Join(e.Home, ".clio") }

type agent struct {
	id, display, cli           string
	installVerb, uninstallVerb string
	removeFlags                []string
	preRemoveMarketplace       bool
}

var marketplaceAgents = map[string]agent{
	"claude":  {id: "claude", display: "Claude Code", cli: "claude", installVerb: "install", uninstallVerb: "uninstall", preRemoveMarketplace: true},
	"codex":   {id: "codex", display: "Codex", cli: "codex", installVerb: "add", uninstallVerb: "remove", preRemoveMarketplace: true},
	"copilot": {id: "copilot", display: "GitHub Copilot CLI", cli: "copilot", installVerb: "install", uninstallVerb: "uninstall", removeFlags: []string{"--force"}},
}

// agentIDs is clio's agent set ordered by AgentId.
var agentIDs = []string{"claude", "codex", "copilot", "cursor"}

func displayName(id string) string {
	if id == "cursor" {
		return "Cursor"
	}
	return marketplaceAgents[id].display
}

// ValidateRepository is clio's RepositorySource.TryValidate.
func ValidateRepository(repository string) string {
	if strings.TrimSpace(repository) == "" {
		return ""
	}
	value := strings.TrimSpace(repository)
	if strings.HasPrefix(value, "-") {
		return fmt.Sprintf("Invalid --repo value '%s': must not start with '-' (it would be parsed as a command-line option).", repository)
	}
	if strings.Contains(value, "::") {
		return fmt.Sprintf("Invalid --repo value '%s': git transport helpers (e.g. 'ext::', 'fd::') are not allowed.", repository)
	}
	if separator := strings.Index(value, "://"); separator > 0 {
		scheme := strings.ToLower(value[:separator])
		switch scheme {
		case "https", "http", "ssh", "git", "file":
		default:
			return fmt.Sprintf("Invalid --repo value '%s': unsupported URL scheme '%s'. Allowed: https, http, ssh, git, file (or a local path).", repository, scheme)
		}
	}
	return ""
}

// Run is clio's SkillInstallService.Run.
func Run(env Environment, operation Operation, target, repository string) Result {
	normalized := strings.TrimSpace(target)
	if normalized != "" {
		known := false
		for _, id := range agentIDs {
			known = known || strings.EqualFold(id, normalized)
		}
		if !known {
			return Result{ExitCode: 1, Summary: fmt.Sprintf("Unknown target '%s'. Valid agents: %s.", target, strings.Join(agentIDs, ", "))}
		}
	}
	if message := ValidateRepository(repository); message != "" {
		return Result{ExitCode: 1, Summary: message}
	}
	marketplace := MarketplaceGitURL
	if strings.TrimSpace(repository) != "" {
		marketplace = strings.TrimSpace(repository)
	}
	manifest := readManifest(env)
	now := time.Now().UTC()
	changed := false
	var outcomes []Outcome
	for _, id := range agentIDs {
		if normalized != "" && !strings.EqualFold(id, normalized) {
			continue
		}
		if info, err := os.Stat(env.agentHome(id)); err != nil || !info.IsDir() {
			outcomes = append(outcomes, Outcome{id, Skipped, displayName(id) + " is not installed; nothing to do."})
			continue
		}
		outcome := dispatch(env, id, operation, repository, marketplace)
		outcomes = append(outcomes, outcome)
		if outcome.Status != Succeeded {
			continue
		}
		if operation == Delete {
			manifest.remove(id)
		} else {
			manifest.upsert(id, marketplace, now)
		}
		changed = true
	}
	if changed {
		if err := saveManifest(env, manifest); err != nil {
			return Result{ExitCode: 1, Outcomes: outcomes, Summary: err.Error()}
		}
	}
	result := Result{Outcomes: outcomes}
	var succeeded []string
	for _, outcome := range outcomes {
		if outcome.Status == Failed {
			result.ExitCode = 1
		}
		if outcome.Status == Succeeded {
			succeeded = append(succeeded, outcome.AgentID)
		}
	}
	if len(succeeded) == 0 {
		result.Summary = fmt.Sprintf("No agents were %s.", operation.pastVerb())
	} else {
		past := operation.pastVerb()
		result.Summary = fmt.Sprintf("%s %s for: %s.", strings.ToUpper(past[:1])+past[1:], PluginName, strings.Join(succeeded, ", "))
	}
	return result
}

func dispatch(env Environment, id string, operation Operation, repository, marketplace string) (outcome Outcome) {
	defer func() {
		if recovered := recover(); recovered != nil {
			outcome = Outcome{id, Failed, fmt.Sprintf("%s %s failed: %v", displayName(id), operation.verb(), recovered)}
		}
	}()
	if id == "cursor" {
		return cursorRun(env, operation, repository)
	}
	return marketplaceRun(env, marketplaceAgents[id], operation, marketplace)
}

// cliFailure is clio's AgentCliFailure.
type cliFailure struct{ message string }

func (f cliFailure) Error() string { return f.message }

// cliResult is clio's AgentCliResult.
type cliResult struct {
	succeeded bool
	stdout    string
	stderr    string
}

// resolveExecutable is clio's AgentCliRunner.ResolveExecutable: PATH entries that are absolute, with
// PATHEXT (plus .PS1) candidates on Windows.
func resolveExecutable(env Environment, name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	if strings.ContainsAny(name, `/\`) && filepath.Ext(name) != "" {
		if info, err := os.Stat(name); err == nil && !info.IsDir() {
			absolute, _ := filepath.Abs(name)
			return absolute
		}
	}
	candidates := []string{name}
	if filepath.Ext(name) == "" && runtime.GOOS == "windows" {
		candidates = nil
		extensions := os.Getenv("PATHEXT")
		if extensions == "" {
			extensions = ".COM;.EXE;.BAT;.CMD"
		}
		seen := map[string]bool{}
		for _, extension := range append(strings.Split(extensions, ";"), ".PS1") {
			extension = strings.TrimSpace(extension)
			if extension == "" || seen[strings.ToLower(extension)] {
				continue
			}
			seen[strings.ToLower(extension)] = true
			candidates = append(candidates, name+strings.ToLower(extension))
		}
	}
	for _, directory := range filepath.SplitList(env.Path) {
		directory = strings.TrimSpace(directory)
		if directory == "" || !filepath.IsAbs(directory) {
			continue
		}
		for _, candidate := range candidates {
			full := filepath.Join(directory, candidate)
			if info, err := os.Stat(full); err == nil && !info.IsDir() {
				return full
			}
		}
	}
	return ""
}

// runCLI is clio's AgentCliRunner.Run: the CLI runs in clio's settings directory, its exit code decides.
func runCLI(env Environment, name string, args ...string) cliResult {
	resolved := resolveExecutable(env, name)
	if resolved == "" {
		return cliResult{stderr: fmt.Sprintf("'%s' was not found on PATH.", name)}
	}
	program, arguments := resolved, args
	switch strings.ToLower(filepath.Ext(resolved)) {
	case ".ps1":
		program, arguments = "powershell", append([]string{"-ExecutionPolicy", "Bypass", "-File", resolved}, args...)
	case ".cmd", ".bat":
		program, arguments = "cmd.exe", append([]string{"/s", "/c", resolved}, args...)
	}
	command := exec.Command(program, arguments...)
	if env.ClioHome != "" {
		_ = os.MkdirAll(env.ClioHome, 0o777)
		command.Dir = env.ClioHome
	}
	command.Env = append(os.Environ(), "PATH="+env.Path, "HOME="+env.Home)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	return cliResult{succeeded: err == nil, stdout: stdout.String(), stderr: stderr.String()}
}

func (r cliResult) errorText() string { return r.stderr }

func marketplaceRun(env Environment, a agent, operation Operation, marketplace string) Outcome {
	if resolveExecutable(env, a.cli) == "" {
		return Outcome{a.id, Skipped, fmt.Sprintf("%s is detected but '%s' is not on PATH; skipped.", a.display, a.cli)}
	}
	err := func() error {
		switch {
		case a.id == "claude" && operation == Install:
			if err := registerAndInstall(env, a, marketplace); err != nil {
				return err
			}
			return enableMarketplaceAutoUpdate(filepath.Join(env.agentHome(a.id), "settings.json"), MarketplaceName)
		case a.id == "claude" && operation == Update:
			if err := runOrFail(env, a, "plugin", "update", PluginSource); err != nil {
				return err
			}
			return enableMarketplaceAutoUpdate(filepath.Join(env.agentHome(a.id), "settings.json"), MarketplaceName)
		case a.id == "claude" && operation == Delete:
			if err := uninstallAndRemoveMarketplace(env, a); err != nil {
				return err
			}
			return removeMarketplaceFromSettings(filepath.Join(env.agentHome(a.id), "settings.json"), MarketplaceName)
		case a.id == "codex" && operation != Delete:
			if err := codexCleanLegacyState(env); err != nil {
				return err
			}
			if err := registerAndInstall(env, a, marketplace); err != nil {
				return err
			}
			return mergeTomlClioServer(filepath.Join(env.agentHome("codex"), "config.toml"))
		case a.id == "copilot" && operation != Delete:
			_ = os.RemoveAll(filepath.Join(env.agentsDir(), "plugins", PluginName))
			_ = os.RemoveAll(filepath.Join(env.agentsDir(), "skills", SkillName))
			return registerAndInstall(env, a, marketplace)
		}
		return uninstallAndRemoveMarketplace(env, a)
	}()
	if err != nil {
		var failure cliFailure
		if errors.As(err, &failure) {
			return Outcome{a.id, Failed, failure.message}
		}
		panic(err.Error())
	}
	verbs := map[Operation]string{Install: "Installed %s for %s.", Update: "Updated %s for %s.", Delete: "Removed %s from %s."}
	return Outcome{a.id, Succeeded, fmt.Sprintf(verbs[operation], PluginName, a.display)}
}

func failure(a agent, label string, result cliResult) error {
	return cliFailure{fmt.Sprintf("%s %s failed: %s", a.cli, label, result.errorText())}
}

func runOrFail(env Environment, a agent, args ...string) error {
	if result := runCLI(env, a.cli, args...); !result.succeeded {
		return failure(a, strings.Join(args, " "), result)
	}
	return nil
}

func marketplaceRemoveCommand(a agent) []string {
	return append([]string{"plugin", "marketplace", "remove", MarketplaceName}, a.removeFlags...)
}

// registerAndInstall is clio's RegisterMarketplaceAndInstall.
func registerAndInstall(env Environment, a agent, marketplace string) error {
	add := []string{"plugin", "marketplace", "add", marketplace}
	install := []string{"plugin", a.installVerb, PluginSource}
	if a.preRemoveMarketplace {
		runCLI(env, a.cli, marketplaceRemoveCommand(a)...)
		if err := runOrFail(env, a, add...); err != nil {
			return err
		}
		return runOrFail(env, a, install...)
	}
	if result := runCLI(env, a.cli, add...); !result.succeeded {
		text := strings.ToLower(result.errorText())
		if !strings.Contains(text, `marketplace "`+MarketplaceName+`" already registered`) &&
			!strings.Contains(text, "marketplace '"+MarketplaceName+"' is already added") {
			return failure(a, "plugin marketplace add", result)
		}
		runCLI(env, a.cli, marketplaceRemoveCommand(a)...)
		if err := runOrFail(env, a, add...); err != nil {
			return err
		}
	}
	return runOrFail(env, a, install...)
}

func notFoundLike(text string) bool {
	text = strings.ToLower(text)
	return strings.Contains(text, "not found") || strings.Contains(text, "not installed") ||
		strings.Contains(text, "no such") || strings.Contains(text, "is not configured")
}

func runTolerant(env Environment, a agent, args ...string) error {
	if result := runCLI(env, a.cli, args...); !result.succeeded && !notFoundLike(result.errorText()) {
		return failure(a, strings.Join(args, " "), result)
	}
	return nil
}

func uninstallAndRemoveMarketplace(env Environment, a agent) error {
	if err := runTolerant(env, a, "plugin", a.uninstallVerb, PluginSource); err != nil {
		return err
	}
	return runTolerant(env, a, marketplaceRemoveCommand(a)...)
}

func codexCleanLegacyState(env Environment) error {
	home := env.agentHome("codex")
	for _, directory := range []string{filepath.Join(home, "plugins", "marketplaces", MarketplaceName),
		filepath.Join(home, "plugins", "cache", MarketplaceName), filepath.Join(env.agentsDir(), "plugins", PluginName),
		filepath.Join(home, "skills", SkillName)} {
		deleteLegacyDirectory(directory)
	}
	if err := removePersonalMarketplacePluginEntry(filepath.Join(env.agentsDir(), "plugins", "marketplace.json"), MarketplaceName, PluginName); err != nil {
		return err
	}
	config := filepath.Join(home, "config.toml")
	if err := removeTomlTables(config, "[marketplaces."+MarketplaceName+"]", "[marketplaces."+tomlQuote(MarketplaceName)+"]"); err != nil {
		return err
	}
	if err := removeTomlTables(config, "[plugins."+tomlQuote(PluginName+"@"+MarketplaceName)+"]"); err != nil {
		return err
	}
	return removeTomlSkillOverride(config, PluginName+":"+SkillName)
}

// deleteLegacyDirectory clears read-only bits (never through a link) and deletes the directory.
func deleteLegacyDirectory(path string) {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() {
		return
	}
	_ = filepath.WalkDir(path, func(child string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return filepath.SkipDir
		}
		if childInfo, err := entry.Info(); err == nil && childInfo.Mode().Perm()&0o200 == 0 {
			_ = os.Chmod(child, childInfo.Mode().Perm()|0o200)
		}
		return nil
	})
	_ = os.RemoveAll(path)
}

// --------------------------------------------------------------------------------------------- manifest

type manifestEntry struct {
	Source         string `json:"Source,omitempty"`
	InstalledAtUtc string `json:"InstalledAtUtc"`
	UpdatedAtUtc   string `json:"UpdatedAtUtc"`
}

type manifestFile struct {
	keys    []string
	entries map[string]manifestEntry
}

func dotnetTime(t time.Time) string {
	return t.UTC().Truncate(100 * time.Nanosecond).Format("2006-01-02T15:04:05.9999999-07:00")
}

func (m *manifestFile) find(id string) (string, bool) {
	for _, key := range m.keys {
		if strings.EqualFold(key, id) {
			return key, true
		}
	}
	return "", false
}

func (m *manifestFile) upsert(id, source string, now time.Time) {
	if key, ok := m.find(id); ok {
		entry := m.entries[key]
		entry.Source, entry.UpdatedAtUtc = source, dotnetTime(now)
		m.entries[key] = entry
		return
	}
	m.keys = append(m.keys, id)
	m.entries[id] = manifestEntry{Source: source, InstalledAtUtc: dotnetTime(now), UpdatedAtUtc: dotnetTime(now)}
}

func (m *manifestFile) remove(id string) {
	if key, ok := m.find(id); ok {
		delete(m.entries, key)
		for i, existing := range m.keys {
			if existing == key {
				m.keys = append(m.keys[:i], m.keys[i+1:]...)
				break
			}
		}
	}
}

func manifestPath(env Environment) string { return filepath.Join(env.clioDir(), "managed-skills.json") }

// readManifest is ManagedSkillsManifestStore.Read: a missing or unreadable manifest is empty.
func readManifest(env Environment) *manifestFile {
	manifest := &manifestFile{entries: map[string]manifestEntry{}}
	data, err := os.ReadFile(manifestPath(env))
	if err != nil {
		return manifest
	}
	root, err := parseObject(data)
	if err != nil {
		return manifest
	}
	agents, _ := root.get("Agents").(*object)
	if agents == nil {
		return manifest
	}
	for _, key := range agents.keys {
		value, _ := agents.values[key].(*object)
		if value == nil {
			continue
		}
		entry := manifestEntry{}
		entry.Source, _ = value.get("Source").(string)
		entry.InstalledAtUtc, _ = value.get("InstalledAtUtc").(string)
		entry.UpdatedAtUtc, _ = value.get("UpdatedAtUtc").(string)
		if _, exists := manifest.find(key); !exists {
			manifest.keys = append(manifest.keys, key)
		}
		manifest.entries[key] = entry
	}
	return manifest
}

// saveManifest is ManagedSkillsManifestStore.Save: an empty manifest deletes the file.
func saveManifest(env Environment, manifest *manifestFile) error {
	if len(manifest.keys) == 0 {
		err := os.Remove(manifestPath(env))
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	agents := newObject()
	for _, key := range manifest.keys {
		entry := manifest.entries[key]
		value := newObject()
		if entry.Source != "" {
			value.set("Source", entry.Source)
		}
		value.set("InstalledAtUtc", rawString(entry.InstalledAtUtc))
		value.set("UpdatedAtUtc", rawString(entry.UpdatedAtUtc))
		agents.set(key, value)
	}
	root := newObject()
	root.set("Agents", agents)
	if err := os.MkdirAll(env.clioDir(), 0o777); err != nil {
		return err
	}
	return os.WriteFile(manifestPath(env), root.indented(false), 0o666)
}
