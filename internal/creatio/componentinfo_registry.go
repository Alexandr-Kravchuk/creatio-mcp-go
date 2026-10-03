package creatio

// The byte transport behind get-component-info: clio's ComponentRegistryClient, ComponentRegistryCacheStore,
// ComponentRegistryDocsClient, ComponentRegistryDocsCacheStore and ComponentRegistryDocsPath. The on-disk
// cache is clio's own (<clio home>/cache/component-registry/), read and written with clio's file names,
// sidecar fields, 5-minute TTL and atomic tmp+rename, so this server and clio share it.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	componentInfoDefaultCDNBaseURL  = "https://academy.creatio.com/api/mcp/"
	componentInfoCDNBaseURLVariable = "CLIO_COMPONENT_REGISTRY_CDN_BASE_URL"
	componentInfoLatestVersion      = "latest"
	componentInfoCDNAttempts        = 3
	componentInfoCacheDirectory     = "component-registry"
	componentInfoCacheTTL           = 5 * time.Minute
	// componentInfoStaleRevalidateBudget bounds the synchronous CDN revalidation of a stale documentation file.
	componentInfoStaleRevalidateBudget = 5 * time.Second
	componentInfoBackgroundTimeout     = 2 * time.Minute
	componentInfoMaxPayloadBytes       = 64 << 20
)

// componentInfoFlavor is clio's RegistryFlavor: which registry file the CDN serves, the developer
// override variable, and the cache sub-directory.
type componentInfoFlavor struct {
	name          string
	registryFile  string
	localVariable string
	cacheSubdir   string
}

var (
	componentInfoWebFlavor    = componentInfoFlavor{"web", "ComponentRegistry.json", "CLIO_COMPONENT_REGISTRY_LOCAL_FILE", ""}
	componentInfoMobileFlavor = componentInfoFlavor{"mobile", "MobileComponentRegistry.json", "CLIO_MOBILE_COMPONENT_REGISTRY_LOCAL_FILE", "mobile"}
	// componentInfoDocsNamespaces maps each documentation namespace to the override variable of the flavor
	// that publishes it (ComponentRegistryDocsPath.NamespaceFlavors).
	componentInfoDocsNamespaces = []struct{ prefix, localVariable string }{
		{"mobile-request-docs/", "CLIO_MOBILE_REQUEST_REGISTRY_LOCAL_FILE"},
		{"mobile-docs/", "CLIO_MOBILE_COMPONENT_REGISTRY_LOCAL_FILE"},
		{"request-docs/", "CLIO_REQUEST_REGISTRY_LOCAL_FILE"},
		{"docs/", "CLIO_COMPONENT_REGISTRY_LOCAL_FILE"},
	}
	componentInfoDocsPathPattern = regexp.MustCompile(`^(?:docs|mobile-docs|request-docs|mobile-request-docs)/[A-Za-z0-9._-]+(?:/[A-Za-z0-9._-]+)*\.md$`)
)

// Seams for tests: the HTTP client (clio's named client has a 30 s timeout), the clock and the retry
// back-off delay.
var (
	componentInfoHTTPClient = &http.Client{Timeout: 30 * time.Second}
	componentInfoNow        = time.Now
	componentInfoDelay      = func(ctx context.Context, delay time.Duration) bool {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return false
		case <-timer.C:
			return true
		}
	}
	// componentInfoBackground tracks stale-while-revalidate refreshes so tests (and a caller that wants
	// to) can wait for them; componentInfoRefreshGates de-duplicates them per flavor and version.
	componentInfoBackground   sync.WaitGroup
	componentInfoRefreshGates sync.Map
)

// componentInfoSource is the tier that served a payload (clio's ComponentRegistrySource and
// ComponentDocumentationSource).
type componentInfoSource int

const (
	componentInfoSourceNone componentInfoSource = iota
	componentInfoSourceLocal
	componentInfoSourceCache
	componentInfoSourceCDN
)

func componentInfoCDNBase() string {
	base := os.Getenv(componentInfoCDNBaseURLVariable)
	if strings.TrimSpace(base) == "" {
		base = componentInfoDefaultCDNBaseURL
	}
	if !strings.HasSuffix(base, "/") {
		base += "/"
	}
	return base
}

