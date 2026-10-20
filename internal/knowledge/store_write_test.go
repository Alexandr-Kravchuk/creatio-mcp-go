package knowledge

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func publication(alias, library, version string, sequence uint64, data []byte) Publication {
	return Publication{SourceAlias: alias, LibraryID: library, LibraryVersion: version, Sequence: sequence,
		TransportType: "github-release", Location: "https://api.github.com/", ResolvedRevision: version, BundleBytes: data}
}

func storeFixture(t *testing.T) (Store, func(version string, sequence uint64) []byte) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	key := fixtureKey(t)
	build := func(version string, sequence uint64) []byte {
		bundle := defaultFixture()
		bundle.version, bundle.sequence = version, sequence
		return bundle.build(t, key)
	}
	return Store{Root: filepath.Join(root, "knowledge")}, build
}

func readJSONFile(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return value
}

// TestPublishWritesClioLayout checks every file clio's reader and clio's next publication rely on.
func TestPublishWritesClioLayout(t *testing.T) {
	store, build := storeFixture(t)
	first := build("1.0.0", 1000)
	result, err := store.Publish(publication("example", fixtureLibrary, "1.0.0", 1000, first))
	if err != nil || result.Status != StatusInstalled {
		t.Fatalf("publish: %+v %v", result, err)
	}
	sourceRoot := store.SourceRoot("example")
	if owner, _ := os.ReadFile(filepath.Join(sourceRoot, sourceOwnerFileName)); string(owner) != "example\n" {
		t.Fatalf("source owner marker %q", owner)
	}
	if owner, _ := os.ReadFile(filepath.Join(store.Root, rootOwnerFileName)); string(owner) != rootOwnerContent {
		t.Fatalf("root owner marker %q", owner)
	}
	digest := computeDigest(first)
	generation := filepath.Join(sourceRoot, "generations", "1000-"+digest[:12])
	for _, name := range []string{"bundle.zip", "install.json", "manifest.json", "manifest.sig", "resources/core-rules.md"} {
		if _, err := os.Stat(filepath.Join(generation, filepath.FromSlash(name))); err != nil {
			t.Fatalf("generation lacks %s: %v", name, err)
		}
	}
	current := readJSONFile(t, filepath.Join(sourceRoot, currentFileName))
	if current["previous"] != nil || current["sourceAlias"] != "example" {
		t.Fatalf("current.json %v", current)
	}
	active := current["active"].(map[string]any)
	if active["relativePath"] != "generations/1000-"+digest[:12] || active["bundleDigest"] != digest ||
		!strings.HasSuffix(active["activatedAtUtc"].(string), "+00:00") {
		t.Fatalf("active pointer %v", active)
	}
	mark := readJSONFile(t, filepath.Join(store.Root, "sources", ".history", SourceKey(fixtureLibrary)+".json"))
	if mark["sequence"] != float64(1000) || mark["bundleDigest"] != digest {
		t.Fatalf("high-water mark %v", mark)
	}
	metadata := readJSONFile(t, filepath.Join(generation, metadataFileName))
	if metadata["transportType"] != "github-release" || metadata["resolvedRevision"] != "1.0.0" {
		t.Fatalf("install.json %v", metadata)
	}
	// The reader side accepts the generation exactly as it accepts one clio wrote.
	state, diagnostic := store.ReadCurrent("example")
	if state == nil || diagnostic != "" {
		t.Fatalf("reader: %v %s", state, diagnostic)
	}
	if _, _, failure := store.ReadCandidate("example", state.Active); failure != "" {
		t.Fatal(failure)
	}
	if staged, _ := os.ReadDir(filepath.Join(sourceRoot, stagingDirectory)); len(staged) != 0 {
		t.Fatalf("staging kept %d entries", len(staged))
	}

	// Two updates later only the active and the previous generation remain.
	for i, version := range []string{"1.1.0", "1.2.0"} {
		state, _ := store.ReadCurrentForWrite("example")
		active := state.Active
		next := publication("example", fixtureLibrary, version, uint64(1001+i), build(version, uint64(1001+i)))
		next.IsUpdate, next.ExpectedActive = true, &active
		if result, err := store.Publish(next); err != nil || result.Status != StatusUpdated {
			t.Fatalf("update %s: %+v %v", version, result, err)
		}
	}
	generations, _ := os.ReadDir(filepath.Join(sourceRoot, "generations"))
	if len(generations) != 2 {
		t.Fatalf("prune kept %d generations", len(generations))
	}
	state, _ = store.ReadCurrent("example")
	if state.Active.Sequence != 1002 || state.Previous == nil || state.Previous.Sequence != 1001 {
		t.Fatalf("after updates: %+v", state)
	}
}

