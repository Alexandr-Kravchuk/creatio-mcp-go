package knowledge

// The write side of the installation store, ported from clio 8.1.0.142's KnowledgeSourceInstallationStore.
// Every mutation runs under clio's own lock files, in clio's order (source lock, then library lock), and
// publishes in clio's crash-safe sequence: the generation is written into staging, the library high-water
// mark is made durable, the staging directory is moved into generations/ in one rename, current.json is
// replaced atomically and only then are obsolete generations pruned. A clio process and this server can
// therefore publish, repair, reconcile and delete the same store at the same time.

import (
	"archive/zip"
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// StoreLockTimeout is clio's KnowledgeInstallationStoreOptions.LockTimeoutMilliseconds.
	StoreLockTimeout = 30 * time.Second
	storeLockPoll    = 25 * time.Millisecond
	quarantinePrefix = ".clio-deleting-"
)

// InstallationStatus is clio's KnowledgeInstallationStatus, spelled as clio reports it
// (Status.ToString().ToLowerInvariant()).
type InstallationStatus string

// The installation outcomes.
const (
	StatusInstalled            InstallationStatus = "installed"
	StatusUpdated              InstallationStatus = "updated"
	StatusAlreadyInstalled     InstallationStatus = "alreadyinstalled"
	StatusDeleted              InstallationStatus = "deleted"
	StatusNotInstalled         InstallationStatus = "notinstalled"
	StatusConfirmationRequired InstallationStatus = "confirmationrequired"
	StatusRejected             InstallationStatus = "rejected"
	StatusFailed               InstallationStatus = "failed"
)

// InstallationResult is clio's KnowledgeInstallationResult.
type InstallationResult struct {
	Status         InstallationStatus
	Message        string
	PackageVersion string
	RootPath       string
}

// Success is clio's IsSuccess.
func (r InstallationResult) Success() bool {
	switch r.Status {
	case StatusInstalled, StatusUpdated, StatusAlreadyInstalled, StatusDeleted:
		return true
	}
	return false
}

// Publication is clio's KnowledgeGenerationPublication.
type Publication struct {
	SourceAlias      string
	LibraryID        string
	LibraryVersion   string
	Sequence         uint64
	TransportType    string
	Location         string
	ResolvedRevision string
	BundleBytes      []byte
	IsUpdate         bool
	ExpectedActive   *GenerationPointer
	AllowRepair      bool
}

// dotnetTime formats a timestamp the way System.Text.Json writes a UTC DateTimeOffset.
func dotnetTime(t time.Time) string {
	return t.UTC().Truncate(100 * time.Nanosecond).Format("2006-01-02T15:04:05.9999999-07:00")
}

// MarshalJSON writes the pointer in clio's field order and timestamp form.
func (p GenerationPointer) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		LibraryID        string `json:"libraryId"`
		LibraryVersion   string `json:"libraryVersion"`
		Sequence         uint64 `json:"sequence"`
		RelativePath     string `json:"relativePath"`
		BundleDigest     string `json:"bundleDigest"`
		ResolvedRevision string `json:"resolvedRevision"`
		ActivatedAtUtc   string `json:"activatedAtUtc"`
	}{p.LibraryID, p.LibraryVersion, p.Sequence, p.RelativePath, p.BundleDigest, p.ResolvedRevision, dotnetTime(p.ActivatedAtUtc)})
}

// samePointer is clio's record equality of two generation pointers.
func samePointer(left, right GenerationPointer) bool {
	return left.LibraryID == right.LibraryID && left.LibraryVersion == right.LibraryVersion &&
		left.Sequence == right.Sequence && left.RelativePath == right.RelativePath &&
		left.BundleDigest == right.BundleDigest && left.ResolvedRevision == right.ResolvedRevision &&
		left.ActivatedAtUtc.Equal(right.ActivatedAtUtc)
}

type currentStateFile struct {
	SchemaVersion int                `json:"schemaVersion"`
	SourceAlias   string             `json:"sourceAlias"`
	Active        GenerationPointer  `json:"active"`
	Previous      *GenerationPointer `json:"previous"`
}

type installMetadataFile struct {
	SchemaVersion    int    `json:"schemaVersion"`
	SourceAlias      string `json:"sourceAlias"`
	LibraryID        string `json:"libraryId"`
	LibraryVersion   string `json:"libraryVersion"`
	Sequence         uint64 `json:"sequence"`
	TransportType    string `json:"transportType"`
	Location         string `json:"location"`
	ResolvedRevision string `json:"resolvedRevision"`
	BundleDigest     string `json:"bundleDigest"`
	InstalledAtUtc   string `json:"installedAtUtc"`
}

