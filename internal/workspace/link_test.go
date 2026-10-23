package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
)

// requireSymlinks skips the test where the account may not create symbolic links (Windows without
// developer mode or elevation).
func requireSymlinks(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if err := createDirectorySymlink(filepath.Join(dir, "probe"), dir); err != nil {
		t.Skipf("symbolic links cannot be created here: %v", err)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func descriptor(name, maintainer string) string {
	return `{"Descriptor":{"Name":"` + name + `","Maintainer":"` + maintainer + `"}}`
}

func linkTarget(t *testing.T, path string) string {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("%s is not a link (mode %v)", path, info.Mode())
	}
	target, err := os.Readlink(path)
	if err != nil {
		t.Fatal(err)
	}
	return target
}

func messages(result creatio.CommandResult) []string {
	values := make([]string, 0, len(result.Messages))
	for _, message := range result.Messages {
		values = append(values, message.MessageType+": "+message.Value)
	}
	return values
}

func assertMessages(t *testing.T, result creatio.CommandResult, exitCode int, want ...string) {
	t.Helper()
	got := messages(result)
	if result.ExitCode != exitCode || strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("exit %d, messages:\n%s\nwant exit %d:\n%s", result.ExitCode, strings.Join(got, "\n"), exitCode, strings.Join(want, "\n"))
	}
}

// fakeSite is a LinkSite that records its calls.
type fakeSite struct {
	maintainer string
	updated    bool
	outcome    string
	packages   []creatio.WorkspaceLinkPackage
	failWith   error
	calls      []string
}

func (s *fakeSite) SysSettingValue(_ context.Context, code string) (string, error) {
	s.calls = append(s.calls, "read "+code)
	return s.maintainer, s.failWith
}

func (s *fakeSite) UpdateSysSetting(_ context.Context, code, value string) (bool, error) {
	s.calls = append(s.calls, "update "+code+"="+value)
	return s.updated, nil
}

func (s *fakeSite) UnlockPackages(_ context.Context, names []string) error {
	s.calls = append(s.calls, "unlock "+strings.Join(names, ","))
	return nil
}

func (s *fakeSite) LoadPackagesToFileSystem(context.Context) ([]creatio.LogMessage, string, error) {
	s.calls = append(s.calls, "2fs")
	return []creatio.LogMessage{{MessageType: "None", Value: "Start load packages to file system on a web application"}}, s.outcome, nil
}

func (s *fakeSite) UnlockedPackages(context.Context) ([]creatio.WorkspaceLinkPackage, error) {
	s.calls = append(s.calls, "unlocked")
	return s.packages, s.failWith
}

// siteDeps serves every remote call with site and records the environment the command named.
func siteDeps(site LinkSite, named *string, environments map[string]LinkEnvironment) LinkDeps {
	return LinkDeps{
		Environment: func(name string) (LinkEnvironment, bool) {
			for key, environment := range environments {
				if strings.EqualFold(key, name) {
					return environment, true
				}
			}
			return LinkEnvironment{}, false
		},
		Site: func(_ context.Context, environment string) (LinkSite, error) {
			if named != nil {
				*named = environment
			}
			return site, nil
		},
	}
}

func TestLinkValidationRunsBothRules(t *testing.T) {
	result := Link(context.Background(), LinkRequest{EnvPkgPath: " ", RepoPath: "r", Packages: ""}, LinkDeps{})
	assertMessages(t, result, 1,
		"Error: ERROR (ArgumentParse.Error) - Either path to creatio directory or environment name must be provided",
		"Error: ERROR (ArgumentParse.Error) - Either --packages or --unlocked must be specified")
	result = Link(context.Background(), LinkRequest{Environment: "e", Unlocked: true, Packages: "A"}, LinkDeps{})
	assertMessages(t, result, 1, "Error: ERROR (ArgumentParse.Error) - --unlocked and --packages are mutually exclusive; use one or the other")
	result = Link(context.Background(), LinkRequest{EnvPkgPath: "p", Unlocked: true}, LinkDeps{})
	assertMessages(t, result, 1,
		"Error: ERROR (ArgumentParse.Error) - When --unlocked is set, environment name (-e) or URI (-u) must be provided for API connection")
}

