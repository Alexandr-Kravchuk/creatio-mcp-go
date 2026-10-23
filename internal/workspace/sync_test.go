package workspace

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"unicode/utf16"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
)

func syncTestWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// syncTestGzip writes clio's package stream for the files, in the given order.
func syncTestGzip(t *testing.T, files [][2]string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := gzip.NewWriter(&buffer)
	for _, file := range files {
		units := utf16.Encode([]rune(file[0]))
		_ = binary.Write(writer, binary.LittleEndian, int32(len(units)))
		_ = binary.Write(writer, binary.LittleEndian, units)
		_ = binary.Write(writer, binary.LittleEndian, int32(len(file[1])))
		_, _ = writer.Write([]byte(file[1]))
	}
	_ = writer.Close()
	return buffer.Bytes()
}

// syncTestEntries decodes clio's package stream into "path=content" entries in archive order.
func syncTestEntries(t *testing.T, data []byte) []string {
	t.Helper()
	reader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	var entries []string
	for {
		var length int32
		if err := binary.Read(reader, binary.LittleEndian, &length); err == io.EOF {
			return entries
		} else if err != nil {
			t.Fatal(err)
		}
		units := make([]uint16, length)
		_ = binary.Read(reader, binary.LittleEndian, units)
		var size int32
		_ = binary.Read(reader, binary.LittleEndian, &size)
		content := make([]byte, size)
		_, _ = io.ReadFull(reader, content)
		entries = append(entries, string(utf16.Decode(units))+"="+string(content))
	}
}

func syncTestMessages(log *Log) string {
	var lines []string
	for _, message := range log.Result(0).Messages {
		lines = append(lines, message.MessageType+": "+message.Value)
	}
	return strings.Join(lines, "\n")
}

func TestIgnoreRulesRewriteAsTheIgnoreLibrary(t *testing.T) {
	for _, test := range []struct {
		rules   []string
		path    string
		ignored bool
	}{
		{[]string{"**/obj"}, "file:///t/Files/obj/project.assets.json", true},
		{[]string{"**/obj"}, "file:///t/Files/MyOBJ", true}, // `.*obj(/.*)?$`, without case
		{[]string{"**/obj"}, "file:///t/Files/objects/a.cs", false},
		{[]string{"**/Autofac.dll"}, "file:///t/Files/Bin/autofac.dll", true},
		{[]string{"**/Autofac.dll"}, "file:///t/Files/Bin/AutofacXdll", false},
		{[]string{"*.log"}, "file:///t/a.log", true},
		{[]string{"*.log"}, "file:///t/a.log.txt", false},
		{[]string{"*.log", "!keep.log"}, "file:///t/keep.log", false},
		{[]string{"# comment", "   "}, "file:///t/x", false},
		{[]string{"Files/Bin/"}, "file:///t/Files/Bin/x.dll", false}, // a middle slash anchors the rule at the start
		{[]string{"bin/"}, "file:///t/Files/bin/x.dll", true},
	} {
		set := &ignoreSet{}
		if err := set.add(test.rules); err != nil {
			t.Fatal(err)
		}
		if got := set.ignored(test.path); got != test.ignored {
			t.Errorf("%v on %s: %v", test.rules, test.path, got)
		}
	}
}

func TestPackageIgnoreMasks(t *testing.T) {
	log := &Log{}
	kept := filterPackages(log, []string{"UsrSkipMe", "UsrSkip", "UsrPkg", "UsrXPkg", "Usr.Pkg"}, []string{"usrskip*", "Usr.Pkg"})
	if strings.Join(kept, ",") != "UsrPkg,UsrXPkg" {
		t.Fatalf("kept %v", kept)
	}
	if syncTestMessages(log) != "Info: Ignored 3 package(s): UsrSkipMe, UsrSkip, Usr.Pkg" {
		t.Fatalf("log %s", syncTestMessages(log))
	}
	if got := withoutExternal([]string{"A", "B", "C"}, []string{"B"}); strings.Join(got, ",") != "A,C" {
		t.Fatalf("external %v", got)
	}
}