func componentInfoCacheRoot() string {
	return filepath.Join(clioHomeDirectory(), "cache", componentInfoCacheDirectory)
}

// componentInfoCDNURL is {base}{version}/{relative}, composed the way clio's Uri(base, relative) does.
func componentInfoCDNURL(base, version, relative string) (string, error) {
	parsed, err := url.Parse(base)
	if err != nil || !parsed.IsAbs() {
		return "", fmt.Errorf("invalid component registry CDN base URL")
	}
	return parsed.ResolveReference(&url.URL{Path: version + "/" + relative}).String(), nil
}

// componentInfoUnavailableError is clio's ComponentRegistryUnavailableException.
type componentInfoUnavailableError struct{ version, cdnBase, localVariable string }

func (e componentInfoUnavailableError) Error() string {
	return fmt.Sprintf("Component registry version '%s' is unavailable: file cache is empty and the CDN at '%s' could not be reached. "+
		"Set %s to a local registry JSON for offline development, or retry once connectivity is restored.", e.version, e.cdnBase, e.localVariable)
}

// componentInfoFetchResult is a registry payload and where it came from.
type componentInfoFetchResult struct {
	content         []byte
	resolvedVersion string
	source          componentInfoSource
}

// componentInfoFetchRegistry is ComponentRegistryClient.GetAsync: local override, cache (stale served while a
// background refresh runs), CDN, then the cached or published 'latest' for a version the CDN does not have.
func componentInfoFetchRegistry(ctx context.Context, flavor componentInfoFlavor, version string) (componentInfoFetchResult, error) {
	if strings.TrimSpace(version) == "" {
		version = componentInfoLatestVersion
	}
	if path := os.Getenv(flavor.localVariable); strings.TrimSpace(path) != "" {
		if !componentInfoFileExists(path) {
			return componentInfoFetchResult{}, fmt.Errorf("Component registry override file does not exist: '%s'. "+
				"Either fix the path or unset %s to fall back to the CDN/cache chain.", path, flavor.localVariable)
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return componentInfoFetchResult{}, err
		}
		return componentInfoFetchResult{content, version, componentInfoSourceLocal}, nil
	}
	store := componentInfoRegistryStore(flavor)
	if cached := store.read(version); cached != nil {
		if !cached.fresh {
			componentInfoScheduleRefresh(flavor, version)
		}
		return componentInfoFetchResult{cached.content, version, componentInfoSourceCache}, nil
	}
	if content := componentInfoFetchRegistryCDN(ctx, flavor, version); content != nil {
		return componentInfoFetchResult{content, version, componentInfoSourceCDN}, nil
	}
	if !strings.EqualFold(version, componentInfoLatestVersion) {
		if cached := store.read(componentInfoLatestVersion); cached != nil {
			if !cached.fresh {
				componentInfoScheduleRefresh(flavor, componentInfoLatestVersion)
			}
			return componentInfoFetchResult{cached.content, componentInfoLatestVersion, componentInfoSourceCache}, nil
		}
		if content := componentInfoFetchRegistryCDN(ctx, flavor, componentInfoLatestVersion); content != nil {
			return componentInfoFetchResult{content, componentInfoLatestVersion, componentInfoSourceCDN}, nil
		}
	}
	if err := ctx.Err(); err != nil {
		return componentInfoFetchResult{}, err
	}
	return componentInfoFetchResult{}, componentInfoUnavailableError{version, componentInfoCDNBase(), flavor.localVariable}
}

// componentInfoScheduleRefresh refreshes a stale cache entry in the background, once per flavor and version
// at a time, as clio's ScheduleBackgroundRefresh does.
func componentInfoScheduleRefresh(flavor componentInfoFlavor, version string) {
	gate, _ := componentInfoRefreshGates.LoadOrStore(flavor.name+"|"+version, &sync.Mutex{})
	mutex := gate.(*sync.Mutex)
	if !mutex.TryLock() {
		return
	}
	componentInfoBackground.Add(1)
	go func() {
		defer componentInfoBackground.Done()
		defer mutex.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), componentInfoBackgroundTimeout)
		defer cancel()
		componentInfoFetchRegistryCDN(ctx, flavor, version)
	}()
}

