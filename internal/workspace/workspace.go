package workspace

// A clio workspace on disk: WorkspacePathBuilder's layout, .clio/workspaceSettings.json and
// workspaceEnvironmentSettings.json (Newtonsoft), WorkspaceCreator.Create and the MainSolution.slnx
// maintenance of SolutionCreator / WorkspaceSolutionCreator.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Layout is WorkspacePathBuilder for one root.
type Layout struct{ Root string }

func (l Layout) ClioDirectory() string { return filepath.Join(l.Root, ".clio") }
func (l Layout) SettingsPath() string {
	return filepath.Join(l.ClioDirectory(), "workspaceSettings.json")
}
func (l Layout) EnvironmentSettingsPath() string {
	return filepath.Join(l.ClioDirectory(), "workspaceEnvironmentSettings.json")
}
func (l Layout) PackagesFolder() string   { return filepath.Join(l.Root, "packages") }
func (l Layout) TestsFolder() string      { return filepath.Join(l.Root, "tests") }
func (l Layout) ProjectsFolder() string   { return filepath.Join(l.Root, "projects") }
func (l Layout) SolutionFolder() string   { return filepath.Join(l.Root, ".solution") }
func (l Layout) TasksFolder() string      { return filepath.Join(l.Root, "tasks") }
func (l Layout) MainSolutionPath() string { return filepath.Join(l.Root, "MainSolution.slnx") }

// IsWorkspace is WorkspacePathBuilder.IsWorkspace: the settings file exists.
func (l Layout) IsWorkspace() bool { return fileExists(l.SettingsPath()) }

// PackageProjectPath is BuildPackageProjectPath: packages/<name>/Files/<name>.csproj.
func (l Layout) PackageProjectPath(packageName string) string {
	return filepath.Join(l.PackagesFolder(), packageName, "Files", packageName+".csproj")
}

// FindRoot is WorkspacePathBuilder's lazy root: the nearest folder upwards from start that holds a .clio
// folder, else start itself.
func FindRoot(start string) string {
	directory := start
	for {
		if directoryExists(filepath.Join(directory, ".clio")) {
			return directory
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return start
		}
		directory = parent
	}
}

// Settings is WorkspaceSettings; ApplicationVersion is a System.Version, kept as its text.
type Settings struct {
	Packages           []string
	ApplicationVersion *string
	IgnorePackages     []string
	ExternalPackages   []string
}

// ReadSettings is Workspace.WorkspaceSettings (Newtonsoft over the settings file): an absent list member
// keeps its empty default, a null one stays null.
func (l Layout) ReadSettings() (Settings, error) {
	text, err := readAllText(l.SettingsPath())
	if err != nil {
		return Settings{}, err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(text), &raw); err != nil {
		return Settings{}, err
	}
	settings := Settings{Packages: []string{}, IgnorePackages: []string{}, ExternalPackages: []string{}}
	list := func(name string, target *[]string) error {
		value, ok := lookupFold(raw, name)
		if !ok {
			return nil
		}
		if string(value) == "null" {
			*target = nil
			return nil
		}
		return json.Unmarshal(value, target)
	}
	if err := list("Packages", &settings.Packages); err != nil {
		return Settings{}, err
	}
	if err := list("IgnorePackages", &settings.IgnorePackages); err != nil {
		return Settings{}, err
	}
	if err := list("ExternalPackages", &settings.ExternalPackages); err != nil {
		return Settings{}, err
	}
	if value, ok := lookupFold(raw, "ApplicationVersion"); ok && string(value) != "null" {
		var version string
		if err := json.Unmarshal(value, &version); err != nil {
			return Settings{}, err
		}
		settings.ApplicationVersion = &version
	}
	return settings, nil
}

// lookupFold finds a member as Newtonsoft does: the exact name first, then case-insensitively.
func lookupFold(raw map[string]json.RawMessage, name string) (json.RawMessage, bool) {
	if value, ok := raw[name]; ok {
		return value, true
	}
	for key, value := range raw {
		if strings.EqualFold(key, name) {
			return value, true
		}
	}
	return nil, false
}

func stringArray(values []string) jsonValue {
	if values == nil {
		return nil
	}
	array := jsonArray{}
	for _, value := range values {
		array = append(array, value)
	}
	return array
}

// WriteSettings is JsonConverter.SerializeObjectToFile over WorkspaceSettings.
func (l Layout) WriteSettings(settings Settings) error {
	var version jsonValue
	if settings.ApplicationVersion != nil {
		version = *settings.ApplicationVersion
	}
	text := indentedJSON(object(
		member("Packages", stringArray(settings.Packages)),
		member("ApplicationVersion", version),
		member("IgnorePackages", stringArray(settings.IgnorePackages)),
		member("ExternalPackages", stringArray(settings.ExternalPackages)),
	), newtonsoftStyle)
	return writeAllText(l.SettingsPath(), text)
}

func (l Layout) writeEnvironmentSettings(environment string) error {
	return writeAllText(l.EnvironmentSettingsPath(), indentedJSON(object(member("Environment", environment)), newtonsoftStyle))
}

