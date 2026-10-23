package workspace

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
)

// offlineNuGet serves the CreatioSDK registration index so no test reaches api.nuget.org.
func offlineNuGet(t *testing.T, body string, status int) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v3/registration5-semver1/creatiosdk/index.json" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	previous := NuGetBaseURL
	NuGetBaseURL = server.URL
	t.Cleanup(func() { NuGetBaseURL = previous; server.Close() })
}

const sdkIndex = `{"items":[{"items":[{"catalogEntry":{"version":"8.0.3.1759"}},{"catalogEntry":{"version":"8.1.3.3000"}},{"catalogEntry":{"version":"8.1.2.10"}}]}]}`

func pinClock(t *testing.T) {
	t.Helper()
	previousNow, previousGUID := now, newGUID
	now = func() time.Time { return time.Date(2026, 10, 23, 18, 15, 37, 123, time.UTC) }
	count := 0
	newGUID = func() string {
		count++
		return "00000000-0000-4000-8000-" + strings.Repeat("0", 10) + string(rune('a'+count%26)) + string(rune('a'+count/26))
	}
	t.Cleanup(func() { now, newGUID = previousNow, previousGUID })
}

func messages(result creatio.CommandResult) string {
	var lines []string
	for _, message := range result.Messages {
		lines = append(lines, message.MessageType+": "+message.Value)
	}
	return strings.Join(lines, "\n")
}

func readText(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func newWorkspace(t *testing.T) string {
	t.Helper()
	offlineNuGet(t, sdkIndex, http.StatusOK)
	base := t.TempDir()
	result := CreateWorkspace(context.Background(), CreateWorkspaceRequest{WorkspaceName: "ws", Directory: base})
	if result.ExitCode != 0 {
		t.Fatalf("create-workspace: %s", messages(result))
	}
	return filepath.Join(base, "ws")
}

func TestEmbeddedTemplatesMatchTheirManifest(t *testing.T) {
	files, err := loadTemplates()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"workspace/MainSolution.slnx", "package/Files/PackageName.csproj", "UnitTest.csproj.tpl",
		"IntegrationTestProject/{{packageName}}.IntegrationTests.csproj.tpl", "ui-project/package.json", "esproj.tpl"} {
		if _, ok := files[name]; !ok {
			t.Errorf("template %s is not embedded", name)
		}
	}
}

func TestCreateWorkspaceWritesClioLayout(t *testing.T) {
	root := newWorkspace(t)
	settings := readText(t, filepath.Join(root, ".clio", "workspaceSettings.json"))
	want := strings.ReplaceAll("{\n  \"Packages\": [],\n  \"ApplicationVersion\": \"8.1.3\",\n  \"IgnorePackages\": [],\n  \"ExternalPackages\": []\n}", "\n", newLine())
	if settings != want {
		t.Fatalf("workspaceSettings.json:\n%s", settings)
	}
	if !fileExists(filepath.Join(root, ".gitignore")) || fileExists(filepath.Join(root, "gitignore.txt")) {
		t.Fatal(".gitignore was not normalized")
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(root, "tasks", "build-netcore.sh"))
		if err != nil || info.Mode()&0o100 == 0 {
			t.Fatalf("shell script is not executable: %v %v", info.Mode(), err)
		}
	}
	again := CreateWorkspace(context.Background(), CreateWorkspaceRequest{WorkspaceName: "ws", Directory: filepath.Dir(root)})
	if again.ExitCode != 1 || !strings.Contains(messages(again), "Destination folder already exists and is not empty") {
		t.Fatalf("second create: %s", messages(again))
	}
}

func TestCreateWorkspaceWithoutNuGetWarnsAndOmitsTheVersion(t *testing.T) {
	offlineNuGet(t, "", http.StatusServiceUnavailable)
	base := t.TempDir()
	result := CreateWorkspace(context.Background(), CreateWorkspaceRequest{WorkspaceName: "ws", Directory: base})
	if result.ExitCode != 0 {
		t.Fatal(messages(result))
	}
	text := messages(result)
	if !strings.Contains(text, "Error: Error while getting Creatio SDK versions from NuGet:") ||
		!strings.Contains(text, "Warning: The workspace application version could not be resolved: Creatio SDK versions could not be read") {
		t.Fatalf("messages:\n%s", text)
	}
	if !strings.Contains(readText(t, filepath.Join(base, "ws", ".clio", "workspaceSettings.json")), `"ApplicationVersion": null`) {
		t.Fatal("ApplicationVersion is not null")
	}
}

