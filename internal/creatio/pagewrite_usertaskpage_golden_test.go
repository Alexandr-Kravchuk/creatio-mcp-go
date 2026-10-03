package creatio

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"testing"
)

// TestCreateUserTaskPageMatchesClioOutput scaffolds the workspace clio 8.1.0.134 scaffolded into
// testdata/usertaskpage-clio.json (GUIDs and /Date()/ stamps normalized) and compares every file.
func TestCreateUserTaskPageMatchesClioOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the golden files were written on macOS (LF line endings)")
	}
	raw, err := os.ReadFile("testdata/usertaskpage-clio.json")
	if err != nil {
		t.Fatal(err)
	}
	var golden map[string]string
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	task, pkg := "4c5d6e7f-0000-4000-8000-000000000001", "4c5d6e7f-0000-4000-8000-0000000000aa"
	inputs := map[string]string{
		".clio/workspaceSettings.json":    golden[".clio/workspaceSettings.json"],
		"packages/UsrPkg/descriptor.json": `{"Descriptor": {"UId": "` + pkg + `", "Name": "UsrPkg", "Maintainer": "Customer"}}`,
		"packages/UsrPkg/Schemas/UsrTask/descriptor.json": `{"Descriptor": {"UId": "` + task + `", "Name": "UsrTask", "ManagerName": "ProcessUserTaskSchemaManager", ` +
			`"ModifiedOnUtc": "/Date(1)/", "Caption": "T\u00e2che <x> & 'y'"}}`,
		"packages/UsrPkg/Schemas/UsrTask/metadata.json": `{"MetaData": {"Schema": {"UId": "` + task + `", "A2": "UsrTask", "ManagerName": "ProcessUserTaskSchemaManager", ` +
			`"B6": "` + pkg + `", "F": 1.5, "FJ1": [{"UId": "a", "A2": "InParam", "L12": 0}, {"UId": "b", "A2": "OutParam", "L12": 1}, ` +
			`{"UId": "c", "A2": "VarParam"}, {"UId": "d", "A2": "Internal", "L12": 3}]}}}`,
		"packages/UsrPkg/Resources/UsrTask.ProcessUserTask/resource.en-US.xml": "<?xml version=\"1.0\" encoding=\"utf-8\"?>\n<Resources Culture=\"en-US\">\n" +
			"  <Group Type=\"String\">\n    <Items>\n      <Item Name=\"Parameters.InParam.Caption\" Value=\"Input &amp; &quot;one&quot;\" />\n" +
			"      <Item Name=\"SmallSvgImage\" Value=\"old\" />\n    </Items>\n  </Group>\n  <Group Type=\"Image\">\n    <Items />\n  </Group>\n</Resources>",
		"icon.svg": `<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 8 8"><g><rect width="8" height="8"/></g></svg>`,
	}
	for path, content := range inputs {
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	icon := filepath.Join(root, "icon.svg")
	result := CreateUserTaskPage(UserTaskPageRequest{WorkspacePath: root, PackageName: "UsrPkg", UserTaskUID: task, PageName: "UsrTaskPage",
		Caption: "Page <caption> & \"quote\" '\u00e9'", SmallIconPath: icon, TitleIconPath: icon})
	if result.ExitCode != 0 {
		t.Fatalf("result %#v", result)
	}
	guid := regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
	stamp := regexp.MustCompile(`/Date\(\d+\)/`)
	for path, want := range golden {
		content, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			t.Errorf("%s: %v", path, err)
			continue
		}
		if got := stamp.ReplaceAllString(guid.ReplaceAllString(string(content), "GUID"), "/Date(N)/"); got != want && path != "packages/UsrPkg/descriptor.json" {
			t.Errorf("%s differs from clio's:\n%s\n--- clio\n%s", path, got, want)
		}
	}
}