type publisherCheckState struct {
	SchemaVersion    int    `json:"schemaVersion"`
	SourceAlias      string `json:"sourceAlias"`
	LibraryID        string `json:"libraryId"`
	Sequence         uint64 `json:"sequence"`
	BundleDigest     string `json:"bundleDigest"`
	ResolvedRevision string `json:"resolvedRevision"`
	CheckedAtUtc     string `json:"checkedAtUtc"`
}

// clioJSON is System.Text.Json with WriteIndented: two-space indentation and the default encoder's escaping.
func clioJSON(value any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	// System.Text.Json escapes ordinary strings with its default encoder; DateTimeOffset values stay raw.
	return STJEscape(bytes.TrimRight(buffer.Bytes(), "\n"), IsDotnetTimestamp), nil
}

// newGUID is a random GUID in .NET's "N" format.
func newGUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return hex.EncodeToString(b[:])
}

func computeDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// writeAtomic is clio's WriteAtomicJson: a uniquely named temporary file in the same directory, moved over
// the target with overwrite, removed on failure.
func writeAtomic(dir, name string, data []byte) error {
	target, err := resolveRelative(dir, name)
	if err != nil {
		return err
	}
	temporary, err := resolveRelative(dir, "."+name+"."+newGUID()+".tmp")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(temporary) }()
	if err := os.WriteFile(temporary, data, 0o666); err != nil {
		return err
	}
	// On Windows a reader holding the target open refuses the replace for a moment; that is retried for
	// the same bounded window clio gives appsettings.json. On Unix the rename cannot be refused that way.
	return publishWithRetry(temporary, target)
}

func (s Store) locksRoot() (string, error) {
	return s.ensureDirectory(s.sourcesRoot(), locksDirectoryName)
}

func (s Store) ensureDirectory(parent, name string) (string, error) {
	path, err := resolveRelative(parent, name)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(path, 0o777); err != nil {
		return "", err
	}
	return path, ensureNoLink(parent, path)
}

// ensureOwnedRoot is clio's EnsureOwnedRoot: the root carries .clio-knowledge-root, or is empty and gets it.
func (s Store) ensureOwnedRoot() error {
	if err := os.MkdirAll(s.Root, 0o777); err != nil {
		return err
	}
	if err := ensureNoLink(s.Root, s.Root); err != nil {
		return err
	}
	owner := filepath.Join(s.Root, rootOwnerFileName)
	for attempt := 0; ; attempt++ {
		if _, err := os.Lstat(owner); err == nil {
			if err := ensureNoLink(s.Root, owner); err != nil {
				return err
			}
			content, err := readBoundedFile(owner, maxMarkerBytes)
			if err == nil && string(content) == rootOwnerContent {
				return nil
			}
			// A concurrent creator may be between creating and filling the marker; clio itself writes it
			// non-atomically. Give it a moment before calling the marker invalid.
			if attempt < 20 {
				time.Sleep(25 * time.Millisecond)
				continue
			}
			return errors.New("Knowledge root ownership marker is invalid.")
		}
		entries, err := os.ReadDir(s.Root)
		if err != nil {
			return err
		}
		if len(entries) > 0 {
			if attempt < 20 {
				time.Sleep(25 * time.Millisecond)
				continue
			}
			return errors.New("Knowledge root is non-empty and is not owned by Clio.")
		}
		return writeAtomic(s.Root, rootOwnerFileName, []byte(rootOwnerContent))
	}
}

