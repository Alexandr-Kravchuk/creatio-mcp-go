package workspace

// new-ui-project: clio's CreateUiProjectCommand over UiProjectCreator — the package is reused when a valid
// one is already in the workspace and created otherwise, the Angular template (current, or the legacy
// snapshot for an older Creatio version) is staged, filled in and moved to projects/<name>, wrapped in an
// .esproj registered in MainSolution.slnx with <Build />, and global.json pins the JavaScript SDK.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
)

const (
	javaScriptSDKName      = "Microsoft.VisualStudio.JavaScript.Sdk"
	javaScriptSDKVersion   = "1.0.5581896"
	devkitPackageName      = "@creatio-devkit/common"
	maxDescriptorBytes     = 1024 * 1024
	invalidExistingPackage = "Directory '%s' exists but is not a valid Creatio package: %s."
)

var (
	uiProjectNamePattern  = regexp.MustCompile(`^([0-9a-z_]+)$`)
	uiVendorPrefixPattern = regexp.MustCompile(`^[a-z]{1,50}$`)
	uiPackageNamePattern  = regexp.MustCompile(`^[A-Za-z0-9_]+$`)
	currentUITemplateFrom = dotnetVersion{8, 3, 4}
	uiTemplateExtensions  = []string{".json", ".js", ".ts", ".conf", ".config", ".scss", ".css"}
)

// UIProjectRequest is CreateUiProjectOptions as CreateUiProjectTool fills it.
type UIProjectRequest struct {
	WorkspaceDirectory string
	ProjectName        string
	PackageName        string
	VendorPrefix       *string
	Empty              bool
	CreatioVersion     string
}

// CreateUIProject runs new-ui-project in the workspace directory.
func CreateUIProject(request UIProjectRequest) creatio.CommandResult {
	log := &Log{}
	failed := false
	if strings.TrimSpace(request.ProjectName) == "" {
		log.Error("Project name is required.")
		failed = true
	}
	if request.VendorPrefix != nil && !uiVendorPrefixPattern.MatchString(*request.VendorPrefix) {
		log.Error("'Vendor Prefix' must be between 1 and 50 characters long and contain only lowercase letters and digits. You entered: '" +
			*request.VendorPrefix + "'." + newLine() +
			"See more: https://academy.creatio.com/docs/developer/front_end_development_freedom_ui/remote_module/implement_a_remote_module/overview")
		failed = true
	}
	if strings.TrimSpace(request.PackageName) == "" {
		log.Error("Package name is required.")
		failed = true
	}
	if request.PackageName != "" && !uiPackageNamePattern.MatchString(request.PackageName) {
		log.Error("'Package Name' must contain only letters, digits and underscores. You entered: '" + request.PackageName + "'. " +
			"The package name is embedded into generated build scripts and folder paths, so path " +
			"separators, quotes and other special characters are rejected.")
		failed = true
	}
	if failed {
		return log.Result(1)
	}
	creator := uiProjectCreator{log: log, layout: Layout{Root: FindRoot(request.WorkspaceDirectory)}, current: request.WorkspaceDirectory}
	if err := creator.create(request); err != nil {
		log.Error(err.Error())
		return log.Result(1)
	}
	log.Info("Done")
	return log.Result(0)
}

type uiProjectCreator struct {
	log     *Log
	layout  Layout
	current string
}

func (c uiProjectCreator) packagesPath() string {
	if c.layout.IsWorkspace() {
		return c.layout.PackagesFolder()
	}
	return filepath.Join(c.current, "packages")
}

func (c uiProjectCreator) projectsPath() string {
	if c.layout.IsWorkspace() {
		return c.layout.ProjectsFolder()
	}
	return filepath.Join(c.current, "projects")
}

func (c uiProjectCreator) create(request UIProjectRequest) error {
	name := request.ProjectName
	if !uiProjectNamePattern.MatchString(name) {
		return errors.New("Not correct project name. Use only 'snake_case' format")
	}
	projectPath := filepath.Join(c.projectsPath(), name)
	if _, err := os.Stat(projectPath); err == nil {
		return fmt.Errorf("UI project path '%s' already exists. Choose a different project name or remove the existing project explicitly.", projectPath)
	}
	legacy, err := c.legacyTemplateVersion(request.CreatioVersion)
	if err != nil {
		return err
	}
	reuse, err := c.reuseLocalPackage(request.PackageName)
	if err != nil {
		return err
	}
	if reuse {
		if err := c.addPackageIfNeeded(request.PackageName); err != nil {
			return err
		}
	} else {
		// FindExistingPackage asks Creatio for its packages only to offer a download, which a silent
		// (MCP) call always declines, so the package is created locally either way.
		creator := packageCreator{log: c.log, layout: c.layout, request: AddPackageRequest{Name: request.PackageName}}
		if err := creator.create(c.packagesPath()); err != nil {
			return err
		}
	}
	vendor := ""
	if request.VendorPrefix != nil {
		vendor = *request.VendorPrefix
	}
	folder, err := c.createProject(name, request.PackageName, vendor, request.Empty, legacy)
	if err != nil {
		return err
	}
	if c.layout.IsWorkspace() {
		if err := c.integrateEsproj(name, request.PackageName); err != nil {
			return err
		}
	}
	c.reportTemplate(name, folder, request.CreatioVersion, legacy)
	return nil
}

