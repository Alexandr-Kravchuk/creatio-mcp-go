package workspace

// link-from-repository-by-environment, link-from-repository-by-env-package-path and
// link-from-repository-unlocked: clio's Link4RepoCommand with Link4RepoOptionsValidator,
// RfsEnvironment.Link4Repo and PackageUtilities.GetPackageContentFolderPath, as LinkFromRepositoryTool runs it.
// A package folder in the environment's Pkg folder is replaced by a symbolic link to the package's folder in
// the repository. When a named package is missing or has no descriptor.json in Pkg, the command first
// prepares the site: one Maintainer for all packages, the packages unlocked, and the packages loaded to the
// file system (2fs). The --unlocked flow asks the site which packages are unlocked and links those.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"runtime"
	"strings"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
)

// LinkRequest is Link4RepoOptions as the tools fill it.
type LinkRequest struct {
	Environment     string
	EnvPkgPath      string
	RepoPath        string
	Packages        string
	Unlocked        bool
	DryRun          bool
	SkipPreparation bool
}

// LinkEnvironment is what the command reads of a registered environment.
type LinkEnvironment struct {
	EnvironmentPath string
	IsNetCore       bool
	URI             string
}

// LinkSite is the Creatio site the command prepares and queries; *creatio.Client's WorkspaceLinkSite is one.
type LinkSite interface {
	SysSettingValue(ctx context.Context, code string) (string, error)
	UpdateSysSetting(ctx context.Context, code, value string) (bool, error)
	UnlockPackages(ctx context.Context, names []string) error
	LoadPackagesToFileSystem(ctx context.Context) ([]creatio.LogMessage, string, error)
	UnlockedPackages(ctx context.Context) ([]creatio.WorkspaceLinkPackage, error)
}

// LinkDeps are the command's collaborators.
type LinkDeps struct {
	// Environment is ISettingsRepository.IsEnvironmentExists plus GetEnvironment: false for a name that is not
	// registered.
	Environment func(name string) (LinkEnvironment, bool)
	// Site connects to the site at the command's first remote call. environment is the registered
	// environment the call named and the Pkg folder was resolved through; it is "" when the call named none
	// (an --envPkgPath call, or a folder path given as the environment name). Its error is reported as the
	// call's failure.
	Site func(ctx context.Context, environment string) (LinkSite, error)
}

// Link runs Link4RepoCommand.Execute.
func Link(ctx context.Context, request LinkRequest, deps LinkDeps) creatio.CommandResult {
	log := &Log{}
	if failures := validateLink(request); len(failures) > 0 {
		for _, failure := range failures {
			log.Error("ERROR (ArgumentParse.Error) - " + failure)
		}
		return log.Result(1)
	}
	run := &linker{ctx: ctx, log: log, request: request, deps: deps}
	code, err := run.execute()
	if err != nil {
		log.Error("Error during linking: " + err.Error())
		return log.Result(1)
	}
	return log.Result(code)
}

func blank(text string) bool { return strings.TrimSpace(text) == "" }

// validateLink is Link4RepoOptionsValidator: both rules run, in order.
func validateLink(request LinkRequest) []string {
	var failures []string
	if blank(request.EnvPkgPath) && blank(request.Environment) {
		failures = append(failures, "Either path to creatio directory or environment name must be provided")
	}
	if request.Unlocked && !blank(request.Packages) {
		return append(failures, "--unlocked and --packages are mutually exclusive; use one or the other")
	}
	if request.Unlocked && blank(request.Environment) {
		failures = append(failures, "When --unlocked is set, environment name (-e) or URI (-u) must be provided for API connection")
	}
	if !request.Unlocked && blank(request.Packages) {
		failures = append(failures, "Either --packages or --unlocked must be specified")
	}
	return failures
}

type linker struct {
	ctx     context.Context
	log     *Log
	request LinkRequest
	deps    LinkDeps
	site    LinkSite
}

