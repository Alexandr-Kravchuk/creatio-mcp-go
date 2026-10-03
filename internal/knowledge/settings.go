package knowledge

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

// Settings locations, exactly as clio's SettingsRepository.AppSettingsFolderPath resolves them:
// CLIO_HOME verbatim, else <HOME or LOCALAPPDATA>/creatio/clio.
const (
	clioHomeVariable     = "CLIO_HOME"
	settingsFileName     = "appsettings.json"
	maximumSettingsBytes = 8 * 1024 * 1024
)

// ClioHome returns clio's settings directory.
func ClioHome() string {
	if home := os.Getenv(clioHomeVariable); strings.TrimSpace(home) != "" {
		return home
	}
	variable := "HOME"
	if runtime.GOOS == "windows" {
		variable = "LOCALAPPDATA"
	}
	return filepath.Join(os.Getenv(variable), "creatio", "clio")
}

// SettingsPath returns clio's appsettings.json path.
func SettingsPath() string { return filepath.Join(ClioHome(), settingsFileName) }

// SourceType is a knowledge transport.
type SourceType string

// The transports clio knows.
const (
	SourceNuGet         SourceType = "nuget"
	SourceGit           SourceType = "git"
	SourceGitHubRelease SourceType = "github-release"
)

// Participation is how a source takes part in topic resolution.
type Participation string

// The participation values clio knows.
const (
	ParticipationIsolated      Participation = "isolated"
	ParticipationSupplement    Participation = "supplement"
	ParticipationAuthoritative Participation = "authoritative"
)

// SourceConfig is one configured knowledge source (clio's KnowledgeSourceConfiguration).
type SourceConfig struct {
	LibraryID            string
	Type                 SourceType
	Location             string
	TrustedKeyID         string
	TrustedPublicKeyPath string
	PackageID            string
	RepositoryOwner      string
	RepositoryName       string
	AssetName            string
	Branch               string
	Tag                  string
	Commit               string
	Enabled              bool
	Priority             int
	Participation        Participation
}

// Config is clio's "knowledge" settings section.
type Config struct {
	RootPath  string
	Sources   map[string]SourceConfig // keyed by alias as written; aliases compare case-insensitively
	TopicPins map[string]string
}

// FeedbackSettings is clio's "knowledge-feedback" settings section.
type FeedbackSettings struct {
	Mode                 string
	Destination          string
	ReportingScope       string
	StandingApprovalHash string
	HasStandingApproval  bool
}

type rawSource struct {
	LibraryID            *string `json:"library-id"`
	Type                 *string `json:"type"`
	Location             *string `json:"location"`
	TrustedKeyID         *string `json:"trusted-key-id"`
	TrustedPublicKeyPath *string `json:"trusted-public-key-path"`
	PackageID            *string `json:"package-id"`
	RepositoryOwner      *string `json:"repository-owner"`
	RepositoryName       *string `json:"repository-name"`
	AssetName            *string `json:"asset-name"`
	Branch               *string `json:"branch"`
	Tag                  *string `json:"tag"`
	Commit               *string `json:"commit"`
	Enabled              *bool   `json:"enabled"`
	Priority             *int    `json:"priority"`
	Participation        *string `json:"participation"`
}

type rawKnowledge struct {
	RootPath  *string               `json:"root-path"`
	Sources   map[string]*rawSource `json:"sources"`
	TopicPins map[string]string     `json:"topic-pins"`
}

type rawFeedback struct {
	Mode             *string `json:"mode"`
	Destination      *string `json:"destination"`
	ReportingScope   *string `json:"reporting-scope"`
	StandingApproval *struct {
		PolicyHash *string `json:"policy-hash"`
	} `json:"standing-approval"`
}

type rawSettings struct {
	Knowledge          *rawKnowledge   `json:"knowledge"`
	LegacyKnowledgeDir *string         `json:"knowledge-root-path"`
	Feedback           *rawFeedback    `json:"knowledge-feedback"`
	Features           map[string]bool `json:"features"`
}

// Settings is the part of clio's appsettings.json this package reads.
type Settings struct {
	Path      string
	Exists    bool
	Knowledge Config
	Feedback  FeedbackSettings
	Features  map[string]bool
}

var (
	aliasPattern      = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]{0,62}[a-z0-9])?$`)
	libraryIDPattern  = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,62}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,62}[a-z0-9])?)+$`)
	packageIDPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)
	gitCommitPattern  = regexp.MustCompile(`^(?:[0-9a-fA-F]{40}|[0-9a-fA-F]{64})$`)
	repoOwnerPattern  = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,37}[A-Za-z0-9])?$`)
	repoNamePattern   = regexp.MustCompile(`^[A-Za-z0-9_](?:[A-Za-z0-9._-]{0,98}[A-Za-z0-9_-])?$`)
	assetNamePattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	topicIDPinPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9._-]{0,126}[a-z0-9])?$`)
)