// resolveSourceRoot is clio's ResolveSourceRoot; with create it makes the root, sources/ and the source
// directory with its ownership marker.
func (s Store) resolveSourceRoot(alias string, create bool) (string, error) {
	if err := ValidateAlias(alias); err != nil {
		return "", err
	}
	if create {
		if err := s.ensureOwnedRoot(); err != nil {
			return "", err
		}
		if err := os.MkdirAll(s.sourcesRoot(), 0o777); err != nil {
			return "", err
		}
	}
	sourceRoot := s.SourceRoot(alias)
	if !create {
		return sourceRoot, nil
	}
	if _, err := os.Stat(sourceRoot); errors.Is(err, os.ErrNotExist) {
		// clio creates the directory and then writes its marker, so a reader can meet a source directory
		// without one. This server builds the directory with its marker beside it and renames it into
		// place, so a source root it creates is never seen unowned.
		building := filepath.Join(s.sourcesRoot(), ".clio-creating-"+SourceKey(alias)+"-"+newGUID())
		if err := os.Mkdir(building, 0o777); err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(building, sourceOwnerFileName), []byte(alias+"\n"), 0o666); err != nil {
			_ = os.RemoveAll(building)
			return "", err
		}
		renameErr := os.Rename(building, sourceRoot)
		_ = os.RemoveAll(building)
		if renameErr == nil {
			return sourceRoot, nil
		}
	}
	// Another writer may have created the directory a moment ago and not yet written its marker.
	var err error
	for attempt := 0; attempt < 20; attempt++ {
		if err = s.validateSourceRoot(alias, sourceRoot); err == nil {
			return sourceRoot, nil
		}
		time.Sleep(25 * time.Millisecond)
	}
	return "", err
}

func (s Store) lockPath(name string) (string, error) {
	locks, err := s.locksRoot()
	if err != nil {
		return "", err
	}
	return resolveRelative(locks, name)
}

// WithSourceLock runs action under clio's source mutation lock (sources/.locks/<key>.lock).
func (s Store) WithSourceLock(alias string, action func() error) error {
	if _, err := s.resolveSourceRoot(alias, true); err != nil {
		return err
	}
	path, err := s.lockPath(SourceKey(alias) + ".lock")
	if err != nil {
		return err
	}
	lock, err := acquireFileLock(path, StoreLockTimeout, storeLockPoll)
	if errors.Is(err, errLockBusy) {
		return errors.New("Timed out waiting for the knowledge source mutation lock.")
	} else if err != nil {
		return err
	}
	defer lock.Unlock()
	return action()
}

// TryWithSourceLock runs action only if the source lock is free; it reports whether it ran.
func (s Store) TryWithSourceLock(alias string, action func() error) (bool, error) {
	if _, err := s.resolveSourceRoot(alias, true); err != nil {
		return false, err
	}
	path, err := s.lockPath(SourceKey(alias) + ".lock")
	if err != nil {
		return false, err
	}
	lock, err := tryFileLock(path)
	if err != nil {
		return false, nil
	}
	defer lock.Unlock()
	return true, action()
}

func (s Store) withLibraryLock(libraryID string, action func() error) error {
	path, err := s.lockPath("library-" + SourceKey(libraryID) + ".lock")
	if err != nil {
		return err
	}
	lock, err := acquireFileLock(path, StoreLockTimeout, storeLockPoll)
	if errors.Is(err, errLockBusy) {
		return errors.New("Timed out waiting for the knowledge library mutation lock.")
	} else if err != nil {
		return err
	}
	defer lock.Unlock()
	return action()
}

func (s Store) writeHighWater(mark highWaterMark) error {
	history, err := s.ensureDirectory(s.sourcesRoot(), historyDirectory)
	if err != nil {
		return err
	}
	data, err := clioJSON(mark)
	if err != nil {
		return err
	}
	return writeAtomic(history, SourceKey(mark.LibraryID)+".json", data)
}

func (s Store) writeCurrent(sourceRoot string, state currentStateFile) error {
	data, err := clioJSON(state)
	if err != nil {
		return err
	}
	return writeAtomic(sourceRoot, currentFileName, data)
}

// ReadCurrentForWrite is clio's ReadCurrent: like ReadCurrent, but a marker that lags the library's
// accepted sequence is reconciled under the source and library locks (current.json is moved to the
// accepted generation and obsolete generations are pruned), exactly as any clio knowledge command does.
// Only lifecycle operations call it; readers keep the non-writing ReadCurrent.
func (s Store) ReadCurrentForWrite(alias string) (*CurrentState, string) {
	state, diagnostic, err := s.readCurrentMarker(alias)
	if err != nil {
		return nil, fmt.Sprintf("Knowledge source '%s' activation marker could not be read: %s", alias, err.Error())
	}
	if state == nil || diagnostic != "" {
		return nil, diagnostic
	}
	mark, err := s.readHighWater(state.Active.LibraryID)
	if err != nil {
		return nil, fmt.Sprintf("Knowledge source '%s' activation marker could not be read: %s", alias, err.Error())
	}
	if mark == nil || !conflictsWithAccepted(state.Active.Sequence, state.Active.BundleDigest, mark.Sequence, mark.BundleDigest) {
		return state, ""
	}
	var recovered *CurrentState
	var recoveryDiagnostic string
	err = s.WithSourceLock(alias, func() error {
		return s.withLibraryLock(state.Active.LibraryID, func() error {
			var innerErr error
			recovered, recoveryDiagnostic, innerErr = s.reconcileInterruptedPublication(alias)
			return innerErr
		})
	})
	if err != nil {
		return nil, fmt.Sprintf("Knowledge source '%s' activation marker could not be read: %s", alias, err.Error())
	}
	return recovered, recoveryDiagnostic
}

