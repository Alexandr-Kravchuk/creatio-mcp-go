package knowledge

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func releaseServer(t *testing.T, release func(base string) map[string]any, asset func(w http.ResponseWriter, r *http.Request)) *httptest.Server {
	t.Helper()
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/releases/latest") || strings.Contains(r.URL.Path, "/releases/tags/"):
			if r.Header.Get("User-Agent") != "clio-knowledge-client" || r.Header.Get("X-GitHub-Api-Version") != "2022-11-28" {
				t.Errorf("release request headers %v", r.Header)
			}
			if r.Header.Get("If-None-Match") == `"same"` {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("ETag", `"same"`)
			_ = json.NewEncoder(w).Encode(release(server.URL))
		default:
			asset(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func releaseSourceConfig(location string) SourceConfig {
	return SourceConfig{LibraryID: fixtureLibrary, Type: SourceGitHubRelease, Location: location + "/", RepositoryOwner: "o",
		RepositoryName: "r", AssetName: "kb.zip", Enabled: true, Participation: ParticipationSupplement}
}

func TestGitHubReleaseTransport(t *testing.T) {
	data := []byte("PK-bundle")
	digest := "sha256:" + computeDigest(data)
	release := func(mutate func(map[string]any, map[string]any)) func(string) map[string]any {
		return func(base string) map[string]any {
			asset := map[string]any{"name": "kb.zip", "state": "uploaded", "content_type": "application/zip", "size": len(data),
				"digest": digest, "url": base + "/asset"}
			value := map[string]any{"tag_name": "1.2.3", "draft": false, "prerelease": false, "immutable": true, "assets": []any{asset}}
			if mutate != nil {
				mutate(value, asset)
			}
			return value
		}
	}
	serveAsset := func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "/asset", http.StatusFound)
			return
		}
		_, _ = w.Write(data)
	}
	cases := []struct {
		name       string
		release    func(map[string]any, map[string]any)
		request    func(*TransportRequest)
		status     TransportStatus
		diagnostic string
	}{
		{"downloads a verified asset", nil, nil, TransportDownloaded, ""},
		{"warns about a mutable release", func(r, _ map[string]any) { r["immutable"] = false }, nil, TransportDownloaded,
			"The release is not marked immutable; its assets could still be replaced upstream."},
		{"follows an allowed redirect", func(_, a map[string]any) { a["url"] = strings.Replace(a["url"].(string), "/asset", "/redirect", 1) }, nil, TransportDownloaded, ""},
		{"refuses a draft", func(r, _ map[string]any) { r["draft"] = true }, nil, TransportFailed, "The selected GitHub release is a draft or prerelease."},
		{"refuses a non-semver tag", func(r, _ map[string]any) { r["tag_name"] = "v1.2.3" }, nil, TransportFailed, "A knowledge release tag must be an exact MAJOR.MINOR.PATCH library version."},
		{"refuses a missing asset", func(_, a map[string]any) { a["name"] = "other.zip" }, nil, TransportFailed, "The GitHub release must expose exactly one 'kb.zip' asset."},
		{"refuses a foreign download host", func(_, a map[string]any) { a["url"] = "https://evil.example/asset" }, nil, TransportFailed,
			"The selected release asset download URL is not an allowed GitHub host."},
		{"refuses a missing digest", func(_, a map[string]any) { delete(a, "digest") }, nil, TransportFailed, "does not publish a well-formed SHA-256 digest"},
		{"rejects a digest mismatch", func(_, a map[string]any) { a["digest"] = "sha256:" + strings.Repeat("0", 64) }, nil, TransportRejected,
			"The downloaded release asset does not match the digest GitHub published for it."},
		{"skips the active revision", nil, func(r *TransportRequest) { r.ActiveRevision = "1.2.3" }, TransportNoCandidate, ""},
		{"skips a rejected revision", nil, func(r *TransportRequest) { r.Rejected["1.2.3"] = true }, TransportNoCandidate, ""},
		{"stops on an unchanged ETag", nil, func(r *TransportRequest) { r.CatalogFingerprint = `"same"` }, TransportNoCandidate, ""},
	}
	for _, tc := range cases {
		server := releaseServer(t, release(tc.release), serveAsset)
		request := TransportRequest{Alias: "example", Source: releaseSourceConfig(server.URL), Rejected: map[string]bool{}}
		if tc.request != nil {
			tc.request(&request)
		}
		result := GitHubReleaseTransport{Client: NewTransportClient()}.Retrieve(context.Background(), request)
		if result.Status != tc.status || !strings.Contains(result.Diagnostic, tc.diagnostic) {
			t.Errorf("%s: status %v diagnostic %q", tc.name, result.Status, result.Diagnostic)
		}
		if tc.status == TransportDownloaded && (!bytes.Equal(result.CandidateBytes, data) || result.ResolvedRevision != "1.2.3") {
			t.Errorf("%s: %+v", tc.name, result)
		}
	}
	rateLimited := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusForbidden) }))
	defer rateLimited.Close()
	result := GitHubReleaseTransport{Client: NewTransportClient()}.Retrieve(context.Background(),
		TransportRequest{Source: releaseSourceConfig(rateLimited.URL), Rejected: map[string]bool{}})
	if result.Status != TransportFailed || result.Diagnostic != "GitHub refused the release metadata request (rate limit or access); no retry was attempted." {
		t.Errorf("rate limit: %+v", result)
	}
}

