package knowledge

// The concurrency proof for the write side. Several writers mutate one clio home at the same time: Go
// goroutines through this package and, when CLIO_DLL names a clio 8.1.0.142 clio.dll, real clio processes
// through their CLI verbs. Readers watch the store and appsettings.json while it happens. The test passes
// only if no reader ever sees a torn file or an activation marker whose generation is missing, no settings
// update is lost, and the quiescent store satisfies every invariant clio's own reader relies on.

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// publisher is a loopback GitHub REST origin serving signed releases of several libraries; the latest
// version of each library can be moved while the test runs.
type publisher struct {
	server   *httptest.Server
	mu       sync.Mutex
	releases map[string][][]byte // repository -> bundles by version index
	latest   map[string]int
}

func newPublisher(t *testing.T, key *ecdsa.PrivateKey, libraries map[string]string, versions int) *publisher {
	p := &publisher{releases: map[string][][]byte{}, latest: map[string]int{}}
	for repository, library := range libraries {
		for index := 0; index < versions; index++ {
			bundle := defaultFixture()
			bundle.libraryID = library
			bundle.version = fmt.Sprintf("1.%d.0", index)
			bundle.sequence = uint64(1000 + index)
			p.releases[repository] = append(p.releases[repository], bundle.build(t, key))
		}
	}
	p.server = httptest.NewServer(http.HandlerFunc(p.serve))
	t.Cleanup(p.server.Close)
	return p
}

func (p *publisher) bump(repository string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.latest[repository] < len(p.releases[repository])-1 {
		p.latest[repository]++
	}
}

func (p *publisher) serve(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	p.mu.Lock()
	defer p.mu.Unlock()
	switch {
	case (len(parts) == 5 && parts[0] == "repos" && parts[4] == "latest") ||
		(len(parts) == 6 && parts[0] == "repos" && parts[3] == "releases" && parts[4] == "tags"):
		bundles, ok := p.releases[parts[2]]
		if !ok {
			http.NotFound(w, r)
			return
		}
		index := p.latest[parts[2]]
		if len(parts) == 6 {
			if _, err := fmt.Sscanf(parts[5], "1.%d.0", &index); err != nil || index >= len(bundles) {
				http.NotFound(w, r)
				return
			}
		}
		version := fmt.Sprintf("1.%d.0", index)
		data := bundles[index]
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"tag_name": version, "draft": false, "prerelease": false, "immutable": true,
			"assets": []any{map[string]any{"name": "kb.zip", "state": "uploaded", "content_type": "application/zip",
				"size": len(data), "digest": "sha256:" + computeDigest(data),
				"url": fmt.Sprintf("%s/assets/%s/%d", p.server.URL, parts[2], index)}}})
	case len(parts) == 3 && parts[0] == "assets":
		var index int
		fmt.Sscan(parts[2], &index)
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(p.releases[parts[1]][index])
	default:
		http.NotFound(w, r)
	}
}

type concurrencyHome struct {
	home, settings, root, keyPath string
	publisher                     *publisher
	manager                       *Manager
	runtime                       *Runtime
	// goPace spaces the Go writers' operations; goLeavesUpdates names libraries the Go writers install
	// and delete but leave updating to another writer.
	goPace          time.Duration
	goLeavesUpdates map[string]bool
}

func releaseSource(location, repository, library, keyPath string) map[string]any {
	return map[string]any{"library-id": library, "type": "github-release", "location": location,
		"trusted-key-id": "test-key", "trusted-public-key-path": keyPath, "repository-owner": "o",
		"repository-name": repository, "asset-name": "kb.zip", "enabled": true, "priority": 10, "participation": "supplement"}
}