// remote is the site, connected at the first call that needs it.
func (l *linker) remote() (LinkSite, error) {
	if l.site != nil {
		return l.site, nil
	}
	if l.deps.Site == nil {
		return nil, fmt.Errorf("no Creatio site is available")
	}
	named := ""
	if name := l.request.Environment; !blank(name) {
		if _, isPath := resolveDirectoryPath(name); !isPath {
			if _, ok := l.environment(name); ok {
				named = name
			}
		}
	}
	site, err := l.deps.Site(l.ctx, named)
	if err != nil {
		return nil, err
	}
	l.site = site
	return site, nil
}

func (l *linker) environment(name string) (LinkEnvironment, bool) {
	if blank(name) || l.deps.Environment == nil {
		return LinkEnvironment{}, false
	}
	return l.deps.Environment(name)
}

func (l *linker) execute() (int, error) {
	request := l.request
	if request.Unlocked {
		envPkgPath, ok := l.resolveEnvPkgPath()
		if !ok || blank(envPkgPath) {
			l.log.Error("Could not resolve environment packages path.")
			return 1, nil
		}
		if request.DryRun {
			return l.unlockedDryRun()
		}
		return l.unlockedLinking(envPkgPath)
	}
	if !blank(request.Packages) {
		if code, err := l.tryPreparePackages(); err != nil || code != 0 {
			return code, err
		}
	}
	if request.DryRun {
		l.log.Info("[dry-run] Skipping symlink creation.")
		return 0, nil
	}
	switch {
	case !blank(request.Environment):
		if resolved, ok := resolveDirectoryPath(request.Environment); ok {
			return l.linkWithDirPath(resolved, request.RepoPath, request.Packages)
		}
		return l.linkByEnvironmentName(request.Environment)
	case !blank(request.EnvPkgPath):
		if resolved, ok := resolveDirectoryPath(request.EnvPkgPath); ok {
			return l.linkWithDirPath(resolved, request.RepoPath, request.Packages)
		}
		return l.linkByEnvironmentName(request.EnvPkgPath)
	}
	return 1, nil
}