func (s Store) reconcileInterruptedPublication(alias string) (*CurrentState, string, error) {
	current, diagnostic, err := s.readCurrentMarker(alias)
	if err != nil || current == nil || diagnostic != "" {
		return nil, diagnostic, err
	}
	mark, err := s.readHighWater(current.Active.LibraryID)
	if err != nil {
		return nil, "", err
	}
	if mark == nil || !conflictsWithAccepted(current.Active.Sequence, current.Active.BundleDigest, mark.Sequence, mark.BundleDigest) {
		return current, "", nil
	}
	if mark.Sequence <= current.Active.Sequence {
		return nil, fmt.Sprintf("Knowledge source '%s' activation marker conflicts with accepted library sequence %d and cannot be recovered automatically.",
			alias, mark.Sequence), nil
	}
	sourceRoot := s.SourceRoot(alias)
	generationsRoot, err := resolveRelative(sourceRoot, generationsDir)
	if err != nil {
		return nil, "", err
	}
	name := fmt.Sprintf("%d-%s", mark.Sequence, mark.BundleDigest[:12])
	generationRoot, err := resolveRelative(generationsRoot, name)
	if err != nil {
		return nil, "", err
	}
	if _, err := os.Stat(generationRoot); err != nil {
		return nil, fmt.Sprintf("Knowledge source '%s' accepted generation '%s' is missing; activation cannot be recovered automatically.", alias, name), nil
	}
	metadata, diagnostic := s.readRecoverableGeneration(generationsRoot, generationRoot, name,
		generationIdentityOf{alias, current.Active.LibraryID, mark.Sequence, mark.BundleDigest})
	if metadata == nil {
		return nil, diagnostic, nil
	}
	previous := current.Active
	recovered := currentStateFile{SchemaVersion: storeSchemaVersion, SourceAlias: alias, Previous: &previous,
		Active: GenerationPointer{LibraryID: metadata.LibraryID, LibraryVersion: metadata.LibraryVersion,
			Sequence: metadata.Sequence, RelativePath: generationsDir + "/" + name, BundleDigest: metadata.BundleDigest,
			ResolvedRevision: metadata.ResolvedRevision, ActivatedAtUtc: time.Now().UTC()}}
	if err := s.writeCurrent(sourceRoot, recovered); err != nil {
		return nil, "", err
	}
	if err := s.prune(generationsRoot, recovered); err != nil {
		return nil, "", err
	}
	return &CurrentState{SchemaVersion: recovered.SchemaVersion, SourceAlias: alias, Active: recovered.Active, Previous: recovered.Previous}, "", nil
}

type generationIdentityOf struct {
	alias, libraryID string
	sequence         uint64
	digest           string
}

// readRecoverableGeneration is clio's TryReadRecoverableGeneration.
func (s Store) readRecoverableGeneration(generationsRoot, generationRoot, name string, expected generationIdentityOf) (*InstallMetadata, string) {
	fail := func(err error) (*InstallMetadata, string) {
		return nil, fmt.Sprintf("Immutable knowledge generation '%s' could not be recovered: %s", name, err.Error())
	}
	if err := ensureTreeHasNoLinks(generationsRoot, generationRoot); err != nil {
		return fail(err)
	}
	bundle, err := readBoundedFile(filepath.Join(generationRoot, bundleFileName), maxBundleBytes)
	if err != nil {
		return fail(err)
	}
	data, err := readBoundedFile(filepath.Join(generationRoot, metadataFileName), maxMarkerBytes)
	if err != nil {
		return fail(err)
	}
	var metadata InstallMetadata
	if err := decodeStrict(data, &metadata); err != nil {
		return fail(err)
	}
	if computeDigest(bundle) != expected.digest || metadata.SchemaVersion != storeSchemaVersion ||
		!strings.EqualFold(metadata.SourceAlias, expected.alias) || metadata.LibraryID != expected.libraryID ||
		metadata.Sequence != expected.sequence || metadata.BundleDigest != expected.digest ||
		blank(metadata.LibraryVersion) || blank(metadata.TransportType) || blank(metadata.Location) || blank(metadata.ResolvedRevision) {
		return nil, fmt.Sprintf("Immutable knowledge generation '%s' exists with unexpected content.", name)
	}
	return &metadata, ""
}