func TestLinkReplacesPackageFoldersWithLinksToTheRepository(t *testing.T) {
	requireSymlinks(t)
	root := t.TempDir()
	repo, pkg := filepath.Join(root, "repo"), filepath.Join(root, "pkg")
	writeFile(t, filepath.Join(repo, "PkgA", "descriptor.json"), descriptor("PkgA", "Acme"))
	writeFile(t, filepath.Join(repo, "PkgB", "descriptor.json"), descriptor("PkgB", "Acme"))
	writeFile(t, filepath.Join(repo, "PkgC", "branches", "7.8.0", "descriptor.json"), "{}")
	writeFile(t, filepath.Join(pkg, "PkgA", "old.txt"), "old")
	mkdir(t, filepath.Join(pkg, "Other"))

	result := Link(context.Background(), LinkRequest{EnvPkgPath: pkg, RepoPath: repo, Packages: "PkgA, PkgB,PkgC", SkipPreparation: true}, LinkDeps{})
	assertMessages(t, result, 0, "Info: Skipping preparation (--skip-preparation).",
		"Info: Linking repository package(s) to environment "+pkg+" from "+repo)
	for name, want := range map[string]string{
		"PkgA": filepath.Join(repo, "PkgA"), "PkgB": filepath.Join(repo, "PkgB"), "PkgC": filepath.Join(repo, "PkgC", "branches", "7.8.0"),
	} {
		if got := linkTarget(t, filepath.Join(pkg, name)); got != want {
			t.Errorf("%s -> %s, want %s", name, got, want)
		}
	}
	if !fileExists(filepath.Join(repo, "PkgA", "descriptor.json")) || fileExists(filepath.Join(repo, "PkgA", "old.txt")) {
		t.Fatal("the repository package must be untouched and the old Pkg folder gone")
	}

	// Linking again replaces the links themselves; the repository keeps its files.
	result = Link(context.Background(), LinkRequest{EnvPkgPath: pkg, RepoPath: repo, Packages: "*", SkipPreparation: true}, LinkDeps{})
	if result.ExitCode != 0 || linkTarget(t, filepath.Join(pkg, "PkgB")) != filepath.Join(repo, "PkgB") ||
		!fileExists(filepath.Join(repo, "PkgB", "descriptor.json")) {
		t.Fatalf("relink = %v", messages(result))
	}
}

func TestLinkFailures(t *testing.T) {
	requireSymlinks(t)
	root := t.TempDir()
	repo, pkg := filepath.Join(root, "repo"), filepath.Join(root, "pkg")
	writeFile(t, filepath.Join(repo, "PkgA", "descriptor.json"), "{}")
	writeFile(t, filepath.Join(repo, "PkgD", "branches", "1.0", "x"), "")
	writeFile(t, filepath.Join(repo, "PkgD", "branches", "2.0", "x"), "")
	mkdir(t, pkg)
	run := func(request LinkRequest) creatio.CommandResult {
		request.SkipPreparation = true
		return Link(context.Background(), request, LinkDeps{})
	}
	skipping := "Info: Skipping preparation (--skip-preparation)."
	assertMessages(t, run(LinkRequest{EnvPkgPath: pkg, RepoPath: repo, Packages: "PkgA,,Nope, X "}), 1, skipping,
		"Error: Error during linking: Packages , Nope, X not found in repository: "+repo+".")
	assertMessages(t, run(LinkRequest{EnvPkgPath: pkg, RepoPath: repo, Packages: "pkga"}), 1, skipping,
		"Error: Error during linking: Packages pkga not found in repository: "+repo+".")
	assertMessages(t, run(LinkRequest{EnvPkgPath: filepath.Join(root, "nopkg"), RepoPath: repo, Packages: "PkgA"}), 1, skipping,
		"Error: Error during linking: Could not find a part of the path '"+filepath.Join(root, "nopkg")+"'.")
	assertMessages(t, run(LinkRequest{EnvPkgPath: pkg, RepoPath: repo, Packages: "PkgD"}), 1, skipping,
		"Error: Error during linking: Unsupported package folder structure.Expected structure contains one package version in folder '"+
			filepath.Join(repo, "PkgD", "branches")+"'.")
	assertMessages(t, run(LinkRequest{EnvPkgPath: "plain-name", RepoPath: repo, Packages: "PkgA"}), 1, skipping,
		"Error: Environment plain-name is not a registered environment. Please correct environment name an try again.")
}