func componentInfoFetchRegistryCDN(ctx context.Context, flavor componentInfoFlavor, version string) []byte {
	base := componentInfoCDNBase()
	target, err := componentInfoCDNURL(base, version, flavor.registryFile)
	if err != nil {
		return nil
	}
	return componentInfoFetchCDN(ctx, target, func(payload []byte, header http.Header) {
		_ = componentInfoRegistryStore(flavor).write(version, payload, header, target)
	})
}

// componentInfoFetchCDN is clio's retry loop: up to three attempts with 1 s and 2 s back-off; a 4xx is
// permanent, a 5xx, transport failure or timeout is retried. A 2xx body is cached through store.
func componentInfoFetchCDN(ctx context.Context, target string, store func([]byte, http.Header)) []byte {
	for attempt := 1; attempt <= componentInfoCDNAttempts; attempt++ {
		payload, header, retry := componentInfoFetchOnce(ctx, target)
		if payload != nil {
			store(payload, header)
			return payload
		}
		if !retry || ctx.Err() != nil {
			return nil
		}
		if attempt < componentInfoCDNAttempts && !componentInfoDelay(ctx, time.Duration(1<<(attempt-1))*time.Second) {
			return nil
		}
	}
	return nil
}

func componentInfoFetchOnce(ctx context.Context, target string) ([]byte, http.Header, bool) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, nil, false
	}
	response, err := componentInfoHTTPClient.Do(request)
	if err != nil {
		return nil, nil, true
	}
	defer response.Body.Close()
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		payload, err := io.ReadAll(io.LimitReader(response.Body, componentInfoMaxPayloadBytes))
		if err != nil {
			return nil, nil, true
		}
		if payload == nil {
			payload = []byte{}
		}
		return payload, response.Header, false
	}
	return nil, nil, response.StatusCode < 400 || response.StatusCode >= 500
}

// componentInfoCacheStore is one cache directory: clio's ComponentRegistryCacheStore for registry payloads
// ({version}.json + {version}.meta.json) and ComponentRegistryDocsCacheStore for documentation
// ({version}/{docPath} + .meta.json).
type componentInfoCacheStore struct{ root string }

type componentInfoCached struct {
	content []byte
	fresh   bool
}

func componentInfoRegistryStore(flavor componentInfoFlavor) componentInfoCacheStore {
	root := componentInfoCacheRoot()
	if flavor.cacheSubdir != "" {
		root = filepath.Join(root, flavor.cacheSubdir)
	}
	return componentInfoCacheStore{root}
}

func (s componentInfoCacheStore) registryPaths(version string) (string, string, bool) {
	safe := componentInfoSanitizeVersion(version)
	if safe == "" {
		return "", "", false
	}
	return filepath.Join(s.root, safe+".json"), filepath.Join(s.root, safe+".meta.json"), true
}

func (s componentInfoCacheStore) read(version string) *componentInfoCached {
	payloadPath, metaPath, ok := s.registryPaths(version)
	if !ok {
		return nil
	}
	return componentInfoReadCached(payloadPath, metaPath)
}

func (s componentInfoCacheStore) write(version string, payload []byte, header http.Header, sourceURL string) error {
	payloadPath, metaPath, ok := s.registryPaths(version)
	if !ok {
		return fmt.Errorf("Version '%s' contains no usable characters.", version)
	}
	return componentInfoWriteCached(payloadPath, metaPath, payload, header, sourceURL)
}

// docsPaths resolves {root}/{version}/{docPath} and keeps it inside the cache root.
func (s componentInfoCacheStore) docsPaths(version, docPath string) (string, string, bool) {
	normalised, ok := componentInfoNormaliseDocPath(docPath)
	safe := componentInfoSanitizeVersion(version)
	if !ok || safe == "" {
		return "", "", false
	}
	root, err := filepath.Abs(s.root)
	if err != nil {
		return "", "", false
	}
	candidate := filepath.Join(root, safe, filepath.FromSlash(normalised))
	if !strings.HasPrefix(candidate, root+string(filepath.Separator)) {
		return "", "", false
	}
	return candidate, candidate + ".meta.json", true
}

func (s componentInfoCacheStore) readDoc(version, docPath string) *componentInfoCached {
	payloadPath, metaPath, ok := s.docsPaths(version, docPath)
	if !ok {
		return nil
	}
	return componentInfoReadCached(payloadPath, metaPath)
}