func ensureTreeHasNoLinks(root, directory string) error {
	if err := ensureNoLink(root, directory); err != nil {
		return err
	}
	return filepath.WalkDir(directory, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == directory {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if isReparsePoint(info) {
			return errors.New("Knowledge generation recovery cannot remove symbolic links or junctions.")
		}
		return nil
	})
}

func (s Store) failed(message string) InstallationResult {
	return InstallationResult{Status: StatusFailed, Message: message, RootPath: s.Root}
}

// Publish is clio's Publish: one generation written under the source and library locks.
func (s Store) Publish(p Publication) (InstallationResult, error) {
	for name, value := range map[string]string{"sourceAlias": p.SourceAlias, "libraryId": p.LibraryID,
		"libraryVersion": p.LibraryVersion, "transportType": p.TransportType, "location": p.Location,
		"resolvedRevision": p.ResolvedRevision} {
		if blank(value) {
			return InstallationResult{}, fmt.Errorf("The value cannot be an empty string or composed entirely of whitespace. (Parameter '%s')", name)
		}
	}
	if p.Sequence == 0 || len(p.BundleBytes) == 0 || len(p.BundleBytes) > maxBundleBytes {
		return s.failed("Knowledge generation is outside supported bounds."), nil
	}
	sourceRoot, err := s.resolveSourceRoot(p.SourceAlias, true)
	if err != nil {
		return InstallationResult{}, err
	}
	var result InstallationResult
	err = s.WithSourceLock(p.SourceAlias, func() error {
		// A delete that held the lock while this call waited for it removed the source directory; make it
		// again now that nobody else can, instead of failing the way clio does in that window.
		if _, err := s.resolveSourceRoot(p.SourceAlias, true); err != nil {
			return err
		}
		return s.withLibraryLock(p.LibraryID, func() error {
			var innerErr error
			result, innerErr = s.publishLocked(sourceRoot, p)
			return innerErr
		})
	})
	return result, err
}

func (s Store) publishLocked(sourceRoot string, p Publication) (InstallationResult, error) {
	if err := s.validateSourceRoot(p.SourceAlias, sourceRoot); err != nil {
		return InstallationResult{}, err
	}
	current, diagnostic, err := s.readCurrentMarker(p.SourceAlias)
	if err != nil {
		return InstallationResult{}, err
	}
	if diagnostic != "" {
		return s.failed(diagnostic), nil
	}
	if p.IsUpdate && (current == nil || p.ExpectedActive == nil || !samePointer(current.Active, *p.ExpectedActive)) {
		return s.failed(fmt.Sprintf("Knowledge source '%s' changed while the operation was in progress; retry.", p.SourceAlias)), nil
	}
	digest := computeDigest(p.BundleBytes)
	if refusal, err := s.evaluateSequenceAcceptance(p, current, digest); err != nil || refusal != nil {
		if refusal != nil {
			return *refusal, err
		}
		return InstallationResult{}, err
	}
	repairing := current != nil && p.Sequence == current.Active.Sequence
	name := fmt.Sprintf("%d-%s", p.Sequence, digest[:12])
	if repairing {
		name += "-repair-" + newGUID()
	}
	generationsRoot, err := s.ensureDirectory(sourceRoot, generationsDir)
	if err != nil {
		return InstallationResult{}, err
	}
	generationRoot, err := resolveRelative(generationsRoot, name)
	if err != nil {
		return InstallationResult{}, err
	}
	if materializationDiagnostic, err := s.materializeGeneration(sourceRoot, generationsRoot, generationRoot, name, p, current, digest); err != nil {
		return InstallationResult{}, err
	} else if materializationDiagnostic != "" {
		return s.failed(materializationDiagnostic), nil
	}
	next := currentStateFile{SchemaVersion: storeSchemaVersion, SourceAlias: p.SourceAlias,
		Active: GenerationPointer{LibraryID: p.LibraryID, LibraryVersion: p.LibraryVersion, Sequence: p.Sequence,
			RelativePath: generationsDir + "/" + name, BundleDigest: digest, ResolvedRevision: p.ResolvedRevision,
			ActivatedAtUtc: time.Now().UTC()}}
	if repairing {
		next.Previous = current.Previous
	} else if current != nil {
		previous := current.Active
		next.Previous = &previous
	}
	if err := s.writeCurrent(sourceRoot, next); err != nil {
		return InstallationResult{}, err
	}
	if err := s.prune(generationsRoot, next); err != nil {
		return InstallationResult{}, err
	}
	status := StatusInstalled
	if p.IsUpdate {
		status = StatusUpdated
	}
	return InstallationResult{Status: status, PackageVersion: p.LibraryVersion, RootPath: s.Root,
		Message: fmt.Sprintf("Knowledge source '%s' sequence %d was installed at %s.", p.SourceAlias, p.Sequence, generationRoot)}, nil
}

