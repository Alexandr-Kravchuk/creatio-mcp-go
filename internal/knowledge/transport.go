package knowledge

// The artifact transports clio's install-knowledge and update-knowledge use: a signed bundle published as
// a GitHub Release asset (KnowledgeGitHubReleaseTransport) and a signed bundle inside a NuGet v3 package
// (KnowledgeBundleNuGetClient). Both bound every body, refuse redirects they have not checked and spend one
// absolute deadline across all of their requests, as clio's do.

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/redact"
)

// TransportStatus is clio's KnowledgeTransportStatus.
type TransportStatus int

// The transport outcomes.
const (
	TransportNoCandidate TransportStatus = iota
	TransportFailed
	TransportRejected
	TransportDownloaded
)

// TransportDeadline is clio's TransportDeadlineMilliseconds for both artifact transports.
const TransportDeadline = 15 * time.Second

// TransportRequest is clio's KnowledgeTransportRequest.
type TransportRequest struct {
	Alias              string
	Source             SourceConfig
	Rejected           map[string]bool // lower-cased revisions (clio's OrdinalIgnoreCase set)
	ActiveRevision     string
	HighestObserved    string
	FallbackCeiling    string
	CatalogFingerprint string
	Deadline           time.Duration // 0: the transport's own deadline
	ExactRevision      string
}

// TransportResult is clio's KnowledgeTransportResult.
type TransportResult struct {
	Status             TransportStatus
	ResolvedRevision   string
	CandidateBytes     []byte
	CatalogFingerprint string
	ResolvedTag        string
	Diagnostic         string
}

// ArtifactTransport is clio's IKnowledgeArtifactTransport.
type ArtifactTransport interface {
	Retrieve(ctx context.Context, request TransportRequest) TransportResult
	GreaterRevision(left, right string) string
}