func (s componentInfoCacheStore) writeDoc(version, docPath string, payload []byte, header http.Header, cdnBase string) error {
	payloadPath, metaPath, ok := s.docsPaths(version, docPath)
	if !ok {
		return nil
	}
	return componentInfoWriteCached(payloadPath, metaPath, payload, header, cdnBase+version+"/"+docPath)
}

// componentInfoReadCached returns the payload when both files exist; a sidecar that cannot be read or parsed
// removes both files, as clio does.
func componentInfoReadCached(payloadPath, metaPath string) *componentInfoCached {
	if !componentInfoFileExists(payloadPath) || !componentInfoFileExists(metaPath) {
		return nil
	}
	metaBytes, err := os.ReadFile(metaPath)
	if err != nil {
		componentInfoDeleteQuietly(payloadPath, metaPath)
		return nil
	}
	var meta struct {
		ExpiresAt string `json:"expiresAt"`
	}
	if trimmed := bytes.TrimSpace(metaBytes); bytes.Equal(trimmed, []byte("null")) || json.Unmarshal(metaBytes, &meta) != nil {
		componentInfoDeleteQuietly(payloadPath, metaPath)
		return nil
	}
	expires, err := time.Parse(time.RFC3339Nano, meta.ExpiresAt)
	if meta.ExpiresAt != "" && err != nil {
		componentInfoDeleteQuietly(payloadPath, metaPath)
		return nil
	}
	payload, err := os.ReadFile(payloadPath)
	if err != nil {
		componentInfoDeleteQuietly(payloadPath, metaPath)
		return nil
	}
	return &componentInfoCached{content: payload, fresh: expires.After(componentInfoNow())}
}

