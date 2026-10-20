package knowledge

// clio's KnowledgeSourceManagementService for the lifecycle and source-management tools: install-knowledge,
// update-knowledge, delete-knowledge, add-, remove-, enable-, disable-knowledge-source, plus the update
// probe of info-knowledge. Settings change only through UpdateSettings and the store only through its
// locked write side, so a clio process can run any of these at the same time.

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/redact"
)

const (
	maxCandidateAttempts      = 64
	operationDeadline         = 30 * time.Second
	batchDeadline             = 120 * time.Second
	maxConcurrentSourceOps    = 8
	stateRejected             = "rejected"
	stateUpToDate             = "up-to-date"
	stateUnknown              = "unknown"
	curatedAlias              = "creatio-curated"
	curatedLibraryID          = "com.creatio.clio"
	defaultKnowledgeDirectory = "knowledge"
)

// SourceOperationResult is clio's KnowledgeSourceOperationResult.
type SourceOperationResult struct {
	SourceAlias string `json:"sourceAlias"`
	Success     bool   `json:"success"`
	Status      string `json:"status"`
	Message     string `json:"message"`
}

// BatchResult is clio's KnowledgeSourceBatchResult.
type BatchResult struct {
	Success bool                    `json:"success"`
	Message string                  `json:"message"`
	Sources []SourceOperationResult `json:"sources"`
}

// CommandResult is clio's KnowledgeSourceCommandResult.
type CommandResult struct {
	Success     bool   `json:"success"`
	Message     string `json:"message"`
	SourceAlias string `json:"sourceAlias,omitempty"`
}

// AddRequest is clio's KnowledgeSourceAddRequest; nil members were not supplied.
type AddRequest struct {
	Alias                string
	LibraryID            string
	Type                 string
	Location             string
	TrustedKeyID         *string
	TrustedPublicKeyPath *string
	PackageID            *string
	RepositoryOwner      *string
	RepositoryName       *string
	AssetName            *string
	Branch               *string
	Tag                  *string
	Commit               *string
	Enabled              bool
	Priority             int
	Participation        string
}

// Manager runs the lifecycle against clio's settings file and installation store.
type Manager struct {
	// SettingsPath is clio's appsettings.json; empty means SettingsPath().
	SettingsPath string
	// Verify validates a candidate bundle against the current trust (Runtime.Verify).
	Verify func(data []byte, expectedVersion, libraryID string) (*PreparedBundle, *Rejection)
	// Transports maps a source type to its artifact transport.
	Transports map[SourceType]ArtifactTransport
	// Changed runs after settings or the store changed, so readers re-activate.
	Changed func()
}

// NewManager wires the production transports.
func NewManager(settingsPath string, verify func([]byte, string, string) (*PreparedBundle, *Rejection), changed func()) *Manager {
	client := NewTransportClient()
	return &Manager{SettingsPath: settingsPath, Verify: verify, Changed: changed, Transports: map[SourceType]ArtifactTransport{
		SourceGitHubRelease: GitHubReleaseTransport{Client: client}, SourceNuGet: NuGetTransport{Client: client}}}
}

func (m *Manager) settingsPath() string {
	if m.SettingsPath != "" {
		return m.SettingsPath
	}
	return SettingsPath()
}

func (m *Manager) changed() {
	if m.Changed != nil {
		m.Changed()
	}
}

func safe(message string) string { return redact.Text(message) }

func failedOperation(alias, message, status string) SourceOperationResult {
	if status == "" {
		status = "failed"
	}
	return SourceOperationResult{SourceAlias: alias, Status: status, Message: safe(message)}
}

func toOperation(alias string, result InstallationResult) SourceOperationResult {
	return SourceOperationResult{SourceAlias: alias, Success: result.Success(), Status: string(result.Status), Message: result.Message}
}

func failedCommand(alias, message string) CommandResult {
	return CommandResult{Message: safe(message), SourceAlias: alias}
}

func untrustedOr(diagnostic, fallback string) string {
	if text := Untrusted(diagnostic); text != "" {
		return text
	}
	return fallback
}

// loadConfiguration is clio's GetKnowledgeConfiguration: a legacy knowledge-root-path is migrated first.
func (m *Manager) loadConfiguration() (*Settings, error) {
	settings, err := ReadSettings(m.settingsPath())
	if err != nil {
		return nil, err
	}
	if settings.legacyRoot {
		if _, err := EnsureRootPath(m.settingsPath(), settings.Knowledge.RootPath); err != nil {
			return nil, err
		}
		return ReadSettings(m.settingsPath())
	}
	return settings, nil
}