func TestLinkByRegisteredEnvironmentPrefersTheLayoutOfItsRuntime(t *testing.T) {
	requireSymlinks(t)
	root := t.TempDir()
	repo, env := filepath.Join(root, "repo"), filepath.Join(root, "env")
	writeFile(t, filepath.Join(repo, "PkgA", "descriptor.json"), "{}")
	framework := filepath.Join(env, "Terrasoft.WebApp", "Terrasoft.Configuration", "Pkg")
	core := filepath.Join(env, "Terrasoft.Configuration", "Pkg")
	mkdir(t, framework)
	mkdir(t, core)
	environments := map[string]LinkEnvironment{"core": {EnvironmentPath: env, IsNetCore: true}, "net": {EnvironmentPath: env},
		"bare": {URI: "http://localhost"}}
	for name, want := range map[string]string{"CORE": core, "net": framework} {
		result := Link(context.Background(), LinkRequest{Environment: name, RepoPath: repo, Packages: "PkgA", SkipPreparation: true},
			siteDeps(nil, nil, environments))
		assertMessages(t, result, 0, "Info: Skipping preparation (--skip-preparation).",
			"Info: Linking repository package(s) to environment "+want+" from "+repo)
	}
	result := Link(context.Background(), LinkRequest{Environment: "bare", RepoPath: repo, Packages: "PkgA", SkipPreparation: true},
		siteDeps(nil, nil, environments))
	want := "Error: Environment bare does not have a valid local package path. Configure EnvironmentPath or use --envPkgPath with the direct package folder path."
	if runtime.GOOS == "windows" {
		want = "Error: Environment bare not found. Please check the environment name and try again."
	}
	assertMessages(t, result, 1, "Info: Skipping preparation (--skip-preparation).", want)
	result = Link(context.Background(), LinkRequest{Environment: "nosuch", RepoPath: repo, Unlocked: true}, siteDeps(nil, nil, environments))
	assertMessages(t, result, 1, "Error: Could not resolve environment packages path.")
}