// settingsCache re-reads appsettings.json only when its size or modification time changes, like
// clio's KnowledgeRuntimeConfigurationProvider.
type settingsCache struct {
	mu       sync.Mutex
	path     string
	size     int64
	modified time.Time
	value    *Settings
	err      error
}

func (c *settingsCache) load(path string) (*Settings, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	info, statErr := os.Stat(path)
	if statErr == nil && c.path == path && c.value != nil && info.Size() == c.size && info.ModTime().Equal(c.modified) {
		return c.value, c.err
	}
	value, err := ReadSettings(path)
	if statErr == nil {
		c.path, c.size, c.modified, c.value, c.err = path, info.Size(), info.ModTime(), value, err
	}
	return value, err
}

// ReadSettings parses and validates the knowledge-related sections of a clio settings file. A missing
// file yields clio's defaults (no sources) with Exists=false.
func ReadSettings(path string) (*Settings, error) {
	settings := &Settings{Path: path, Feedback: defaultFeedback(), Features: map[string]bool{},
		Knowledge: Config{Sources: map[string]SourceConfig{}, TopicPins: map[string]string{}}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return settings, nil
	}
	if err != nil {
		return settings, err
	}
	settings.Exists = true
	if len(data) == 0 || len(data) > maximumSettingsBytes {
		return settings, errors.New("Clio appsettings is outside the supported size bounds.")
	}
	data = bytes.TrimPrefix(data, []byte("\xEF\xBB\xBF"))
	var raw rawSettings
	if err := json.Unmarshal(data, &raw); err != nil {
		return settings, err
	}
	if raw.Features != nil {
		settings.Features = raw.Features
	}
	if raw.Feedback != nil {
		settings.Feedback = feedbackFrom(raw.Feedback)
	}
	if raw.Knowledge != nil {
		knowledge, err := validateKnowledge(raw.Knowledge)
		if err != nil {
			return settings, err
		}
		settings.Knowledge = knowledge
	}
	if settings.Knowledge.RootPath == "" && raw.LegacyKnowledgeDir != nil && strings.TrimSpace(*raw.LegacyKnowledgeDir) != "" {
		settings.Knowledge.RootPath = filepath.Clean(*raw.LegacyKnowledgeDir)
	}
	return settings, nil
}

func defaultFeedback() FeedbackSettings {
	return FeedbackSettings{Mode: "ask", Destination: "https://github.com/Advance-Technologies-Foundation/clio", ReportingScope: "sanitized"}
}

func feedbackFrom(raw *rawFeedback) FeedbackSettings {
	feedback := defaultFeedback()
	if raw.Mode != nil {
		feedback.Mode = *raw.Mode
	}
	if raw.Destination != nil {
		feedback.Destination = *raw.Destination
	}
	if raw.ReportingScope != nil {
		feedback.ReportingScope = *raw.ReportingScope
	}
	if raw.StandingApproval != nil && raw.StandingApproval.PolicyHash != nil {
		feedback.HasStandingApproval = true
		feedback.StandingApprovalHash = *raw.StandingApproval.PolicyHash
	}
	return feedback
}

func validateKnowledge(raw *rawKnowledge) (Config, error) {
	config := Config{Sources: map[string]SourceConfig{}, TopicPins: map[string]string{}}
	if raw.RootPath != nil && strings.TrimSpace(*raw.RootPath) != "" {
		if !filepath.IsAbs(*raw.RootPath) {
			return config, errors.New("Knowledge root path must be absolute. (Parameter 'rootPath')")
		}
		config.RootPath = filepath.Clean(*raw.RootPath)
	}
	aliases := make([]string, 0, len(raw.Sources))
	for alias := range raw.Sources {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)
	seenAliases := map[string]bool{}
	seenLibraries := map[string]bool{}
	for _, alias := range aliases {
		if err := ValidateAlias(alias); err != nil {
			return config, err
		}
		if raw.Sources[alias] == nil {
			return config, errors.New("Value cannot be null. (Parameter 'source')")
		}
		source, err := validateSource(raw.Sources[alias])
		if err != nil {
			return config, err
		}
		if seenAliases[strings.ToLower(alias)] {
			return config, fmt.Errorf("An item with the same key has already been added. Key: %s", alias)
		}
		if seenLibraries[strings.ToLower(source.LibraryID)] {
			return config, fmt.Errorf("Knowledge library ID '%s' is already configured. (Parameter 'configuration')", source.LibraryID)
		}
		seenAliases[strings.ToLower(alias)] = true
		seenLibraries[strings.ToLower(source.LibraryID)] = true
		config.Sources[alias] = source
	}
	for topic, library := range raw.TopicPins {
		if strings.TrimSpace(topic) == "" || !topicIDPinPattern.MatchString(topic) {
			return config, fmt.Errorf("Knowledge topic pin '%s' is invalid. (Parameter 'configuration')", topic)
		}
		if err := validateLibraryID(library); err != nil {
			return config, err
		}
		config.TopicPins[topic] = library
	}
	return config, nil
}