func newConcurrencyHome(t *testing.T) *concurrencyHome {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLIO_HOME", home)
	key := fixtureKey(t)
	h := &concurrencyHome{home: home, settings: filepath.Join(home, "appsettings.json"), root: filepath.Join(home, "knowledge"),
		keyPath: filepath.Join(home, "test-key.pem")}
	if err := os.WriteFile(h.keyPath, publicKeyPEM(t, key), 0o600); err != nil {
		t.Fatal(err)
	}
	h.publisher = newPublisher(t, key, map[string]string{"alpha": "com.example.alpha", "beta": "com.example.beta"}, publishedVersions)
	location := h.publisher.server.URL + "/"
	settings := map[string]any{
		"Environments":           map[string]any{"sentinel": map[string]any{"Uri": "http://localhost:1", "Login": "Supervisor"}},
		"ActiveEnvironmentKey":   "sentinel",
		"dbConnectionStringKeys": map[string]any{"keep-me": map[string]any{"Uri": "mssql://localhost:1433"}},
		"autoupdate": map[string]any{"clio": map[string]any{"enabled": false}, "knowledge": map[string]any{"enabled": false},
			"toolkit": map[string]any{"enabled": false}},
		"knowledge": map[string]any{"root-path": h.root, "sources": map[string]any{
			"alpha": releaseSource(location, "alpha", "com.example.alpha", h.keyPath),
			"beta":  releaseSource(location, "beta", "com.example.beta", h.keyPath)}},
		"features": map[string]any{},
	}
	data, _ := json.MarshalIndent(settings, "", "  ")
	if err := os.WriteFile(h.settings, data, 0o600); err != nil {
		t.Fatal(err)
	}
	h.runtime = testRuntime()
	h.manager = &Manager{SettingsPath: h.settings, Verify: h.runtime.Verify, Changed: h.runtime.InvalidateSettings,
		Transports: map[SourceType]ArtifactTransport{SourceGitHubRelease: GitHubReleaseTransport{Client: NewTransportClient()}}}
	return h
}

// violations collects invariant failures from every goroutine.
type violations struct {
	mu   sync.Mutex
	list []string
}

func (v *violations) add(format string, args ...any) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if len(v.list) < 50 {
		v.list = append(v.list, fmt.Sprintf(format, args...))
	}
}

// watchStore is a reader that must never observe a torn store: whenever a generation named by the
// activation marker cannot be read, the marker must have moved on (or the source been deleted) meanwhile.
func watchStore(ctx context.Context, h *concurrencyHome, aliases []string, seen *violations, reads *atomic.Int64) {
	store := Store{Root: h.root}
	for ctx.Err() == nil {
		for _, alias := range aliases {
			before, diagnostic, err := store.readCurrentMarker(alias)
			if err != nil || before == nil || diagnostic != "" {
				continue // not installed, being deleted, or (between the high-water mark and current.json) lagging
			}
			if _, _, failure := store.ReadCandidate(alias, before.Active); failure != "" {
				after, _, _ := store.readCurrentMarker(alias)
				if after != nil && samePointer(after.Active, before.Active) {
					seen.add("reader: %s active generation %s unreadable while still active: %s", alias, before.Active.RelativePath, failure)
				}
			}
			reads.Add(1)
		}
	}
}

// watchSettings is a reader that must always find a complete appsettings.json with its untouched sections.
func watchSettings(ctx context.Context, h *concurrencyHome, seen *violations, reads *atomic.Int64) {
	for ctx.Err() == nil {
		data, err := os.ReadFile(h.settings)
		if err != nil {
			seen.add("settings reader: %v", err)
			continue
		}
		root, err := ParseJSONObject(data)
		if err != nil {
			seen.add("settings reader: torn file: %v", err)
			continue
		}
		if environments := root.Object("Environments"); environments == nil || environments.Object("sentinel") == nil {
			seen.add("settings reader: the Environments section lost its sentinel")
		}
		if root.Object("dbConnectionStringKeys") == nil {
			seen.add("settings reader: dbConnectionStringKeys was dropped")
		}
		reads.Add(1)
	}
}