func TestLinkPreparesIncompletePackagesOnTheNamedSite(t *testing.T) {
	requireSymlinks(t)
	root := t.TempDir()
	repo, env := filepath.Join(root, "repo"), filepath.Join(root, "env")
	pkg := filepath.Join(env, "Terrasoft.Configuration", "Pkg")
	writeFile(t, filepath.Join(repo, "PkgA", "descriptor.json"), descriptor("PkgA", "Acme"))
	writeFile(t, filepath.Join(repo, "PkgB", "descriptor.json"), `{"descriptor":{"maintainer":"ACME"}}`)
	writeFile(t, filepath.Join(repo, "PkgC", "descriptor.json"), descriptor("PkgC", "Other"))
	writeFile(t, filepath.Join(repo, "PkgN", "descriptor.json"), `{"Descriptor":{"Name":"PkgN"}}`)
	writeFile(t, filepath.Join(pkg, "PkgA", "descriptor.json"), "{}")
	mkdir(t, filepath.Join(pkg, "PkgB"))
	environments := map[string]LinkEnvironment{"dev": {EnvironmentPath: env, IsNetCore: true}}
	preparation := []string{
		"Info: Some packages are missing or incomplete in Pkg folder — running preparation...",
		"Warning: Preparation required:",
		"Warning:   - SysSetting 'Maintainer' will change: 'Customer' -> 'Acme'",
		"Warning:   - 2 package(s) will be unlocked: PkgA, PkgB",
		"Warning:   - Packages will be synced to file system (2fs)",
	}

	site := &fakeSite{maintainer: "Customer", updated: true, outcome: "completed"}
	var named string
	result := Link(context.Background(), LinkRequest{Environment: "dev", RepoPath: repo, Packages: "PkgA,PkgB", DryRun: true},
		siteDeps(site, &named, environments))
	assertMessages(t, result, 0, append(preparation, "Info: [dry-run] No changes applied.", "Info: [dry-run] Skipping symlink creation.")...)
	if named != "dev" || strings.Join(site.calls, "|") != "read Maintainer" {
		t.Fatalf("named %q, calls %q", named, site.calls)
	}

	site.calls = nil
	result = Link(context.Background(), LinkRequest{Environment: "dev", RepoPath: repo, Packages: "PkgA,PkgB"}, siteDeps(site, nil, environments))
	assertMessages(t, result, 0, append(preparation, "Info: Proceeding...",
		"Info: Updating SysSetting 'Maintainer' from 'Customer' to 'Acme'", "Info: Unlocking packages: PkgA, PkgB",
		"Info: Loading packages to file system (2fs)...", "None: Start load packages to file system on a web application",
		"Info: Packages synced to file system successfully.",
		"Info: Linking repository package(s) to environment "+pkg+" from "+repo)...)
	if strings.Join(site.calls, "|") != "read Maintainer|update Maintainer=Acme|unlock PkgA,PkgB|2fs" {
		t.Fatalf("calls = %q", site.calls)
	}

	// The packages are links now, complete: nothing is prepared.
	site.calls = nil
	result = Link(context.Background(), LinkRequest{Environment: "dev", RepoPath: repo, Packages: "PkgA", DryRun: true}, siteDeps(site, nil, environments))
	assertMessages(t, result, 0, "Info: [dry-run] Skipping symlink creation.")

	writeFile(t, filepath.Join(pkg, "PkgC", "x"), "")
	result = Link(context.Background(), LinkRequest{Environment: "dev", RepoPath: repo, Packages: "PkgA,PkgC,PkgN"}, siteDeps(site, nil, environments))
	assertMessages(t, result, 1, "Info: Some packages are missing or incomplete in Pkg folder — running preparation...",
		"Warning: Could not read Maintainer from descriptor.json for package 'PkgN' in repo — skipping preparation",
		"Error: Packages have different Maintainer values: 'PkgA'='Acme', 'PkgC'='Other'. All packages must have the same Maintainer.")
	if len(site.calls) != 0 {
		t.Fatalf("the site was called: %q", site.calls)
	}

	for _, item := range []struct {
		outcome string
		tail    []string
	}{
		{"disabled", []string{"Warning: Load packages to file system on a web application ended with error: disabled file design mode",
			"Warning: The packages were unlocked and 'Maintainer' was applied, and the links are still created, but the environment " +
				"exported nothing to its file system. Run 'clio turn-fsm on' and then 'clio pkg-to-file-system' to finish the export.",
			"Info: Linking repository package(s) to environment " + pkg + " from " + repo}},
		{"refused", []string{"Error: Packages were not synced to the file system, so no links were created. The packages were unlocked and " +
			"'Maintainer' was applied. Fix the reported error and run 'clio link-to-repository' again."}},
	} {
		outcome, tail := item.outcome, item.tail
		if err := os.RemoveAll(filepath.Join(pkg, "PkgC")); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(pkg, "PkgC", "x"), "")
		site := &fakeSite{maintainer: "acme", outcome: outcome}
		result := Link(context.Background(), LinkRequest{Environment: "dev", RepoPath: repo, Packages: "PkgC"}, siteDeps(site, nil, environments))
		want := []string{"Info: Some packages are missing or incomplete in Pkg folder — running preparation...",
			"Warning: Preparation required:", "Warning:   - SysSetting 'Maintainer' will change: 'acme' -> 'Other'",
			"Warning:   - 1 package(s) will be unlocked: PkgC", "Warning:   - Packages will be synced to file system (2fs)",
			"Info: Proceeding...", "Info: Updating SysSetting 'Maintainer' from 'acme' to 'Other'", "Error: Failed to update Maintainer sys setting."}
		assertMessages(t, result, 1, want...)
		site.updated = true
		result = Link(context.Background(), LinkRequest{Environment: "dev", RepoPath: repo, Packages: "PkgC"}, siteDeps(site, nil, environments))
		want = append(want[:len(want)-1], "Info: Unlocking packages: PkgC", "Info: Loading packages to file system (2fs)...",
			"None: Start load packages to file system on a web application")
		code := 0
		if outcome == "refused" {
			code = 1
		}
		assertMessages(t, result, code, append(want, tail...)...)
	}

	failing := &fakeSite{failWith: errors.New("Connection refused (127.0.0.1:9)")}
	writeFile(t, filepath.Join(pkg, "PkgD", "x"), "")
	result = Link(context.Background(), LinkRequest{Environment: "dev", RepoPath: repo, Packages: "PkgB,PkgD"}, siteDeps(failing, nil, environments))
	assertMessages(t, result, 1, "Info: Some packages are missing or incomplete in Pkg folder — running preparation...",
		"Warning: Could not read Maintainer from descriptor.json for package 'PkgD' in repo — skipping preparation",
		"Error: Error during linking: Connection refused (127.0.0.1:9)")
}

