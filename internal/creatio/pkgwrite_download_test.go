package creatio

import (
	"archive/zip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func pkgWriteWriteFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for path, content := range files {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func pkgWriteNewWorkspace(t *testing.T, settings string) string {
	t.Helper()
	root := t.TempDir()
	pkgWriteWriteFiles(t, root, map[string]string{".clio/workspaceSettings.json": settings})
	return root
}

func pkgWriteExists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Stat(path)
	return err == nil
}

func TestPkgWriteDownloadFromBuildDirectory(t *testing.T) {
	workspace := pkgWriteNewWorkspace(t, `{"Packages":[]}`)
	build := t.TempDir()
	pkgWriteWriteFiles(t, build, map[string]string{
		"Terrasoft.Core.dll": "core", "Terrasoft.Core.pdb": "pdb", "readme.txt": "x",
		"Terrasoft.Configuration/Lib/Lib.dll":                          "lib",
		"conf/bin/3/Terrasoft.Configuration.dll":                       "old",
		"conf/bin/12/Terrasoft.Configuration.dll":                      "new",
		"Terrasoft.Configuration/Pkg/UsrA/Files/bin/UsrA.dll":          "a",
		"Terrasoft.Configuration/Pkg/UsrA/Schemas/S/metadata.json":     "{}",
		"Terrasoft.Configuration/Pkg/UsrNoBin/Schemas/S/metadata.json": "{}",
	})
	result := DownloadConfigurationFromBuild(workspace, build)
	values := strings.Join(pkgWriteValues(result), "|")
	app := filepath.Join(workspace, ".application", "net-core")
	if result.ExitCode != 0 || !strings.HasPrefix(values, "Info:Processing Creatio configuration from directory: "+build+"|Info:Detected NetCore Creatio|") ||
		!strings.Contains(values, "Warning:No root assemblies found in "+build+"|Info:Copied 2 root assemblies (DLL and PDB files)") ||
		!strings.Contains(values, "Warning:File not found: "+filepath.Join(build, "conf", "bin", "12", "Terrasoft.Configuration.ODataEntities.dll")) ||
		!strings.Contains(values, "Info:Copying NetCore package UsrA|") || strings.Contains(values, "UsrNoBin") ||
		!strings.HasSuffix(values, "Info:NetCore configuration downloaded successfully|Info:Configuration download from directory completed successfully") {
		t.Fatalf("values = %q", values)
	}
	for _, path := range []string{"core-bin/Terrasoft.Core.dll", "core-bin/Terrasoft.Core.pdb", "Lib/Lib.dll", "packages/UsrA/Files/bin/UsrA.dll", "packages/UsrA/Schemas/S/metadata.json"} {
		if !pkgWriteExists(t, filepath.Join(app, filepath.FromSlash(path))) {
			t.Fatalf("missing %s", path)
		}
	}
	if data, _ := os.ReadFile(filepath.Join(app, "bin", "Terrasoft.Configuration.dll")); string(data) != "new" {
		t.Fatalf("configuration dll = %q", data)
	}
	if pkgWriteExists(t, filepath.Join(app, "core-bin", "readme.txt")) {
		t.Fatal("only dll and pdb files are root assemblies")
	}

	if result := DownloadConfigurationFromBuild(t.TempDir(), build); result.ExitCode != 1 || len(result.Messages) != 1 ||
		result.Messages[0].Value != "Current directory is not a workspace. Please run this command from a workspace directory." {
		t.Fatalf("not a workspace = %q", pkgWriteValues(result))
	}
	missing := filepath.Join(build, "nowhere")
	if result := DownloadConfigurationFromBuild(workspace, missing); result.ExitCode != 1 || result.Messages[0].Value != "Directory not found: "+missing {
		t.Fatalf("missing = %q", pkgWriteValues(result))
	}
	if result := DownloadConfigurationFromBuild(workspace, missing+".zip"); result.ExitCode != 1 || result.Messages[0].Value != "Zip file not found: "+missing+".zip" {
		t.Fatalf("missing zip = %q", pkgWriteValues(result))
	}
}

