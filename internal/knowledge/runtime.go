package knowledge

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// LookupStatus mirrors clio's KnowledgeArticleLookupStatus.
type LookupStatus int

// Lookup outcomes.
const (
	LookupActive LookupStatus = iota
	LookupNotFound
	LookupUnavailable
	LookupAmbiguous
)

// Provenance identifies the generation that served an article.
type Provenance struct {
	SourceAlias    string
	LibraryID      string
	LibraryVersion string
	ItemID         string
	TopicID        string
	Sequence       uint64
	BundleDigest   string
	LocalPath      string
}

// Lookup is the answer for one name or URI.
type Lookup struct {
	Status     LookupStatus
	Article    *Article
	Provenance *Provenance
	Diagnostic string
}

// Library is one active library generation.
type Library struct {
	SourceAlias    string
	LibraryID      string
	LibraryVersion string
	Priority       int
	Participation  Participation
	Sequence       uint64
	BundleDigest   string
	ContentRoot    string
	Articles       []Article
}

// Descriptor is one entry of the guidance catalog (resources/list).
type Descriptor struct {
	Name, Title, Description, URI, MediaType string
}

type failedActivation struct {
	identity   string
	diagnostic string
	retryAfter time.Time
}

// Runtime is the activated knowledge set plus the activator that keeps it in step with clio's settings
// and installation store (KnowledgeMultiSourceActivator + KnowledgeBundleRuntime + KnowledgeGuidanceSource).
type Runtime struct {
	Capabilities Capabilities
	// SettingsPath overrides clio's appsettings.json location; empty means SettingsPath().
	SettingsPath string
	// FailureRetry is how long a rejected generation is not retried (clio: 1 s).
	FailureRetry time.Duration

	mu             sync.Mutex
	settings       settingsCache
	libraries      []*Library
	topicPins      map[string]string
	observed       map[string]string
	failed         map[string]failedActivation
	lastDiagnostic string
	catalog        *resolvedCatalog
	generation     int
	lastSettings   *Settings
	lastRoot       string
}

type resolvedCatalog struct {
	generation int
	features   string
	names      []string
	byName     []Descriptor
	byURI      []Descriptor
}

// NewRuntime creates a runtime that claims the given capabilities.
func NewRuntime(capabilities Capabilities) *Runtime {
	return &Runtime{Capabilities: capabilities, FailureRetry: time.Second}
}

func (r *Runtime) settingsPath() string {
	if r.SettingsPath != "" {
		return r.SettingsPath
	}
	return SettingsPath()
}

// LoadSettings returns the current clio settings (cached by size and modification time).
func (r *Runtime) LoadSettings() (*Settings, error) { return r.settings.load(r.settingsPath()) }

// LastDiagnostic is why the last activation pass left something inactive, or "".
func (r *Runtime) LastDiagnostic() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastDiagnostic
}

func (r *Runtime) verifier(settings *Settings) *Verifier {
	return &Verifier{Capabilities: r.Capabilities, Trust: configuredTrustStore{sources: func() (map[string]SourceConfig, error) {
		return settings.Knowledge.Sources, nil
	}}}
}

// Verify validates a candidate against the current settings' trust, without activating it.
func (r *Runtime) Verify(data []byte, expectedVersion, expectedLibraryID string) (*PreparedBundle, *Rejection) {
	settings, err := r.LoadSettings()
	if err != nil {
		return nil, &Rejection{Code: RejectMalformed, Message: err.Error()}
	}
	return r.verifier(settings).Prepare(data, expectedVersion, expectedLibraryID)
}