// store is clio's GetRootPath: the configured root, or <settings directory>/knowledge persisted as
// knowledge.root-path; the directory is created.
func (m *Manager) store(settings *Settings) (Store, error) {
	configured := settings.Knowledge.RootPath
	if strings.TrimSpace(configured) == "" {
		var err error
		configured, err = EnsureRootPath(m.settingsPath(), filepath.Join(filepath.Dir(m.settingsPath()), defaultKnowledgeDirectory))
		if err != nil {
			return Store{}, err
		}
	}
	root, err := ResolveRoot(configured)
	if err != nil {
		return Store{}, err
	}
	if err := mkdirRoot(root); err != nil {
		return Store{}, err
	}
	return Store{Root: root}, nil
}

type selection int

const (
	enabledOnly selection = iota
	explicitDisabled
	allConfigured
)

type selectedSource struct {
	alias  string
	source SourceConfig
}

// selectSources is clio's TrySelect.
func selectSources(config Config, alias *string, mode selection) ([]selectedSource, string) {
	if alias != nil {
		configured, source, ok := config.FindSource(*alias)
		if !ok {
			return nil, fmt.Sprintf("Knowledge source '%s' is not configured.", *alias)
		}
		if !source.Enabled && mode == enabledOnly {
			return nil, fmt.Sprintf("Knowledge source '%s' is disabled.", *alias)
		}
		return []selectedSource{{configured, source}}, ""
	}
	var selected []selectedSource
	for _, configured := range config.SortedAliases() {
		source := config.Sources[configured]
		if mode == allConfigured || source.Enabled {
			selected = append(selected, selectedSource{configured, source})
		}
	}
	if len(selected) == 0 {
		if mode == allConfigured {
			return nil, "No knowledge sources are configured."
		}
		return nil, "No enabled knowledge sources are configured."
	}
	return selected, ""
}

type lifecycleOperation func(ctx context.Context, store Store, item selectedSource, deadline time.Duration) (SourceOperationResult, error)

// executeLifecycle is clio's ExecuteLifecycle plus ExecuteBounded: one source runs inline, several run in
// parallel (at most eight) under a 120 s batch deadline; a source not started in time is reported as such.
func (m *Manager) executeLifecycle(ctx context.Context, alias *string, mode selection, operation lifecycleOperation) (BatchResult, error) {
	failure := func(err error) (BatchResult, error) {
		return BatchResult{Message: safe(err.Error()), Sources: []SourceOperationResult{}}, nil
	}
	settings, err := m.loadConfiguration()
	if err != nil {
		return failure(err)
	}
	selected, diagnostic := selectSources(settings.Knowledge, alias, mode)
	if diagnostic != "" {
		return BatchResult{Message: diagnostic, Sources: []SourceOperationResult{}}, nil
	}
	store, err := m.store(settings)
	if err != nil {
		return failure(err)
	}
	if err := ctx.Err(); err != nil {
		return BatchResult{}, err
	}
	results := make([]*SourceOperationResult, len(selected))
	errs := make([]error, len(selected))
	if len(selected) == 1 {
		result, err := operation(ctx, store, selected[0], operationDeadline)
		if err != nil {
			return failure(err)
		}
		results[0] = &result
	} else {
		started := time.Now()
		batchCtx, cancel := context.WithTimeout(ctx, batchDeadline)
		defer cancel()
		gate := make(chan struct{}, maxConcurrentSourceOps)
		var wait sync.WaitGroup
		for index := range selected {
			select {
			case gate <- struct{}{}:
			case <-batchCtx.Done():
			}
			if batchCtx.Err() != nil {
				break
			}
			wait.Add(1)
			go func(index int) {
				defer wait.Done()
				defer func() { <-gate }()
				remaining := batchDeadline - time.Since(started)
				if remaining <= 0 {
					return
				}
				result, err := operation(batchCtx, store, selected[index], min(operationDeadline, remaining))
				if err != nil {
					errs[index] = err
					return
				}
				results[index] = &result
			}(index)
		}
		wait.Wait()
	}
	if err := ctx.Err(); err != nil {
		return BatchResult{}, err
	}
	if joined := errors.Join(errs...); joined != nil {
		return BatchResult{}, fmt.Errorf("One or more errors occurred. (%s)", joined.Error())
	}
	batch := BatchResult{Success: true, Sources: make([]SourceOperationResult, len(selected))}
	for index, result := range results {
		if result == nil {
			batch.Sources[index] = failedOperation(selected[index].alias, "Knowledge operation timed out before this source was processed.", "")
		} else {
			batch.Sources[index] = *result
		}
		batch.Success = batch.Success && batch.Sources[index].Success
	}
	if batch.Success {
		batch.Message = fmt.Sprintf("Knowledge operation completed for %d source(s).", len(batch.Sources))
	} else {
		batch.Message = "Knowledge operation failed for one or more sources; successful sources remain active."
	}
	return batch, nil
}