func TestDescriptorMissingMessage(t *testing.T) {
	found := foldersWithoutDescriptor([]string{
		"descriptor.json", "Schemas/placeholder.txt", "Schemas/UsrA/descriptor.json", "Schemas/UsrA/a.js",
		"Schemas/UsrB/b.js", `Data\UsrC\Localization\x.json`, "Data/UsrD/.DS_Store", "Files/src/a.cs",
		"Schemas/UsrE/Descriptor.json", "Schemas/UsrF/1", "Schemas/UsrF/2", "Schemas/UsrF/3", "Schemas/UsrF/4",
	})
	var names []string
	for _, folder := range found {
		names = append(names, folder.path)
	}
	if strings.Join(names, ",") != "Data/UsrC,Data/UsrD,Schemas/UsrB,Schemas/UsrF" {
		t.Fatalf("folders %v", names)
	}
	message := (&descriptorMissingError{folders: found}).Error()
	for _, part := range []string{"These 4 package folders have no descriptor.json: Data/UsrC (1 file: Localization/x.json; only Localization files, delete the folder)",
		"Data/UsrD (1 file: .DS_Store; only operating system files, delete the folder)",
		"Schemas/UsrB (1 file: b.js; element files, restore descriptor.json)",
		"Schemas/UsrF (4 files, e.g. 1, 2, 3; element files, restore descriptor.json). Creatio rejects"} {
		if !strings.Contains(message, part) {
			t.Fatalf("missing %q in %s", part, message)
		}
	}
	many := make([]folderWithoutDescriptor, 22)
	for index := range many {
		many[index] = folderWithoutDescriptor{path: fmt.Sprintf("F%d", index), files: []string{"a", "b"}}
	}
	if message := (&descriptorMissingError{folders: many}).Error(); !strings.Contains(message, "F19 (2 files: a, b; element files, restore descriptor.json); and 2 more.") ||
		strings.Contains(message, "F20 ") {
		t.Fatalf("capped list %s", message)
	}
	if single := (&descriptorMissingError{folders: many[:1]}).Error(); !strings.HasPrefix(single, "This package folder has no descriptor.json: F0 ") {
		t.Fatalf("single %s", single)
	}
}

func TestPageTextViolations(t *testing.T) {
	web := `define("P", [], function() { return { viewConfigDiff: /**SCHEMA_VIEW_CONFIG_DIFF*/[
		// comment
		{"operation": "insert", "name": "B", "values": {"caption": "Save", "type": "crt.Button", "title": "$Attr",
			"tooltip": "#MacrosTemplateString(#ResourceString(B_tooltip)#)#", "label": "$_private",
			"items": [{"label": "Inner text"}, {"name": "N", "placeholder": "Type"}],},},
		/* block */ {"operation": "insert", "name": "Img", "values": {"type": "crt.ImageInput", "tooltip": "$Resources.Strings.T"}},
		{"operation": "merge", "name": "Img", "values": {"tooltip": "#ResourceString(T2)#"}},
		{"operation": "insert", "name": "G", "values": {"type": "crt.Gallery", "itemConfig": {"templateValuesMapping": {"title": "Name"}, "caption": "Visible"}}},
		{"operation": "insert", "name": "D", "values": {"type": "crt.X", "_designOptions": {"caption": "x"}, "data": {"typeName": "T", "caption": "y"}}},
		{"operation": "insert", "name": "B", "values": {"caption": "Save"}},
	]/**SCHEMA_VIEW_CONFIG_DIFF*/ }; });`
	violations := findTextViolations(web)
	if got := strings.Join(formatViolations(violations, textViolationInline), ", "); got != "B.caption, B.label, N.placeholder, G.caption" {
		t.Fatalf("inline %q", got)
	}
	if got := strings.Join(formatViolations(violations, textViolationLiteralOnly), ", "); got != "Img.tooltip" {
		t.Fatalf("literal-only %q", got)
	}
	mobile := `{"viewConfigDiff": [{"operation": "insert", "name": "M", "values": {"type": "crt.Label", "caption": "Mobile"}}]}`
	if got := formatViolations(findTextViolations(mobile), textViolationInline); strings.Join(got, ",") != "M.caption" {
		t.Fatalf("mobile %v", got)
	}
	for _, skipped := range []string{
		`{"viewConfigDiff": [{"values": {"caption": "x"}},]}`, // strict JSON: a trailing comma does not parse
		`define({}); /**SCHEMA_VIEW_CONFIG_DIFF*/[{"values": {"caption": "x"}}`,
		`define({}); // no markers`,
		"   ",
		`{"other": [{"values": {"caption": "x"}}]}`,
	} {
		if got := findTextViolations(skipped); len(got) != 0 {
			t.Fatalf("%s: %v", skipped, got)
		}
	}
	log := &Log{}
	pageTextWarnings(log, []pageTextFinding{{packageName: "P", schemaName: "S", inline: []string{"a.caption"}, literalOnly: []string{"b.tooltip"}}})
	text := syncTestMessages(log)
	if !strings.HasPrefix(text, "Warning: Page schema 'S' (package 'P') sets user-visible text as inline literals: a.caption. ") ||
		!strings.Contains(text, "Warning: Page schema 'S' (package 'P') binds literal-only text properties to localizable resources: b.tooltip. ") {
		t.Fatalf("warnings %s", text)
	}
}