// legacyTemplateVersion is ResolveLegacyTemplateVersion: nil for the current template.
func (c uiProjectCreator) legacyTemplateVersion(requested string) (dotnetVersion, error) {
	if strings.TrimSpace(requested) == "" {
		return nil, nil
	}
	version, ok := parseVersionLenient(strings.TrimSpace(requested))
	if !ok {
		return nil, fmt.Errorf("Creatio version '%s' is not a valid version. Use major.minor[.build[.revision]], for example 10.0.0.", requested)
	}
	if version.compare(currentUITemplateFrom) >= 0 {
		return nil, nil
	}
	names, err := templateDirectories("ui")
	if err != nil {
		return nil, err
	}
	var best, oldest dotnetVersion
	for _, name := range names {
		candidate, ok := parseVersionLenient(name)
		if !ok {
			continue
		}
		if oldest == nil || candidate.compare(oldest) < 0 {
			oldest = candidate
		}
		if candidate.compare(version) <= 0 && (best == nil || candidate.compare(best) > 0) {
			best = candidate
		}
	}
	if best == nil {
		if oldest == nil {
			oldest = currentUITemplateFrom
		}
		return nil, fmt.Errorf("Creatio version '%s' is not supported: the oldest UI project template targets Creatio %s.", requested, oldest)
	}
	return best, nil
}

// parseVersionLenient is Version.TryParse: two to four components, each an Int32 as int.TryParse reads it
// (surrounding blanks and a leading plus allowed), none negative.
func parseVersionLenient(text string) (dotnetVersion, bool) {
	parts := strings.Split(text, ".")
	if len(parts) < 2 || len(parts) > 4 {
		return nil, false
	}
	version := make(dotnetVersion, len(parts))
	for index, part := range parts {
		part = strings.TrimSpace(part)
		part = strings.TrimPrefix(part, "+")
		if part == "" || strings.Trim(part, "0123456789") != "" || len(part) > 10 {
			return nil, false
		}
		var number int64
		fmt.Sscan(part, &number)
		if number > 2147483647 {
			return nil, false
		}
		version[index] = int(number)
	}
	return version, true
}

func (c uiProjectCreator) invalidPackage(path, reason string) error {
	return fmt.Errorf(invalidExistingPackage, path, reason)
}

// reuseLocalPackage is ReuseLocalPackageIfValid.
func (c uiProjectCreator) reuseLocalPackage(name string) (bool, error) {
	packages := c.packagesPath()
	packagePath := filepath.Join(packages, name)
	if fileExists(packagePath) {
		return false, c.invalidPackage(packagePath, "the package path is a file")
	}
	if directoryExists(packages) {
		entries, err := os.ReadDir(packages)
		if err != nil {
			return false, err
		}
		exact := false
		for _, entry := range entries {
			exact = exact || (entry.IsDir() && entry.Name() == name)
		}
		if !exact {
			for _, entry := range entries {
				if entry.IsDir() && strings.EqualFold(entry.Name(), name) {
					return false, c.invalidPackage(filepath.Join(packages, entry.Name()),
						fmt.Sprintf("package directory name '%s' does not match the requested casing '%s'", entry.Name(), name))
				}
			}
		}
	}
	if !directoryExists(packagePath) {
		return false, nil
	}
	descriptorPath := filepath.Join(packagePath, "descriptor.json")
	info, err := os.Lstat(descriptorPath)
	if err != nil || info.IsDir() {
		return false, c.invalidPackage(packagePath, "'descriptor.json' is missing")
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return false, c.invalidPackage(packagePath, "linked package descriptors are not supported")
	}
	if info.Size() > maxDescriptorBytes {
		return false, c.invalidPackage(packagePath, fmt.Sprintf("package descriptor exceeds the %d-byte size limit", maxDescriptorBytes))
	}
	data, err := os.ReadFile(descriptorPath)
	if err != nil {
		return false, err
	}
	descriptor, err := parseStrictDescriptor(bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF}))
	if err != nil {
		return false, c.invalidPackage(packagePath, "'descriptor.json' is malformed")
	}
	if descriptor == nil {
		return false, c.invalidPackage(packagePath, "'descriptor.json' does not contain a package descriptor")
	}
	if descriptor.name != name {
		return false, c.invalidPackage(packagePath, fmt.Sprintf("descriptor name '%s' does not match '%s'", descriptor.name, name))
	}
	if descriptor.uid == "" || descriptor.uid == "00000000-0000-0000-0000-000000000000" {
		return false, c.invalidPackage(packagePath, "descriptor UId is empty")
	}
	return true, nil
}