func (s Store) evaluateSequenceAcceptance(p Publication, current *CurrentState, digest string) (*InstallationResult, error) {
	mark, err := s.readHighWater(p.LibraryID)
	if err != nil {
		return nil, err
	}
	if mark != nil && conflictsWithAccepted(p.Sequence, digest, mark.Sequence, mark.BundleDigest) {
		return &InstallationResult{Status: StatusRejected, PackageVersion: p.LibraryVersion, RootPath: s.Root,
			Message: fmt.Sprintf("Knowledge library '%s' rejected sequence %d; highest accepted sequence is %d.", p.LibraryID, p.Sequence, mark.Sequence)}, nil
	}
	if current == nil {
		return nil, nil
	}
	if conflictsWithAccepted(p.Sequence, digest, current.Active.Sequence, current.Active.BundleDigest) {
		return &InstallationResult{Status: StatusRejected, PackageVersion: p.LibraryVersion, RootPath: s.Root,
			Message: fmt.Sprintf("Knowledge source '%s' rejected sequence %d; active sequence is %d.", p.SourceAlias, p.Sequence, current.Active.Sequence)}, nil
	}
	if p.Sequence == current.Active.Sequence && !p.AllowRepair {
		return &InstallationResult{Status: StatusAlreadyInstalled, PackageVersion: p.LibraryVersion, RootPath: s.Root,
			Message: fmt.Sprintf("Knowledge source '%s' sequence %d is already installed.", p.SourceAlias, p.Sequence)}, nil
	}
	if !p.IsUpdate {
		result := s.failed(fmt.Sprintf("Knowledge source '%s' is already installed; use update-knowledge.", p.SourceAlias))
		return &result, nil
	}
	return nil, nil
}

// materializeGeneration is clio's TryMaterializeGeneration. The order of the three steps at the end is
// load-bearing: the high-water mark must be durable before the generation becomes visible.
func (s Store) materializeGeneration(sourceRoot, generationsRoot, generationRoot, name string, p Publication,
	current *CurrentState, digest string) (string, error) {
	staging, err := s.ensureDirectory(sourceRoot, stagingDirectory)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(generationRoot); err == nil {
		if diagnostic := s.removeRecoverableOrphan(generationsRoot, generationRoot, name, current, p, digest); diagnostic != "" {
			return diagnostic, nil
		}
	}
	stagingRoot, err := resolveRelative(staging, name+"-"+newGUID())
	if err != nil {
		return "", err
	}
	if err := os.Mkdir(stagingRoot, 0o777); err != nil {
		return "", err
	}
	defer func() {
		if _, err := os.Stat(stagingRoot); err == nil {
			_ = deleteTree(stagingRoot)
		}
	}()
	if err := writeGeneration(stagingRoot, p, digest); err != nil {
		return "", err
	}
	if err := s.writeHighWater(highWaterMark{SchemaVersion: storeSchemaVersion, LibraryID: p.LibraryID, Sequence: p.Sequence, BundleDigest: digest}); err != nil {
		return "", err
	}
	if _, err := os.Lstat(generationRoot); err == nil {
		return "", fmt.Errorf("Cannot create '%s' because a file or directory with the same name already exists.", generationRoot)
	}
	return "", os.Rename(stagingRoot, generationRoot)
}