// EnsureActivated re-reads the configuration and the installation markers and activates what changed.
func (r *Runtime) EnsureActivated() {
	r.mu.Lock()
	defer r.mu.Unlock()
	settings, err := r.LoadSettings()
	var root string
	if err == nil {
		root, err = ResolveRoot(settings.Knowledge.RootPath)
	}
	if err != nil {
		r.lastDiagnostic = "Knowledge configuration could not be refreshed: " + err.Error()
		r.setLibraries(nil)
		r.observed, r.failed = nil, nil
		return
	}
	r.lastSettings, r.lastRoot = settings, root
	if r.observed == nil {
		r.observed, r.failed = map[string]string{}, map[string]failedActivation{}
	}
	if !samePins(r.topicPins, settings.Knowledge.TopicPins) {
		r.topicPins = copyPins(settings.Knowledge.TopicPins)
		r.generation++
	}
	store := Store{Root: root}
	verifier := r.verifier(settings)
	var diagnostics []string
	configured := map[string]bool{}
	for alias := range settings.Knowledge.Sources {
		configured[strings.ToLower(alias)] = true
	}
	for _, library := range append([]*Library{}, r.libraries...) {
		if !configured[strings.ToLower(library.SourceAlias)] {
			r.deactivate(library.SourceAlias)
		}
	}
	for alias := range r.failed {
		if !configured[strings.ToLower(alias)] {
			delete(r.failed, alias)
		}
	}
	for _, alias := range settings.Knowledge.SortedAliases() {
		source := settings.Knowledge.Sources[alias]
		if !source.Enabled {
			r.deactivate(alias)
			delete(r.failed, alias)
			continue
		}
		if source.Type == SourceGit {
			r.deactivate(alias)
			diagnostics = append(diagnostics, fmt.Sprintf("Git knowledge source '%s' is not served by creatio-mcp-go; "+
				"only installed signed bundles (github-release, nuget) are read.", alias))
			continue
		}
		r.activateInstalled(store, verifier, alias, source, &diagnostics)
	}
	r.lastDiagnostic = strings.Join(diagnostics, " ")
}

func (r *Runtime) activateInstalled(store Store, verifier *Verifier, alias string, source SourceConfig, diagnostics *[]string) {
	current, diagnostic := store.ReadCurrent(alias)
	if diagnostic != "" {
		*diagnostics = append(*diagnostics, diagnostic)
		r.deactivate(alias)
		return
	}
	if current == nil {
		r.deactivate(alias)
		return
	}
	identity := generationIdentity(current.Active, source)
	if r.observed[strings.ToLower(alias)] == identity {
		return
	}
	if failed, ok := r.failed[strings.ToLower(alias)]; ok && failed.identity == identity && time.Now().Before(failed.retryAfter) {
		*diagnostics = append(*diagnostics, failed.diagnostic)
		return
	}
	activeFailure, ok := r.tryActivate(store, verifier, alias, source, current.Active)
	if ok {
		r.observed[strings.ToLower(alias)] = identity
		delete(r.failed, strings.ToLower(alias))
		return
	}
	if activeFailure == "" {
		activeFailure = fmt.Sprintf("Knowledge source '%s' rejected its current generation.", alias)
	}
	if current.Previous != nil {
		previousIdentity := generationIdentity(*current.Previous, source)
		previousFailure := ""
		if r.observed[strings.ToLower(alias)] == previousIdentity {
			ok = true
		} else {
			previousFailure, ok = r.tryActivate(store, verifier, alias, source, *current.Previous)
		}
		if ok {
			r.observed[strings.ToLower(alias)] = previousIdentity
			r.recordFailed(alias, identity, activeFailure)
			*diagnostics = append(*diagnostics, activeFailure)
			return
		}
		if previousFailure != "" {
			activeFailure += " Previous generation also failed: " + previousFailure
		}
	}
	*diagnostics = append(*diagnostics, activeFailure)
	r.recordFailed(alias, identity, activeFailure)
	r.deactivate(alias)
}

func (r *Runtime) recordFailed(alias, identity, diagnostic string) {
	r.failed[strings.ToLower(alias)] = failedActivation{identity: identity, diagnostic: diagnostic, retryAfter: time.Now().Add(r.FailureRetry)}
}

func generationIdentity(pointer GenerationPointer, source SourceConfig) string {
	return fmt.Sprintf("%s:%s:%d:%s:%d:%s:%s:%s:%s", source.LibraryID, pointer.LibraryID, pointer.Sequence, pointer.BundleDigest,
		source.Priority, source.Participation, source.TrustedKeyID, source.TrustedPublicKeyPath, TrustFingerprint(source.TrustedPublicKeyPath))
}