func TestPkgWriteDownloadFromFrameworkZip(t *testing.T) {
	workspace := pkgWriteNewWorkspace(t, `{}`)
	archive := filepath.Join(t.TempDir(), "creatio.zip")
	file, _ := os.Create(archive)
	writer := zip.NewWriter(file)
	for name, content := range map[string]string{
		"Terrasoft.WebApp/bin/Terrasoft.Core.dll":                               "core",
		"Terrasoft.WebApp/Terrasoft.Configuration/Lib/Lib.dll":                  "lib",
		"Terrasoft.WebApp/conf/bin/7/Terrasoft.Configuration.ODataEntities.dll": "odata",
		"Terrasoft.WebApp/Terrasoft.Configuration/Pkg/UsrB/Files/bin/UsrB.dll":  "b",
	} {
		entry, _ := writer.Create(name)
		_, _ = entry.Write([]byte(content))
	}
	_ = writer.Close()
	_ = file.Close()
	result := DownloadConfigurationFromBuild(workspace, archive)
	values := strings.Join(pkgWriteValues(result), "|")
	app := filepath.Join(workspace, ".application", "net-framework")
	if result.ExitCode != 0 || !strings.HasPrefix(values, "Info:Extracting Creatio from "+archive+"|Info:Extracted to temporary directory: ") ||
		!strings.Contains(values, "|Info:Detected NetFramework Creatio|") || !strings.HasSuffix(values, "Info:Configuration download from zip completed successfully") {
		t.Fatalf("values = %q", values)
	}
	for _, path := range []string{"core-bin/Terrasoft.Core.dll", "bin/Terrasoft.Configuration.ODataEntities.dll", "packages/UsrB/Files/bin/UsrB.dll", "lib/Lib.dll"} {
		if !pkgWriteExists(t, filepath.Join(app, filepath.FromSlash(path))) {
			t.Fatalf("missing %s", path)
		}
	}
}

