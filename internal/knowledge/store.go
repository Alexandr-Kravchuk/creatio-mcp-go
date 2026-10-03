package knowledge

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// The installation store layout, shared with clio's KnowledgeSourceInstallationStore:
//
//	<root>/.clio-knowledge-root                  "clio-knowledge-store-v1\n"
//	<root>/sources/<key>/.clio-knowledge-source  "<alias>\n"
//	<root>/sources/<key>/current.json            active and previous generation pointers
//	<root>/sources/<key>/generations/<seq>-<digest12>/{bundle.zip,install.json,manifest.*,resources/}
//	<root>/sources/.history/<library key>.json   accepted (high-water) sequence per library
//	<root>/sources/.locks/<key>.lock             clio's mutation locks
//
// <key> is the first 24 hex characters of SHA-256 over the lower-cased alias (or library ID).
const (
	storeSchemaVersion  = 1
	maxMarkerBytes      = 64 * 1024
	maxBundleBytes      = 40 * 1024 * 1024
	rootOwnerFileName   = ".clio-knowledge-root"
	rootOwnerContent    = "clio-knowledge-store-v1\n"
	sourceOwnerFileName = ".clio-knowledge-source"
	currentFileName     = "current.json"
	publisherCheckFile  = "publisher-check.json"
	locksDirectoryName  = ".locks"
	historyDirectory    = ".history"
	bundleFileName      = "bundle.zip"
	metadataFileName    = "install.json"
	sourcesDirectory    = "sources"
	generationsDir      = "generations"
	stagingDirectory    = "staging"
	defaultRootName     = "knowledge"
)

// GenerationPointer is one entry of current.json.
type GenerationPointer struct {
	LibraryID        string    `json:"libraryId"`
	LibraryVersion   string    `json:"libraryVersion"`
	Sequence         uint64    `json:"sequence"`
	RelativePath     string    `json:"relativePath"`
	BundleDigest     string    `json:"bundleDigest"`
	ResolvedRevision string    `json:"resolvedRevision"`
	ActivatedAtUtc   time.Time `json:"activatedAtUtc"`
}

// CurrentState is current.json.
type CurrentState struct {
	SchemaVersion int                `json:"schemaVersion"`
	SourceAlias   string             `json:"sourceAlias"`
	Active        GenerationPointer  `json:"active"`
	Previous      *GenerationPointer `json:"previous,omitempty"`
}

// InstallMetadata is a generation's install.json.
type InstallMetadata struct {
	SchemaVersion    int       `json:"schemaVersion"`
	SourceAlias      string    `json:"sourceAlias"`
	LibraryID        string    `json:"libraryId"`
	LibraryVersion   string    `json:"libraryVersion"`
	Sequence         uint64    `json:"sequence"`
	TransportType    string    `json:"transportType"`
	Location         string    `json:"location"`
	ResolvedRevision string    `json:"resolvedRevision"`
	BundleDigest     string    `json:"bundleDigest"`
	InstalledAtUtc   time.Time `json:"installedAtUtc"`
}

type highWaterMark struct {
	SchemaVersion int    `json:"schemaVersion"`
	LibraryID     string `json:"libraryId"`
	Sequence      uint64 `json:"sequence"`
	BundleDigest  string `json:"bundleDigest"`
}

// Store reads clio's installation store. It never writes on a read: reconciling an interrupted
// publication and pruning generations stay clio's (or an explicit install's) job.
type Store struct {
	Root string
}

// ResolveRoot applies KnowledgeRootPathProvider's rules to the configured root, defaulting to
// <clio home>/knowledge. It does not create the directory.
func ResolveRoot(configured string) (string, error) {
	resolved := configured
	if strings.TrimSpace(resolved) == "" {
		resolved = filepath.Join(ClioHome(), defaultRootName)
	}
	if !filepath.IsAbs(resolved) {
		return "", errors.New("The configured knowledge.root-path must be an absolute directory path.")
	}
	normalized := filepath.Clean(resolved)
	if runtime.GOOS == "windows" && strings.HasPrefix(normalized, `\\`) {
		return "", errors.New("The configured knowledge.root-path must be on a local non-device filesystem.")
	}
	if filepath.Dir(normalized) == normalized {
		return "", errors.New("The configured knowledge.root-path cannot be a filesystem root.")
	}
	if hasLinkInAncestry(normalized) {
		return "", errors.New("The configured knowledge.root-path cannot contain symbolic links or junctions.")
	}
	return normalized, nil
}