type strictDescriptor struct{ name, uid string }

var guidPattern = regexp.MustCompile(`^\{?[0-9a-fA-F]{8}-?[0-9a-fA-F]{4}-?[0-9a-fA-F]{4}-?[0-9a-fA-F]{4}-?[0-9a-fA-F]{12}\}?$`)

// parseStrictDescriptor reads descriptor.json as System.Text.Json reads PackageDescriptorDto with
// case-insensitive names: a member of the wrong JSON kind is a JsonException; nil when Descriptor is absent.
func parseStrictDescriptor(data []byte) (*strictDescriptor, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, err
	}
	raw, ok := lookupFold(root, "Descriptor")
	if !ok || string(raw) == "null" {
		return nil, nil
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(raw, &members); err != nil {
		return nil, err
	}
	descriptor := &strictDescriptor{}
	text := func(name string) (string, error) {
		value, ok := lookupFold(members, name)
		if !ok || string(value) == "null" {
			return "", nil
		}
		var result string
		return result, json.Unmarshal(value, &result)
	}
	var err error
	for _, name := range []string{"PackageVersion", "ProjectPath", "ModifiedOnUtc", "Maintainer"} {
		if _, err = text(name); err != nil {
			return nil, err
		}
	}
	if descriptor.name, err = text("Name"); err != nil {
		return nil, err
	}
	if value, ok := lookupFold(members, "UId"); ok {
		var uid string
		if err := json.Unmarshal(value, &uid); err != nil || !guidPattern.MatchString(uid) {
			return nil, errors.New("UId is not a GUID")
		}
		descriptor.uid = strings.ToLower(strings.Trim(uid, "{}"))
	}
	for _, name := range []string{"Type", "InstallBehavior"} {
		if value, ok := lookupFold(members, name); ok && string(value) != "null" {
			var number int32
			if err := json.Unmarshal(value, &number); err != nil {
				return nil, err
			}
		}
	}
	if value, ok := lookupFold(members, "DependsOn"); ok && string(value) != "null" {
		var dependencies []map[string]json.RawMessage
		if err := json.Unmarshal(value, &dependencies); err != nil {
			return nil, err
		}
	}
	return descriptor, nil
}

// addPackageIfNeeded is Workspace.AddPackageIfNeeded.
func (c uiProjectCreator) addPackageIfNeeded(name string) error {
	if !c.layout.IsWorkspace() {
		return nil
	}
	settings, err := c.layout.ReadSettings()
	if err != nil {
		return err
	}
	for _, existing := range settings.Packages {
		if existing == name {
			return nil
		}
	}
	settings.Packages = append(settings.Packages, name)
	if err := c.layout.WriteSettings(settings); err != nil {
		return err
	}
	return c.layout.createWorkspaceSolution(c.log)
}

// uiDistPath is BuildDistPath: Path.Combine of parts that all end in "/", so the same on every OS.
func uiDistPath(packageName, projectName string) string {
	return "../../packages/" + packageName + "/Files/src/js/" + projectName
}

// createProject stages the template under projects/.<name>.<guid>.tmp, fills in its macros and moves it to
// projects/<name>; the staging folder is removed when anything fails.
func (c uiProjectCreator) createProject(name, packageName, vendor string, empty bool, legacy dotnetVersion) (string, error) {
	projects := c.projectsPath()
	if err := mkdirAll(projects); err != nil {
		return "", err
	}
	staging := filepath.Join(projects, "."+name+"."+strings.ReplaceAll(newGUID(), "-", "")+".tmp")
	folder := "ui-project"
	if empty {
		folder = "ui-project-Empty"
	}
	err := func() error {
		source := folder
		if legacy != nil {
			source = "ui/" + legacy.String() + "/" + folder
		}
		if err := copyTemplateFolder(source, staging, true); err != nil {
			return err
		}
		if err := filepath.WalkDir(staging, func(path string, entry os.DirEntry, err error) error {
			if err != nil || entry.IsDir() || !hasTemplateExtension(path) {
				return err
			}
			content, err := readAllText(path)
			if err != nil {
				return err
			}
			content = replaceFold(content, "<%vendorPrefix%>", vendor)
			content = replaceFold(content, "<%projectName%>", name)
			content = replaceFold(content, "<%distPath%>", uiDistPath(packageName, name))
			return writeAllText(path, content)
		}); err != nil {
			return err
		}
		return os.Rename(staging, filepath.Join(projects, name))
	}()
	if err != nil {
		os.RemoveAll(staging)
		return "", err
	}
	return folder, nil
}