func TestLinkNamesNoSiteForAFolderPath(t *testing.T) {
	root := t.TempDir()
	repo, pkg := filepath.Join(root, "repo"), filepath.Join(root, "pkg")
	writeFile(t, filepath.Join(repo, "PkgA", "descriptor.json"), descriptor("PkgA", "Acme"))
	mkdir(t, pkg)
	named := "unset"
	deps := siteDeps(nil, &named, nil)
	deps.Site = func(_ context.Context, environment string) (LinkSite, error) {
		named = environment
		return nil, errors.New("no site")
	}
	for _, request := range []LinkRequest{{EnvPkgPath: pkg, RepoPath: repo, Packages: "PkgA"}, {Environment: pkg, RepoPath: repo, Packages: "PkgA"}} {
		result := Link(context.Background(), request, deps)
		assertMessages(t, result, 1, "Info: Some packages are missing or incomplete in Pkg folder — running preparation...",
			"Error: Error during linking: no site")
		if named != "" {
			t.Fatalf("named = %q", named)
		}
	}
}

func TestLinkUnlockedFlatAndVersioned(t *testing.T) {
	requireSymlinks(t)
	root := t.TempDir()
	flat, versioned, env := filepath.Join(root, "flat"), filepath.Join(root, "versioned"), filepath.Join(root, "env")
	pkg := filepath.Join(env, "Terrasoft.Configuration", "Pkg")
	writeFile(t, filepath.Join(flat, "PkgA", "descriptor.json"), "{}")
	writeFile(t, filepath.Join(versioned, "PkgV", "main", "1.0.0", "descriptor.json"), "{}")
	writeFile(t, filepath.Join(versioned, "PkgV", "release", "2.0.0", "descriptor.json"), "{}")
	writeFile(t, filepath.Join(pkg, "PkgV", "descriptor.json"), "{}")
	environments := map[string]LinkEnvironment{"dev": {EnvironmentPath: env, IsNetCore: true}}
	site := &fakeSite{packages: []creatio.WorkspaceLinkPackage{{Name: "pkga", Version: "1.0.0"}, {Name: "Missing", Version: "2.0.0"}}}
	query := "Info: Querying Creatio site for unlocked packages..."

	result := Link(context.Background(), LinkRequest{Environment: "dev", RepoPath: flat, Unlocked: true, DryRun: true}, siteDeps(site, nil, environments))
	assertMessages(t, result, 0, query, "Info: [dry-run] Found 2 unlocked package(s) on site: pkga, Missing",
		"Info: [dry-run] Repository structure: flat (PackageName/descriptor.json)",
		"Info: [dry-run]   'pkga' — found in repository, would link", "Warning: [dry-run]   'Missing' — not found in repository, would skip",
		"Info: [dry-run] No changes applied.")
	// Found without case, then linked by the site's spelling, which the repository check compares exactly.
	result = Link(context.Background(), LinkRequest{Environment: "dev", RepoPath: flat, Unlocked: true}, siteDeps(site, nil, environments))
	assertMessages(t, result, 1, query, "Info: Found 2 unlocked package(s): pkga, Missing",
		"Warning: Unlocked package 'Missing' not found in repository — skipping",
		"Error: Error during linking: Packages pkga not found in repository: "+flat+".")

	site.packages = []creatio.WorkspaceLinkPackage{{Name: "PkgV", Version: "2.0.0"}, {Name: "PkgW", Version: "1.0.0"}}
	release := filepath.Join(versioned, "PkgV", "release", "2.0.0")
	result = Link(context.Background(), LinkRequest{Environment: "dev", RepoPath: versioned, Unlocked: true, DryRun: true}, siteDeps(site, nil, environments))
	assertMessages(t, result, 0, query, "Info: [dry-run] Found 2 unlocked package(s) on site: PkgV, PkgW",
		"Info: [dry-run] Repository structure: versioned (PackageName/branch/version)",
		"Info: [dry-run]   'PkgV' (v2.0.0) — would link from "+release,
		"Warning: [dry-run]   'PkgW' (v1.0.0) — not found in repository, would skip", "Info: [dry-run] No changes applied.")
	result = Link(context.Background(), LinkRequest{Environment: "dev", RepoPath: versioned, Unlocked: true}, siteDeps(site, nil, environments))
	assertMessages(t, result, 0, query, "Info: Found 2 unlocked package(s): PkgV, PkgW",
		"Warning: Existing directory '"+filepath.Join(pkg, "PkgV")+"' will be replaced with symlink",
		"Info: Linked 'PkgV' (v2.0.0) → "+release,
		"Warning: Unlocked package 'PkgW' (v1.0.0) not found in repository — skipping", "Info: Linking completed: 1 linked, 1 skipped")
	if linkTarget(t, filepath.Join(pkg, "PkgV")) != release {
		t.Fatal("PkgV must link to the release branch")
	}

	site.packages = nil
	result = Link(context.Background(), LinkRequest{Environment: "dev", RepoPath: versioned, Unlocked: true}, siteDeps(site, nil, environments))
	assertMessages(t, result, 0, query, "Info: No unlocked packages found on the site.")
	site.packages = []creatio.WorkspaceLinkPackage{{Name: "PkgV"}}
	missing := filepath.Join(root, "norepo")
	result = Link(context.Background(), LinkRequest{Environment: "dev", RepoPath: missing, Unlocked: true}, siteDeps(site, nil, environments))
	assertMessages(t, result, 1, query, "Info: Found 1 unlocked package(s): PkgV", "Error: Repository path does not exist: "+missing)
}