// Install is install-knowledge.
func (m *Manager) Install(ctx context.Context, alias *string) (BatchResult, error) {
	defer m.changed()
	return m.executeLifecycle(ctx, alias, enabledOnly, func(ctx context.Context, store Store, item selectedSource, deadline time.Duration) (SourceOperationResult, error) {
		return m.installOrUpdate(ctx, store, item, false, deadline), nil
	})
}

// Update is update-knowledge.
func (m *Manager) Update(ctx context.Context, alias *string) (BatchResult, error) {
	defer m.changed()
	return m.executeLifecycle(ctx, alias, enabledOnly, func(ctx context.Context, store Store, item selectedSource, deadline time.Duration) (SourceOperationResult, error) {
		return m.installOrUpdate(ctx, store, item, true, deadline), nil
	})
}

// Delete is delete-knowledge.
func (m *Manager) Delete(ctx context.Context, alias *string, confirmed bool) (BatchResult, error) {
	defer m.changed()
	return m.executeLifecycle(ctx, alias, explicitDisabled, func(_ context.Context, store Store, item selectedSource, _ time.Duration) (SourceOperationResult, error) {
		result, err := store.Delete(item.alias, confirmed)
		if err != nil {
			return SourceOperationResult{}, err
		}
		return toOperation(item.alias, result), nil
	})
}

func (m *Manager) verify(data []byte, expectedVersion, libraryID string) (*PreparedBundle, *Rejection) {
	if m.Verify == nil {
		return nil, &Rejection{Code: RejectMalformed, Message: "No knowledge verifier is configured."}
	}
	return m.Verify(data, expectedVersion, libraryID)
}

func (m *Manager) installOrUpdate(ctx context.Context, store Store, item selectedSource, isUpdate bool, deadline time.Duration) SourceOperationResult {
	alias, source := item.alias, item.source
	if source.Type == SourceGit {
		return failedOperation(alias, fmt.Sprintf("Git knowledge sources are not installed by creatio-mcp-go; run "+
			"`clio %s --source %s`.", map[bool]string{false: "install-knowledge", true: "update-knowledge"}[isUpdate], alias), "")
	}
	current, diagnostic := store.ReadCurrentForWrite(alias)
	if diagnostic != "" {
		return failedOperation(alias, diagnostic, "")
	}
	if isUpdate && current == nil {
		return failedOperation(alias, fmt.Sprintf("Knowledge source '%s' is not installed; use install-knowledge.", alias), "")
	}
	if !isUpdate && current != nil {
		valid, validationDiagnostic := m.isCurrentValid(store, alias, source, current)
		if valid {
			return SourceOperationResult{SourceAlias: alias, Success: true, Status: "already-installed",
				Message: fmt.Sprintf("Knowledge source '%s' sequence %d is already installed.", alias, current.Active.Sequence)}
		}
		diagnostic = validationDiagnostic
	}
	repair := !isUpdate && current != nil
	transport, ok := m.Transports[source.Type]
	if !ok {
		return failedOperation(alias, fmt.Sprintf("Knowledge transport '%s' is not registered.", source.Type), "")
	}
	result, err := m.searchCandidate(ctx, store, alias, source, current, repair, transport, deadline, diagnostic)
	if err != nil {
		return failedOperation(alias, err.Error(), "")
	}
	return result
}