// tryActivate reads, verifies and activates one generation (KnowledgeMultiSourceActivator.TryActivate
// plus KnowledgeBundleRuntime.ActivateLibrary).
func (r *Runtime) tryActivate(store Store, verifier *Verifier, alias string, source SourceConfig, pointer GenerationPointer) (string, bool) {
	contentRoot, data, diagnostic := store.ReadCandidate(alias, pointer)
	if diagnostic != "" {
		return diagnostic, false
	}
	prepared, rejection := verifier.Prepare(data, pointer.LibraryVersion, source.LibraryID)
	if rejection != nil {
		return rejection.Message, false
	}
	if prepared.LibraryID != source.LibraryID {
		return fmt.Sprintf("Candidate library '%s' does not match configured library '%s'.", prepared.LibraryID, source.LibraryID), false
	}
	current := r.find(alias)
	unchanged := current != nil && prepared.Sequence == current.Sequence && prepared.BundleDigest == current.BundleDigest
	if !unchanged && current != nil && prepared.Sequence <= current.Sequence {
		return fmt.Sprintf("Candidate sequence %d must be greater than active sequence %d for source '%s'.",
			prepared.Sequence, current.Sequence, alias), false
	}
	if prepared.Sequence != pointer.Sequence {
		return "", false
	}
	articles := make([]Article, len(prepared.Articles))
	for i, article := range prepared.Articles {
		article.LocalPath = filepath.Clean(filepath.Join(contentRoot, filepath.FromSlash(article.LocalPath)))
		articles[i] = article
	}
	library := &Library{SourceAlias: alias, LibraryID: prepared.LibraryID, LibraryVersion: prepared.LibraryVersion,
		Priority: source.Priority, Participation: source.Participation, Sequence: prepared.Sequence,
		BundleDigest: prepared.BundleDigest, ContentRoot: contentRoot, Articles: articles}
	next := []*Library{}
	for _, existing := range r.libraries {
		if !strings.EqualFold(existing.SourceAlias, alias) {
			next = append(next, existing)
		}
	}
	r.setLibraries(append(next, library))
	return "", true
}

func (r *Runtime) find(alias string) *Library {
	for _, library := range r.libraries {
		if strings.EqualFold(library.SourceAlias, alias) {
			return library
		}
	}
	return nil
}

func (r *Runtime) deactivate(alias string) {
	if r.find(alias) != nil {
		next := []*Library{}
		for _, library := range r.libraries {
			if !strings.EqualFold(library.SourceAlias, alias) {
				next = append(next, library)
			}
		}
		r.setLibraries(next)
	}
	delete(r.observed, strings.ToLower(alias))
}

func (r *Runtime) setLibraries(libraries []*Library) {
	r.libraries = libraries
	r.generation++
}

// samePins treats a never-set (nil) left side as different, so the first pass records the pins.
func samePins(left, right map[string]string) bool {
	if left == nil || len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}

func copyPins(pins map[string]string) map[string]string {
	out := make(map[string]string, len(pins))
	for key, value := range pins {
		out[key] = value
	}
	return out
}

// Libraries returns the active libraries after activation.
func (r *Runtime) Libraries() []*Library {
	r.EnsureActivated()
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*Library{}, r.libraries...)
}

// Settings returns the settings the last activation pass read, or nil when they were unreadable.
func (r *Runtime) Settings() *Settings {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastSettings
}

// FindByName resolves a guide name (get-guidance).
func (r *Runtime) FindByName(name string) Lookup { return r.lookup(name) }

// FindByURI resolves a resource URI (resources/read).
func (r *Runtime) FindByURI(uri string) Lookup { return r.lookup(uri) }

func (r *Runtime) lookup(identifier string) Lookup {
	r.EnsureActivated()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.libraries) == 0 {
		return Lookup{Status: LookupUnavailable}
	}
	return resolve(identifier, r.libraries, r.topicPins, r.eligible())
}

func (r *Runtime) eligible() func(*Article) bool {
	features := map[string]bool{}
	if r.lastSettings != nil {
		features = r.lastSettings.Features
	}
	return func(article *Article) bool {
		for _, feature := range article.RequiredFeatures {
			if !featureEnabled(features, feature) {
				return false
			}
		}
		return true
	}
}

func featureEnabled(features map[string]bool, name string) bool {
	for key, value := range features {
		if strings.EqualFold(key, name) {
			return value
		}
	}
	return false
}

// GuideNames lists availableGuides: the item IDs of guidance articles that resolve.
func (r *Runtime) GuideNames() []string { return r.resolvedCatalog().names }

