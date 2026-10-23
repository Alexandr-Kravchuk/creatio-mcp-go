package workspace

// new-test-project (CreateTestProjectCommand) and new-integration-test-project
// (CreateIntegrationTestProjectCommand): project templates written under tests/ and registered in
// tests/UnitTests.slnx or tests/IntegrationTests.slnx and in MainSolution.slnx.

import (
	"path/filepath"
	"regexp"
	"strings"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
)

const notAWorkspace = "Current directory is not a clio workspace. Run the command from a workspace or pass a valid workspace path."

// TestProjectRequest is CreateTestProjectArgs / CreateIntegrationTestProjectArgs.
type TestProjectRequest struct {
	PackageName     *string
	WorkspacePath   string
	TargetFramework string
	// WorkingDirectory is the process directory, the workspace when WorkspacePath is blank.
	WorkingDirectory string
}

func (r TestProjectRequest) layout() Layout {
	if strings.TrimSpace(r.WorkspacePath) != "" {
		return Layout{Root: r.WorkspacePath}
	}
	return Layout{Root: FindRoot(r.WorkingDirectory)}
}

// validationLine is Command.PrintErrors for one FluentValidation failure.
func validationLine(log *Log, code, message string) {
	log.Line("ERROR (" + code + ") - " + message)
}

// CreateTestProject runs new-test-project.
func CreateTestProject(request TestProjectRequest) creatio.CommandResult {
	log := &Log{}
	layout := request.layout()
	if !layout.IsWorkspace() {
		log.Error(notAWorkspace)
		return log.Result(1)
	}
	if request.PackageName == nil || *request.PackageName == "" {
		validationLine(log, "NotEmptyValidator", "Project name is required.")
		return log.Result(1)
	}
	if err := createTestProjects(log, layout, *request.PackageName); err != nil {
		if err != errSilentFailure {
			log.Error(err.Error())
		}
		return log.Result(1)
	}
	log.Info("Done")
	return log.Result(0)
}

func createTestProjects(log *Log, layout Layout, packageNames string) error {
	var packages []string
	for _, name := range strings.Split(packageNames, ",") {
		if name != "" {
			packages = append(packages, name)
		}
	}
	for _, name := range packages {
		if project := layout.PackageProjectPath(name); !fileExists(project) {
			log.Error("Package project not found: " + project + ". Generate the package project with clio before creating unit tests.")
			return errSilentFailure
		}
	}
	tests := layout.TestsFolder()
	if err := mkdirAll(tests); err != nil {
		return err
	}
	projectTemplate, err := template("UnitTest.csproj")
	if err != nil {
		return err
	}
	fixtureTemplate, err := template("BaseComposableAppTestFixture.cs")
	if err != nil {
		return err
	}
	for _, name := range packages {
		folder := filepath.Join(tests, name)
		projectFile := name + ".Tests.csproj"
		projectPath := filepath.Join(folder, projectFile)
		fixturePath := filepath.Join(folder, "BaseComposableAppTestFixture.cs")
		if err := mkdirAll(folder); err != nil {
			return err
		}
		if !fileExists(projectPath) {
			if err := writeAllText(projectPath, strings.ReplaceAll(projectTemplate, "{{packageUnderTest}}", name)); err != nil {
				return err
			}
			if err := copyTemplateFolder("UnitTestLibs", filepath.Join(folder, "Libs"), true); err != nil {
				return err
			}
		}
		if !fileExists(fixturePath) {
			if err := writeAllText(fixturePath, strings.ReplaceAll(fixtureTemplate, "{{packageUnderTest}}", name)); err != nil {
				return err
			}
		}
		if err := addProjectsToSolution(log, filepath.Join(tests, "UnitTests.slnx"), []solutionProject{
			{path: filepath.Join(name, projectFile)},
			{path: relativePath(tests, layout.PackageProjectPath(name))},
		}); err != nil {
			return err
		}
		if err := addProjectsToSolution(log, filepath.Join(layout.Root, "MainSolution.slnx"), []solutionProject{
			{path: relativePath(layout.Root, projectPath)},
		}); err != nil {
			return err
		}
	}
	tasks := layout.TasksFolder()
	if err := mkdirAll(tasks); err != nil {
		return err
	}
	for _, script := range []string{"open-test-solution-framework.cmd", "open-test-solution-netcore.cmd"} {
		content, err := template(filepath.Join("workspace", "tasks", script))
		if err != nil {
			return err
		}
		if target := filepath.Join(tasks, script); !fileExists(target) {
			if err := writeAllText(target, content); err != nil {
				return err
			}
		}
	}
	return nil
}