func syncTestTemp(t *testing.T) {
	t.Helper()
	temp := t.TempDir()
	for _, name := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(name, temp)
	}
	t.Setenv("CLIO_WORKING_DIRECTORY", "")
}

func TestPackPackageFiltersAndOrdersAsClio(t *testing.T) {
	syncTestTemp(t)
	t.Setenv("CLIO_WORKING_DIRECTORY", "")
	root := t.TempDir()
	syncTestWrite(t, filepath.Join(root, ".clio", "clioignore"), "**/obj\n**/Autofac.dll\n")
	pkg := filepath.Join(root, "packages", "UsrPkg")
	syncTestWrite(t, filepath.Join(pkg, "descriptor.json"), "{}")
	syncTestWrite(t, filepath.Join(pkg, "Files", "Bin", "UsrPkg.pdb"), "pdb")
	syncTestWrite(t, filepath.Join(pkg, "Files", "Bin", "Autofac.dll"), "lib")
	syncTestWrite(t, filepath.Join(pkg, "Files", "Bin", "UsrPkg.dll"), "dll")
	syncTestWrite(t, filepath.Join(pkg, "Files", "obj", "x.json"), "{}")
	syncTestWrite(t, filepath.Join(pkg, "Schemas", "UsrA", "descriptor.json"), "{}")
	syncTestWrite(t, filepath.Join(pkg, "notes.txt"), "not packed")
	packed := filepath.Join(t.TempDir(), "UsrPkg.gz")
	log := &Log{}
	if err := packPackage(log, pkg, packed); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(packed)
	entries := syncTestEntries(t, data)
	if len(entries) != 3 || entries[0] != "descriptor.json={}" {
		t.Fatalf("entries %v", entries)
	}
	joined := strings.Join(entries, ";")
	for _, want := range []string{"Files/Bin/UsrPkg.dll=dll", "Schemas/UsrA/descriptor.json={}"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %s in %v", want, entries)
		}
	}
	pattern := regexp.MustCompile(`^Info: Creating temporary directory\.\.\.\nInfo: Created temporary directory (.+[/\\\\]clio[/\\\\][0-9a-f]{32})\nInfo: Deleted temporary directory (.+)$`)
	match := pattern.FindStringSubmatch(syncTestMessages(log))
	if match == nil || match[1] != match[2] || directoryExists(match[1]) {
		t.Fatalf("temporary folder log %q", syncTestMessages(log))
	}

	syncTestWrite(t, filepath.Join(pkg, "Schemas", "UsrB", "b.js"), "x")
	err := packPackage(&Log{}, pkg, packed)
	if err == nil || !strings.Contains(err.Error(), filepath.Join(pkg, "Schemas", "UsrB")+" (1 file: b.js;") {
		t.Fatalf("descriptor check %v", err)
	}
	if data2, _ := os.ReadFile(packed); !bytes.Equal(data, data2) {
		t.Fatal("a refused package must leave the earlier archive in place")
	}
	if err := packPackage(&Log{}, filepath.Join(root, "packages", "Missing"), packed); err == nil ||
		err.Error() != "Could not find file '"+filepath.Join(root, "packages", "Missing", "descriptor.json")+"'." {
		t.Fatalf("missing descriptor %v", err)
	}
}