func TestPublishRefusesReplayAndStaleUpdates(t *testing.T) {
	store, build := storeFixture(t)
	original := build("1.0.0", 1000)
	if _, err := store.Publish(publication("example", fixtureLibrary, "1.0.0", 1000, original)); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name    string
		modify  func(*Publication)
		status  InstallationStatus
		message string
	}{
		{"same sequence and digest", func(p *Publication) { p.BundleBytes = original }, StatusAlreadyInstalled, "sequence 1000 is already installed"},
		{"same sequence, other content", func(p *Publication) { p.BundleBytes = build("1.0.0", 1000) }, StatusRejected,
			"Knowledge library 'com.example.kb' rejected sequence 1000; highest accepted sequence is 1000."},
		{"lower sequence", func(p *Publication) { p.Sequence = 999; p.BundleBytes = build("0.9.0", 999) }, StatusRejected, "highest accepted sequence is 1000"},
		{"install over an installed source", func(p *Publication) { p.Sequence = 1001; p.BundleBytes = build("1.1.0", 1001) }, StatusFailed,
			"is already installed; use update-knowledge."},
		{"update with a stale expectation", func(p *Publication) {
			p.Sequence, p.BundleBytes, p.IsUpdate = 1001, build("1.1.0", 1001), true
			p.ExpectedActive = &GenerationPointer{LibraryID: fixtureLibrary, Sequence: 1}
		}, StatusFailed, "changed while the operation was in progress; retry."},
	}
	for _, tc := range cases {
		p := publication("example", fixtureLibrary, "1.0.0", 1000, nil)
		tc.modify(&p)
		result, err := store.Publish(p)
		if err != nil || result.Status != tc.status || !strings.Contains(result.Message, tc.message) {
			t.Errorf("%s: %+v %v", tc.name, result, err)
		}
	}
}

// TestInterruptedPublicationIsReconciledByWritersOnly simulates a crash between the high-water mark and
// current.json: the reader reports the lag and leaves it, a lifecycle operation repairs it like clio.
func TestInterruptedPublicationIsReconciledByWritersOnly(t *testing.T) {
	store, build := storeFixture(t)
	if _, err := store.Publish(publication("example", fixtureLibrary, "1.0.0", 1000, build("1.0.0", 1000))); err != nil {
		t.Fatal(err)
	}
	sourceRoot := store.SourceRoot("example")
	before, _ := os.ReadFile(filepath.Join(sourceRoot, currentFileName))
	state, _ := store.ReadCurrentForWrite("example")
	active := state.Active
	next := publication("example", fixtureLibrary, "1.1.0", 1001, build("1.1.0", 1001))
	next.IsUpdate, next.ExpectedActive = true, &active
	if _, err := store.Publish(next); err != nil {
		t.Fatal(err)
	}
	// Put the old marker back: high-water mark and generation exist, current.json lags.
	if err := os.WriteFile(filepath.Join(sourceRoot, currentFileName), before, 0o600); err != nil {
		t.Fatal(err)
	}
	if state, diagnostic := store.ReadCurrent("example"); state != nil || !strings.Contains(diagnostic, "conflicts with accepted library sequence 1001") {
		t.Fatalf("reader: %+v %q", state, diagnostic)
	}
	if after, _ := os.ReadFile(filepath.Join(sourceRoot, currentFileName)); string(after) != string(before) {
		t.Fatal("the reader rewrote current.json")
	}
	recovered, diagnostic := store.ReadCurrentForWrite("example")
	if recovered == nil || diagnostic != "" || recovered.Active.Sequence != 1001 || recovered.Previous.Sequence != 1000 {
		t.Fatalf("writer did not reconcile: %+v %q", recovered, diagnostic)
	}
}