// verifyQuiescentStore checks what clio's reader and the next publication rely on once every writer stopped.
func verifyQuiescentStore(t *testing.T, h *concurrencyHome) {
	t.Helper()
	sources := filepath.Join(h.root, sourcesDirectory)
	entries, err := os.ReadDir(sources)
	if err != nil {
		t.Fatal(err)
	}
	store := Store{Root: h.root}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, quarantinePrefix) {
			t.Errorf("an abandoned quarantine %s was left behind", name)
			continue
		}
		if name == locksDirectoryName || name == historyDirectory || !entry.IsDir() {
			continue
		}
		sourceRoot := filepath.Join(sources, name)
		owner, err := os.ReadFile(filepath.Join(sourceRoot, sourceOwnerFileName))
		if err != nil {
			t.Errorf("source %s has no ownership marker: %v", name, err)
			continue
		}
		alias := strings.TrimSuffix(string(owner), "\n")
		if SourceKey(alias) != name {
			t.Errorf("source directory %s is owned by %q", name, alias)
		}
		files, _ := os.ReadDir(sourceRoot)
		for _, file := range files {
			if strings.HasSuffix(file.Name(), ".tmp") {
				t.Errorf("source %s kept a temporary file %s", alias, file.Name())
			}
		}
		if staged, _ := os.ReadDir(filepath.Join(sourceRoot, stagingDirectory)); len(staged) > 0 {
			t.Errorf("source %s kept %d staging entries", alias, len(staged))
		}
		state, diagnostic, err := store.readCurrentMarker(alias)
		if err != nil || diagnostic != "" {
			t.Errorf("source %s activation marker: %v %s", alias, err, diagnostic)
			continue
		}
		if state == nil {
			continue
		}
		if _, _, failure := store.ReadCandidate(alias, state.Active); failure != "" {
			t.Errorf("source %s active generation: %s", alias, failure)
		}
		if state.Previous != nil {
			if _, _, failure := store.ReadCandidate(alias, *state.Previous); failure != "" {
				t.Errorf("source %s previous generation: %s", alias, failure)
			}
		}
		mark, err := store.readHighWater(state.Active.LibraryID)
		if err != nil || mark == nil || mark.Sequence < state.Active.Sequence {
			t.Errorf("source %s high-water mark %+v below active sequence %d (%v)", alias, mark, state.Active.Sequence, err)
		}
		generations, _ := os.ReadDir(filepath.Join(sourceRoot, generationsDir))
		for _, generation := range generations {
			relative := generationsDir + "/" + generation.Name()
			if relative != state.Active.RelativePath && (state.Previous == nil || relative != state.Previous.RelativePath) {
				if runtime.GOOS == "windows" {
					// A prune refused because a reader still had a file open leaves the directory for the
					// next publication's prune, exactly as clio's does there.
					t.Logf("source %s kept an unreferenced generation %s", alias, generation.Name())
				} else {
					t.Errorf("source %s kept an unreferenced generation %s", alias, generation.Name())
				}
			}
		}
	}
}

// goWorker mixes every write this package performs, in random order, until ctx ends. It returns the
// feature value it wrote last and the extra sources it expects to exist.
func goWorker(ctx context.Context, h *concurrencyHome, id int, seen *violations, operations *atomic.Int64) (bool, map[string]bool) {
	random := rand.New(rand.NewSource(int64(id)))
	feature := false
	extras := map[string]bool{}
	location := h.publisher.server.URL + "/"
	for n := 0; ctx.Err() == nil; n++ {
		if h.goPace > 0 {
			time.Sleep(time.Duration(random.Int63n(int64(h.goPace))))
		}
		alias := []string{"alpha", "beta"}[random.Intn(2)]
		source := alias
		var err error
		var batch BatchResult
		switch operation := random.Intn(9); {
		case operation <= 1:
			batch, err = h.manager.Install(context.Background(), &source)
		case (operation == 2 || operation == 3 || operation == 5) && h.goLeavesUpdates[alias]:
			batch, err = h.manager.Install(context.Background(), &source)
		case operation == 2 || operation == 3 || operation == 5:
			batch, err = h.manager.Update(context.Background(), &source)
		case operation == 4:
			_, err = h.manager.Delete(context.Background(), &source, true)
		default:
			switch operation {
			case 6:
				feature = !feature
				err = SetFeature(h.settings, fmt.Sprintf("go-worker-%d", id), feature)
			case 7:
				extra := fmt.Sprintf("go-%d-%d", id, n)
				config, validationErr := ValidateSourceInput(SourceInput{LibraryID: fmt.Sprintf("com.example.go%d.n%d", id, n),
					Type: SourceGitHubRelease, Location: location, RepositoryOwner: ptr("o"), RepositoryName: ptr("none"),
					AssetName: ptr("kb.zip"), Enabled: true, Participation: ParticipationSupplement})
				if validationErr != nil {
					seen.add("go worker %d: %v", id, validationErr)
					continue
				}
				added, addErr := TryAddSource(h.settings, extra, config)
				if addErr != nil || !added {
					seen.add("go worker %d: add %s: added=%v %v", id, extra, added, addErr)
				} else {
					extras[extra] = true
				}
			case 8:
				for extra := range extras {
					result, removeErr := h.manager.Remove(extra, true)
					if removeErr != nil || !result.Success {
						seen.add("go worker %d: remove %s: %+v %v", id, extra, result, removeErr)
					} else {
						delete(extras, extra)
					}
					break
				}
			}
		}
		if err != nil && !windowsContention(err.Error()) {
			seen.add("go worker %d: %v", id, err)
		}
		for _, result := range batch.Sources {
			switch result.Status {
			case "installed", "updated":
				publications.Add(1)
			case "failed":
				// A concurrent writer may legitimately win the compare-and-swap; anything else is a fault.
				if !strings.Contains(result.Message, "changed while the operation was in progress") &&
					!strings.Contains(result.Message, "is already installed; use update-knowledge") &&
					!strings.Contains(result.Message, "is not installed; use install-knowledge") &&
					!windowsContention(result.Message) {
					seen.add("go worker %d: %s %s: %s", id, alias, result.Status, result.Message)
				}
			}
		}
		operations.Add(1)
	}
	return feature, extras
}

