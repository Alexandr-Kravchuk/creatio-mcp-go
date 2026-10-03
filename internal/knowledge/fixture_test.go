package knowledge

import (
	"archive/zip"
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// fixtureLibrary is a test publisher: a fresh P-256 key, trusted only through a configured source.
const fixtureLibrary = "com.example.kb"

type fixtureResource struct {
	itemID, topicID, role, text string
	legacy                      []string
}

type fixtureBundle struct {
	libraryID      string
	version        string
	sequence       uint64
	clioRange      [2]string
	tools          []string
	resources      []fixtureResource
	extraEntry     string // an unsigned file added to the archive
	tamperResource bool   // change one resource byte after the manifest digest was computed
	tamperManifest bool   // change the manifest after signing
}

func defaultFixture() fixtureBundle {
	return fixtureBundle{
		libraryID: fixtureLibrary, version: "1.0.0", sequence: 1000, clioRange: [2]string{"8.1.0", "8.1.999"},
		tools: []string{"get-guidance"},
		resources: []fixtureResource{
			{itemID: "core-rules", topicID: "example.core-rules", role: "guidance", text: "core rules text\n", legacy: []string{"docs://mcp/guides/core-rules"}},
			{itemID: "routing", topicID: "example.routing", role: "guidance", text: "routing text\n"},
			{itemID: "reference.one", topicID: "example.reference-one", role: "reference", text: "reference text\n"},
		},
	}
}

func fixtureKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func publicKeyPEM(t *testing.T, key *ecdsa.PrivateKey) []byte {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
}

// build writes the archive the way clio-knowledge's producer does, with a P1363 (r||s) signature.
func (f fixtureBundle) build(t *testing.T, key *ecdsa.PrivateKey) []byte {
	t.Helper()
	var resources []map[string]any
	var itemIDs, uris []string
	for _, r := range f.resources {
		sum := sha256.Sum256([]byte(r.text))
		uri := namespacedURIPrefix + f.libraryID + "/" + r.itemID
		entry := map[string]any{"itemId": r.itemID, "topicId": r.topicID, "role": r.role, "title": "Title " + r.itemID,
			"description": "Description " + r.itemID, "uri": uri, "path": "resources/" + r.itemID + ".md",
			"mediaType": "text/plain", "length": len(r.text), "digest": hex.EncodeToString(sum[:])}
		if r.legacy != nil {
			entry["legacyUris"] = r.legacy
		}
		resources = append(resources, entry)
		itemIDs = append(itemIDs, r.itemID)
		uris = append(uris, uri)
	}
	manifest := map[string]any{
		"contractVersion": "1.0.0", "bundleSchemaVersion": "1.0.0", "libraryId": f.libraryID, "libraryVersion": f.version,
		"sequence": f.sequence, "source": map[string]any{"repository": "example/kb", "commit": "0123456789abcdef0123456789abcdef01234567"},
		"compatibility": map[string]any{"clio": map[string]any{"min": f.clioRange[0], "max": f.clioRange[1]},
			"mcpToolContract": map[string]any{"min": "1.1.0", "max": "1.1.0"}},
		"requirements": map[string]any{"tools": f.tools, "itemIds": itemIDs, "resourceUris": uris},
		"digestAlg":    "SHA-256", "signature": map[string]any{"algorithm": "ECDSA-P256-SHA256", "keyId": "test-key"},
		"resources": resources,
	}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(manifestBytes)
	r, s, err := ecdsa.Sign(rand.Reader, key, hash[:])
	if err != nil {
		t.Fatal(err)
	}
	signature := make([]byte, 64)
	r.FillBytes(signature[:32])
	s.FillBytes(signature[32:])
	if f.tamperManifest {
		manifestBytes = bytes.Replace(manifestBytes, []byte("Title core-rules"), []byte("Title core-ruleZ"), 1)
	}
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	add := func(name string, data []byte) {
		w, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write(data)
	}
	add("manifest.json", manifestBytes)
	add("manifest.sig", signature)
	for i, r := range f.resources {
		text := r.text
		if f.tamperResource && i == 0 {
			text = "CORE rules text\n"
		}
		add("resources/"+r.itemID+".md", []byte(text))
	}
	if f.extraEntry != "" {
		add(f.extraEntry, []byte("unsigned"))
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

// fixtureHome is a temporary clio home with one configured source trusting the test key.
type fixtureHome struct {
	home, settings, root string
	key                  *ecdsa.PrivateKey
}

func newFixtureHome(t *testing.T, sources map[string]any) *fixtureHome {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir()) // macOS temp dirs sit behind the /var symlink, which clio refuses
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLIO_HOME", home)
	h := &fixtureHome{home: home, settings: filepath.Join(home, "appsettings.json"), root: filepath.Join(home, "knowledge"), key: fixtureKey(t)}
	keyPath := filepath.Join(home, "test-key.pem")
	if err := os.WriteFile(keyPath, publicKeyPEM(t, h.key), 0o600); err != nil {
		t.Fatal(err)
	}
	if sources == nil {
		sources = map[string]any{"example": h.sourceConfig(keyPath, fixtureLibrary, 50)}
	}
	h.writeSettings(t, map[string]any{"knowledge": map[string]any{"root-path": h.root, "sources": sources}})
	return h
}

func (h *fixtureHome) sourceConfig(keyPath, libraryID string, priority int) map[string]any {
	return map[string]any{"library-id": libraryID, "type": "github-release", "location": "https://api.github.com/",
		"repository-owner": "example", "repository-name": "kb", "asset-name": "kb.zip", "enabled": true,
		"priority": priority, "participation": "authoritative", "trusted-key-id": "test-key", "trusted-public-key-path": keyPath}
}

func (h *fixtureHome) writeSettings(t *testing.T, settings map[string]any) {
	t.Helper()
	data, _ := json.MarshalIndent(settings, "", "  ")
	if err := os.WriteFile(h.settings, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// install lays a generation out as clio's installation store does and points current.json at it.
func (h *fixtureHome) install(t *testing.T, alias string, bundles ...[]byte) {
	t.Helper()
	sourceRoot := filepath.Join(h.root, "sources", SourceKey(alias))
	os.MkdirAll(filepath.Join(sourceRoot, "generations"), 0o755)
	os.WriteFile(filepath.Join(h.root, rootOwnerFileName), []byte(rootOwnerContent), 0o600)
	os.WriteFile(filepath.Join(sourceRoot, sourceOwnerFileName), []byte(alias+"\n"), 0o600)
	var pointers []GenerationPointer
	for i, data := range bundles {
		sum := sha256.Sum256(data)
		digest := hex.EncodeToString(sum[:])
		sequence := uint64(1000 + i)
		var manifest struct {
			Sequence       uint64 `json:"sequence"`
			LibraryID      string `json:"libraryId"`
			LibraryVersion string `json:"libraryVersion"`
		}
		if reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data))); err == nil {
			for _, file := range reader.File {
				if file.Name == "manifest.json" {
					rc, _ := file.Open()
					json.NewDecoder(rc).Decode(&manifest)
					rc.Close()
				}
			}
		}
		if manifest.Sequence != 0 {
			sequence = manifest.Sequence
		}
		relative := fmt.Sprintf("generations/%d-%s", sequence, digest[:12])
		os.MkdirAll(filepath.Join(sourceRoot, relative), 0o755)
		os.WriteFile(filepath.Join(sourceRoot, relative, bundleFileName), data, 0o600)
		pointers = append(pointers, GenerationPointer{LibraryID: manifest.LibraryID, LibraryVersion: manifest.LibraryVersion,
			Sequence: sequence, RelativePath: relative, BundleDigest: digest, ResolvedRevision: manifest.LibraryVersion})
	}
	state := CurrentState{SchemaVersion: 1, SourceAlias: alias, Active: pointers[len(pointers)-1]}
	if len(pointers) > 1 {
		state.Previous = &pointers[len(pointers)-2]
	}
	data, _ := json.Marshal(state)
	if err := os.WriteFile(filepath.Join(sourceRoot, currentFileName), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func testRuntime() *Runtime {
	return NewRuntime(Capabilities{ClioVersion: Version{8, 1, 0}, McpToolContractVersion: Version{1, 1, 0}, Tools: map[string]bool{"get-guidance": true}})
}

func TestFixtureBundleActivates(t *testing.T) {
	h := newFixtureHome(t, nil)
	h.install(t, "example", defaultFixture().build(t, h.key))
	runtime := testRuntime()
	lookup := runtime.FindByName("core-rules")
	if lookup.Status != LookupActive {
		t.Fatalf("status %v: %s", lookup.Status, runtime.LastDiagnostic())
	}
	if lookup.Article.Text != "core rules text\n" || lookup.Provenance.SourceAlias != "example" || lookup.Provenance.Sequence != 1000 {
		t.Fatalf("unexpected lookup %+v %+v", lookup.Article, lookup.Provenance)
	}
	if got := runtime.FindByURI("docs://mcp/guides/core-rules"); got.Status != LookupActive || got.Article.ItemID != "core-rules" {
		t.Fatalf("legacy URI did not resolve: %+v", got)
	}
	if got := runtime.FindByName("example.routing"); got.Status != LookupActive || got.Article.ItemID != "routing" {
		t.Fatalf("topic ID did not resolve: %+v", got)
	}
	if got := runtime.FindByName("reference.one"); got.Status != LookupNotFound {
		t.Fatalf("a reference article must not resolve by name, got %v", got.Status)
	}
	if names := runtime.GuideNames(); fmt.Sprint(names) != "[core-rules routing]" {
		t.Fatalf("guide names %v", names)
	}
	if catalog := runtime.Catalog(); len(catalog) != 3 {
		t.Fatalf("catalog has %d entries", len(catalog))
	}
}

// TestRefusedBundles proves every trust and integrity rule refuses the bundle as clio does.
func TestRefusedBundles(t *testing.T) {
	cases := []struct {
		name   string
		modify func(*fixtureBundle)
		code   RejectionCode
		wrong  bool // signed by a different key than the configured one
	}{
		{"tampered resource", func(f *fixtureBundle) { f.tamperResource = true }, RejectInvalidContent, false},
		{"tampered manifest", func(f *fixtureBundle) { f.tamperManifest = true }, RejectInvalidSignature, false},
		{"foreign signing key", func(*fixtureBundle) {}, RejectInvalidSignature, true},
		{"unsigned extra entry", func(f *fixtureBundle) { f.extraEntry = "resources/extra.md" }, RejectInvalidContent, false},
		{"incompatible clio range", func(f *fixtureBundle) { f.clioRange = [2]string{"9.0.0", "9.0.999"} }, RejectIncompatible, false},
		{"missing tool capability", func(f *fixtureBundle) { f.tools = []string{"get-guidance", "manage-user"} }, RejectMissingCapability, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newFixtureHome(t, nil)
			fixture := defaultFixture()
			c.modify(&fixture)
			signer := h.key
			if c.wrong {
				signer = fixtureKey(t)
			}
			data := fixture.build(t, signer)
			runtime := testRuntime()
			if _, rejection := runtime.Verify(data, "", fixtureLibrary); rejection == nil || rejection.Code != c.code {
				t.Fatalf("rejection = %+v, want %s", rejection, c.code)
			}
			h.install(t, "example", data)
			if lookup := runtime.FindByName("core-rules"); lookup.Status != LookupUnavailable {
				t.Fatalf("a refused bundle was served: %v", lookup.Status)
			}
			if runtime.LastDiagnostic() == "" {
				t.Fatal("no diagnostic for the refused bundle")
			}
		})
	}
}

// TestPinnedLibraryCannotBeReKeyed: a bundle claiming the built-in library, signed by a key a settings
// entry trusts, is still refused, because the built-in library is verified only by the pinned key.
func TestPinnedLibraryCannotBeReKeyed(t *testing.T) {
	h := newFixtureHome(t, nil)
	keyPath := filepath.Join(h.home, "test-key.pem")
	h.writeSettings(t, map[string]any{"knowledge": map[string]any{"root-path": h.root, "sources": map[string]any{
		CuratedAlias: h.sourceConfig(keyPath, CuratedLibraryID, 100)}}})
	fixture := defaultFixture()
	fixture.libraryID = CuratedLibraryID
	data := fixture.build(t, h.key)
	runtime := testRuntime()
	if _, rejection := runtime.Verify(data, "", CuratedLibraryID); rejection == nil || rejection.Code != RejectUntrustedKey {
		t.Fatalf("rejection = %+v, want UntrustedKey", rejection)
	}
}

func TestActivationDigestMismatch(t *testing.T) {
	h := newFixtureHome(t, nil)
	h.install(t, "example", defaultFixture().build(t, h.key))
	matches, _ := filepath.Glob(filepath.Join(h.root, "sources", SourceKey("example"), "generations", "*", bundleFileName))
	other := defaultFixture()
	other.version = "1.0.0"
	os.WriteFile(matches[0], other.build(t, h.key), 0o600) // a different archive under the recorded digest
	runtime := testRuntime()
	if lookup := runtime.FindByName("core-rules"); lookup.Status != LookupUnavailable {
		t.Fatalf("status %v", lookup.Status)
	}
	if diagnostic := runtime.LastDiagnostic(); !bytes.Contains([]byte(diagnostic), []byte("does not match its activation digest")) {
		t.Fatalf("diagnostic %q", diagnostic)
	}
}

func TestFallsBackToPreviousGeneration(t *testing.T) {
	h := newFixtureHome(t, nil)
	good := defaultFixture().build(t, h.key)
	bad := defaultFixture()
	bad.sequence, bad.version, bad.tamperResource = 1001, "1.0.1", true
	h.install(t, "example", good, bad.build(t, h.key))
	runtime := testRuntime()
	lookup := runtime.FindByName("core-rules")
	if lookup.Status != LookupActive || lookup.Provenance.Sequence != 1000 {
		t.Fatalf("expected the previous generation, got %v %+v", lookup.Status, lookup.Provenance)
	}
	if runtime.LastDiagnostic() == "" {
		t.Fatal("the rejected active generation left no diagnostic")
	}
}

func TestPriorityPinsAndAmbiguity(t *testing.T) {
	h := newFixtureHome(t, nil)
	keyPath := filepath.Join(h.home, "test-key.pem")
	second := defaultFixture()
	second.libraryID = "com.example.other"
	for i := range second.resources {
		second.resources[i].legacy = nil
	}
	write := func(priorityB int, pins map[string]string) {
		h.writeSettings(t, map[string]any{"knowledge": map[string]any{"root-path": h.root, "topic-pins": pins, "sources": map[string]any{
			"example": h.sourceConfig(keyPath, fixtureLibrary, 50),
			"other":   h.sourceConfig(keyPath, "com.example.other", priorityB)}}})
	}
	write(50, map[string]string{})
	h.install(t, "example", defaultFixture().build(t, h.key))
	h.install(t, "other", second.build(t, h.key))
	runtime := testRuntime()
	if lookup := runtime.FindByName("routing"); lookup.Status != LookupAmbiguous {
		t.Fatalf("equal priorities must be ambiguous, got %v", lookup.Status)
	}
	write(50, map[string]string{"example.routing": "com.example.other"})
	if lookup := NewRuntimeForTest().FindByName("routing"); lookup.Status != LookupActive || lookup.Provenance.LibraryID != "com.example.other" {
		t.Fatalf("pin was not honored: %v %+v", lookup.Status, lookup.Provenance)
	}
	write(60, map[string]string{})
	if lookup := NewRuntimeForTest().FindByName("routing"); lookup.Status != LookupActive || lookup.Provenance.LibraryID != "com.example.other" {
		t.Fatalf("higher priority did not win: %v", lookup.Status)
	}
	if lookup := NewRuntimeForTest().FindByURI("docs://knowledge/com.example.kb/routing"); lookup.Status != LookupActive || lookup.Provenance.LibraryID != fixtureLibrary {
		t.Fatalf("namespaced URI must select its library exactly: %v", lookup.Status)
	}
}

func NewRuntimeForTest() *Runtime { return testRuntime() }

func TestReferenceExampleYAML(t *testing.T) {
	text := "schemaVersion: 0\nid: example.ref\ntitle: Example reference\nstatus: published\nprimaryUseCase:\n  id: use-case\n  summary: Do a thing.\n" +
		"source:\n  repository: https://github.com/example/ref\n  revision: 0123456789abcdef0123456789abcdef01234567\n  defaultBranch: main\n" +
		"entryPoints:\n  overview: README.md\n  package: packages/Example\nsupportingCapabilities:\n  - unit-testing\n  - alpha\n" +
		"compatibility:\n  status: example-declared\n  details: \"See the README.\"\ntrust:\n  publisher: Example Org # comment\n  level: published\nnotes:\n  - 'It''s a note.'\n"
	doc, err := parseExampleYAML(text)
	if err != nil {
		t.Fatal(err)
	}
	example, problem := mapExample(RoleArticle{Article: Article{ItemID: "example.ref"}}, doc)
	if problem != "" {
		t.Fatal(problem)
	}
	if example.Trust.Publisher != "Example Org" || example.Notes[0] != "It's a note." || example.SupportingCapabilities[0] != "alpha" ||
		example.EntryPoints["package"] != "packages/Example" || example.Compatibility.Details != "See the README." {
		t.Fatalf("unexpected mapping %+v", example)
	}
	if _, err := parseExampleYAML("id: x\nunknownKey: y\n"); err == nil {
		t.Fatal("an undeclared key must be refused")
	}
	if _, err := parseExampleYAML("id: [a, b]\n"); err == nil {
		t.Fatal("a flow collection must be refused")
	}
}