func (s Store) removeRecoverableOrphan(generationsRoot, generationRoot, name string, current *CurrentState, p Publication, digest string) string {
	relative := generationsDir + "/" + name
	if current != nil && (current.Active.RelativePath == relative || (current.Previous != nil && current.Previous.RelativePath == relative)) {
		return fmt.Sprintf("Immutable knowledge generation '%s' is already referenced by the activation marker.", name)
	}
	metadata, diagnostic := s.readRecoverableGeneration(generationsRoot, generationRoot, name,
		generationIdentityOf{p.SourceAlias, p.LibraryID, p.Sequence, digest})
	if metadata == nil {
		return diagnostic
	}
	if metadata.LibraryVersion != p.LibraryVersion || metadata.TransportType != p.TransportType ||
		metadata.Location != p.Location || metadata.ResolvedRevision != p.ResolvedRevision {
		return fmt.Sprintf("Immutable knowledge generation '%s' already exists with unexpected content.", name)
	}
	if err := deleteTree(generationRoot); err != nil {
		return fmt.Sprintf("Immutable knowledge generation '%s' could not be recovered: %s", name, err.Error())
	}
	return ""
}

func isSymlinkEntry(file *zip.File) bool { return (file.ExternalAttrs>>16)&0xF000 == 0xA000 }

// writeGeneration is clio's WriteGeneration: bundle.zip, the extracted archive and install.json.
func writeGeneration(stagingRoot string, p Publication, digest string) error {
	if err := os.WriteFile(filepath.Join(stagingRoot, bundleFileName), p.BundleBytes, 0o666); err != nil {
		return err
	}
	archive, err := zip.NewReader(bytes.NewReader(p.BundleBytes), int64(len(p.BundleBytes)))
	if err != nil {
		return err
	}
	if len(archive.File) > maxArchiveEntries {
		return errors.New("Knowledge archive contains too many entries.")
	}
	var extracted uint64
	for _, entry := range archive.File {
		if isSymlinkEntry(entry) {
			return errors.New("Knowledge archive contains a symbolic-link entry.")
		}
		destination, err := resolveRelative(stagingRoot, entry.Name)
		if err != nil {
			return err
		}
		if strings.HasSuffix(entry.Name, "/") || entry.Name == "" {
			if err := os.MkdirAll(destination, 0o777); err != nil {
				return err
			}
			continue
		}
		if extracted > uint64(maxBundleBytes)-min(entry.UncompressedSize64, uint64(maxBundleBytes)) || entry.UncompressedSize64 > uint64(maxBundleBytes) {
			return errors.New("Knowledge archive exceeds the extracted-size limit.")
		}
		extracted += entry.UncompressedSize64
		if err := os.MkdirAll(filepath.Dir(destination), 0o777); err != nil {
			return err
		}
		if err := extractEntry(entry, destination); err != nil {
			return err
		}
	}
	data, err := clioJSON(installMetadataFile{SchemaVersion: storeSchemaVersion, SourceAlias: p.SourceAlias,
		LibraryID: p.LibraryID, LibraryVersion: p.LibraryVersion, Sequence: p.Sequence, TransportType: p.TransportType,
		Location: p.Location, ResolvedRevision: p.ResolvedRevision, BundleDigest: digest, InstalledAtUtc: dotnetTime(time.Now())})
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(stagingRoot, metadataFileName), data, 0o666)
}