// componentInfoWriteCached writes the payload and clio's provenance sidecar to unique tmp files, then moves
// the payload first so a reader never pairs a new payload with stale metadata.
func componentInfoWriteCached(payloadPath, metaPath string, payload []byte, header http.Header, sourceURL string) error {
	if err := os.MkdirAll(filepath.Dir(payloadPath), 0o755); err != nil {
		return err
	}
	suffix := ".tmp." + randomHex(16)
	fetched := componentInfoNow().UTC()
	digest := sha256.Sum256(payload)
	var meta bytes.Buffer
	meta.WriteString(`{"fetchedAt":`)
	meta.WriteString(`"` + componentInfoDotnetTime(fetched) + `"`)
	meta.WriteString(`,"expiresAt":`)
	meta.WriteString(`"` + componentInfoDotnetTime(fetched.Add(componentInfoCacheTTL)) + `"`)
	meta.WriteString(`,"sourceUrl":`)
	writeJSONString(&meta, sourceURL, true)
	if etag := header.Get("ETag"); etag != "" {
		meta.WriteString(`,"etag":`)
		writeJSONString(&meta, etag, true)
	}
	if modified, err := http.ParseTime(header.Get("Last-Modified")); err == nil {
		meta.WriteString(`,"lastModified":`)
		meta.WriteString(`"` + componentInfoDotnetTime(modified.UTC()) + `"`)
	}
	meta.WriteString(`,"contentSha256":`)
	writeJSONString(&meta, strings.ToUpper(hex.EncodeToString(digest[:])), true)
	meta.WriteByte('}')
	if err := os.WriteFile(payloadPath+suffix, payload, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(metaPath+suffix, meta.Bytes(), 0o644); err != nil {
		_ = os.Remove(payloadPath + suffix)
		return err
	}
	if err := os.Rename(payloadPath+suffix, payloadPath); err != nil {
		componentInfoDeleteQuietly(payloadPath+suffix, metaPath+suffix)
		return err
	}
	if err := os.Rename(metaPath+suffix, metaPath); err != nil {
		componentInfoDeleteQuietly(metaPath + suffix)
		return err
	}
	return nil
}

// componentInfoDotnetTime is a UTC DateTimeOffset as System.Text.Json writes it: up to seven fractional
// digits without trailing zeros, and a +00:00 offset, unescaped (only strings get the encoder's escaping).
func componentInfoDotnetTime(value time.Time) string {
	return value.Truncate(100 * time.Nanosecond).Format("2006-01-02T15:04:05.9999999-07:00")
}

func componentInfoFileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func componentInfoDeleteQuietly(paths ...string) {
	for _, path := range paths {
		_ = os.Remove(path)
	}
}

// componentInfoSanitizeVersion keeps letters, digits, '.', '-' and '_' (clio's SanitizeVersion); an empty
// result means the version has no usable characters.
func componentInfoSanitizeVersion(version string) string {
	var builder strings.Builder
	for _, r := range version {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' || r == '-' || r == '_' {
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

// componentInfoNormaliseDocPath is ComponentRegistryDocsPath.TryNormalise: a trimmed path in one of the four
// documentation namespaces, ending in .md, with no '..', backslash or leading slash.
func componentInfoNormaliseDocPath(raw string) (string, bool) {
	path := strings.TrimSpace(raw)
	if path == "" || strings.Contains(path, "..") || strings.Contains(path, `\`) || strings.HasPrefix(path, "/") {
		return path, false
	}
	return path, componentInfoDocsPathPattern.MatchString(path)
}

// componentInfoDocResult is clio's ComponentDocumentationFetchResult.
type componentInfoDocResult struct {
	content       *string
	source        componentInfoSource
	localVariable string
}

// componentInfoFetchDoc is ComponentRegistryDocsClient.GetDocAsync: local working copy next to the flavor's
// override file (authoritative while the override is set), fresh cache, stale cache revalidated against the
// CDN within 5 s, CDN.
func componentInfoFetchDoc(ctx context.Context, version, docPath string) componentInfoDocResult {
	normalised, ok := componentInfoNormaliseDocPath(docPath)
	if !ok {
		return componentInfoDocResult{}
	}
	if local, ok := componentInfoReadLocalDoc(normalised); ok {
		return local
	}
	store := componentInfoCacheStore{componentInfoCacheRoot()}
	if cached := store.readDoc(version, normalised); cached != nil {
		if !cached.fresh {
			budget, cancel := context.WithTimeout(ctx, componentInfoStaleRevalidateBudget)
			refreshed := componentInfoFetchDocCDN(budget, version, normalised)
			cancel()
			if refreshed != nil {
				return componentInfoDocText(refreshed, componentInfoSourceCDN)
			}
		}
		return componentInfoDocText(cached.content, componentInfoSourceCache)
	}
	if fetched := componentInfoFetchDocCDN(ctx, version, normalised); fetched != nil {
		return componentInfoDocText(fetched, componentInfoSourceCDN)
	}
	return componentInfoDocResult{}
}

func componentInfoDocText(content []byte, source componentInfoSource) componentInfoDocResult {
	text := string(bytes.ToValidUTF8(content, []byte("�")))
	return componentInfoDocResult{content: &text, source: source}
}

func componentInfoFetchDocCDN(ctx context.Context, version, docPath string) []byte {
	base := componentInfoCDNBase()
	target, err := componentInfoCDNURL(base, version, docPath)
	if err != nil {
		return nil
	}
	return componentInfoFetchCDN(ctx, target, func(payload []byte, header http.Header) {
		_ = componentInfoCacheStore{componentInfoCacheRoot()}.writeDoc(version, docPath, payload, header, base)
	})
}

// componentInfoReadLocalDoc resolves the path in the directory of the registry override file of the flavor
// that owns the namespace. ok is false when no override is active for it.
func componentInfoReadLocalDoc(docPath string) (componentInfoDocResult, bool) {
	variable := ""
	for _, namespace := range componentInfoDocsNamespaces {
		if strings.HasPrefix(docPath, namespace.prefix) {
			variable = namespace.localVariable
			break
		}
	}
	registryFile := strings.TrimSpace(os.Getenv(variable))
	if variable == "" || registryFile == "" {
		return componentInfoDocResult{}, false
	}
	absolute, err := filepath.Abs(registryFile)
	if err != nil {
		return componentInfoDocResult{}, false
	}
	root := filepath.Dir(absolute)
	candidate := filepath.Join(root, filepath.FromSlash(docPath))
	if !strings.HasPrefix(candidate, root+string(filepath.Separator)) {
		return componentInfoDocResult{}, true
	}
	content, err := os.ReadFile(candidate)
	if err != nil {
		return componentInfoDocResult{localVariable: variable}, true
	}
	text := string(content)
	return componentInfoDocResult{content: &text, source: componentInfoSourceLocal}, true
}