func ptr(value string) *string { return &value }

// windowsContention is a Windows refusal to rename or delete a file another handle has open. clio meets
// the same refusal there and reports it as a retryable failure; nothing is torn by it.
func windowsContention(text string) bool {
	return runtime.GOOS == "windows" && (strings.Contains(text, "Access is denied") ||
		strings.Contains(text, "being used by another process"))
}

// publishedVersions is how many releases of each library the publisher holds; one is released per tick.
const publishedVersions = 40

// publications counts generations the Go writers published (installed or updated).
var publications atomic.Int64

var clioStatus = regexp.MustCompile(`"status": "([a-z-]+)",\s*"message": "([^"]{0,90})`)

// TestConcurrentWritersKeepStoreAndSettingsConsistent runs Go writers against one home.
func TestConcurrentWritersKeepStoreAndSettingsConsistent(t *testing.T) {
	h := newConcurrencyHome(t)
	runConcurrency(t, h, 4, 3*time.Second, 70*time.Millisecond, nil)
}

type workerResult struct {
	feature bool
	extras  map[string]bool
}

func runConcurrency(t *testing.T, h *concurrencyHome, goWorkers int, duration, releaseEvery time.Duration,
	external func(ctx context.Context, seen *violations)) {
	t.Helper()
	seen := &violations{}
	var storeReads, settingsReads, operations atomic.Int64
	readers, stopReaders := context.WithCancel(context.Background())
	var readerGroup sync.WaitGroup
	readerGroup.Add(1)
	go func() { // the publisher releases a new version of each library at a steady pace
		defer readerGroup.Done()
		ticker := time.NewTicker(releaseEvery)
		defer ticker.Stop()
		for {
			select {
			case <-readers.Done():
				return
			case <-ticker.C:
				h.publisher.bump("alpha")
				h.publisher.bump("beta")
			}
		}
	}()
	for i := 0; i < 2; i++ {
		readerGroup.Add(2)
		go func() { defer readerGroup.Done(); watchStore(readers, h, []string{"alpha", "beta"}, seen, &storeReads) }()
		go func() { defer readerGroup.Done(); watchSettings(readers, h, seen, &settingsReads) }()
	}
	writers, stopWriters := context.WithTimeout(context.Background(), duration)
	defer stopWriters()
	results := make([]workerResult, goWorkers)
	var writerGroup sync.WaitGroup
	for id := 0; id < goWorkers; id++ {
		writerGroup.Add(1)
		go func(id int) {
			defer writerGroup.Done()
			results[id].feature, results[id].extras = goWorker(writers, h, id, seen, &operations)
		}(id)
	}
	if external != nil {
		external(writers, seen)
		stopWriters()
	}
	writerGroup.Wait()
	stopReaders()
	readerGroup.Wait()
	for _, violation := range seen.list {
		t.Error(violation)
	}
	t.Logf("%d Go operations (%d publications), %d store reads, %d settings reads", operations.Load(),
		publications.Load(), storeReads.Load(), settingsReads.Load())
	if operations.Load() < int64(goWorkers) || publications.Load() == 0 || storeReads.Load() == 0 || settingsReads.Load() == 0 {
		t.Fatalf("the test did not exercise concurrency")
	}
	// No update was lost: every worker's last feature value and its surviving sources are in the file.
	settings, err := ReadSettings(h.settings)
	if err != nil {
		t.Fatalf("final settings: %v", err)
	}
	for id, result := range results {
		if got := settings.Features[fmt.Sprintf("go-worker-%d", id)]; got != result.feature {
			t.Errorf("worker %d feature = %v, last written %v", id, got, result.feature)
		}
		for extra := range result.extras {
			if _, _, ok := settings.Knowledge.FindSource(extra); !ok {
				t.Errorf("worker %d source %s was lost", id, extra)
			}
		}
	}
	for _, alias := range settings.Knowledge.SortedAliases() {
		if strings.HasPrefix(alias, "go-") {
			owner, extra := 0, alias
			fmt.Sscanf(alias, "go-%d-", &owner)
			if owner < len(results) && !results[owner].extras[extra] {
				t.Errorf("source %s was removed by its worker but is still configured", alias)
			}
		}
	}
	verifyQuiescentStore(t, h)
	// The quiescent store still serves: both libraries install and activate.
	for _, alias := range []string{"alpha", "beta"} {
		source := alias
		if err := SetSourceEnabled(h.settings, alias, true); err != nil {
			t.Fatal(err)
		}
		result, err := h.manager.Install(context.Background(), &source)
		if err != nil || !result.Success {
			t.Fatalf("final install of %s: %+v %v", alias, result, err)
		}
	}
	h.runtime.InvalidateSettings()
	if lookup := h.runtime.FindByName("core-rules"); lookup.Status == LookupNotFound {
		t.Fatalf("nothing is served after the run: %s", h.runtime.LastDiagnostic())
	}
}

