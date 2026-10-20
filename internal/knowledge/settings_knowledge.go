package knowledge

// The knowledge-related settings mutations clio's SettingsRepository performs (TryAddKnowledgeSource,
// TryRemoveKnowledgeSource, SetKnowledgeSourceEnabled, GetOrCreateKnowledgeRootPath,
// UpdateKnowledgeFeedbackSettings, SetFeature), each one run through UpdateSettings so it takes clio's
// settings lock and re-reads the latest file.

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// SourceInput is one source as a caller supplies it; nil members were not supplied (clio's null).
type SourceInput struct {
	LibraryID            string
	Type                 SourceType
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
	Participation        Participation
}

// ValidateSourceInput is clio's KnowledgeSourceConfigurationValidator.ValidateAndClone for one source.
func ValidateSourceInput(in SourceInput) (SourceConfig, error) {
	libraryID, location, sourceType, participation := in.LibraryID, in.Location, string(in.Type), string(in.Participation)
	enabled, priority := in.Enabled, in.Priority
	return validateSource(&rawSource{LibraryID: &libraryID, Type: &sourceType, Location: &location,
		TrustedKeyID: in.TrustedKeyID, TrustedPublicKeyPath: in.TrustedPublicKeyPath, PackageID: in.PackageID,
		RepositoryOwner: in.RepositoryOwner, RepositoryName: in.RepositoryName, AssetName: in.AssetName,
		Branch: in.Branch, Tag: in.Tag, Commit: in.Commit, Enabled: &enabled, Priority: &priority,
		Participation: &participation})
}

// sourcesEqual is clio's KnowledgeSourcesEqual (which does not compare the GitHub release coordinates).
func sourcesEqual(left, right SourceConfig) bool {
	return left.LibraryID == right.LibraryID && left.Type == right.Type && left.Location == right.Location &&
		left.TrustedKeyID == right.TrustedKeyID && left.TrustedPublicKeyPath == right.TrustedPublicKeyPath &&
		left.PackageID == right.PackageID && left.Branch == right.Branch && left.Tag == right.Tag &&
		left.Commit == right.Commit && left.Enabled == right.Enabled && left.Priority == right.Priority &&
		left.Participation == right.Participation
}

// sourceObject serializes a validated source as clio's Newtonsoft model does (nulls omitted).
func sourceObject(source SourceConfig) *JSONObject {
	object := NewJSONObject()
	object.Set("library-id", source.LibraryID)
	object.Set("type", string(source.Type))
	object.Set("location", source.Location)
	for _, member := range []struct{ key, value string }{
		{"trusted-key-id", source.TrustedKeyID}, {"trusted-public-key-path", source.TrustedPublicKeyPath},
		{"package-id", source.PackageID}, {"repository-owner", source.RepositoryOwner},
		{"repository-name", source.RepositoryName}, {"asset-name", source.AssetName},
		{"branch", source.Branch}, {"tag", source.Tag}, {"commit", source.Commit},
	} {
		if member.value != "" {
			object.Set(member.key, member.value)
		}
	}
	object.Set("enabled", source.Enabled)
	object.Set("priority", json.Number(fmt.Sprint(source.Priority)))
	object.Set("participation", string(source.Participation))
	return object
}

type knowledgeSection struct {
	config Config
	order  []string // aliases in file order
}

// readKnowledgeSection validates the knowledge part of a parsed settings tree with the reader's rules.
func readKnowledgeSection(root *JSONObject) (knowledgeSection, error) {
	partial := NewJSONObject()
	for _, key := range []string{"knowledge", "knowledge-root-path"} {
		if value, ok := root.Get(key); ok {
			partial.Set(key, value)
		}
	}
	data, err := partial.MarshalIndented()
	if err != nil {
		return knowledgeSection{}, err
	}
	var raw rawSettings
	if err := json.Unmarshal(data, &raw); err != nil {
		return knowledgeSection{}, err
	}
	section := knowledgeSection{config: Config{Sources: map[string]SourceConfig{}, TopicPins: map[string]string{}}}
	if raw.Knowledge != nil {
		if section.config, err = validateKnowledge(raw.Knowledge); err != nil {
			return knowledgeSection{}, err
		}
	}
	if section.config.RootPath == "" && raw.LegacyKnowledgeDir != nil && strings.TrimSpace(*raw.LegacyKnowledgeDir) != "" {
		section.config.RootPath = filepath.Clean(*raw.LegacyKnowledgeDir)
	}
	if knowledge := root.Object("knowledge"); knowledge != nil {
		if sources := knowledge.Object("sources"); sources != nil {
			section.order = sources.Keys()
		}
	}
	return section, nil
}

// writeKnowledgeSection replaces the knowledge section with the canonical form clio writes and drops the
// legacy knowledge-root-path key, as every clio knowledge mutation does.
func writeKnowledgeSection(root *JSONObject, section knowledgeSection) {
	knowledge := NewJSONObject()
	if section.config.RootPath != "" {
		knowledge.Set("root-path", section.config.RootPath)
	}
	sources := NewJSONObject()
	seen := map[string]bool{}
	for _, alias := range section.order {
		if source, ok := section.config.Sources[alias]; ok && !seen[alias] {
			sources.Set(alias, sourceObject(source))
			seen[alias] = true
		}
	}
	for _, alias := range section.config.SortedAliases() {
		if !seen[alias] {
			sources.Set(alias, sourceObject(section.config.Sources[alias]))
		}
	}
	knowledge.Set("sources", sources)
	pins := NewJSONObject()
	if existing := root.Object("knowledge"); existing != nil {
		if rawPins := existing.Object("topic-pins"); rawPins != nil {
			for _, topic := range rawPins.Keys() {
				if library, ok := section.config.TopicPins[topic]; ok {
					pins.Set(topic, library)
				}
			}
		}
	}
	knowledge.Set("topic-pins", pins)
	root.Set("knowledge", knowledge)
	root.Delete("knowledge-root-path")
}