func nugetPackage(t *testing.T, bundle []byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for name, content := range map[string][]byte{"example.nuspec": []byte("<package/>"), innerBundlePath: bundle} {
		entry, _ := writer.Create(name)
		_, _ = entry.Write(content)
	}
	_ = writer.Close()
	return buffer.Bytes()
}

func TestNuGetTransportSelectsLikeClio(t *testing.T) {
	versions := []string{"1.0.0", "1.2.0", "1.10.0", "2.0.0-beta", "garbage"}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v3/index.json":
			_ = json.NewEncoder(w).Encode(map[string]any{"resources": []any{
				map[string]any{"@id": "https://other.example/flat/", "@type": "PackageBaseAddress/3.0.0"},
				map[string]any{"@id": server.URL + "/flat/", "@type": []any{"PackageBaseAddress/3.0.0"}}}})
		case r.URL.Path == "/flat/example.kb/index.json":
			_ = json.NewEncoder(w).Encode(map[string]any{"versions": versions})
		case strings.HasSuffix(r.URL.Path, ".nupkg"):
			version := strings.Split(r.URL.Path, "/")[3]
			_, _ = w.Write(nugetPackage(t, []byte("bundle-"+version)))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	source := SourceConfig{LibraryID: fixtureLibrary, Type: SourceNuGet, Location: server.URL + "/v3/index.json", PackageID: "Example.KB"}
	retrieve := func(request TransportRequest) TransportResult {
		request.Source = source
		if request.Rejected == nil {
			request.Rejected = map[string]bool{}
		}
		return NuGetTransport{Client: NewTransportClient()}.Retrieve(context.Background(), request)
	}
	latest := retrieve(TransportRequest{})
	if latest.Status != TransportDownloaded || latest.ResolvedRevision != "1.10.0" || string(latest.CandidateBytes) != "bundle-1.10.0" {
		t.Fatalf("latest: %+v", latest)
	}
	if result := retrieve(TransportRequest{ActiveRevision: "1.10.0"}); result.Status != TransportNoCandidate {
		t.Fatalf("at the latest: %+v", result)
	}
	if result := retrieve(TransportRequest{Rejected: map[string]bool{"1.10.0": true}, HighestObserved: "1.10.0", FallbackCeiling: "1.10.0",
		CatalogFingerprint: latest.CatalogFingerprint}); result.ResolvedRevision != "1.2.0" {
		t.Fatalf("fallback below the rejected candidate: %+v", result)
	}
	if result := retrieve(TransportRequest{ExactRevision: "1.0.0"}); result.ResolvedRevision != "1.0.0" {
		t.Fatalf("exact revision: %+v", result)
	}
	if latest.CatalogFingerprint != strings.ToUpper(latest.CatalogFingerprint) || len(latest.CatalogFingerprint) != 64 {
		t.Fatalf("fingerprint %q", latest.CatalogFingerprint)
	}
	if _, err := extractInnerBundle([]byte("not a zip")); err == nil {
		t.Fatal("a broken package must be refused")
	}
}

// TestManagerRejectsAnUnverifiedCandidate shows a downloaded bundle is published only after verification.
func TestManagerRejectsAnUnverifiedCandidate(t *testing.T) {
	h := newConcurrencyHome(t)
	h.manager.Verify = func([]byte, string, string) (*PreparedBundle, *Rejection) {
		return nil, &Rejection{Code: RejectMalformed, Message: "Knowledge bundle signature is invalid."}
	}
	alpha := "alpha"
	result, _ := h.manager.Install(context.Background(), &alpha)
	if result.Sources[0].Status != stateRejected ||
		!strings.HasPrefix(result.Sources[0].Message, "No compatible knowledge candidate was found after rejecting 1.0.0: [untrusted-source-text begin] Knowledge bundle signature is invalid.") {
		t.Fatalf("%+v", result)
	}
	if state, _ := (Store{Root: h.root}).ReadCurrent("alpha"); state != nil {
		t.Fatal("an unverified bundle was published")
	}
}