// SourceKey is clio's directory key for an alias or library ID.
func SourceKey(value string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(value)))
	return hex.EncodeToString(sum[:])[:24]
}

// SourceRoot is the directory of one source.
func (s Store) SourceRoot(alias string) string {
	return filepath.Join(s.Root, sourcesDirectory, SourceKey(alias))
}

func (s Store) sourcesRoot() string { return filepath.Join(s.Root, sourcesDirectory) }

// resolveRelative joins and refuses a result outside parent, like clio's ResolveRelative.
func resolveRelative(parent, relative string) (string, error) {
	fullParent := filepath.Clean(parent)
	candidate := filepath.Clean(filepath.Join(fullParent, filepath.FromSlash(relative)))
	prefix := strings.TrimRight(fullParent, `/\`) + string(filepath.Separator)
	inside := strings.HasPrefix(candidate, prefix)
	if runtime.GOOS == "windows" {
		inside = strings.HasPrefix(strings.ToLower(candidate), strings.ToLower(prefix))
	}
	if !inside {
		return "", errors.New("Knowledge path escapes its managed root.")
	}
	return candidate, nil
}

// ensureNoLink refuses a symbolic link or junction between root and path, inclusive.
func ensureNoLink(root, path string) error {
	fullRoot := filepath.Clean(root)
	current := filepath.Clean(path)
	for strings.HasPrefix(current, fullRoot) {
		if info, err := os.Lstat(current); err == nil && isReparsePoint(info) {
			return errors.New("Knowledge storage paths cannot contain symbolic links or junctions.")
		} else if err != nil {
			return err
		}
		if current == fullRoot {
			break
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	return nil
}

func readBoundedFile(path string, maximum int64) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Size() <= 0 || info.Size() > maximum {
		return nil, fmt.Errorf("Knowledge file '%s' is outside supported bounds.", filepath.Base(path))
	}
	return os.ReadFile(path)
}

// decodeStrict decodes a marker with clio's UnmappedMemberHandling.Disallow.
func decodeStrict(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

func (s Store) validateSourceRoot(alias, sourceRoot string) error {
	if err := ensureNoLink(s.Root, sourceRoot); err != nil {
		return err
	}
	marker := filepath.Join(sourceRoot, sourceOwnerFileName)
	if _, err := os.Stat(marker); err != nil {
		return fmt.Errorf("Knowledge source root '%s' is not owned by Clio.", alias)
	}
	if err := ensureNoLink(s.Root, marker); err != nil {
		return err
	}
	content, err := readBoundedFile(marker, maxMarkerBytes)
	if err != nil {
		return err
	}
	if string(content) != alias+"\n" {
		return fmt.Errorf("Knowledge source root '%s' is not owned by Clio.", alias)
	}
	return nil
}

func validPointer(p GenerationPointer) bool {
	return strings.TrimSpace(p.LibraryID) != "" && strings.TrimSpace(p.LibraryVersion) != "" && p.Sequence > 0 &&
		strings.TrimSpace(p.RelativePath) != "" && len(p.BundleDigest) == 64 && isHex(p.BundleDigest) &&
		strings.TrimSpace(p.ResolvedRevision) != ""
}

// readCurrentMarker returns (nil, "", nil) when nothing is installed.
func (s Store) readCurrentMarker(alias string) (*CurrentState, string, error) {
	sourceRoot := s.SourceRoot(alias)
	if _, err := os.Stat(sourceRoot); err != nil {
		return nil, "", nil
	}
	if err := s.validateSourceRoot(alias, sourceRoot); err != nil {
		return nil, "", err
	}
	markerPath := filepath.Join(sourceRoot, currentFileName)
	if _, err := os.Stat(markerPath); err != nil {
		return nil, "", nil
	}
	if err := ensureNoLink(sourceRoot, markerPath); err != nil {
		return nil, "", err
	}
	data, err := readBoundedFile(markerPath, maxMarkerBytes)
	if err != nil {
		return nil, "", err
	}
	var state CurrentState
	if err := decodeStrict(data, &state); err != nil {
		return nil, "", err
	}
	if state.SchemaVersion != storeSchemaVersion || !strings.EqualFold(state.SourceAlias, alias) || !validPointer(state.Active) {
		return nil, fmt.Sprintf("Knowledge source '%s' activation marker is invalid.", alias), nil
	}
	return &state, "", nil
}

func (s Store) readHighWater(libraryID string) (*highWaterMark, error) {
	path := filepath.Join(s.sourcesRoot(), historyDirectory, SourceKey(libraryID)+".json")
	if _, err := os.Stat(path); err != nil {
		return nil, nil
	}
	if err := ensureNoLink(filepath.Join(s.sourcesRoot(), historyDirectory), path); err != nil {
		return nil, err
	}
	data, err := readBoundedFile(path, maxMarkerBytes)
	if err != nil {
		return nil, err
	}
	var mark highWaterMark
	if err := decodeStrict(data, &mark); err != nil {
		return nil, err
	}
	if mark.SchemaVersion != storeSchemaVersion || mark.Sequence == 0 || mark.LibraryID != libraryID ||
		len(mark.BundleDigest) != 64 || !isHex(mark.BundleDigest) {
		return nil, fmt.Errorf("Knowledge library '%s' replay marker is invalid.", libraryID)
	}
	return &mark, nil
}

func conflictsWithAccepted(sequence uint64, digest string, accepted uint64, acceptedDigest string) bool {
	return sequence < accepted || (sequence == accepted && digest != acceptedDigest)
}

// ReadCurrent returns the activation marker for alias, or a diagnostic. Unlike clio it does not
// reconcile a marker that lags the library's accepted sequence (that rewrites current.json and prunes
// generations); it reports the conflict and leaves the repair to clio.
func (s Store) ReadCurrent(alias string) (*CurrentState, string) {
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
	if mark != nil && conflictsWithAccepted(state.Active.Sequence, state.Active.BundleDigest, mark.Sequence, mark.BundleDigest) {
		return nil, fmt.Sprintf("Knowledge source '%s' activation marker conflicts with accepted library sequence %d; "+
			"run any clio knowledge command (for example `clio info-knowledge`) to reconcile it.", alias, mark.Sequence)
	}
	return state, ""
}

// ReadCandidate reads a generation's bundle.zip and checks it against the pointer's digest.
func (s Store) ReadCandidate(alias string, pointer GenerationPointer) (contentRoot string, data []byte, diagnostic string) {
	sourceRoot := s.SourceRoot(alias)
	fail := func(err error) (string, []byte, string) {
		return "", nil, fmt.Sprintf("Installed knowledge source '%s' could not be read: %s", alias, err.Error())
	}
	if err := s.validateSourceRoot(alias, sourceRoot); err != nil {
		return fail(err)
	}
	generationRoot, err := resolveRelative(sourceRoot, pointer.RelativePath)
	if err != nil {
		return fail(err)
	}
	if err := ensureNoLink(sourceRoot, generationRoot); err != nil {
		return fail(err)
	}
	bundlePath, err := resolveRelative(generationRoot, bundleFileName)
	if err != nil {
		return fail(err)
	}
	data, err = readBoundedFile(bundlePath, maxBundleBytes)
	if err != nil {
		return fail(err)
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != pointer.BundleDigest {
		return "", nil, fmt.Sprintf("Installed knowledge source '%s' does not match its activation digest.", alias)
	}
	return generationRoot, data, ""
}

// ReadMetadata reads the active generation's install.json; a missing or invalid file yields nil.
func (s Store) ReadMetadata(alias string, state *CurrentState) *InstallMetadata {
	sourceRoot := s.SourceRoot(alias)
	generationRoot, err := resolveRelative(sourceRoot, state.Active.RelativePath)
	if err != nil {
		return nil
	}
	data, err := readBoundedFile(filepath.Join(generationRoot, metadataFileName), maxMarkerBytes)
	if err != nil {
		return nil
	}
	var metadata InstallMetadata
	if decodeStrict(data, &metadata) != nil {
		return nil
	}
	return &metadata
}

// PublisherCheckedAt reads publisher-check.json when it still matches the active generation.
func (s Store) PublisherCheckedAt(alias string, active GenerationPointer) *time.Time {
	data, err := readBoundedFile(filepath.Join(s.SourceRoot(alias), publisherCheckFile), maxMarkerBytes)
	if err != nil {
		return nil
	}
	var check struct {
		SchemaVersion    int       `json:"schemaVersion"`
		SourceAlias      string    `json:"sourceAlias"`
		LibraryID        string    `json:"libraryId"`
		Sequence         uint64    `json:"sequence"`
		BundleDigest     string    `json:"bundleDigest"`
		ResolvedRevision string    `json:"resolvedRevision"`
		CheckedAtUtc     time.Time `json:"checkedAtUtc"`
	}
	if decodeStrict(data, &check) != nil || check.LibraryID != active.LibraryID || check.Sequence != active.Sequence ||
		check.BundleDigest != active.BundleDigest {
		return nil
	}
	return &check.CheckedAtUtc
}