func TestOrphanGenerationIsRecoveredOnlyWhenExact(t *testing.T) {
	store, build := storeFixture(t)
	data := build("1.0.0", 1000)
	if _, err := store.Publish(publication("example", fixtureLibrary, "1.0.0", 1000, data)); err != nil {
		t.Fatal(err)
	}
	sourceRoot := store.SourceRoot("example")
	generation := filepath.Join(sourceRoot, "generations", "1000-"+computeDigest(data)[:12])
	// Detach the generation from current.json and the history, as a crash before activation would.
	_ = os.Remove(filepath.Join(sourceRoot, currentFileName))
	_ = os.RemoveAll(filepath.Join(store.Root, "sources", ".history"))
	if result, err := store.Publish(publication("example", fixtureLibrary, "1.0.0", 1000, data)); err != nil || result.Status != StatusInstalled {
		t.Fatalf("exact orphan: %+v %v", result, err)
	}
	_ = os.Remove(filepath.Join(sourceRoot, currentFileName))
	_ = os.RemoveAll(filepath.Join(store.Root, "sources", ".history"))
	other := publication("example", fixtureLibrary, "1.0.0", 1000, data)
	other.Location = "https://elsewhere.example/"
	if result, _ := store.Publish(other); result.Status != StatusFailed || !strings.Contains(result.Message, "already exists with unexpected content") {
		t.Fatalf("foreign orphan: %+v", result)
	}
	if _, err := os.Stat(generation); err != nil {
		t.Fatal("a foreign orphan must be left in place")
	}
}

func TestDeleteQuarantinesTheSource(t *testing.T) {
	store, build := storeFixture(t)
	if _, err := store.Publish(publication("example", fixtureLibrary, "1.0.0", 1000, build("1.0.0", 1000))); err != nil {
		t.Fatal(err)
	}
	if result, _ := store.Delete("example", false); result.Status != StatusConfirmationRequired {
		t.Fatalf("unconfirmed: %+v", result)
	}
	abandoned := filepath.Join(store.Root, "sources", quarantinePrefix+SourceKey("example")+"-old")
	if err := os.MkdirAll(abandoned, 0o755); err != nil {
		t.Fatal(err)
	}
	result, err := store.Delete("example", true)
	if err != nil || result.Status != StatusDeleted || result.Message != "Installed knowledge for source 'example' was deleted." {
		t.Fatalf("delete: %+v %v", result, err)
	}
	entries, _ := os.ReadDir(filepath.Join(store.Root, "sources"))
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), quarantinePrefix) || entry.Name() == SourceKey("example") {
			t.Fatalf("left behind %s", entry.Name())
		}
	}
	if result, _ := store.Delete("example", true); result.Status != StatusNotInstalled {
		t.Fatalf("second delete: %+v", result)
	}
}

// TestLockHelperProcess holds a lock file for another test; it does nothing when run normally.
func TestLockHelperProcess(t *testing.T) {
	path := os.Getenv("KNOWLEDGE_LOCK_HELPER")
	if path == "" {
		t.Skip("helper process only")
	}
	lock, err := acquireFileLock(path, time.Second, 10*time.Millisecond)
	if err != nil {
		fmt.Println("lock failed:", err)
		os.Exit(2)
	}
	fmt.Println("locked")
	time.Sleep(1500 * time.Millisecond)
	lock.Unlock()
	os.Exit(0)
}

// TestLockExcludesAnotherProcess proves the lock is the operating system's, not only this process's: a
// second process holding the file keeps this one out until it lets go.
func TestLockExcludesAnotherProcess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.lock")
	helper := exec.Command(os.Args[0], "-test.run=^TestLockHelperProcess$")
	helper.Env = append(os.Environ(), "KNOWLEDGE_LOCK_HELPER="+path)
	output, err := helper.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := helper.Start(); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 64)
	if n, _ := output.Read(buffer); !strings.Contains(string(buffer[:n]), "locked") {
		t.Fatalf("helper did not lock: %q", buffer[:n])
	}
	if _, err := tryFileLock(path); err == nil {
		t.Fatal("the lock held by another process was granted")
	}
	started := time.Now()
	lock, err := acquireFileLock(path, 10*time.Second, 25*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	lock.Unlock()
	if waited := time.Since(started); waited < 500*time.Millisecond {
		t.Fatalf("acquired after %v while the helper still held the lock", waited)
	}
	_ = helper.Wait()
}