// TryAddSource adds alias unless the alias or the library is already configured.
func TryAddSource(settingsPath, alias string, source SourceConfig) (bool, error) {
	if err := ValidateAlias(alias); err != nil {
		return false, err
	}
	added := false
	err := UpdateSettings(settingsPath, func(root *JSONObject) (bool, error) {
		section, err := readKnowledgeSection(root)
		if err != nil {
			return false, err
		}
		if _, _, exists := section.config.FindSource(alias); exists {
			return false, nil
		}
		for _, existing := range section.config.Sources {
			if strings.EqualFold(existing.LibraryID, source.LibraryID) {
				return false, nil
			}
		}
		section.config.Sources[alias] = source
		section.order = append(section.order, alias)
		writeKnowledgeSection(root, section)
		added = true
		return true, nil
	})
	return added, err
}

// TryRemoveSource removes alias only while it still equals expected.
func TryRemoveSource(settingsPath, alias string, expected SourceConfig) (bool, error) {
	if err := ValidateAlias(alias); err != nil {
		return false, err
	}
	removed := false
	err := UpdateSettings(settingsPath, func(root *JSONObject) (bool, error) {
		section, err := readKnowledgeSection(root)
		if err != nil {
			return false, err
		}
		if configured, current, ok := section.config.FindSource(alias); ok && sourcesEqual(current, expected) {
			delete(section.config.Sources, configured)
			removed = true
		}
		writeKnowledgeSection(root, section)
		return true, nil
	})
	return removed, err
}

// ErrSourceNotConfigured is clio's KeyNotFoundException from SetKnowledgeSourceEnabled.
type ErrSourceNotConfigured struct{ Alias string }

func (e ErrSourceNotConfigured) Error() string {
	return fmt.Sprintf("Knowledge source '%s' is not configured.", e.Alias)
}

// SetSourceEnabled is clio's SetKnowledgeSourceEnabled.
func SetSourceEnabled(settingsPath, alias string, enabled bool) error {
	if err := ValidateAlias(alias); err != nil {
		return err
	}
	return UpdateSettings(settingsPath, func(root *JSONObject) (bool, error) {
		section, err := readKnowledgeSection(root)
		if err != nil {
			return false, err
		}
		configured, source, ok := section.config.FindSource(alias)
		if !ok {
			return false, ErrSourceNotConfigured{alias}
		}
		source.Enabled = enabled
		section.config.Sources[configured] = source
		writeKnowledgeSection(root, section)
		return true, nil
	})
}

// EnsureRootPath is clio's GetKnowledgeRootPath + GetOrCreateKnowledgeRootPath: the configured root, else
// the legacy one, else defaultPath, persisted as knowledge.root-path.
func EnsureRootPath(settingsPath, defaultPath string) (string, error) {
	resolved := ""
	err := UpdateSettings(settingsPath, func(root *JSONObject) (bool, error) {
		section, err := readKnowledgeSection(root)
		if err != nil {
			return false, err
		}
		_, hasLegacy := root.Get("knowledge-root-path")
		if section.config.RootPath != "" && !hasLegacy {
			if knowledge := root.Object("knowledge"); knowledge != nil {
				if _, ok := knowledge.Get("root-path"); ok {
					resolved = section.config.RootPath
					return false, nil
				}
			}
		}
		candidate := section.config.RootPath
		if candidate == "" {
			candidate = defaultPath
		}
		if !filepath.IsAbs(candidate) {
			return false, errors.New("Knowledge root path must be absolute. (Parameter 'knowledge.root-path')")
		}
		section.config.RootPath = filepath.Clean(candidate)
		resolved = section.config.RootPath
		writeKnowledgeSection(root, section)
		return true, nil
	})
	return resolved, err
}

// UpdateFeedbackSettings is clio's UpdateKnowledgeFeedbackSettings: the knowledge-feedback section is read
// (with clio's defaults for missing members), mutated and written back in clio's member order.
func UpdateFeedbackSettings(settingsPath string, mutation func(*FeedbackSettings) error) (FeedbackSettings, error) {
	var persisted FeedbackSettings
	err := UpdateSettings(settingsPath, func(root *JSONObject) (bool, error) {
		current := defaultFeedback()
		if section := root.Object("knowledge-feedback"); section != nil {
			data, err := section.MarshalIndented()
			if err != nil {
				return false, err
			}
			var raw rawFeedback
			if err := json.Unmarshal(data, &raw); err != nil {
				return false, err
			}
			current = feedbackFrom(&raw)
		}
		if err := mutation(&current); err != nil {
			return false, err
		}
		section := NewJSONObject()
		section.Set("mode", current.Mode)
		section.Set("destination", current.Destination)
		section.Set("reporting-scope", current.ReportingScope)
		if current.HasStandingApproval {
			approval := NewJSONObject()
			approval.Set("policy-hash", current.StandingApprovalHash)
			section.Set("standing-approval", approval)
		}
		root.Set("knowledge-feedback", section)
		persisted = current
		return true, nil
	})
	return persisted, err
}

// SetFeature is clio's SetFeature: the key is written as given; an existing key differing only in case
// is updated in place, as clio's case-insensitive feature dictionary does.
func SetFeature(settingsPath, name string, enabled bool) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("Feature name must be a non-empty value. (Parameter 'featureName')")
	}
	return UpdateSettings(settingsPath, func(root *JSONObject) (bool, error) {
		features := root.EnsureObject("features")
		key := name
		if existing, ok := features.FindFold(name); ok {
			key = existing
		}
		features.Set(key, enabled)
		return true, nil
	})
}