func TestUnpackGzipWritesEntriesAndRefusesEscapes(t *testing.T) {
	folder := t.TempDir()
	archive := filepath.Join(t.TempDir(), "p.gz")
	_ = os.WriteFile(archive, syncTestGzip(t, [][2]string{{`Schemas\A\a.js`, "a"}, {"descriptor.json", "{}"}}), 0o644)
	if err := unpackGzip(archive, folder); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(folder, "Schemas", "A", "a.js")); string(data) != "a" {
		t.Fatalf("entry %q", data)
	}
	_ = os.WriteFile(archive, syncTestGzip(t, [][2]string{{"../escape.txt", "x"}}), 0o644)
	if err := unpackGzip(archive, folder); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("escape %v", err)
	}
	_ = os.WriteFile(archive, []byte("not gzip"), 0o644)
	if err := unpackGzip(archive, folder); err == nil {
		t.Fatal("a body that is not gzip must fail")
	}
}

// syncTestStand is a fake Creatio site for restore: the ping, GetZipPackages and GetAutogeneratedFolder.
func syncTestStand(t *testing.T, packages map[string][]byte, autogenerated map[string][]byte) (*creatio.Client, *[]string) {
	t.Helper()
	var mu sync.Mutex
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/AuthService.svc/Login") {
			_, _ = io.WriteString(w, `{"Code":0}`)
			return
		}
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		requests = append(requests, r.Method+" "+r.URL.Path+" "+string(body))
		mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/GetZipPackages"):
			name := strings.Trim(string(body), `[]"`)
			if data, ok := packages[name]; ok {
				_, _ = w.Write(data)
				return
			}
			w.WriteHeader(http.StatusNotFound)
		case strings.HasSuffix(r.URL.Path, "/GetAutogeneratedFolder"):
			_, _ = w.Write(autogenerated[strings.Trim(string(body), `"`)])
		default:
			_, _ = io.WriteString(w, "ok")
		}
	}))
	t.Cleanup(server.Close)
	client, err := creatio.NewClient(creatio.Config{BaseURL: server.URL, Login: "example-user", Password: "replace-me", IsNetCore: true})
	if err != nil {
		t.Fatal(err)
	}
	return client, &requests
}

func syncTestPackageZip(t *testing.T, name string, files [][2]string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	entry, _ := archive.Create(name + ".gz")
	_, _ = entry.Write(syncTestGzip(t, files))
	_ = archive.Close()
	return buffer.Bytes()
}