// ValidateAlias applies clio's alias rule.
func ValidateAlias(alias string) error {
	if strings.TrimSpace(alias) == "" || !aliasPattern.MatchString(alias) {
		return errors.New("Knowledge source alias must contain only lowercase letters, digits, dots, and hyphens. (Parameter 'alias')")
	}
	return nil
}

func validateLibraryID(libraryID string) error {
	if strings.TrimSpace(libraryID) == "" || len(libraryID) > 255 || !libraryIDPattern.MatchString(libraryID) {
		return errors.New("Knowledge library ID must be a lowercase reverse-DNS identifier. (Parameter 'libraryId')")
	}
	return nil
}

func optional(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func validateSource(raw *rawSource) (SourceConfig, error) {
	source := SourceConfig{LibraryID: strings.TrimSpace(optional(raw.LibraryID)), Enabled: true, Participation: ParticipationSupplement}
	if err := validateLibraryID(optional(raw.LibraryID)); err != nil {
		return source, err
	}
	location, err := validateRemoteURI(optional(raw.Location))
	if err != nil {
		return source, err
	}
	source.Location = location
	if raw.Enabled != nil {
		source.Enabled = *raw.Enabled
	}
	if raw.Priority != nil {
		source.Priority = *raw.Priority
	}
	if raw.Participation != nil {
		switch Participation(strings.ToLower(*raw.Participation)) {
		case ParticipationIsolated, ParticipationSupplement, ParticipationAuthoritative:
			source.Participation = Participation(strings.ToLower(*raw.Participation))
		default:
			return source, fmt.Errorf("Error converting value \"%s\" to type 'KnowledgeSourceParticipation'.", *raw.Participation)
		}
	}
	sourceType := SourceNuGet // the enum's zero value when "type" is absent
	if raw.Type != nil {
		switch SourceType(strings.ToLower(*raw.Type)) {
		case SourceNuGet, SourceGit, SourceGitHubRelease:
			sourceType = SourceType(strings.ToLower(*raw.Type))
		default:
			return source, fmt.Errorf("Error converting value \"%s\" to type 'KnowledgeSourceType'.", *raw.Type)
		}
	}
	source.Type = sourceType
	gitRefs := raw.Branch != nil || raw.Tag != nil || raw.Commit != nil
	releaseSettings := raw.RepositoryOwner != nil || raw.RepositoryName != nil || raw.AssetName != nil
	switch sourceType {
	case SourceNuGet:
		if gitRefs {
			return source, errors.New("Git references are not valid for a NuGet knowledge source. (Parameter 'source')")
		}
		if releaseSettings {
			return source, errors.New("GitHub release settings are not valid for a NuGet knowledge source. (Parameter 'source')")
		}
		if source.TrustedKeyID, err = normalizeTrustedKeyID(raw.TrustedKeyID); err != nil {
			return source, err
		}
		if source.TrustedPublicKeyPath, err = normalizeTrustedPublicKeyPath(raw.TrustedPublicKeyPath); err != nil {
			return source, err
		}
		if strings.TrimSpace(optional(raw.PackageID)) == "" || !packageIDPattern.MatchString(optional(raw.PackageID)) {
			return source, errors.New("A valid package-id is required for a NuGet knowledge source. (Parameter 'source')")
		}
		source.PackageID = strings.TrimSpace(*raw.PackageID)
	case SourceGitHubRelease:
		if gitRefs {
			return source, errors.New("Git references are not valid for a GitHub release knowledge source. (Parameter 'source')")
		}
		if raw.PackageID != nil {
			return source, errors.New("A NuGet package ID is not valid for a GitHub release knowledge source. (Parameter 'source')")
		}
		owner, name, asset := optional(raw.RepositoryOwner), optional(raw.RepositoryName), optional(raw.AssetName)
		if strings.TrimSpace(owner) == "" || !repoOwnerPattern.MatchString(owner) {
			return source, errors.New("A valid repository-owner is required for a GitHub release knowledge source. (Parameter 'source')")
		}
		if strings.TrimSpace(name) == "" || !repoNamePattern.MatchString(name) || strings.Contains(name, "..") {
			return source, errors.New("A valid repository-name is required for a GitHub release knowledge source. (Parameter 'source')")
		}
		if strings.TrimSpace(asset) == "" || !assetNamePattern.MatchString(asset) || strings.Contains(asset, "..") || !strings.HasSuffix(asset, ".zip") {
			return source, errors.New("A GitHub release knowledge source must declare an exact '.zip' asset-name. (Parameter 'source')")
		}
		source.RepositoryOwner, source.RepositoryName, source.AssetName = owner, name, asset
		if raw.TrustedKeyID != nil || raw.TrustedPublicKeyPath != nil {
			if source.TrustedKeyID, err = normalizeTrustedKeyID(raw.TrustedKeyID); err != nil {
				return source, err
			}
			if source.TrustedPublicKeyPath, err = normalizeTrustedPublicKeyPath(raw.TrustedPublicKeyPath); err != nil {
				return source, err
			}
		}
	case SourceGit:
		if releaseSettings {
			return source, errors.New("GitHub release settings are not valid for a Git knowledge source. (Parameter 'source')")
		}
		if raw.PackageID != nil || raw.TrustedKeyID != nil || raw.TrustedPublicKeyPath != nil {
			return source, errors.New("NuGet package and signing settings are not valid for a Git knowledge source. (Parameter 'source')")
		}
		source.Branch = strings.TrimSpace(optional(raw.Branch))
		source.Tag = strings.TrimSpace(optional(raw.Tag))
		source.Commit = strings.ToLower(strings.TrimSpace(optional(raw.Commit)))
		if source.Commit != "" && !gitCommitPattern.MatchString(source.Commit) {
			return source, errors.New("Knowledge Git commit must be a complete SHA-1 or SHA-256 object ID. (Parameter 'value')")
		}
	}
	return source, nil
}

func normalizeTrustedKeyID(value *string) (string, error) {
	if value == nil || strings.TrimSpace(*value) == "" {
		return "", errors.New("A trusted-key-id is required for every NuGet knowledge source. (Parameter 'value')")
	}
	keyID := strings.TrimSpace(*value)
	for _, r := range keyID {
		if unicode.IsControl(r) {
			return "", errors.New("Knowledge trusted-key-id must be a printable value of at most 255 characters. (Parameter 'value')")
		}
	}
	if len(keyID) > 255 {
		return "", errors.New("Knowledge trusted-key-id must be a printable value of at most 255 characters. (Parameter 'value')")
	}
	return keyID, nil
}

func normalizeTrustedPublicKeyPath(value *string) (string, error) {
	normalized, ok := normalizeLocalPublicKeyPath(optional(value), false)
	if !ok {
		return "", errors.New("Knowledge trusted-public-key-path must be an absolute local file path. (Parameter 'value')")
	}
	return normalized, nil
}

// validateRemoteURI applies clio's location rules and returns the location in System.Uri.AbsoluteUri
// form (lowercase scheme and host, "/" for an empty path).
func validateRemoteURI(location string) (string, error) {
	parsed, err := url.Parse(location)
	if err != nil || !parsed.IsAbs() || parsed.Host == "" {
		return "", errors.New("Knowledge source location is not an absolute URI. Give a full 'https://<host>/<path>' form; an scp-style Git remote such as 'user@<host>:<path>' is not one. (Parameter 'location')")
	}
	scheme := strings.ToLower(parsed.Scheme)
	host := strings.ToLower(parsed.Hostname())
	loopback := host == "localhost" || host == "127.0.0.1" || host == "::1" || strings.HasPrefix(host, "127.")
	if scheme != "https" && (scheme != "http" || !loopback) {
		return "", errors.New("Knowledge source location must use HTTPS, or HTTP only when the host is loopback. This location's scheme and host are neither. (Parameter 'location')")
	}
	if parsed.User != nil {
		return "", errors.New("Knowledge source location must not carry credentials in the URI (the 'user:password@' part). Remove them; this transport is credential-free. (Parameter 'location')")
	}
	if parsed.RawQuery != "" || parsed.ForceQuery {
		return "", errors.New("Knowledge source location must not carry a query string. Remove everything from the '?'. (Parameter 'location')")
	}
	if parsed.Fragment != "" {
		return "", errors.New("Knowledge source location must not carry a fragment. Remove everything from the '#'. (Parameter 'location')")
	}
	parsed.Scheme = scheme
	parsed.Host = strings.ToLower(parsed.Host)
	if parsed.Path == "" {
		parsed.Path = "/"
	}
	return parsed.String(), nil
}

// FindSource looks an alias up case-insensitively and returns the alias as configured.
func (c Config) FindSource(alias string) (string, SourceConfig, bool) {
	for configured, source := range c.Sources {
		if strings.EqualFold(configured, alias) {
			return configured, source, true
		}
	}
	return "", SourceConfig{}, false
}

// SortedAliases returns the configured aliases in clio's case-insensitive order.
func (c Config) SortedAliases() []string {
	aliases := make([]string, 0, len(c.Sources))
	for alias := range c.Sources {
		aliases = append(aliases, alias)
	}
	sort.Slice(aliases, func(i, j int) bool { return strings.ToLower(aliases[i]) < strings.ToLower(aliases[j]) })
	return aliases
}