func TestCreateWorkspaceRefusals(t *testing.T) {
	base := t.TempDir()
	cases := map[string]CreateWorkspaceRequest{
		"WorkspaceName is required when using --empty. (Parameter 'options')":                                 {Directory: base},
		"Provide --directory for create-workspace --empty or configure 'workspaces-root' in appsettings.json.": {WorkspaceName: "x"},
		"--directory and appsettings 'workspaces-root' must be absolute paths.":                               {WorkspaceName: "x", Directory: "relative"},
		"WorkspaceName must be located under the resolved base directory.":                                    {WorkspaceName: "../x", Directory: base},
	}
	for want, request := range cases {
		result := CreateWorkspace(context.Background(), request)
		if result.ExitCode != 1 || messages(result) != "Error: "+want {
			t.Errorf("%+v: %s", request, messages(result))
		}
	}
}

func TestAddPackageAsAppWritesSchemaAndAppDescriptor(t *testing.T) {
	root := newWorkspace(t)
	pinClock(t)
	prefix := "Usr"
	result := AddPackage(context.Background(), AddPackageRequest{Name: "App1", WorkspacePath: root, AsApp: true, SchemaNamePrefix: &prefix})
	if result.ExitCode != 0 {
		t.Fatal(messages(result))
	}
	pkg := filepath.Join(root, "packages", "App1")
	for _, path := range []string{"Files/App1.csproj", "Files/src/cs/App1App.cs", "Files/app-descriptor.json",
		"Schemas/UsrApp1LocalizableStrings/UsrApp1LocalizableStrings.cs", "Resources/UsrApp1LocalizableStrings.SourceCode/resource.en-US.xml"} {
		if !fileExists(filepath.Join(pkg, filepath.FromSlash(path))) {
			t.Errorf("%s missing", path)
		}
	}
	descriptor := readText(t, filepath.Join(pkg, "descriptor.json"))
	if !strings.Contains(descriptor, `"ModifiedOnUtc": "/Date(1792779337000)/"`) || !strings.Contains(descriptor, `"Maintainer": "Customer"`) {
		t.Fatalf("descriptor.json:\n%s", descriptor)
	}
	if !strings.Contains(readText(t, filepath.Join(root, "MainSolution.slnx")), `<Project Path="`+filepath.Join("packages", "App1", "Files", "App1.csproj")+`" />`) {
		t.Fatal("package project not added to MainSolution.slnx")
	}
	second := AddPackage(context.Background(), AddPackageRequest{Name: "App1", WorkspacePath: root})
	if second.ExitCode != -1 || !strings.HasPrefix(messages(second), "Error: [InvalidOperationException] Directory '") {
		t.Fatalf("existing package: %s", messages(second))
	}
}

func TestAddPackageReadsThePrefixAndDownloadsConfiguration(t *testing.T) {
	root := newWorkspace(t)
	name := "dev"
	downloads := 0
	result := AddPackage(context.Background(), AddPackageRequest{Name: "App1", WorkspacePath: root, AsApp: true,
		EnvironmentName: &name, EnvironmentURI: "http://example.invalid",
		ReadPrefix: func(context.Context) (string, PrefixReadFailure, error) { return "Abc", PrefixReadOther, nil },
		DownloadConfiguration: func(_ context.Context, folder, value string, isEnvironment bool) creatio.CommandResult {
			downloads++
			if folder != root || value != "dev" || !isEnvironment {
				t.Errorf("download %s %s %v", folder, value, isEnvironment)
			}
			return creatio.NewCommandResult(0, "None", "downloaded")
		}})
	text := messages(result)
	if result.ExitCode != 0 || downloads != 1 || !strings.Contains(text, "Using schema-name prefix 'Abc'") || !strings.HasSuffix(text, "None: downloaded") {
		t.Fatalf("exit %d, downloads %d:\n%s", result.ExitCode, downloads, text)
	}
	if !directoryExists(filepath.Join(root, "packages", "App1", "Schemas", "AbcApp1LocalizableStrings")) {
		t.Fatal("prefixed schema missing")
	}
}