func syncTestNuGet(t *testing.T, versions ...string) {
	t.Helper()
	var items []string
	for _, version := range versions {
		items = append(items, `{"catalogEntry":{"version":"`+version+`"}}`)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"items":[{"items":[`+strings.Join(items, ",")+`]}]}`)
	}))
	t.Cleanup(server.Close)
	saved := NuGetBaseURL
	NuGetBaseURL = server.URL
	t.Cleanup(func() { NuGetBaseURL = saved })
}

func syncTestDotnet(t *testing.T) *[][]string {
	t.Helper()
	var calls [][]string
	saved := runDotnet
	runDotnet = func(_ context.Context, args ...string) string {
		calls = append(calls, args)
		project, _ := os.ReadFile(args[1])
		if !strings.Contains(string(project), `<PackageReference Include="CreatioSDK" Version="8.1.3.6802" />`) {
			t.Errorf("restore project %s", project)
		}
		return "  Determining projects to restore...\n  Restored " + args[1] + " (in 1 sec)."
	}
	t.Cleanup(func() { runDotnet = saved })
	return &calls
}

func TestRestoreWorkspaceDownloadsPackagesAndPreparesTheWorkspace(t *testing.T) {
	syncTestTemp(t)
	t.Setenv("CLIO_WORKING_DIRECTORY", "")
	syncTestNuGet(t, "8.0.3.1759", "8.1.3.6802", "8.1.2.100")
	calls := syncTestDotnet(t)
	root := t.TempDir()
	if err := copyTemplateFolder("workspace", root, false); err != nil {
		t.Fatal(err)
	}
	syncTestWrite(t, filepath.Join(root, ".clio", "workspaceSettings.json"),
		`{"Packages":["UsrPkg","UsrSkip","UsrExt"],"ApplicationVersion":"8.1.3","IgnorePackages":["UsrS*"],"ExternalPackages":["UsrExt"]}`)
	syncTestWrite(t, filepath.Join(root, "packages", "UsrPkg", "stale.txt"), "old")
	syncTestWrite(t, filepath.Join(root, "packages", "UsrOld", "Files", "UsrOld.csproj"), "<Project xmlns=\"http://schemas.microsoft.com/developer/msbuild/2003\" />")
	project := "<?xml version=\"1.0\" encoding=\"utf-8\"?>\n<Project Sdk=\"Microsoft.NET.Sdk\">\n  <PropertyGroup>\n    <TargetFramework>netstandard2.0</TargetFramework>\n  </PropertyGroup>\n</Project>\n"
	client, requests := syncTestStand(t,
		map[string][]byte{"UsrPkg": syncTestPackageZip(t, "UsrPkg", [][2]string{{"descriptor.json", "{}"}, {`Files\UsrPkg.csproj`, project},
			{`Schemas\UsrHelper\UsrHelper.cs`, "class UsrHelper {}"}, {`Schemas\UsrHelper\descriptor.json`, "{}"}})},
		map[string][]byte{"UsrPkg": syncTestGzip(t, [][2]string{{`Src\UsrHelper.UsrPkg.cs`, "generated"}, {`Src\UsrEntity.UsrPkg.cs`, "entity"}})})

	result := RestoreWorkspace(context.Background(), client, RestoreRequest{Root: root})
	if result.ExitCode != 0 {
		t.Fatalf("restore %+v", result)
	}
	log := &Log{messages: result.Messages}
	text := syncTestMessages(log)
	for _, want := range []string{"Info: Ignored 1 package(s): UsrSkip", "None: Start download packages (UsrPkg).",
		"None: Unzip package (UsrPkg) completed.", "None: Run download Autogenerated.UsrPkg",
		`None:   Determining projects to restore...` + "\n" + `  Restored "` + filepath.Join(root, ".nuget") + `" (in 1 sec).`,
		"Warning: [WARNING] Project file " + filepath.Join(root, "packages", "UsrOld", "Files", "UsrOld.csproj") + " does not contain a root <Project> node.",
		"Info: Done"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in\n%s", want, text)
		}
	}
	if len(*calls) != 1 || strings.Join((*calls)[0][2:], " ") != "--source https://api.nuget.org/v3/index.json --packages "+filepath.Join(root, ".nuget")+" --force --no-cache" {
		t.Fatalf("dotnet %v", *calls)
	}
	if fileExists(filepath.Join(root, "packages", "UsrPkg", "stale.txt")) {
		t.Fatal("the package folder was not replaced")
	}
	if fileExists(filepath.Join(root, "packages", "UsrPkg", "Autogenerated", "Src", "UsrHelper.UsrPkg.cs")) ||
		!fileExists(filepath.Join(root, "packages", "UsrPkg", "Autogenerated", "Src", "UsrEntity.UsrPkg.cs")) {
		t.Fatal("the Autogenerated folder keeps the package's own source schemas")
	}
	script, _ := os.ReadFile(filepath.Join(root, ".solution", "set-framework-environment.cmd"))
	if string(script) != "set CoreLibPath="+filepath.Join("..", "..", "..", ".nuget", "creatiosdk", "8.1.3.6802", "lib", "net40")+"\r\nset CoreTargetFramework=net472" {
		t.Fatalf("script %q", script)
	}
	csproj, _ := os.ReadFile(filepath.Join(root, "packages", "UsrPkg", "Files", "UsrPkg.csproj"))
	if !bytes.HasPrefix(csproj, []byte("\uFEFF<?xml")) || !strings.Contains(string(csproj), "<Project Sdk=\"Microsoft.NET.Sdk\">"+newLine()+
		"  <Import Project=\"..\\..\\..\\.build-props\\env.$(Configuration).props\" Condition=\"Exists('..\\..\\..\\.build-props\\env.$(Configuration).props')\" />") {
		t.Fatalf("csproj %s", csproj)
	}
	solution, _ := os.ReadFile(filepath.Join(root, "MainSolution.slnx"))
	if !strings.Contains(string(solution), "packages/UsrPkg/Files/UsrPkg.csproj") {
		t.Fatalf("solution %s", solution)
	}
	if got := strings.Join(*requests, "\n"); got != "GET / \nPOST /ServiceModel/PackageInstallerService.svc/GetZipPackages [\"UsrPkg\"]\n"+
		"POST /rest/CreatioApiGateway/GetAutogeneratedFolder \"UsrPkg\"" {
		t.Fatalf("requests\n%s", got)
	}
}

func TestRestoreWorkspaceFailuresAndNoEligiblePackages(t *testing.T) {
	syncTestTemp(t)
	syncTestNuGet(t, "8.1.3.6802")
	syncTestDotnet(t)
	client, _ := syncTestStand(t, nil, nil)
	root := t.TempDir()
	if result := RestoreWorkspace(context.Background(), client, RestoreRequest{Root: root}); result.ExitCode != 1 ||
		result.Messages[0].Value != "Json file not found by path: '"+filepath.Join(root, ".clio", "workspaceSettings.json")+"'" {
		t.Fatalf("no settings %+v", result)
	}
	if err := copyTemplateFolder("workspace", root, false); err != nil {
		t.Fatal(err)
	}
	syncTestWrite(t, filepath.Join(root, ".clio", "workspaceSettings.json"), `{"Packages":["UsrPkg"],"ApplicationVersion":"8.1.3"}`)
	result := RestoreWorkspace(context.Background(), client, RestoreRequest{Root: root})
	text := syncTestMessages(&Log{messages: result.Messages})
	if result.ExitCode != 1 || !strings.Contains(text, "None: Download packages (UsrPkg) not completed.") ||
		!strings.HasSuffix(text, "Error: The remote server returned an error: (404) Not Found.") {
		t.Fatalf("download failure %d\n%s", result.ExitCode, text)
	}
	syncTestWrite(t, filepath.Join(root, ".clio", "workspaceSettings.json"), `{"Packages":[],"ApplicationVersion":"8.1.3"}`)
	result = RestoreWorkspace(context.Background(), client, RestoreRequest{Root: root})
	if result.ExitCode != 0 || result.Messages[0].Value != noPackagesEligible {
		t.Fatalf("no packages %+v", result)
	}
	NuGetBaseURL = "http://127.0.0.1:1"
	result = RestoreWorkspace(context.Background(), client, RestoreRequest{Root: root})
	text = syncTestMessages(&Log{messages: result.Messages})
	if result.ExitCode != 1 || !strings.HasPrefix(text, "Error: Error while getting Creatio SDK versions from NuGet: ") ||
		!strings.HasSuffix(text, "Error: "+sdkVersionsUnavailable) {
		t.Fatalf("feed down %d\n%s", result.ExitCode, text)
	}
}

func TestRunDotnetReportsAProgramThatDoesNotStart(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	output := runDotnet(context.Background(), "--version")
	if !strings.HasPrefix(output, "An error occurred trying to start process 'dotnet' with working directory '") ||
		!strings.HasSuffix(output, startFailureText(exec.ErrNotFound)) {
		t.Fatalf("output %q", output)
	}
}