// resolveDirectoryPath is TryResolveDirectoryPath: a file URI's local path, or the full path of a value that
// looks like a path (rooted, starting with a dot, holding a separator, or naming an existing folder).
func resolveDirectoryPath(value string) (string, bool) {
	if blank(value) {
		return "", false
	}
	if local, ok := fileURIPath(value); ok {
		return local, true
	}
	looksLikePath := isPathRooted(value) || strings.HasPrefix(value, ".") || strings.ContainsAny(value, `/\`) ||
		directoryExists(value)
	if !looksLikePath {
		return "", false
	}
	return fullPath(value), true
}

// fileURIPath is Uri.TryCreate(value, UriKind.Absolute) with IsFile: an explicit file:// URI, or a rooted
// path, which .NET reads as an implicit file URI. Its LocalPath has the dot segments resolved.
func fileURIPath(value string) (string, bool) {
	if len(value) >= 7 && strings.EqualFold(value[:7], "file://") {
		parsed, err := url.Parse(value)
		if err != nil {
			return "", false
		}
		local := parsed.Path
		if runtime.GOOS == "windows" {
			local = strings.TrimPrefix(local, "/")
			if parsed.Host != "" {
				local = `\\` + parsed.Host + "/" + local
			}
			local = strings.ReplaceAll(local, "/", `\`)
		} else if parsed.Host != "" {
			local = "//" + parsed.Host + local
		}
		return local, true
	}
	if IsFullyQualified(value) {
		return fullPath(value), true
	}
	return "", false
}

// resolveEnvPkgPath is ResolveEnvPkgPath: --envPkgPath first (as given when it does not look like a path),
// then the environment name as a path, then the registered environment's package folder. ok is false when
// nothing resolved (clio's null).
func (l *linker) resolveEnvPkgPath() (string, bool) {
	request := l.request
	if !blank(request.EnvPkgPath) {
		if resolved, ok := resolveDirectoryPath(request.EnvPkgPath); ok {
			return resolved, true
		}
		return request.EnvPkgPath, true
	}
	if !blank(request.Environment) {
		if resolved, ok := resolveDirectoryPath(request.Environment); ok {
			return resolved, true
		}
		if environment, ok := l.environment(request.Environment); ok {
			path := environmentPackagePath(environment)
			return path, path != ""
		}
	}
	return "", false
}

// environmentPackagePath is ResolveEnvironmentPackagePath: the first existing Pkg folder under
// EnvironmentPath, the .NET Core layout first for a .NET Core environment.
func environmentPackagePath(environment LinkEnvironment) string {
	if blank(environment.EnvironmentPath) {
		return ""
	}
	root := environment.EnvironmentPath
	core := combinePath(combinePath(root, "Terrasoft.Configuration"), "Pkg")
	framework := combinePath(combinePath(combinePath(root, "Terrasoft.WebApp"), "Terrasoft.Configuration"), "Pkg")
	candidates := []string{framework, core}
	if environment.IsNetCore {
		candidates = []string{core, framework}
	}
	for _, candidate := range candidates {
		if directoryExists(candidate) {
			return candidate
		}
	}
	return ""
}

// linkByEnvironmentName is HandleLinkingByEnvName.
func (l *linker) linkByEnvironmentName(name string) (int, error) {
	environment, ok := l.environment(name)
	if !ok {
		l.log.Error(fmt.Sprintf("Environment %s is not a registered environment. Please correct environment name an try again.", name))
		return 1, nil
	}
	if path := environmentPackagePath(environment); path != "" {
		return l.linkWithDirPath(path, l.request.RepoPath, l.request.Packages)
	}
	if runtime.GOOS != "windows" {
		l.log.Error(fmt.Sprintf("Environment %s does not have a valid local package path. Configure EnvironmentPath or use --envPkgPath with the direct package folder path.", name))
		return 1, nil
	}
	// clio looks the site up among the Creatio sites registered in IIS; this server does not scan IIS, so
	// it answers as clio does when IIS holds no site with the environment's address.
	if parsed, err := url.Parse(environment.URI); err != nil || !parsed.IsAbs() || parsed.Host == "" {
		l.log.Error(fmt.Sprintf("Environment %s missing Url. Please correct environment name an try again.", name))
		return 1, nil
	}
	l.log.Error(fmt.Sprintf("Environment %s not found. Please check the environment name and try again.", name))
	return 1, nil
}

// linkWithDirPath is HandleLinkWithDirPath.
func (l *linker) linkWithDirPath(sitePath, repoPath, packages string) (int, error) {
	if err := link4Repo(sitePath, repoPath, packages); err != nil {
		return 1, err
	}
	l.log.Info(fmt.Sprintf("Linking repository package(s) to environment %s from %s", sitePath, repoPath))
	return 0, nil
}

// ---------------------------------------------------------------------------------------------- preparation

// tryPreparePackages is TryPreparePackages: preparation runs only when a named package is missing or has no
// descriptor.json in an existing Pkg folder.
func (l *linker) tryPreparePackages() (int, error) {
	request := l.request
	if request.SkipPreparation {
		l.log.Info("Skipping preparation (--skip-preparation).")
		return 0, nil
	}
	envPkgPath, ok := l.resolveEnvPkgPath()
	if !ok || blank(envPkgPath) || !directoryExists(envPkgPath) {
		return 0, nil
	}
	var names []string
	if request.Packages != "*" {
		for _, part := range strings.Split(request.Packages, ",") {
			if trimmed := dotnetTrim(part); trimmed != "" {
				names = append(names, trimmed)
			}
		}
	}
	if len(names) == 0 {
		return 0, nil
	}
	incomplete := false
	for _, name := range names {
		if packageIncomplete(envPkgPath, name) {
			incomplete = true
			break
		}
	}
	if !incomplete {
		return 0, nil
	}
	l.log.Info("Some packages are missing or incomplete in Pkg folder — running preparation...")
	return l.preparePackages(request.RepoPath, names, request.DryRun)
}

// dotnetTrim is String.Trim(): leading and trailing white space.
func dotnetTrim(text string) string { return strings.TrimSpace(text) }

// packageIncomplete is IsPackageIncomplete: no package folder, or no descriptor.json in it.
func packageIncomplete(envPkgPath, name string) bool {
	folder := combinePath(envPkgPath, name)
	if !directoryExists(folder) {
		return true
	}
	return !fileExists(combinePath(folder, "descriptor.json"))
}

// repositoryPackagesFolder is the repository's packages folder when it has one, else the repository itself.
func repositoryPackagesFolder(repoPath string) string {
	packages := combinePath(repoPath, "packages")
	if directoryExists(packages) {
		return packages
	}
	return repoPath
}

// repoPackageMaintainer is ReadRepoPackageMaintainer: Descriptor.Maintainer of the package's descriptor.json,
// "" when the file is missing.
func repoPackageMaintainer(repoPath, name string) (string, error) {
	path := combinePath(combinePath(repositoryPackagesFolder(repoPath), name), "descriptor.json")
	if !fileExists(path) {
		return "", nil
	}
	text, err := readAllText(path)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(text) == "" {
		return "", nil
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal([]byte(text), &root); err != nil {
		return "", fmt.Errorf("Error reading JSON from %s: %s", path, err.Error())
	}
	descriptorRaw, ok := lookupFold(root, "Descriptor")
	if !ok || string(descriptorRaw) == "null" {
		return "", nil
	}
	var descriptor map[string]json.RawMessage
	if err := json.Unmarshal(descriptorRaw, &descriptor); err != nil {
		return "", fmt.Errorf("Error reading JSON from %s: %s", path, err.Error())
	}
	raw, ok := lookupFold(descriptor, "Maintainer")
	if !ok {
		return "", nil
	}
	return newtonsoftString(raw), nil
}

// newtonsoftString is how Json.NET reads a JSON value into a string property: a string as is, true/false as
// True/False, a number as its text, null as null.
func newtonsoftString(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	switch strings.TrimSpace(string(raw)) {
	case "null":
		return ""
	case "true":
		return "True"
	case "false":
		return "False"
	}
	return strings.TrimSpace(string(raw))
}

// preparePackages is PreparePackagesForLinking.
func (l *linker) preparePackages(repoPath string, names []string, dryRun bool) (int, error) {
	type entry struct{ name, maintainer string }
	var maintainers []entry
	index := map[string]int{}
	for _, name := range names {
		maintainer, err := repoPackageMaintainer(repoPath, name)
		if err != nil {
			return 1, err
		}
		if blank(maintainer) {
			l.log.Warning(fmt.Sprintf("Could not read Maintainer from descriptor.json for package '%s' in repo — skipping preparation", name))
			continue
		}
		if position, ok := index[name]; ok {
			maintainers[position].maintainer = maintainer
			continue
		}
		index[name] = len(maintainers)
		maintainers = append(maintainers, entry{name: name, maintainer: maintainer})
	}
	if len(maintainers) == 0 {
		l.log.Warning("No package descriptors with Maintainer found in repository — skipping preparation")
		return 0, nil
	}
	var distinct []string
	for _, item := range maintainers {
		known := false
		for _, value := range distinct {
			if ordinalIgnoreCaseEqual(value, item.maintainer) {
				known = true
				break
			}
		}
		if !known {
			distinct = append(distinct, item.maintainer)
		}
	}
	if len(distinct) > 1 {
		pairs := make([]string, 0, len(maintainers))
		for _, item := range maintainers {
			pairs = append(pairs, fmt.Sprintf("'%s'='%s'", item.name, item.maintainer))
		}
		l.log.Error("Packages have different Maintainer values: " + strings.Join(pairs, ", ") + ". " +
			"All packages must have the same Maintainer.")
		return 1, nil
	}
	required := distinct[0]
	site, err := l.remote()
	if err != nil {
		return 1, err
	}
	current, err := site.SysSettingValue(l.ctx, "Maintainer")
	if err != nil {
		return 1, err
	}
	needsUpdate := !ordinalIgnoreCaseEqual(current, required)
	l.log.Warning("Preparation required:")
	if needsUpdate {
		l.log.Warning(fmt.Sprintf("  - SysSetting 'Maintainer' will change: '%s' -> '%s'", current, required))
	}
	list := strings.Join(names, ", ")
	l.log.Warning(fmt.Sprintf("  - %d package(s) will be unlocked: %s", len(names), list))
	l.log.Warning("  - Packages will be synced to file system (2fs)")
	if dryRun {
		l.log.Info("[dry-run] No changes applied.")
		return 0, nil
	}
	l.log.Info("Proceeding...")
	if needsUpdate {
		l.log.Info(fmt.Sprintf("Updating SysSetting 'Maintainer' from '%s' to '%s'", current, required))
		updated, err := site.UpdateSysSetting(l.ctx, "Maintainer", required)
		if err != nil {
			return 1, err
		}
		if !updated {
			l.log.Error("Failed to update Maintainer sys setting.")
			return 1, nil
		}
	}
	l.log.Info("Unlocking packages: " + list)
	if err := site.UnlockPackages(l.ctx, names); err != nil {
		return 1, err
	}
	l.log.Info("Loading packages to file system (2fs)...")
	messages, outcome, err := site.LoadPackagesToFileSystem(l.ctx)
	l.log.Append(messages)
	if err != nil {
		return 1, err
	}
	switch outcome {
	case "completed":
		l.log.Info("Packages synced to file system successfully.")
	case "disabled":
		l.log.Warning("Load packages to file system on a web application ended with error: disabled file design mode")
		l.log.Warning("The packages were unlocked and 'Maintainer' was applied, and the links are still created, " +
			"but the environment exported nothing to its file system. Run 'clio turn-fsm on' and then " +
			"'clio pkg-to-file-system' to finish the export.")
	default:
		l.log.Error("Packages were not synced to the file system, so no links were created. The packages were " +
			"unlocked and 'Maintainer' was applied. Fix the reported error and run " +
			"'clio link-to-repository' again.")
		return 1, nil
	}
	return 0, nil
}

// ordinalIgnoreCaseEqual is String.Equals with StringComparison.OrdinalIgnoreCase.
func ordinalIgnoreCaseEqual(left, right string) bool {
	return strings.ToUpper(left) == strings.ToUpper(right)
}

// ---------------------------------------------------------------------------------------------- unlocked

func (l *linker) unlockedPackages() ([]creatio.WorkspaceLinkPackage, error) {
	l.log.Info("Querying Creatio site for unlocked packages...")
	site, err := l.remote()
	if err != nil {
		return nil, err
	}
	return site.UnlockedPackages(l.ctx)
}

func packageNames(packages []creatio.WorkspaceLinkPackage) string {
	names := make([]string, 0, len(packages))
	for _, item := range packages {
		names = append(names, item.Name)
	}
	return strings.Join(names, ", ")
}

// repositoryFolderNames is the names of the repository's package folders, matched case-insensitively.
func repositoryFolderNames(repoPath string) (map[string]bool, error) {
	folders, err := listDirectories(repositoryPackagesFolder(repoPath))
	if err != nil {
		return nil, err
	}
	names := map[string]bool{}
	for _, folder := range folders {
		names[strings.ToUpper(folder)] = true
	}
	return names, nil
}

// unlockedDryRun is HandleUnlockedDryRun.
func (l *linker) unlockedDryRun() (int, error) {
	packages, err := l.unlockedPackages()
	if err != nil {
		return 1, err
	}
	if len(packages) == 0 {
		l.log.Info("[dry-run] No unlocked packages found on the site.")
		return 0, nil
	}
	l.log.Info(fmt.Sprintf("[dry-run] Found %d unlocked package(s) on site: %s", len(packages), packageNames(packages)))
	repoPath := l.request.RepoPath
	if !directoryExists(repoPath) {
		l.log.Error("Repository path does not exist: " + repoPath)
		return 1, nil
	}
	versioned, err := isVersionedRepo(repoPath)
	if err != nil {
		return 1, err
	}
	structure := "flat (PackageName/descriptor.json)"
	if versioned {
		structure = "versioned (PackageName/branch/version)"
	}
	l.log.Info("[dry-run] Repository structure: " + structure)
	if versioned {
		for _, item := range packages {
			versionPath, err := findPackageVersionPath(repoPath, item.Name, item.Version)
			if err != nil {
				return 1, err
			}
			if versionPath == "" {
				l.log.Warning(fmt.Sprintf("[dry-run]   '%s' (v%s) — not found in repository, would skip", item.Name, item.Version))
			} else {
				l.log.Info(fmt.Sprintf("[dry-run]   '%s' (v%s) — would link from %s", item.Name, item.Version, versionPath))
			}
		}
	} else {
		names, err := repositoryFolderNames(repoPath)
		if err != nil {
			return 1, err
		}
		for _, item := range packages {
			if names[strings.ToUpper(item.Name)] {
				l.log.Info(fmt.Sprintf("[dry-run]   '%s' — found in repository, would link", item.Name))
			} else {
				l.log.Warning(fmt.Sprintf("[dry-run]   '%s' — not found in repository, would skip", item.Name))
			}
		}
	}
	l.log.Info("[dry-run] No changes applied.")
	return 0, nil
}

// unlockedLinking is HandleUnlockedLinking.
func (l *linker) unlockedLinking(envPkgPath string) (int, error) {
	packages, err := l.unlockedPackages()
	if err != nil {
		return 1, err
	}
	if len(packages) == 0 {
		l.log.Info("No unlocked packages found on the site.")
		return 0, nil
	}
	l.log.Info(fmt.Sprintf("Found %d unlocked package(s): %s", len(packages), packageNames(packages)))
	repoPath := l.request.RepoPath
	if !directoryExists(repoPath) {
		l.log.Error("Repository path does not exist: " + repoPath)
		return 1, nil
	}
	versioned, err := isVersionedRepo(repoPath)
	if err != nil {
		return 1, err
	}
	if versioned {
		return l.unlockedVersionedRepo(envPkgPath, repoPath, packages)
	}
	return l.unlockedFlatRepo(envPkgPath, repoPath, packages)
}

// unlockedFlatRepo is HandleUnlockedFlatRepo: the unlocked packages found in the repository (by name,
// case-insensitively) are linked as one --packages list.
func (l *linker) unlockedFlatRepo(envPkgPath, repoPath string, packages []creatio.WorkspaceLinkPackage) (int, error) {
	names, err := repositoryFolderNames(repoPath)
	if err != nil {
		return 1, err
	}
	var toLink []string
	for _, item := range packages {
		if names[strings.ToUpper(item.Name)] {
			toLink = append(toLink, item.Name)
		} else {
			l.log.Warning(fmt.Sprintf("Unlocked package '%s' not found in repository — skipping", item.Name))
		}
	}
	if len(toLink) == 0 {
		l.log.Warning("No unlocked packages found in the repository.")
		return 0, nil
	}
	return l.linkWithDirPath(envPkgPath, repoPath, strings.Join(toLink, ","))
}

// unlockedVersionedRepo is HandleUnlockedVersionedRepo: each package is linked to the first branch folder
// holding its version.
func (l *linker) unlockedVersionedRepo(envPkgPath, repoPath string, packages []creatio.WorkspaceLinkPackage) (int, error) {
	linked, skipped := 0, 0
	for _, item := range packages {
		versionPath, err := findPackageVersionPath(repoPath, item.Name, item.Version)
		if err != nil {
			return 1, err
		}
		if versionPath == "" {
			l.log.Warning(fmt.Sprintf("Unlocked package '%s' (v%s) not found in repository — skipping", item.Name, item.Version))
			skipped++
			continue
		}
		envPackagePath := combinePath(envPkgPath, item.Name)
		if directoryExists(envPackagePath) {
			l.log.Warning(fmt.Sprintf("Existing directory '%s' will be replaced with symlink", envPackagePath))
			if err := deleteDirectory(envPackagePath); err != nil {
				return 1, err
			}
		}
		if err := createDirectorySymlink(envPackagePath, versionPath); err != nil {
			return 1, err
		}
		l.log.Info(fmt.Sprintf("Linked '%s' (v%s) → %s", item.Name, item.Version, versionPath))
		linked++
	}
	l.log.Info(fmt.Sprintf("Linking completed: %d linked, %d skipped", linked, skipped))
	return 0, nil
}

// isVersionedRepo is IsVersionedRepo: no folder (other than dot folders) holds a descriptor.json, and the
// first one the file system lists has sub-folders.
func isVersionedRepo(repoPath string) (bool, error) {
	folders, err := listDirectories(repoPath)
	if err != nil {
		return false, err
	}
	var packageFolders []string
	for _, folder := range folders {
		if !strings.HasPrefix(folder, ".") {
			packageFolders = append(packageFolders, childPath(repoPath, folder))
		}
	}
	if len(packageFolders) == 0 {
		return false, nil
	}
	for _, folder := range packageFolders {
		if fileExists(combinePath(folder, "descriptor.json")) {
			return false, nil
		}
	}
	children, err := listDirectories(packageFolders[0])
	if err != nil {
		return false, err
	}
	return len(children) > 0, nil
}

// findPackageVersionPath is FindPackageVersionPath: repo/<name>/<branch>/<version> for the first branch, in
// the file system's order, that has the version; "" when none does.
func findPackageVersionPath(repoPath, name, version string) (string, error) {
	packageFolder := combinePath(repoPath, name)
	if !directoryExists(packageFolder) {
		return "", nil
	}
	branches, err := listDirectories(packageFolder)
	if err != nil {
		return "", err
	}
	for _, branch := range branches {
		versionPath := combinePath(childPath(packageFolder, branch), version)
		if directoryExists(versionPath) {
			return versionPath, nil
		}
	}
	return "", nil
}

// ---------------------------------------------------------------------------------------------- linking

// link4Repo is RfsEnvironment.Link4Repo: every named package (all repository packages for "*") must be in
// the repository; each one's Pkg folder, when there is one, is deleted and replaced by a link to the
// package's content folder in the repository.
func link4Repo(environmentPackagePath, repositoryPath, packages string) error {
	if packages == "" {
		return fmt.Errorf("At least one package must be specified or use '*' to include all packages. " +
			"Multiple packages can be separated by comma.")
	}
	var names []string
	if packages == "*" {
		folders, err := listDirectories(repositoryPackagesFolder(repositoryPath))
		if err != nil {
			return err
		}
		names = folders
	} else {
		for _, part := range strings.Split(packages, ",") {
			names = append(names, dotnetTrim(part))
		}
	}
	environmentFolders, err := listDirectories(environmentPackagePath)
	if err != nil {
		return err
	}
	repositoryRoot := repositoryPackagesFolder(repositoryPath)
	repositoryFolders, err := listDirectories(repositoryRoot)
	if err != nil {
		return err
	}
	inRepository := map[string]bool{}
	for _, folder := range repositoryFolders {
		inRepository[folder] = true
	}
	var missing []string
	for _, name := range names {
		if !inRepository[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("Packages %s not found in repository: %s.", strings.Join(missing, ", "), repositoryPath)
	}
	inEnvironment := map[string]bool{}
	for _, folder := range environmentFolders {
		inEnvironment[folder] = true
	}
	environmentFull := fullPath(environmentPackagePath)
	repositoryFull := fullPath(repositoryRoot)
	for _, name := range names {
		linkPath := combinePath(environmentPackagePath, name)
		if inEnvironment[name] {
			linkPath = childPath(environmentFull, name)
			if err := deleteDirectory(linkPath); err != nil {
				return err
			}
		}
		target, err := packageContentFolder(childPath(repositoryFull, name))
		if err != nil {
			return err
		}
		if err := createDirectorySymlink(linkPath, target); err != nil {
			return err
		}
	}
	return nil
}

// packageContentFolder is PackageUtilities.GetPackageContentFolderPath: the one folder under branches/ when
// the package has a branches folder, else the package folder itself.
func packageContentFolder(packageFolder string) (string, error) {
	branches := combinePath(packageFolder, "branches")
	if !directoryExists(branches) {
		return packageFolder, nil
	}
	folders, err := listDirectories(branches)
	if err != nil {
		return "", err
	}
	if len(folders) == 1 {
		return childPath(fullPath(branches), folders[0]), nil
	}
	return "", fmt.Errorf("Unsupported package folder structure.Expected structure contains one package version in folder '%s'.", branches)
}