func TestTestProjectsRegisterInSolutions(t *testing.T) {
	root := newWorkspace(t)
	if result := AddPackage(context.Background(), AddPackageRequest{Name: "Pkg1", WorkspacePath: root}); result.ExitCode != 0 {
		t.Fatal(messages(result))
	}
	name := "Pkg1"
	unit := CreateTestProject(TestProjectRequest{PackageName: &name, WorkspacePath: root})
	if unit.ExitCode != 0 {
		t.Fatal(messages(unit))
	}
	integration := CreateIntegrationTestProject(TestProjectRequest{PackageName: &name, WorkspacePath: root, TargetFramework: "net10.0"})
	if integration.ExitCode != 0 {
		t.Fatal(messages(integration))
	}
	main := readText(t, filepath.Join(root, "MainSolution.slnx"))
	for _, path := range []string{filepath.Join("tests", "Pkg1", "Pkg1.Tests.csproj"), filepath.Join("tests", "Pkg1.IntegrationTests", "Pkg1.IntegrationTests.csproj")} {
		if !strings.Contains(main, `Path="`+path+`"`) {
			t.Errorf("%s not in MainSolution.slnx:\n%s", path, main)
		}
	}
	bad := "class"
	refused := CreateIntegrationTestProject(TestProjectRequest{PackageName: &bad, WorkspacePath: root, TargetFramework: "net"})
	want := "None: ERROR (PredicateValidator) - Package name must not contain C# reserved keywords.\n" +
		"None: ERROR (RegularExpressionValidator) - Target framework must be a valid .NET target framework moniker, for example net10.0 or net472."
	if refused.ExitCode != 1 || messages(refused) != want {
		t.Fatalf("refusal:\n%s", messages(refused))
	}
}

func TestUIProjectUsesLegacyTemplateAndPinsSDK(t *testing.T) {
	root := newWorkspace(t)
	vendor := "usr"
	result := CreateUIProject(UIProjectRequest{WorkspaceDirectory: root, ProjectName: "my_module", PackageName: "UsrUi", VendorPrefix: &vendor, CreatioVersion: "8.0.9"})
	if result.ExitCode != 0 {
		t.Fatal(messages(result))
	}
	if !strings.Contains(messages(result), "UI project template: ui/8.0.8/ui-project (legacy template for Creatio 8.0.8); requested Creatio version: 8.0.9;") {
		t.Fatal(messages(result))
	}
	global := readText(t, filepath.Join(root, "global.json"))
	if !strings.Contains(global, `"Microsoft.VisualStudio.JavaScript.Sdk": "1.0.5581896"`) {
		t.Fatalf("global.json:\n%s", global)
	}
	main := readText(t, filepath.Join(root, "MainSolution.slnx"))
	if !strings.Contains(main, `my_module.esproj">`+newLine()+"    <Build />") {
		t.Fatalf("MainSolution.slnx:\n%s", main)
	}
}

func TestXMLSaveMatchesXmlDocument(t *testing.T) {
	document, err := parseXML("<Solution>\r\n\t<Configurations>\r\n\t\t<BuildType Name=\"Debug\" />\r\n\t</Configurations>\r\n\t<!-- c -->\r\n\t<Folder Name=\"/a/\"><File Path=\"x&amp;y\" /></Folder>\r\n</Solution>\r\n")
	if err != nil {
		t.Fatal(err)
	}
	project := document.root().appendElement("Project")
	project.setAttribute("Path", "a/b.csproj")
	project.appendElement("Build")
	text, bom := document.save()
	want := strings.ReplaceAll(`<Solution>
  <Configurations>
    <BuildType Name="Debug" />
  </Configurations>
  <!-- c -->
  <Folder Name="/a/">
    <File Path="x&amp;y" />
  </Folder>
  <Project Path="a/b.csproj">
    <Build />
  </Project>
</Solution>`, "\n", newLine())
	if bom || text != want {
		t.Fatalf("saved:\n%s", text)
	}
}

func TestJSONWritersEscapeAsDotNet(t *testing.T) {
	value := object(member("a", "<é>\u2028\"'"), member("n", jsonNumber("1.50")), member("e", jsonArray{}))
	if got := indentedJSON(value, newtonsoftStyle); got != strings.ReplaceAll("{\n  \"a\": \"<é>\\u2028\\\"'\",\n  \"n\": 1.50,\n  \"e\": []\n}", "\n", newLine()) {
		t.Errorf("newtonsoft: %s", got)
	}
	if got := indentedJSON(value, systemTextJSONStyle); got != strings.ReplaceAll("{\n  \"a\": \"\\u003C\\u00E9\\u003E\\u2028\\u0022\\u0027\",\n  \"n\": 1.50,\n  \"e\": []\n}", "\n", newLine()) {
		t.Errorf("system.text.json: %s", got)
	}
}

func TestCultureOrderPutsPunctuationFirstAndLowerCaseBeforeUpper(t *testing.T) {
	values := []string{"packages/b/x", "packages/A_b/x", "packages/a/x", "packages/A/x", "packages/1/x", "packages/ab/x"}
	sortCulture(values)
	// The order clio 8.1.0.142 wrote for such paths (scripts/workspace-scenarios/local.json, solution-order).
	want := "packages/1/x packages/A_b/x packages/a/x packages/A/x packages/ab/x packages/b/x"
	if strings.Join(values, " ") != want {
		t.Fatalf("got %s", strings.Join(values, " "))
	}
}