func TestManagerLifecycleMessages(t *testing.T) {
	h := newConcurrencyHome(t)
	ctx := context.Background()
	alpha := "alpha"
	steps := []struct {
		name    string
		run     func() (BatchResult, error)
		status  string
		message string
	}{
		{"update before install", func() (BatchResult, error) { return h.manager.Update(ctx, &alpha) }, "failed", "Knowledge source 'alpha' is not installed; use install-knowledge."},
		{"install", func() (BatchResult, error) { return h.manager.Install(ctx, &alpha) }, "installed", "Knowledge source 'alpha' sequence 1000 was installed at "},
		{"install again", func() (BatchResult, error) { return h.manager.Install(ctx, &alpha) }, "already-installed", "Knowledge source 'alpha' sequence 1000 is already installed."},
		{"update at latest", func() (BatchResult, error) { return h.manager.Update(ctx, &alpha) }, "up-to-date", "Knowledge source 'alpha' is up to date at 1.0.0."},
		{"update after a release", func() (BatchResult, error) { h.publisher.bump("alpha"); return h.manager.Update(ctx, &alpha) }, "updated", "sequence 1001 was installed at "},
		{"delete unconfirmed", func() (BatchResult, error) { return h.manager.Delete(ctx, &alpha, false) }, "confirmationrequired", "requires explicit confirmation"},
		{"delete", func() (BatchResult, error) { return h.manager.Delete(ctx, &alpha, true) }, "deleted", "Installed knowledge for source 'alpha' was deleted."},
	}
	for _, step := range steps {
		result, err := step.run()
		if err != nil || len(result.Sources) != 1 || result.Sources[0].Status != step.status || !strings.Contains(result.Sources[0].Message, step.message) {
			t.Fatalf("%s: %+v %v", step.name, result, err)
		}
	}
	missing := "nope"
	if result, _ := h.manager.Install(ctx, &missing); result.Success || result.Message != "Knowledge source 'nope' is not configured." {
		t.Fatalf("unknown alias: %+v", result)
	}
	if result := h.manager.SetEnabled("beta", false); !result.Success || result.Message != "Knowledge source 'beta' was disabled; its cache was retained." {
		t.Fatalf("disable: %+v", result)
	}
	beta := "beta"
	if result, _ := h.manager.Install(ctx, &beta); result.Message != "Knowledge source 'beta' is disabled." {
		t.Fatalf("install disabled: %+v", result)
	}
	if result, _ := h.manager.Remove(CuratedAlias, true); result.Success {
		t.Fatalf("the curated source must not be removable: %+v", result)
	}
	if result, _ := h.manager.Remove("beta", false); result.Message != "Removing a knowledge source requires explicit confirmation." {
		t.Fatalf("remove unconfirmed: %+v", result)
	}
	if result, _ := h.manager.Remove("beta", true); !result.Success || result.Message != "Knowledge source 'beta' was removed." {
		t.Fatalf("remove: %+v", result)
	}
	add := h.manager.Add(AddRequest{Alias: "gamma", LibraryID: "com.example.alpha", Type: "github-release", Location: "https://api.github.com",
		RepositoryOwner: ptr("o"), RepositoryName: ptr("r"), AssetName: ptr("g.zip"), Enabled: true, Participation: "supplement"})
	if add.Success || add.Message != "Knowledge source alias 'gamma' or library 'com.example.alpha' is already configured." {
		t.Fatalf("duplicate library: %+v", add)
	}
	add = h.manager.Add(AddRequest{Alias: "gamma", LibraryID: "com.example.gamma", Type: "Git", Location: "https://example.com/kb.git",
		Branch: ptr("  main "), Enabled: true, Priority: 3, Participation: "Isolated"})
	if !add.Success {
		t.Fatalf("add: %+v", add)
	}
	settings, _ := ReadSettings(h.settings)
	if source := settings.Knowledge.Sources["gamma"]; source.Branch != "main" || source.Participation != ParticipationIsolated || source.Type != SourceGit {
		t.Fatalf("persisted %+v", source)
	}
	gamma := "gamma"
	if result, _ := h.manager.Update(ctx, &gamma); result.Sources[0].Message != "Knowledge source 'gamma' is not installed; use install-knowledge." {
		t.Fatalf("git update: %+v", result)
	}
}