func (m *Manager) isCurrentValid(store Store, alias string, source SourceConfig, current *CurrentState) (bool, string) {
	_, data, diagnostic := store.ReadCandidate(alias, current.Active)
	if diagnostic != "" {
		return false, diagnostic
	}
	prepared, rejection := m.verify(data, current.Active.LibraryVersion, source.LibraryID)
	if rejection != nil {
		return false, rejection.Message
	}
	return prepared.Sequence == current.Active.Sequence && prepared.LibraryID == source.LibraryID, ""
}

type candidateSearch struct {
	rejected        map[string]bool
	highest         string
	fallbackCeiling string
	fingerprint     string
	lastRejected    string
	lastDiagnostic  string
}

// searchCandidate is clio's SearchInstallableCandidate / TryInstallNextCandidate / CompleteCandidateSearch.
func (m *Manager) searchCandidate(ctx context.Context, store Store, alias string, source SourceConfig, current *CurrentState,
	repair bool, transport ArtifactTransport, deadline time.Duration, initialDiagnostic string) (SourceOperationResult, error) {
	search := candidateSearch{rejected: map[string]bool{}, lastDiagnostic: initialDiagnostic}
	started := time.Now()
	for attempt := 0; attempt < maxCandidateAttempts; attempt++ {
		remaining := deadline - time.Since(started)
		if remaining <= 0 {
			search.lastDiagnostic = "The operation-wide knowledge retrieval deadline elapsed."
			break
		}
		request := TransportRequest{Alias: alias, Source: source, Rejected: search.rejected,
			HighestObserved: search.highest, FallbackCeiling: search.fallbackCeiling,
			CatalogFingerprint: search.fingerprint, Deadline: remaining}
		if current != nil {
			if repair {
				request.ExactRevision = current.Active.ResolvedRevision
			} else {
				request.ActiveRevision = current.Active.ResolvedRevision
			}
		}
		retrieved := transport.Retrieve(ctx, request)
		if retrieved.CatalogFingerprint != "" {
			search.fingerprint = retrieved.CatalogFingerprint
		}
		switch retrieved.Status {
		case TransportNoCandidate:
			if current != nil && !repair && len(search.rejected) == 0 {
				store.TryRecordPublisherCheck(alias, current.Active)
			}
			return m.completeSearch(alias, current, repair, search), nil
		case TransportFailed:
			return failedOperation(alias, untrustedOr(retrieved.Diagnostic, fmt.Sprintf("Knowledge source '%s' could not be retrieved.", alias)), ""), nil
		}
		revision := retrieved.ResolvedRevision
		if strings.TrimSpace(revision) == "" {
			search.lastDiagnostic = firstNonEmpty(retrieved.Diagnostic, fmt.Sprintf("Knowledge source '%s' returned no usable candidate.", alias))
			break
		}
		if search.rejected[strings.ToLower(revision)] {
			search.lastDiagnostic = firstNonEmpty(retrieved.Diagnostic, fmt.Sprintf("Knowledge transport repeated rejected revision '%s'.", revision))
			break
		}
		search.highest = transport.GreaterRevision(search.highest, revision)
		search.fallbackCeiling = revision
		search.lastRejected = revision
		if retrieved.Status != TransportDownloaded {
			search.lastDiagnostic = firstNonEmpty(retrieved.Diagnostic, "The transport rejected the candidate.")
			if search.rejectAgain(revision) {
				break
			}
			continue
		}
		if len(retrieved.CandidateBytes) == 0 {
			return SourceOperationResult{}, errors.New("Knowledge transport returned no candidate bytes or path.")
		}
		prepared, rejection := m.verify(retrieved.CandidateBytes, revision, source.LibraryID)
		if rejection != nil || prepared == nil || blank(prepared.LibraryID) || blank(prepared.LibraryVersion) || prepared.LibraryID != source.LibraryID {
			message := "The downloaded knowledge bundle was rejected."
			if rejection != nil && rejection.Message != "" {
				message = rejection.Message
			}
			search.lastDiagnostic = message
			if search.rejectAgain(revision) {
				break
			}
			continue
		}
		publication := Publication{SourceAlias: alias, LibraryID: source.LibraryID, LibraryVersion: prepared.LibraryVersion,
			Sequence: prepared.Sequence, TransportType: string(source.Type), Location: source.Location,
			ResolvedRevision: revision, BundleBytes: retrieved.CandidateBytes, IsUpdate: current != nil, AllowRepair: repair}
		if current != nil {
			active := current.Active
			publication.ExpectedActive = &active
		}
		published, err := store.Publish(publication)
		if err != nil {
			return SourceOperationResult{}, err
		}
		operation := toOperation(alias, published)
		if operation.Success && strings.TrimSpace(retrieved.Diagnostic) != "" {
			operation.Message += " " + safe(retrieved.Diagnostic)
		}
		return operation, nil
	}
	return m.completeSearch(alias, current, repair, search), nil
}