// create is WorkspaceCreator.Create for a new empty workspace (no environment, no package names): the
// workspace template, .gitignore, the settings files when absent, and +x on the shell scripts.
func (l Layout) create(log *Log, versions *SDKVersions) error {
	if l.IsWorkspace() {
		message := "This operation can not execute inside existing workspace! " +
			"Workspace was detected because file exists: " + l.SettingsPath() + ". " +
			"Resolved workspace root: " + l.Root + ". " +
			"Use --force to bypass this check."
		log.Warning(message)
		return fmt.Errorf("%s", message)
	}
	// ValidateDirectory compares the root's folders with the template's by full path, which never match,
	// so it never refuses.
	if err := copyTemplateFolder("workspace", l.Root, false); err != nil {
		return err
	}
	if err := l.normalizeGitIgnore(); err != nil {
		return err
	}
	if !fileExists(l.SettingsPath()) {
		settings := Settings{Packages: []string{}, IgnorePackages: []string{}, ExternalPackages: []string{}}
		version, err := versions.Last()
		if err != nil {
			log.Warning("The workspace application version could not be resolved: " + err.Error() + " " +
				"The workspace is created without one; set ApplicationVersion in " + l.SettingsPath() + " " +
				"before running restore.")
		} else {
			text := version.threePart()
			settings.ApplicationVersion = &text
		}
		if err := l.WriteSettings(settings); err != nil {
			return err
		}
		if err := l.writeEnvironmentSettings(""); err != nil {
			return err
		}
	}
	if runtime.GOOS == "windows" {
		return nil
	}
	for _, folder := range []string{l.SolutionFolder(), l.TasksFolder()} {
		if err := makeScriptsExecutable(folder); err != nil {
			return err
		}
	}
	return nil
}

func (l Layout) normalizeGitIgnore() error {
	gitIgnore := filepath.Join(l.Root, ".gitignore")
	fallback := filepath.Join(l.Root, "gitignore.txt")
	if fileExists(gitIgnore) {
		if fileExists(fallback) {
			return os.Remove(fallback)
		}
		return nil
	}
	if fileExists(fallback) {
		return os.Rename(fallback, gitIgnore)
	}
	return nil
}

// makeScriptsExecutable is ExecutablePermissionsActualizer.Actualize: `chmod +x` on every *.sh below the
// folder, which adds the execute bits the umask allows.
func makeScriptsExecutable(folder string) error {
	return filepath.WalkDir(folder, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sh") {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		return os.Chmod(path, info.Mode()|os.FileMode(0o111&^umask()))
	})
}

// ---------------------------------------------------------------------------------------------- solutions

// solutionProject is SolutionProject: the path a solution lists, and whether it carries <Build />.
type solutionProject struct {
	path       string
	forceBuild bool
}

// addProjectsToSolution is SolutionCreator.AddProjectToSolution: the solution is created from
// workspace/MainSolution.slnx when missing (logged), each project is added unless a <Project> with the
// same path (slashes ignored; case too on Windows) exists, and the document is saved indented.
func addProjectsToSolution(log *Log, solutionPath string, projects []solutionProject) error {
	if !fileExists(solutionPath) {
		content, err := template("workspace/MainSolution.slnx")
		if err != nil {
			return err
		}
		if err := writeAllText(solutionPath, content); err != nil {
			return err
		}
		log.Info("Created solution file " + solutionPath)
	}
	content, err := readAllText(solutionPath)
	if err != nil {
		return err
	}
	document, err := parseXML(content)
	if err != nil {
		return err
	}
	solution := document.root()
	if solution == nil || solution.name != "Solution" {
		return fmt.Errorf("Solution file %s does not contain a root <Solution> node. Repair the solution and rerun the command.", solutionPath)
	}
	for _, project := range projects {
		node := findProjectNode(solution, project.path)
		if node == nil {
			node = solution.appendElement("Project")
			node.setAttribute("Path", project.path)
		}
		if project.forceBuild && node.child("Build") == nil {
			node.appendElement("Build")
		}
	}
	text, bom := document.save()
	if bom {
		text = "\uFEFF" + text
	}
	return writeAllText(solutionPath, text)
}

func findProjectNode(solution *xmlNode, path string) *xmlNode {
	wanted := strings.ReplaceAll(path, `\`, "/")
	for _, node := range solution.descendants("Project") {
		value, ok := node.attribute("Path")
		if !ok {
			continue
		}
		value = strings.ReplaceAll(value, `\`, "/")
		if value == wanted || (runtime.GOOS == "windows" && strings.EqualFold(value, wanted)) {
			return node
		}
	}
	return nil
}

// createWorkspaceSolution is WorkspaceSolutionCreator.Create: every package with Files/<name>.csproj, by
// its path relative to the workspace root, ordered by that path, added to MainSolution.slnx.
func (l Layout) createWorkspaceSolution(log *Log) error {
	entries, err := os.ReadDir(l.PackagesFolder())
	if err != nil {
		return err
	}
	var paths []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		project := l.PackageProjectPath(entry.Name())
		if fileExists(project) {
			paths = append(paths, relativePath(l.Root, project))
		}
	}
	sortCulture(paths)
	projects := make([]solutionProject, 0, len(paths))
	for _, path := range paths {
		projects = append(projects, solutionProject{path: path})
	}
	return addProjectsToSolution(log, l.MainSolutionPath(), projects)
}