// Catalog lists guidance and reference articles ordered by URI, for resources/list.
func (r *Runtime) Catalog() []Descriptor { return r.resolvedCatalog().byURI }

func (r *Runtime) resolvedCatalog() *resolvedCatalog {
	r.EnsureActivated()
	r.mu.Lock()
	defer r.mu.Unlock()
	eligible := r.eligible()
	signature := r.featureSignature()
	if r.catalog != nil && r.catalog.generation == r.generation && r.catalog.features == signature {
		return r.catalog
	}
	var guidance []*Article
	seenURI := map[string]bool{}
	for _, name := range resolvableNames(r.libraries, eligible) {
		lookup := resolve(name, r.libraries, r.topicPins, eligible)
		if lookup.Status == LookupActive && !seenURI[lookup.Article.URI] {
			seenURI[lookup.Article.URI] = true
			guidance = append(guidance, lookup.Article)
		}
	}
	names := map[string]bool{}
	for _, article := range guidance {
		names[article.ItemID] = true
	}
	all := append([]*Article{}, guidance...)
	for _, item := range articlesByRole(r.libraries, referenceRole) {
		if eligible(item.article) {
			all = append(all, item.article)
		}
	}
	byURI := map[string]bool{}
	var byName []Descriptor
	for _, article := range all {
		if byURI[article.URI] {
			continue
		}
		byURI[article.URI] = true
		byName = append(byName, Descriptor{Name: article.ItemID, Title: article.Title, Description: article.Description, URI: article.URI, MediaType: article.MediaType})
	}
	sort.SliceStable(byName, func(i, j int) bool { return byName[i].Name < byName[j].Name })
	ordered := append([]Descriptor{}, byName...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].URI != ordered[j].URI {
			return ordered[i].URI < ordered[j].URI
		}
		return ordered[i].Name < ordered[j].Name
	})
	r.catalog = &resolvedCatalog{generation: r.generation, features: signature, names: sortedStrings(names), byName: byName, byURI: ordered}
	return r.catalog
}

func (r *Runtime) featureSignature() string {
	gated := map[string]bool{}
	for _, library := range r.libraries {
		for _, article := range library.Articles {
			if article.Role == defaultRole || article.Role == referenceRole {
				for _, feature := range article.RequiredFeatures {
					gated[feature] = true
				}
			}
		}
	}
	features := map[string]bool{}
	if r.lastSettings != nil {
		features = r.lastSettings.Features
	}
	var parts []string
	for _, feature := range sortedStrings(gated) {
		parts = append(parts, fmt.Sprintf("%s=%t", feature, featureEnabled(features, feature)))
	}
	return strings.Join(parts, "\x1f")
}

type roleArticle struct {
	article *Article
	library *Library
}

func articlesByRole(libraries []*Library, role string) []roleArticle {
	var out []roleArticle
	for _, library := range libraries {
		for i := range library.Articles {
			if library.Articles[i].Role == role {
				out = append(out, roleArticle{article: &library.Articles[i], library: library})
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].library.Priority != out[j].library.Priority {
			return out[i].library.Priority > out[j].library.Priority
		}
		if out[i].library.LibraryID != out[j].library.LibraryID {
			return out[i].library.LibraryID < out[j].library.LibraryID
		}
		return out[i].article.ItemID < out[j].article.ItemID
	})
	return out
}

// ArticlesByRole lists every active article with a role, highest priority first.
func (r *Runtime) ArticlesByRole(role string) []RoleArticle {
	r.EnsureActivated()
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []RoleArticle
	for _, item := range articlesByRole(r.libraries, role) {
		out = append(out, RoleArticle{Article: *item.article, Provenance: *provenance(item.article, item.library),
			Priority: item.library.Priority, Participation: item.library.Participation})
	}
	return out
}

// RoleArticle is an article with the library that serves it.
type RoleArticle struct {
	Article       Article
	Provenance    Provenance
	Priority      int
	Participation Participation
}

func provenance(article *Article, library *Library) *Provenance {
	return &Provenance{SourceAlias: library.SourceAlias, LibraryID: library.LibraryID, LibraryVersion: library.LibraryVersion,
		ItemID: article.ItemID, TopicID: article.TopicID, Sequence: library.Sequence, BundleDigest: library.BundleDigest,
		LocalPath: article.LocalPath}
}