func TestPkgWriteDownloadFromEnvironment(t *testing.T) {
	workspace := pkgWriteNewWorkspace(t, `{"Packages":["UsrA"]}`)
	pkgWriteWriteFiles(t, workspace, map[string]string{
		"packages/UsrA/Schemas/UsrSchema/UsrSchema.cs": "class X {}",
		"packages/UsrA/Files/UsrA.csproj": `<Project><ItemGroup Label="Package References">` +
			`<Reference Include="Terrasoft.Configuration"><SpecificVersion>false</SpecificVersion><Private>false</Private></Reference>` +
			`<Reference Include="UsrDep"><HintPath>../../UsrDep/Files/bin/UsrDep.dll</HintPath><SpecificVersion>false</SpecificVersion><Private>false</Private></Reference>` +
			`</ItemGroup></Project>`,
	})
	archive := func(files map[string]string) []byte {
		root := t.TempDir()
		pkgWriteWriteFiles(t, root, files)
		packed := filepath.Join(t.TempDir(), "a.gz")
		if err := appWriteGzip(root, packed); err != nil {
			t.Fatal(err)
		}
		data, _ := os.ReadFile(packed)
		return data
	}
	var mu sync.Mutex
	bodies := map[string]string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		route := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		mu.Lock()
		bodies[route] = string(body)
		mu.Unlock()
		switch route {
		case "Login":
			_, _ = io.WriteString(w, `{"Code":0}`)
		case "SelectQuery":
			_, _ = io.WriteString(w, `{"success":true,"rows":[{"Name":"cliogate","Version":"2.0.0.32"}]}`)
		case "GetCoreBinFolder":
			_, _ = w.Write(archive(map[string]string{"Terrasoft.Core.dll": "core"}))
		case "GetConfigurationLibFolder":
			_, _ = w.Write(archive(map[string]string{"Lib.dll": "lib"}))
		case "GetConfigurationBinFolder":
			_, _ = w.Write(archive(map[string]string{"Terrasoft.Configuration.dll": "conf"}))
		case "GetAutogeneratedFolder":
			_, _ = w.Write(archive(map[string]string{"Src/UsrSchema.UsrA.cs": "generated", "Src/Other.UsrA.cs": "keep"}))
		case "DownloadFile":
			_, _ = io.WriteString(w, "dll bytes")
		default:
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	client := newFormsTestClient(t, server.URL)
	result := client.DownloadConfigurationFromEnvironment(context.Background(), workspace, "parity-env", "")
	values := strings.Join(pkgWriteValues(result), "|")
	for _, want := range []string{"None:Run download CoreBin", "None:Run download Lib", "None:Run download ConfigurationBin", "None:Run download Autogenerated.UsrA",
		"Info:Run download - OK: UsrDep.dll"} {
		if !strings.Contains(values, want) {
			t.Fatalf("values = %q, missing %q", values, want)
		}
	}
	if result.ExitCode != 0 || !strings.HasSuffix(values, "None:Download from parity-env environment completed successfully") {
		t.Fatalf("values = %q", values)
	}
	app := filepath.Join(workspace, ".application", "net-framework")
	for _, path := range []string{"core-bin/Terrasoft.Core.dll", "Lib/Lib.dll", "bin/Terrasoft.Configuration.dll", "packages/UsrDep/Files/Bin/UsrDep.dll", "packages"} {
		if !pkgWriteExists(t, filepath.Join(app, filepath.FromSlash(path))) {
			t.Fatalf("missing %s", path)
		}
	}
	autogenerated := filepath.Join(workspace, "packages", "UsrA", "Autogenerated", "Src")
	if pkgWriteExists(t, filepath.Join(autogenerated, "UsrSchema.UsrA.cs")) || !pkgWriteExists(t, filepath.Join(autogenerated, "Other.UsrA.cs")) {
		t.Fatal("the source schemas' generated twins are removed, the rest kept")
	}
	mu.Lock()
	defer mu.Unlock()
	if bodies["GetAutogeneratedFolder"] != `"UsrA"` || bodies["DownloadFile"] != `{"packageName":"UsrDep","dllName":"UsrDep.dll"}` || bodies["GetCoreBinFolder"] != "" {
		t.Fatalf("bodies = %q", bodies)
	}
}

func TestPkgWriteDownloadFromEnvironmentNeedsCliogateAndAWorkspace(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/Login") {
			_, _ = io.WriteString(w, `{"Code":0}`)
			return
		}
		_, _ = io.WriteString(w, `{"success":true,"rows":[{"Name":"cliogate","Version":"1.9.0.0"}]}`)
	}))
	defer server.Close()
	client := newFormsTestClient(t, server.URL)
	workspace := pkgWriteNewWorkspace(t, `{"Packages":[]}`)
	result := client.DownloadConfigurationFromEnvironment(context.Background(), workspace, "parity-env", "")
	if result.ExitCode != 0 || strings.Join(pkgWriteValues(result), "|") !=
		"Warning:Downloading of core libraries requires \x1b[33mcliogate\x1b[0m version 2.0.0.0 or higher.|None:Download from parity-env environment completed successfully" {
		t.Fatalf("old cliogate = %q", pkgWriteValues(result))
	}
	plain := t.TempDir()
	result = client.DownloadConfigurationFromEnvironment(context.Background(), plain, "parity-env", "")
	settings := filepath.Join(plain, ".clio", "workspaceSettings.json")
	if result.ExitCode != 1 || strings.Join(pkgWriteValues(result), "|") != "Error:Error: Json file not found by path: '"+settings+"'|Error:Json file not found by path: '"+settings+"'" {
		t.Fatalf("no workspace = %q", pkgWriteValues(result))
	}
}