// errSilentFailure ends a command whose failure is already logged.
var errSilentFailure = silentFailure{}

type silentFailure struct{}

func (silentFailure) Error() string { return "" }

var (
	integrationPackagePattern   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)*$`)
	integrationFrameworkPattern = regexp.MustCompile(`^net(?:[0-9]{3}|[0-9]+\.[0-9]+)$`)
	csharpKeywords              = map[string]bool{}
)

func init() {
	for _, keyword := range strings.Fields(`abstract as base bool break byte case catch char checked class const
		continue decimal default delegate do double else enum event explicit extern false finally fixed float for
		foreach goto if implicit in int interface internal is lock long namespace new null object operator out
		override params private protected public readonly ref return sbyte sealed short sizeof stackalloc static
		string struct switch this throw true try typeof uint ulong unchecked unsafe ushort using virtual void
		volatile while`) {
		csharpKeywords[keyword] = true
	}
}

// CreateIntegrationTestProject runs new-integration-test-project.
func CreateIntegrationTestProject(request TestProjectRequest) creatio.CommandResult {
	log := &Log{}
	layout := request.layout()
	if !layout.IsWorkspace() {
		log.Error(notAWorkspace)
		return log.Result(1)
	}
	// CreateIntegrationTestProjectOptionsValidator: every rule of every property runs.
	failed := false
	name := ""
	if request.PackageName != nil {
		name = *request.PackageName
	}
	if name == "" {
		validationLine(log, "NotEmptyValidator", "Package name is required.")
		failed = true
	}
	if request.PackageName != nil && !integrationPackagePattern.MatchString(name) {
		validationLine(log, "RegularExpressionValidator", "Package name must contain valid dot-separated identifiers without path separators.")
		failed = true
	}
	keyword := request.PackageName == nil
	if !keyword {
		for _, segment := range strings.Split(name, ".") {
			keyword = keyword || csharpKeywords[segment]
		}
	}
	if keyword {
		validationLine(log, "PredicateValidator", "Package name must not contain C# reserved keywords.")
		failed = true
	}
	if request.TargetFramework == "" {
		validationLine(log, "NotEmptyValidator", "Target framework is required.")
		failed = true
	}
	if !integrationFrameworkPattern.MatchString(request.TargetFramework) {
		validationLine(log, "RegularExpressionValidator", "Target framework must be a valid .NET target framework moniker, for example net10.0 or net472.")
		failed = true
	}
	if failed {
		return log.Result(1)
	}
	tests := layout.TestsFolder()
	directory := filepath.Join(tests, name+".IntegrationTests")
	projectFile := name + ".IntegrationTests.csproj"
	projectPath := filepath.Join(directory, projectFile)
	if directoryExists(directory) {
		log.Error("Integration-test project directory already exists: " + directory + ". Existing files were not changed.")
		return log.Result(1)
	}
	err := func() error {
		if err := mkdirAll(tests); err != nil {
			return err
		}
		macros := [][2]string{{"{{packageName}}", name}, {"{{targetFramework}}", request.TargetFramework}}
		if err := copyTemplateFolderWithMacros("IntegrationTestProject", directory, macros); err != nil {
			return err
		}
		if err := addProjectsToSolution(log, filepath.Join(tests, "IntegrationTests.slnx"), []solutionProject{
			{path: filepath.Join(name+".IntegrationTests", projectFile)},
		}); err != nil {
			return err
		}
		return addProjectsToSolution(log, filepath.Join(layout.Root, "MainSolution.slnx"), []solutionProject{
			{path: relativePath(layout.Root, projectPath)},
		})
	}()
	if err != nil {
		log.Error(err.Error())
		return log.Result(1)
	}
	log.Info("Created " + projectPath)
	return log.Result(0)
}