// TestConcurrentWithClioProcesses runs real clio 8.1.0.142 processes (CLIO_DLL) as concurrent writers next to
// the Go writers, on the same home, and then checks that each side reads what the other wrote.
func TestConcurrentWithClioProcesses(t *testing.T) {
	dll := os.Getenv("CLIO_DLL")
	if dll == "" {
		t.Skip("set CLIO_DLL to a clio 8.1.0.142 clio.dll to run real clio processes next to the Go writers")
	}
	if _, err := exec.LookPath("dotnet"); err != nil {
		t.Skip("dotnet is not installed")
	}
	h := newConcurrencyHome(t)
	// The Go writers are far faster than a clio process start, so they are paced and leave updating beta
	// to clio: Go still installs, deletes and reads beta, so both sides keep writing the same source.
	h.goPace, h.goLeavesUpdates = 80*time.Millisecond, map[string]bool{"beta": true}
	clio := func(ctx context.Context, args ...string) (string, error) {
		command := exec.CommandContext(ctx, "dotnet", append([]string{dll}, args...)...)
		command.Dir = h.home
		command.Env = append(os.Environ(), "CLIO_HOME="+h.home, "HOME="+filepath.Join(h.home, "dotnet-home"),
			"CLIO_TELEMETRY_ENABLED=false", "CLIO_TELEMETRY_HOME="+filepath.Join(h.home, "telemetry"),
			"CLIO_KNOWLEDGE_CURATED_API_BASE_URL="+h.publisher.server.URL+"/", "DOTNET_CLI_TELEMETRY_OPTOUT=1")
		output, err := command.CombinedOutput()
		return string(output), err
	}
	if output, err := clio(context.Background(), "info-knowledge", "--json"); err != nil {
		t.Fatalf("clio does not run: %v\n%s", err, output)
	}
	var clioRuns, clioPublications atomic.Int64
	var clioOutcomes sync.Map
	defer func() {
		clioOutcomes.Range(func(key, _ any) bool { t.Log("clio outcome:", key); return true })
	}()
	external := func(ctx context.Context, seen *violations) {
		var group sync.WaitGroup
		for id := 0; id < 3; id++ {
			group.Add(1)
			go func(id int) {
				defer group.Done()
				scripts := [][]string{
					{"install-knowledge", "--source", "alpha", "--json"},
					{"update-knowledge", "--source", "beta", "--json"},
					{"add-knowledge-source", "--alias", fmt.Sprintf("clio-%d", id), "--library-id", fmt.Sprintf("com.example.clio%d", id),
						"--type", "github-release", "--location", h.publisher.server.URL + "/", "--repository-owner", "o",
						"--repository-name", "none", "--asset-name", "kb.zip", "--json"},
					{"experimental", "--name", fmt.Sprintf("clio-worker-%d", id), "--enable"},
					{"update-knowledge", "--source", "alpha", "--json"},
					{"update-knowledge", "--source", "beta", "--json"},
					{"delete-knowledge", "--source", "beta", "--force", "--json"},
					{"install-knowledge", "--source", "beta", "--json"},
					{"update-knowledge", "--source", "beta", "--json"},
					{"update-knowledge", "--json"},
					{"disable-knowledge-source", "--alias", fmt.Sprintf("clio-%d", id), "--json"},
				}
				for _, script := range scripts {
					runCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
					output, err := clio(runCtx, script...)
					cancel()
					clioRuns.Add(1)
					if strings.Contains(output, `"status": "installed"`) || strings.Contains(output, `"status": "updated"`) {
						clioPublications.Add(1)
					}
					for _, match := range clioStatus.FindAllStringSubmatch(output, -1) {
						clioOutcomes.Store(script[0]+" "+match[1]+" "+match[2], true)
					}
					var exitError *exec.ExitError
					if err != nil && !(errors.As(err, &exitError) && exitError.ExitCode() == 1) {
						seen.add("clio %v: %v\n%s", script, err, output)
					}
					// clio's own reader can meet a source directory another process is renaming away ("is not
					// owned by Clio") exactly as it does next to a second clio process; that is not flagged.
					if strings.Contains(output, "Timed out waiting") ||
						strings.Contains(output, "unreadable content") || strings.Contains(output, "kept changing") {
						seen.add("clio %v reported a store or settings conflict:\n%s", script, output)
					}
				}
			}(id)
		}
		group.Wait()
	}
	runConcurrency(t, h, 3, 2*time.Minute, 400*time.Millisecond, external)
	t.Logf("%d clio processes ran, %d of them published a generation", clioRuns.Load(), clioPublications.Load())
	if clioPublications.Load() == 0 {
		t.Errorf("no clio process published a generation, so clio never wrote the store concurrently")
	}
	settings, err := ReadSettings(h.settings)
	if err != nil {
		t.Fatal(err)
	}
	for id := 0; id < 3; id++ {
		if !settings.Features[fmt.Sprintf("clio-worker-%d", id)] {
			t.Errorf("clio worker %d's feature write was lost", id)
		}
		if source, ok := settings.Knowledge.Sources[fmt.Sprintf("clio-%d", id)]; !ok || source.Enabled {
			t.Errorf("clio worker %d's source is %+v (present=%v), want present and disabled", id, source, ok)
		}
	}
	// clio reads what Go published last, and Go activates what clio publishes next.
	output, err := clio(context.Background(), "info-knowledge", "--json")
	if err != nil {
		t.Fatalf("clio info-knowledge: %v\n%s", err, output)
	}
	var info struct {
		Sources []struct {
			Alias          string `json:"alias"`
			IsInstalled    bool   `json:"isInstalled"`
			IsValid        bool   `json:"isValid"`
			ActiveSequence uint64 `json:"activeSequence"`
		} `json:"sources"`
	}
	if err := json.Unmarshal([]byte(output[strings.Index(output, "{"):]), &info); err != nil {
		t.Fatalf("clio info-knowledge output: %v\n%s", err, output)
	}
	for _, source := range info.Sources {
		if (source.Alias == "alpha" || source.Alias == "beta") && (!source.IsInstalled || !source.IsValid) {
			t.Errorf("clio does not accept the generation of %s: %+v", source.Alias, source)
		}
	}
	h.publisher.mu.Lock()
	h.publisher.latest["alpha"] = len(h.publisher.releases["alpha"]) - 1
	h.publisher.mu.Unlock()
	if output, err := clio(context.Background(), "update-knowledge", "--source", "alpha", "--json"); err != nil && !strings.Contains(output, "up-to-date") {
		t.Fatalf("clio update-knowledge: %v\n%s", err, output)
	}
	h.runtime.InvalidateSettings()
	state, diagnostic := Store{Root: h.root}.ReadCurrent("alpha")
	if state == nil || state.Active.Sequence != 1000+publishedVersions-1 {
		t.Fatalf("Go does not see clio's publication: %+v %s", state, diagnostic)
	}
	verifyQuiescentStore(t, h)
}