// HTTPDoer sends one request; *http.Client implements it.
type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// NewTransportClient is clio's named HttpClient for both transports: a 15 s timeout and no automatic
// redirects (the GitHub transport checks and follows each hop itself).
func NewTransportClient() *http.Client {
	return &http.Client{Timeout: TransportDeadline, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
}

var stableVersionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

type stableVersion [3]int

func parseStableVersion(value string) (stableVersion, bool) {
	if value == "" || len(value) > 32 || !stableVersionPattern.MatchString(value) {
		return stableVersion{}, false
	}
	var version stableVersion
	for i, part := range strings.Split(value, ".") {
		number, err := strconv.ParseInt(part, 10, 32)
		if err != nil {
			return stableVersion{}, false
		}
		version[i] = int(number)
	}
	return version, true
}

func (v stableVersion) compare(other stableVersion) int {
	for i := range v {
		if v[i] != other[i] {
			if v[i] < other[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

func (v stableVersion) String() string { return fmt.Sprintf("%d.%d.%d", v[0], v[1], v[2]) }

// greaterStable is clio's GreaterRevision/GreaterVersion over MAJOR.MINOR.PATCH revisions.
func greaterStable(left, right string) string {
	leftVersion, leftOK := parseStableVersion(left)
	rightVersion, rightOK := parseStableVersion(right)
	switch {
	case !leftOK && rightOK:
		return right
	case !leftOK:
		return ""
	case !rightOK:
		return left
	case leftVersion.compare(rightVersion) >= 0:
		return left
	}
	return right
}

// httpStatusError is .NET's EnsureSuccessStatusCode message.
func httpStatusError(response *http.Response) error {
	return fmt.Errorf("Response status code does not indicate success: %d (%s).", response.StatusCode,
		strings.TrimPrefix(http.StatusText(response.StatusCode), ""))
}

// readBoundedBody is clio's KnowledgeTransportHttp.ReadBounded.
func readBoundedBody(response *http.Response, maximum int64) ([]byte, error) {
	if response.ContentLength > maximum {
		return nil, fmt.Errorf("HTTP content exceeds the %d-byte limit.", maximum)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maximum {
		return nil, fmt.Errorf("Content exceeds the %d-byte limit.", maximum)
	}
	return data, nil
}

func ensureTrailingSlash(u *url.URL) *url.URL {
	copied := *u
	if !strings.HasSuffix(copied.Path, "/") {
		copied.Path += "/"
		if copied.RawPath != "" {
			copied.RawPath += "/"
		}
	}
	return &copied
}

func isLoopbackHost(host string) bool {
	host = strings.ToLower(strings.Trim(host, "[]"))
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func effectivePort(u *url.URL) string {
	if port := u.Port(); port != "" {
		return port
	}
	if strings.EqualFold(u.Scheme, "https") {
		return "443"
	}
	return "80"
}

func transportDeadline(requested time.Duration) time.Duration {
	if requested > 0 && requested < TransportDeadline {
		return requested
	}
	return TransportDeadline
}

// ---------------------------------------------------------------------------------------------------------
// GitHub release transport

const (
	maxReleaseMetadataBytes = 1024 * 1024
	maxReleaseAssetBytes    = 40 * 1024 * 1024
	maxReleaseAssets        = 512
	maxReleaseRedirects     = 3
	maxEntityTagLength      = 256
	githubAPIVersion        = "2022-11-28"
)

var (
	sha256HexPattern     = regexp.MustCompile(`^[0-9a-f]{64}$`)
	allowedDownloadHosts = map[string]bool{"api.github.com": true, "github.com": true,
		"release-assets.githubusercontent.com": true, "objects.githubusercontent.com": true}
	allowedAssetTypes = map[string]bool{"application/zip": true, "application/x-zip-compressed": true,
		"application/octet-stream": true}
)

// GitHubReleaseTransport is clio's KnowledgeGitHubReleaseTransport.
type GitHubReleaseTransport struct {
	Client HTTPDoer
}

// GreaterRevision orders release tags.
func (GitHubReleaseTransport) GreaterRevision(left, right string) string { return greaterStable(left, right) }

type releaseFailure struct{ message string }

func (e releaseFailure) Error() string { return e.message }

// Retrieve is clio's Retrieve.
func (t GitHubReleaseTransport) Retrieve(ctx context.Context, request TransportRequest) TransportResult {
	if request.Source.Type != SourceGitHubRelease {
		return TransportResult{Status: TransportFailed, CatalogFingerprint: request.CatalogFingerprint,
			Diagnostic: "GitHub release transport received a non-GitHub-release source. (Parameter 'request')"}
	}
	ctx, cancel := context.WithTimeout(ctx, transportDeadline(request.Deadline))
	defer cancel()
	result, err := t.retrieve(ctx, request)
	if err != nil {
		return TransportResult{Status: TransportFailed, CatalogFingerprint: request.CatalogFingerprint,
			Diagnostic: redact.Text(transportErrorText(err))}
	}
	return result
}

func transportErrorText(err error) string {
	var failure releaseFailure
	if errors.As(err, &failure) {
		return failure.message
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return "The operation was canceled."
	}
	var urlError *url.Error
	if errors.As(err, &urlError) {
		return urlError.Err.Error()
	}
	return err.Error()
}

func (t GitHubReleaseTransport) retrieve(ctx context.Context, request TransportRequest) (TransportResult, error) {
	apiBase, err := url.Parse(request.Source.Location)
	if err != nil {
		return TransportResult{}, err
	}
	relative := "releases/latest"
	if request.ExactRevision != "" {
		relative = "releases/tags/" + EscapeDataString(request.ExactRevision)
	}
	metadataURL, err := ensureTrailingSlash(apiBase).Parse("repos/" + EscapeDataString(request.Source.RepositoryOwner) + "/" +
		EscapeDataString(request.Source.RepositoryName) + "/" + relative)
	if err != nil {
		return TransportResult{}, err
	}
	fingerprintIn := ""
	if request.ExactRevision == "" {
		fingerprintIn = request.CatalogFingerprint
	}
	payload, entityTag, notModified, err := t.readReleaseMetadata(ctx, metadataURL, fingerprintIn)
	if err != nil {
		return TransportResult{}, err
	}
	if notModified {
		fingerprint := entityTag
		if fingerprint == "" {
			fingerprint = request.CatalogFingerprint
		}
		return TransportResult{Status: TransportNoCandidate, CatalogFingerprint: fingerprint}, nil
	}
	fingerprint := entityTag
	if fingerprint == "" {
		fingerprint = request.CatalogFingerprint
	}
	selection, err := selectReleaseAsset(payload, request.Source, apiBase)
	if err != nil {
		return TransportResult{Status: TransportFailed, CatalogFingerprint: fingerprint, Diagnostic: err.Error()}, nil
	}
	if request.Rejected[strings.ToLower(selection.revision)] || selection.revision == request.ActiveRevision {
		return TransportResult{Status: TransportNoCandidate, CatalogFingerprint: fingerprint}, nil
	}
	asset, err := t.downloadAsset(ctx, selection, apiBase)
	if err != nil {
		return TransportResult{}, err
	}
	if computeDigest(asset) != selection.digest {
		return TransportResult{Status: TransportRejected, ResolvedRevision: selection.revision, CatalogFingerprint: fingerprint,
			Diagnostic: "The downloaded release asset does not match the digest GitHub published for it."}, nil
	}
	result := TransportResult{Status: TransportDownloaded, ResolvedRevision: selection.revision, CandidateBytes: asset,
		CatalogFingerprint: fingerprint, ResolvedTag: selection.revision}
	if !selection.immutable {
		result.Diagnostic = "The release is not marked immutable; its assets could still be replaced upstream."
	}
	return result, nil
}

func usableEntityTag(tag string) bool {
	if strings.TrimSpace(tag) == "" || len(tag) > maxEntityTagLength {
		return false
	}
	for _, r := range tag {
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
			return false
		}
	}
	return true
}

func isRateLimited(response *http.Response) bool {
	return response.StatusCode == http.StatusForbidden || response.StatusCode == http.StatusTooManyRequests
}

func (t GitHubReleaseTransport) readReleaseMetadata(ctx context.Context, metadataURL *url.URL, entityTag string) (map[string]any, string, bool, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, metadataURL.String(), nil)
	if err != nil {
		return nil, "", false, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", githubAPIVersion)
	request.Header.Set("User-Agent", "clio-knowledge-client")
	if usableEntityTag(entityTag) {
		request.Header.Set("If-None-Match", entityTag)
	}
	response, err := t.Client.Do(request)
	if err != nil {
		return nil, "", false, err
	}
	defer response.Body.Close()
	tag := response.Header.Get("ETag")
	if !usableEntityTag(tag) {
		tag = ""
	}
	if response.StatusCode == http.StatusNotModified {
		if tag == "" {
			tag = entityTag
		}
		return nil, tag, true, nil
	}
	if isRateLimited(response) {
		return nil, "", false, releaseFailure{"GitHub refused the release metadata request (rate limit or access); no retry was attempted."}
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return nil, "", false, httpStatusError(response)
	}
	body, err := readBoundedBody(response, maxReleaseMetadataBytes)
	if err != nil {
		return nil, "", false, err
	}
	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, "", false, err
	}
	object, _ := payload.(map[string]any)
	if object == nil {
		return nil, tag, false, nil
	}
	return object, tag, false, nil
}

type releaseAsset struct {
	revision  string
	download  *url.URL
	digest    string
	size      int64
	immutable bool
}

func jsonString(object map[string]any, key string) (string, bool) {
	value, ok := object[key].(string)
	return value, ok
}

func jsonTrue(object map[string]any, key string) bool {
	value, ok := object[key].(bool)
	return ok && value
}

func selectReleaseAsset(release map[string]any, source SourceConfig, apiBase *url.URL) (releaseAsset, error) {
	if release == nil {
		return releaseAsset{}, errors.New("The GitHub release response is not a release object.")
	}
	if jsonTrue(release, "draft") || jsonTrue(release, "prerelease") {
		return releaseAsset{}, errors.New("The selected GitHub release is a draft or prerelease.")
	}
	tag, ok := jsonString(release, "tag_name")
	if !ok {
		return releaseAsset{}, errors.New("The GitHub release does not declare a tag.")
	}
	if !stableVersionPattern.MatchString(tag) {
		return releaseAsset{}, errors.New("A knowledge release tag must be an exact MAJOR.MINOR.PATCH library version.")
	}
	assets, ok := release["assets"].([]any)
	if !ok || len(assets) > maxReleaseAssets {
		return releaseAsset{}, errors.New("The GitHub release does not expose a bounded asset list.")
	}
	var matches []map[string]any
	for _, candidate := range assets {
		if asset, ok := candidate.(map[string]any); ok {
			if name, _ := jsonString(asset, "name"); name == source.AssetName {
				matches = append(matches, asset)
			}
		}
	}
	if len(matches) != 1 {
		return releaseAsset{}, fmt.Errorf("The GitHub release must expose exactly one '%s' asset.", source.AssetName)
	}
	selected := matches[0]
	if state, _ := jsonString(selected, "state"); state != "uploaded" {
		return releaseAsset{}, errors.New("The selected release asset is not in the uploaded state.")
	}
	if contentType, ok := jsonString(selected, "content_type"); !ok || !allowedAssetTypes[strings.ToLower(contentType)] {
		return releaseAsset{}, errors.New("The selected release asset does not declare an archive content type.")
	}
	size, ok := selected["size"].(float64)
	if !ok || size != float64(int64(size)) || size <= 0 || size > maxReleaseAssetBytes {
		return releaseAsset{}, fmt.Errorf("The selected release asset size is missing or exceeds the %d-byte limit.", maxReleaseAssetBytes)
	}
	rawDigest, _ := jsonString(selected, "digest")
	if !strings.HasPrefix(rawDigest, "sha256:") || !sha256HexPattern.MatchString(rawDigest[len("sha256:"):]) {
		return releaseAsset{}, errors.New("The selected release asset does not publish a well-formed SHA-256 digest.")
	}
	rawURL, _ := jsonString(selected, "url")
	download, err := url.Parse(rawURL)
	if err != nil || !download.IsAbs() || download.Host == "" {
		return releaseAsset{}, errors.New("The selected release asset does not declare a download URL.")
	}
	if !allowedDownloadURL(download, apiBase) {
		return releaseAsset{}, errors.New("The selected release asset download URL is not an allowed GitHub host.")
	}
	return releaseAsset{revision: tag, download: download, digest: rawDigest[len("sha256:"):], size: int64(size),
		immutable: jsonTrue(release, "immutable")}, nil
}

// allowedDownloadURL is clio's IsAllowedDownloadUri: credential-free; for a loopback API origin the same
// loopback scheme and port, otherwise HTTPS on GitHub's documented download hosts.
func allowedDownloadURL(candidate, apiBase *url.URL) bool {
	if candidate.User != nil {
		return false
	}
	if isLoopbackHost(apiBase.Hostname()) {
		return isLoopbackHost(candidate.Hostname()) && strings.EqualFold(candidate.Scheme, apiBase.Scheme) &&
			effectivePort(candidate) == effectivePort(apiBase)
	}
	return strings.EqualFold(candidate.Scheme, "https") && allowedDownloadHosts[strings.ToLower(candidate.Hostname())]
}

func (t GitHubReleaseTransport) downloadAsset(ctx context.Context, selection releaseAsset, apiBase *url.URL) ([]byte, error) {
	current := selection.download
	for hop := 0; hop <= maxReleaseRedirects; hop++ {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, current.String(), nil)
		if err != nil {
			return nil, err
		}
		request.Header.Set("Accept", "application/octet-stream")
		request.Header.Set("X-GitHub-Api-Version", githubAPIVersion)
		request.Header.Set("User-Agent", "clio-knowledge-client")
		response, err := t.Client.Do(request)
		if err != nil {
			return nil, err
		}
		switch response.StatusCode {
		case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
			location := response.Header.Get("Location")
			response.Body.Close()
			if location == "" {
				return nil, releaseFailure{"A release asset redirect carried no location."}
			}
			next, err := current.Parse(location)
			if err != nil || !allowedDownloadURL(next, apiBase) {
				return nil, releaseFailure{"A release asset redirect targeted a host or scheme that is not allowed."}
			}
			current = next
			continue
		}
		defer response.Body.Close()
		if isRateLimited(response) {
			return nil, releaseFailure{"GitHub refused the release asset download (rate limit or access); no retry was attempted."}
		}
		if response.StatusCode < 200 || response.StatusCode > 299 {
			return nil, httpStatusError(response)
		}
		if response.ContentLength >= 0 && response.ContentLength != selection.size {
			return nil, releaseFailure{"The release asset transfer length differs from its published size."}
		}
		return readBoundedBody(response, selection.size)
	}
	return nil, releaseFailure{"The release asset download exceeded the allowed number of redirects."}
}

// ---------------------------------------------------------------------------------------------------------
// NuGet transport

const (
	maxServiceIndexBytes     = 1024 * 1024
	maxVersionIndexBytes     = 1024 * 1024
	maxVersionEntries        = 4096
	maxPackageBytes          = 48 * 1024 * 1024
	maxInnerBundleBytes      = 40 * 1024 * 1024
	maxPackageEntries        = 256
	maxPackageDirectoryBytes = 2 * 1024 * 1024
	innerBundlePath          = "content/knowledge-bundle.zip"
)

// NuGetTransport is clio's KnowledgeBundleNuGetClient.
type NuGetTransport struct {
	Client HTTPDoer
}

// GreaterRevision orders stable package versions.
func (NuGetTransport) GreaterRevision(left, right string) string { return greaterStable(left, right) }

// Retrieve is clio's Retrieve/DownloadNext.
func (t NuGetTransport) Retrieve(ctx context.Context, request TransportRequest) TransportResult {
	if request.Source.Type != SourceNuGet {
		return TransportResult{Status: TransportFailed, Diagnostic: "NuGet transport received a non-NuGet source. (Parameter 'request')"}
	}
	ctx, cancel := context.WithTimeout(ctx, transportDeadline(request.Deadline))
	defer cancel()
	source, err := url.Parse(request.Source.Location)
	if err != nil {
		return TransportResult{Status: TransportFailed, Diagnostic: err.Error()}
	}
	base, err := t.discoverPackageBase(ctx, source)
	if err != nil {
		return TransportResult{Status: TransportFailed, Diagnostic: transportErrorText(err)}
	}
	versions, err := t.readVersions(ctx, base, request.Source.PackageID)
	if err != nil {
		return TransportResult{Status: TransportFailed, Diagnostic: transportErrorText(err)}
	}
	fingerprint := catalogFingerprint(versions)
	changed := request.CatalogFingerprint != "" && request.CatalogFingerprint != fingerprint
	selected, ok := selectPackageVersion(versions, request, changed)
	if !ok {
		return TransportResult{Status: TransportNoCandidate, CatalogFingerprint: fingerprint}
	}
	version := selected.String()
	packageBytes, err := t.downloadPackage(ctx, base, request.Source.PackageID, version)
	if err != nil {
		var invalid invalidPackage
		if errors.As(err, &invalid) {
			return TransportResult{Status: TransportRejected, ResolvedRevision: version, CatalogFingerprint: fingerprint}
		}
		return TransportResult{Status: TransportFailed, CatalogFingerprint: fingerprint, Diagnostic: transportErrorText(err)}
	}
	bundle, err := extractInnerBundle(packageBytes)
	if err != nil {
		return TransportResult{Status: TransportRejected, ResolvedRevision: version, CatalogFingerprint: fingerprint}
	}
	return TransportResult{Status: TransportDownloaded, ResolvedRevision: version, CandidateBytes: bundle, CatalogFingerprint: fingerprint}
}

type invalidPackage struct{ message string }

func (e invalidPackage) Error() string { return e.message }

func allowedFeedURL(u *url.URL) bool {
	return strings.EqualFold(u.Scheme, "https") || (strings.EqualFold(u.Scheme, "http") && isLoopbackHost(u.Hostname()))
}

func (t NuGetTransport) getJSON(ctx context.Context, target string, maximum int64) (map[string]any, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", "")
	response, err := t.Client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return nil, httpStatusError(response)
	}
	body, err := readBoundedBody(response, maximum)
	if err != nil {
		return nil, err
	}
	var document any
	if err := json.Unmarshal(body, &document); err != nil {
		return nil, err
	}
	object, _ := document.(map[string]any)
	return object, nil
}

func (t NuGetTransport) discoverPackageBase(ctx context.Context, source *url.URL) (*url.URL, error) {
	document, err := t.getJSON(ctx, source.String(), maxServiceIndexBytes)
	if err != nil {
		return nil, err
	}
	resources, ok := document["resources"].([]any)
	if !ok {
		return nil, releaseFailure{"NuGet service index has no resources array."}
	}
	for _, entry := range resources {
		resource, _ := entry.(map[string]any)
		if resource == nil {
			continue
		}
		id, ok := resource["@id"].(string)
		if !ok || !packageBaseType(resource["@type"]) {
			continue
		}
		base, err := url.Parse(id)
		if err != nil || !base.IsAbs() || !allowedFeedURL(base) || !strings.EqualFold(base.Scheme, source.Scheme) ||
			!strings.EqualFold(base.Hostname(), source.Hostname()) || effectivePort(base) != effectivePort(source) {
			continue
		}
		return ensureTrailingSlash(base), nil
	}
	return nil, releaseFailure{"NuGet service index has no PackageBaseAddress resource."}
}

func packageBaseType(value any) bool {
	switch typed := value.(type) {
	case string:
		return strings.HasPrefix(typed, "PackageBaseAddress/")
	case []any:
		for _, item := range typed {
			if text, ok := item.(string); ok && strings.HasPrefix(text, "PackageBaseAddress/") {
				return true
			}
		}
	}
	return false
}

func (t NuGetTransport) readVersions(ctx context.Context, base *url.URL, packageID string) ([]stableVersion, error) {
	target, err := base.Parse(strings.ToLower(packageID) + "/index.json")
	if err != nil {
		return nil, err
	}
	document, err := t.getJSON(ctx, target.String(), maxVersionIndexBytes)
	if err != nil {
		return nil, err
	}
	raw, ok := document["versions"].([]any)
	if !ok {
		return nil, releaseFailure{"NuGet flat-container index has no versions array."}
	}
	if len(raw) > maxVersionEntries {
		return nil, releaseFailure{"NuGet flat-container index contains too many versions."}
	}
	seen := map[stableVersion]bool{}
	var versions []stableVersion
	for _, item := range raw {
		if text, ok := item.(string); ok {
			if version, ok := parseStableVersion(text); ok && !seen[version] {
				seen[version] = true
				versions = append(versions, version)
			}
		}
	}
	return versions, nil
}

// catalogFingerprint is clio's ComputeCatalogFingerprint: upper-case SHA-256 of the sorted versions.
func catalogFingerprint(versions []stableVersion) string {
	sorted := append([]stableVersion{}, versions...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].compare(sorted[j]) < 0 })
	parts := make([]string, len(sorted))
	for i, version := range sorted {
		parts[i] = version.String()
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

// selectPackageVersion is clio's SelectCandidateVersion.
func selectPackageVersion(versions []stableVersion, request TransportRequest, catalogChanged bool) (stableVersion, bool) {
	floor, hasFloor := parseStableVersion(request.ActiveRevision)
	var eligible []stableVersion
	for _, version := range versions {
		if request.Rejected[strings.ToLower(version.String())] {
			continue
		}
		if hasFloor && version.compare(floor) <= 0 {
			continue
		}
		eligible = append(eligible, version)
	}
	exactRequested := request.ExactRevision != ""
	if exactRequested {
		exact, ok := parseStableVersion(request.ExactRevision)
		found := false
		for _, version := range versions {
			found = found || version == exact
		}
		if !ok || !found || request.Rejected[strings.ToLower(exact.String())] {
			return stableVersion{}, false
		}
		eligible = []stableVersion{exact}
	}
	highest, hasHighest := parseStableVersion(request.HighestObserved)
	ceiling, hasCeiling := parseStableVersion(request.FallbackCeiling)
	if catalogChanged {
		hasHighest, hasCeiling = false, false
	}
	best, found := stableVersion{}, false
	for _, version := range eligible {
		if exactRequested || !hasHighest || version.compare(highest) > 0 {
			if !found || version.compare(best) > 0 {
				best, found = version, true
			}
		}
	}
	if !found && !exactRequested && hasCeiling {
		for _, version := range eligible {
			if version.compare(ceiling) < 0 && (!found || version.compare(best) > 0) {
				best, found = version, true
			}
		}
	}
	return best, found
}

func (t NuGetTransport) downloadPackage(ctx context.Context, base *url.URL, packageID, version string) ([]byte, error) {
	id := strings.ToLower(packageID)
	target, err := base.Parse(id + "/" + version + "/" + id + "." + version + ".nupkg")
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", "")
	response, err := t.Client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return nil, httpStatusError(response)
	}
	body, err := readBoundedBody(response, maxPackageBytes)
	if err != nil {
		return nil, invalidPackage{err.Error()}
	}
	return body, nil
}

// extractInnerBundle is clio's ExtractInnerBundle with its central-directory bounds.
func extractInnerBundle(packageBytes []byte) ([]byte, error) {
	if err := validatePackageDirectory(packageBytes); err != nil {
		return nil, err
	}
	archive, err := zip.NewReader(bytes.NewReader(packageBytes), int64(len(packageBytes)))
	if err != nil {
		return nil, err
	}
	if len(archive.File) > maxPackageEntries {
		return nil, errors.New("NuGet package contains too many entries.")
	}
	var matches []*zip.File
	for _, entry := range archive.File {
		if entry.Name == innerBundlePath {
			matches = append(matches, entry)
		}
	}
	if len(matches) != 1 || matches[0].UncompressedSize64 == 0 || matches[0].UncompressedSize64 > maxInnerBundleBytes {
		return nil, errors.New("NuGet package must contain exactly one bounded knowledge bundle.")
	}
	reader, err := matches[0].Open()
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	data, err := io.ReadAll(io.LimitReader(reader, maxInnerBundleBytes+1))
	if err != nil {
		return nil, err
	}
	if uint64(len(data)) != matches[0].UncompressedSize64 {
		return nil, errors.New("NuGet package must contain exactly one bounded knowledge bundle.")
	}
	return data, nil
}

func validatePackageDirectory(data []byte) error {
	const minimum = 22
	tailLength := min(len(data), minimum+0xFFFF)
	if tailLength < minimum {
		return errors.New("NuGet package is missing its central directory.")
	}
	tail := data[len(data)-tailLength:]
	for index := len(tail) - minimum; index >= 0; index-- {
		record := tail[index:]
		if binary.LittleEndian.Uint32(record) != 0x06054b50 {
			continue
		}
		commentLength := int(binary.LittleEndian.Uint16(record[20:]))
		if index+minimum+commentLength != len(tail) {
			continue
		}
		disk := binary.LittleEndian.Uint16(record[4:])
		directoryDisk := binary.LittleEndian.Uint16(record[6:])
		onDisk := binary.LittleEndian.Uint16(record[8:])
		total := binary.LittleEndian.Uint16(record[10:])
		size := binary.LittleEndian.Uint32(record[12:])
		offset := binary.LittleEndian.Uint32(record[16:])
		recordOffset := int64(len(data) - tailLength + index)
		if disk != 0 || directoryDisk != 0 || onDisk != total || total == 0xFFFF || size == 0xFFFFFFFF ||
			offset == 0xFFFFFFFF || total > maxPackageEntries || size > maxPackageDirectoryBytes ||
			int64(offset)+int64(size) != recordOffset {
			return errors.New("NuGet package central directory exceeds supported bounds.")
		}
		return nil
	}
	return errors.New("NuGet package is missing its central directory.")
}