func hasTemplateExtension(path string) bool {
	lower := strings.ToLower(path)
	for _, extension := range uiTemplateExtensions {
		if strings.HasSuffix(lower, extension) {
			return true
		}
	}
	return false
}

// replaceFold is String.Replace(old, new, ignoreCase: true, InvariantCulture) for an ASCII pattern.
func replaceFold(text, old, replacement string) string {
	lowerText, lowerOld := strings.ToLower(text), strings.ToLower(old)
	if len(lowerText) != len(text) {
		return strings.ReplaceAll(text, old, replacement)
	}
	var builder strings.Builder
	for {
		index := strings.Index(lowerText, lowerOld)
		if index < 0 {
			builder.WriteString(text)
			return builder.String()
		}
		builder.WriteString(text[:index])
		builder.WriteString(replacement)
		text, lowerText = text[index+len(old):], lowerText[index+len(old):]
	}
}

// integrateEsproj writes projects/<name>/<name>.esproj, pins the JavaScript SDK in global.json and adds
// the .esproj to MainSolution.slnx with <Build />.
func (c uiProjectCreator) integrateEsproj(name, packageName string) error {
	esproj := filepath.Join(c.projectsPath(), name, name+".esproj")
	content, err := template("esproj")
	if err != nil {
		return err
	}
	content = strings.ReplaceAll(content, "<%projectName%>", name)
	content = strings.ReplaceAll(content, "<%distPath%>", uiDistPath(packageName, name))
	if err := writeAllText(esproj, content); err != nil {
		return err
	}
	if err := pinJavaScriptSDK(filepath.Join(c.layout.Root, "global.json")); err != nil {
		return err
	}
	return addProjectsToSolution(c.log, c.layout.MainSolutionPath(), []solutionProject{
		{path: relativePath(c.layout.Root, esproj), forceBuild: true},
	})
}

// pinJavaScriptSDK is EnsureJavaScriptSdkPinnedInGlobalJson: global.json read as a JsonObject (anything
// else starts empty), msbuild-sdks set, written by JsonNode.ToJsonString with WriteIndented.
func pinJavaScriptSDK(path string) error {
	root := object()
	if fileExists(path) {
		text, err := readAllText(path)
		if err != nil {
			return err
		}
		parsed, err := parseJSONNode([]byte(text))
		if err != nil {
			return err
		}
		if typed, ok := parsed.(*jsonObject); ok {
			root = typed
		}
	}
	sdks, ok := root.get("msbuild-sdks").(*jsonObject)
	if !ok {
		sdks = object()
		root.set("msbuild-sdks", sdks)
	}
	sdks.set(javaScriptSDKName, javaScriptSDKVersion)
	return writeAllText(path, indentedJSON(root, systemTextJSONStyle))
}

// reportTemplate is ReportSelectedTemplate.
func (c uiProjectCreator) reportTemplate(name, folder, requested string, legacy dotnetVersion) {
	version := strings.TrimSpace(requested)
	if version == "" {
		version = "not specified"
	}
	devkit := devkitRange(filepath.Join(c.projectsPath(), name, "package.json"))
	if legacy == nil {
		c.log.Info(fmt.Sprintf("UI project template: %s (current template, targets Creatio %s and later); requested Creatio version: %s; %s: %s.",
			folder, currentUITemplateFrom, version, devkitPackageName, devkit))
		return
	}
	c.log.Info(fmt.Sprintf("UI project template: ui/%s/%s (legacy template for Creatio %s); requested Creatio version: %s; %s: %s.",
		legacy, folder, legacy, version, devkitPackageName, devkit))
}

func devkitRange(packageJSON string) string {
	text, err := readAllText(packageJSON)
	if err != nil {
		return "unknown"
	}
	var root struct {
		Dependencies map[string]json.RawMessage `json:"dependencies"`
	}
	if json.Unmarshal([]byte(text), &root) != nil {
		return "unknown"
	}
	var value string
	if json.Unmarshal(root.Dependencies[devkitPackageName], &value) != nil || strings.TrimSpace(value) == "" {
		return "unknown"
	}
	return value
}