// rejectAgain records a refused revision and reports whether it had been refused before.
func (s *candidateSearch) rejectAgain(revision string) bool {
	key := strings.ToLower(revision)
	if s.rejected[key] {
		return true
	}
	s.rejected[key] = true
	return false
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func (m *Manager) completeSearch(alias string, current *CurrentState, repair bool, search candidateSearch) SourceOperationResult {
	if search.lastRejected != "" {
		return failedOperation(alias, fmt.Sprintf("No compatible knowledge candidate was found after rejecting %s: %s",
			search.lastRejected, Untrusted(search.lastDiagnostic)), stateRejected)
	}
	if current != nil && !repair {
		return SourceOperationResult{SourceAlias: alias, Success: true, Status: stateUpToDate,
			Message: fmt.Sprintf("Knowledge source '%s' is up to date at %s.", alias, current.Active.ResolvedRevision)}
	}
	return failedOperation(alias, untrustedOr(search.lastDiagnostic, fmt.Sprintf("Knowledge source '%s' returned no installable candidate.", alias)), "")
}

// ProbeUpdate is clio's ProbeArtifactUpdate for info-knowledge checkUpdates=true: one transport call,
// nothing published.
func (m *Manager) ProbeUpdate(ctx context.Context, alias string, source SourceConfig, current *CurrentState) (string, string) {
	fallback := "not-installed"
	activeRevision := ""
	if current != nil {
		fallback, activeRevision = stateUnknown, current.Active.ResolvedRevision
	}
	transport, ok := m.Transports[source.Type]
	if !ok {
		return fallback, ""
	}
	remote := transport.Retrieve(ctx, TransportRequest{Alias: alias, Source: source, Rejected: map[string]bool{},
		ActiveRevision: activeRevision, Deadline: operationDeadline})
	if remote.Status != TransportDownloaded {
		availability := stateUnknown
		if remote.Status == TransportNoCandidate {
			availability = stateUpToDate
		}
		diagnostic := ""
		if remote.Status == TransportRejected || remote.Status == TransportFailed {
			diagnostic = Untrusted(firstNonEmpty(remote.Diagnostic, "The remote knowledge source could not be checked."))
		}
		return availability, diagnostic
	}
	prepared, rejection := m.verify(remote.CandidateBytes, remote.ResolvedRevision, source.LibraryID)
	if rejection == nil && prepared != nil && prepared.LibraryID == source.LibraryID {
		return "available", ""
	}
	message := ""
	if rejection != nil {
		message = rejection.Message
	}
	return stateRejected, untrustedOr(message, "The remote candidate failed verification.")
}

func participationFrom(value string) (Participation, error) {
	switch strings.ToLower(value) {
	case "isolated", "supplement", "authoritative":
		return Participation(strings.ToLower(value)), nil
	}
	return "", errors.New("Knowledge source participation must be isolated, supplement, or authoritative. (Parameter 'value')")
}

func sourceTypeFrom(value string) (SourceType, error) {
	switch strings.ToLower(value) {
	case "git", "nuget", "github-release":
		return SourceType(strings.ToLower(value)), nil
	}
	return "", errors.New("Knowledge source type must be 'github-release', 'git', or 'nuget'. (Parameter 'value')")
}

// Add is add-knowledge-source after the caller confirmed it.
func (m *Manager) Add(request AddRequest) CommandResult {
	sourceType, err := sourceTypeFrom(request.Type)
	if err != nil {
		return failedCommand(request.Alias, err.Error())
	}
	participation, err := participationFrom(request.Participation)
	if err != nil {
		return failedCommand(request.Alias, err.Error())
	}
	validated, err := ValidateSourceInput(SourceInput{LibraryID: request.LibraryID, Type: sourceType, Location: request.Location,
		TrustedKeyID: request.TrustedKeyID, TrustedPublicKeyPath: request.TrustedPublicKeyPath, PackageID: request.PackageID,
		RepositoryOwner: request.RepositoryOwner, RepositoryName: request.RepositoryName, AssetName: request.AssetName,
		Branch: request.Branch, Tag: request.Tag, Commit: request.Commit, Enabled: request.Enabled, Priority: request.Priority,
		Participation: participation})
	if err != nil {
		return failedCommand(request.Alias, err.Error())
	}
	if validated.TrustedPublicKeyPath != "" {
		if _, _, ok := readPublicKeyFile(validated.TrustedPublicKeyPath); !ok {
			return failedCommand(request.Alias, "Knowledge trusted-public-key-path must identify an existing bounded local regular file "+
				"containing one P-256 PUBLIC KEY PEM; network, device, reparse, and private-key files are refused.")
		}
	}
	added, err := TryAddSource(m.settingsPath(), request.Alias, validated)
	if err != nil {
		return failedCommand(request.Alias, err.Error())
	}
	m.changed()
	if !added {
		return failedCommand(request.Alias, fmt.Sprintf("Knowledge source alias '%s' or library '%s' is already configured.", request.Alias, validated.LibraryID))
	}
	return CommandResult{Success: true, SourceAlias: request.Alias,
		Message: fmt.Sprintf("Knowledge source '%s' was added. Run install-knowledge --source %s to install it.", request.Alias, request.Alias)}
}

// Remove is remove-knowledge-source.
func (m *Manager) Remove(alias string, confirmed bool) (CommandResult, error) {
	if !confirmed {
		return failedCommand(alias, "Removing a knowledge source requires explicit confirmation."), nil
	}
	settings, err := m.loadConfiguration()
	if err != nil {
		return CommandResult{}, err
	}
	_, expected, ok := settings.Knowledge.FindSource(alias)
	if !ok {
		return failedCommand(alias, fmt.Sprintf("Knowledge source '%s' is not configured.", alias)), nil
	}
	if strings.EqualFold(alias, curatedAlias) || strings.EqualFold(expected.LibraryID, curatedLibraryID) {
		return failedCommand(alias, fmt.Sprintf("Built-in knowledge source '%s' cannot be removed. "+
			"Use disable-knowledge-source --alias %s to stop serving it while retaining its cache.", alias, alias)), nil
	}
	removed, err := TryRemoveSource(m.settingsPath(), alias, expected)
	if err != nil {
		return CommandResult{}, err
	}
	if !removed {
		return failedCommand(alias, fmt.Sprintf("Knowledge source '%s' changed while it was being removed; retry.", alias)), nil
	}
	m.changed()
	orphaned := func(reason string) CommandResult {
		return failedCommand(alias, fmt.Sprintf("Knowledge source '%s' was removed and deactivated, but its orphaned cache could not be deleted: %s", alias, reason))
	}
	store, err := m.store(settings)
	if err != nil {
		return orphaned(safe(err.Error())), nil
	}
	deletion, err := store.Delete(alias, true)
	if err != nil {
		return orphaned(safe(err.Error())), nil
	}
	if deletion.Status != StatusDeleted && deletion.Status != StatusNotInstalled {
		return orphaned(deletion.Message), nil
	}
	return CommandResult{Success: true, SourceAlias: alias, Message: fmt.Sprintf("Knowledge source '%s' was removed.", alias)}, nil
}

// SetEnabled is enable-/disable-knowledge-source.
func (m *Manager) SetEnabled(alias string, enabled bool) CommandResult {
	if err := SetSourceEnabled(m.settingsPath(), alias, enabled); err != nil {
		return failedCommand(alias, err.Error())
	}
	m.changed()
	state := "disabled"
	if enabled {
		state = "enabled"
	}
	return CommandResult{Success: true, SourceAlias: alias, Message: fmt.Sprintf("Knowledge source '%s' was %s; its cache was retained.", alias, state)}
}

// mkdirRoot is the directory half of clio's GetOrCreateRoot: links in the ancestry are refused before and
// after the directory is created.
func mkdirRoot(root string) error {
	if hasLinkInAncestry(root) {
		return errors.New("The configured knowledge.root-path cannot contain symbolic links or junctions.")
	}
	if err := os.MkdirAll(root, 0o777); err != nil {
		return err
	}
	if hasLinkInAncestry(root) {
		return errors.New("The configured knowledge.root-path cannot contain symbolic links or junctions.")
	}
	return nil
}