func extractEntry(entry *zip.File, destination string) error {
	source, err := entry.Open()
	if err != nil {
		return err
	}
	defer source.Close()
	target, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(target, io.LimitReader(source, int64(entry.UncompressedSize64)+1))
	closeErr := target.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

// prune is clio's Prune: every generation directory except the active and previous ones is deleted.
func (s Store) prune(generationsRoot string, state currentStateFile) error {
	retained := map[string]bool{filepath.Base(filepath.FromSlash(state.Active.RelativePath)): true}
	if state.Previous != nil {
		retained[filepath.Base(filepath.FromSlash(state.Previous.RelativePath))] = true
	}
	entries, err := os.ReadDir(generationsRoot)
	if err != nil {
		return err
	}
	var obsolete []string
	for _, entry := range entries {
		if entry.IsDir() && !retained[entry.Name()] {
			obsolete = append(obsolete, filepath.Join(generationsRoot, entry.Name()))
		}
	}
	for _, directory := range obsolete {
		if err := ensureNoLink(generationsRoot, directory); err != nil {
			return err
		}
		if err := deleteTree(directory); err != nil {
			return err
		}
	}
	return nil
}

// Delete is clio's store Delete: the whole source directory, under the source lock, through a quarantine.
func (s Store) Delete(alias string, confirmed bool) (InstallationResult, error) {
	if !confirmed {
		return InstallationResult{Status: StatusConfirmationRequired, RootPath: s.Root,
			Message: fmt.Sprintf("Deleting installed knowledge for source '%s' requires explicit confirmation.", alias)}, nil
	}
	sourceRoot, err := s.resolveSourceRoot(alias, false)
	if err != nil {
		return InstallationResult{}, err
	}
	if _, err := os.Stat(sourceRoot); err != nil {
		return InstallationResult{Status: StatusNotInstalled, RootPath: s.Root,
			Message: fmt.Sprintf("Knowledge source '%s' is not installed.", alias)}, nil
	}
	if err := s.validateSourceRoot(alias, sourceRoot); err != nil {
		return InstallationResult{}, err
	}
	// clio takes the lock on the existing directory without re-creating it; the lock file lives in .locks.
	path, err := s.lockPath(SourceKey(alias) + ".lock")
	if err != nil {
		return InstallationResult{}, err
	}
	lock, err := acquireFileLock(path, StoreLockTimeout, storeLockPoll)
	if errors.Is(err, errLockBusy) {
		return InstallationResult{}, errors.New("Timed out waiting for the knowledge source mutation lock.")
	} else if err != nil {
		return InstallationResult{}, err
	}
	defer lock.Unlock()
	if err := s.validateSourceRoot(alias, sourceRoot); err != nil {
		return InstallationResult{}, err
	}
	if err := deleteRecoverably(sourceRoot); err != nil {
		return InstallationResult{}, err
	}
	return InstallationResult{Status: StatusDeleted, RootPath: s.Root,
		Message: fmt.Sprintf("Installed knowledge for source '%s' was deleted.", alias)}, nil
}

// TryRecordPublisherCheck is clio's best-effort freshness stamp: it never waits for the source lock and
// writes only when expected is still the active generation.
func (s Store) TryRecordPublisherCheck(alias string, expected GenerationPointer) bool {
	recorded := false
	acquired, err := s.TryWithSourceLock(alias, func() error {
		current, diagnostic, err := s.readCurrentMarker(alias)
		if err != nil || diagnostic != "" || current == nil || !samePointer(current.Active, expected) {
			return err
		}
		data, err := clioJSON(publisherCheckState{SchemaVersion: storeSchemaVersion, SourceAlias: current.SourceAlias,
			LibraryID: current.Active.LibraryID, Sequence: current.Active.Sequence, BundleDigest: current.Active.BundleDigest,
			ResolvedRevision: current.Active.ResolvedRevision, CheckedAtUtc: dotnetTime(time.Now())})
		if err != nil {
			return err
		}
		if err := writeAtomic(s.SourceRoot(alias), publisherCheckFile, data); err != nil {
			return err
		}
		recorded = true
		return nil
	})
	return acquired && err == nil && recorded
}

// ReadValidatedMetadata is clio's ReadMetadata: install.json of the active generation, checked against it.
func (s Store) ReadValidatedMetadata(alias string, state *CurrentState) (*InstallMetadata, string) {
	contentRoot, _, diagnostic := s.ReadCandidate(alias, state.Active)
	if diagnostic != "" {
		return nil, diagnostic
	}
	data, err := readBoundedFile(filepath.Join(contentRoot, metadataFileName), maxMarkerBytes)
	if err != nil {
		return nil, fmt.Sprintf("Knowledge source '%s' metadata could not be read: %s", alias, err.Error())
	}
	var metadata InstallMetadata
	if err := decodeStrict(data, &metadata); err != nil {
		return nil, fmt.Sprintf("Knowledge source '%s' metadata could not be read: %s", alias, err.Error())
	}
	if metadata.SchemaVersion != storeSchemaVersion || metadata.Sequence != state.Active.Sequence ||
		metadata.LibraryID != state.Active.LibraryID || metadata.BundleDigest != state.Active.BundleDigest {
		return nil, fmt.Sprintf("Knowledge source '%s' metadata does not match its activation marker.", alias)
	}
	return &metadata, ""
}