func TestResolveDirectoryPath(t *testing.T) {
	root, sep := t.TempDir(), string(filepath.Separator)
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for value, want := range map[string]string{
		"plain":    "",
		"a/b":      filepath.Join(wd, "a", "b"),
		"./x":      filepath.Join(wd, "x"),
		root + sep: root + sep,
		root + sep + "a" + sep + ".." + sep + "b": filepath.Join(root, "b"),
	} {
		got, ok := resolveDirectoryPath(value)
		if (want == "") == ok || got != want {
			t.Errorf("resolveDirectoryPath(%q) = %q, %v; want %q", value, got, ok, want)
		}
	}
	if runtime.GOOS != "windows" {
		if got, ok := resolveDirectoryPath("file://" + root); !ok || got != root {
			t.Errorf("file URI = %q, %v", got, ok)
		}
	}
}

func TestIsVersionedRepo(t *testing.T) {
	root := t.TempDir()
	if versioned, err := isVersionedRepo(root); err != nil || versioned {
		t.Fatalf("empty = %v, %v", versioned, err)
	}
	mkdir(t, filepath.Join(root, ".git", "objects"))
	mkdir(t, filepath.Join(root, "PkgA", "main"))
	if versioned, err := isVersionedRepo(root); err != nil || !versioned {
		t.Fatalf("versioned = %v, %v", versioned, err)
	}
	writeFile(t, filepath.Join(root, "PkgB", "descriptor.json"), "{}")
	if versioned, err := isVersionedRepo(root); err != nil || versioned {
		t.Fatalf("a package with a descriptor makes it flat: %v, %v", versioned, err)
	}
}
