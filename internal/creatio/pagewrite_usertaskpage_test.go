package creatio

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	userTaskTestUID     = "4c5d6e7f-0000-4000-8000-000000000001"
	userTaskTestPackage = "4c5d6e7f-0000-4000-8000-0000000000aa"
)

func userTaskTestWorkspace(t *testing.T, resource string) string {
	t.Helper()
	root := t.TempDir()
	write := func(path, content string) {
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(".clio/workspaceSettings.json", `{"Packages":["UsrPkg"]}`)
	write("packages/UsrPkg/descriptor.json", `{"Descriptor":{"UId":"`+userTaskTestPackage+`","Name":"UsrPkg"}}`)
	write("packages/UsrPkg/Schemas/UsrTask/descriptor.json", `{"Descriptor":{"UId":"`+userTaskTestUID+`","Name":"UsrTask","ManagerName":"ProcessUserTaskSchemaManager","Caption":"T<x>"}}`)
	write("packages/UsrPkg/Schemas/UsrTask/metadata.json", `{"MetaData":{"Schema":{"UId":"`+userTaskTestUID+`","A2":"UsrTask","ManagerName":"ProcessUserTaskSchemaManager","B6":"`+
		userTaskTestPackage+`","FJ1":[{"A2":"InParam","L12":0},{"A2":"OutParam","L12":1},{"A2":"VarParam"}]}}}`)
	if resource != "" {
		write("packages/UsrPkg/Resources/UsrTask.ProcessUserTask/resource.en-US.xml", resource)
	}
	write("icon.svg", `<svg xmlns="http://www.w3.org/2000/svg"><rect width="8" height="8"/></svg>`)
	return root
}

func TestCreateUserTaskPageScaffoldsAndLinksThePage(t *testing.T) {
	resource := "<?xml version=\"1.0\"?>\n<Resources Culture=\"en-US\">\n  <Group Type=\"String\">\n    <Items>\n" +
		"      <Item Name=\"Parameters.InParam.Caption\" Value=\"Input &amp; one\" />\n    </Items>\n  </Group>\n</Resources>"
	root := userTaskTestWorkspace(t, resource)
	result := CreateUserTaskPage(UserTaskPageRequest{WorkspacePath: root, PackageName: "UsrPkg", UserTaskUID: strings.ToUpper(userTaskTestUID),
		PageName: "UsrTaskPage", Caption: "Page \"one\"", SmallIconPath: filepath.Join(root, "icon.svg")})
	if result.ExitCode != 0 || result.Note != UserTaskPageNote || !strings.HasSuffix(result.Messages[0].Value, "UsrTaskPage.js") {
		t.Fatalf("result %#v", result)
	}
	read := func(path string) string {
		content, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			t.Fatal(err)
		}
		return string(content)
	}
	body := read("packages/UsrPkg/Schemas/UsrTaskPage/UsrTaskPage.js")
	if !strings.Contains(body, `"InParam": {dataValueType: Terrasoft.DataValueType.MAPPING`) || !strings.Contains(body, `caption: "Input \u0026 one"`) ||
		!strings.Contains(body, `name: "VarParam"`) || strings.Contains(body, "OutParam") {
		t.Fatalf("body %s", body)
	}
	metadata := read("packages/UsrPkg/Schemas/UsrTask/metadata.json")
	if !strings.Contains(metadata, "\"FK11\": \"") || !strings.Contains(metadata, "\n  \"MetaData\": {") {
		t.Fatalf("metadata %s", metadata)
	}
	if descriptor := read("packages/UsrPkg/Schemas/UsrTask/descriptor.json"); !strings.Contains(descriptor, `"Caption": "T\u003Cx\u003E"`) ||
		!strings.Contains(descriptor, `"ModifiedOnUtc": "/Date(`) {
		t.Fatalf("descriptor %s", descriptor)
	}
	icons := read("packages/UsrPkg/Resources/UsrTask.ProcessUserTask/resource.en-US.xml")
	if !strings.HasPrefix(icons, "\n<Resources") || !strings.Contains(icons, `<Item Name="SmallSvgImage" Type="Image" ContentType="Data" FileExtension=".svg" Value="PHN2Zy`) {
		t.Fatalf("task resources %q", icons)
	}
	if caption := read("packages/UsrPkg/Resources/UsrTaskPage.ClientUnit/resource.en-US.xml"); caption != "<Resources Culture=\"en-US\">\n  <Group Type=\"String\">\n    <Items>\n      <Item Name=\"Caption\" Value=\"Page &quot;one&quot;\" />\n    </Items>\n  </Group>\n</Resources>" {
		t.Fatalf("page resources %q", caption)
	}
	again := CreateUserTaskPage(UserTaskPageRequest{WorkspacePath: root, PackageName: "UsrPkg", UserTaskUID: userTaskTestUID, PageName: "UsrOther", Caption: "x"})
	if again.ExitCode != 1 || again.Messages[0].Value != "The user task already has a parameter page. Edit that page instead of replacing it." {
		t.Fatalf("again %#v", again)
	}
}

func TestCreateUserTaskPageRefusesBadInput(t *testing.T) {
	root := userTaskTestWorkspace(t, "")
	if err := os.WriteFile(filepath.Join(root, "bad.svg"), []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>x</script></svg>`), 0o644); err != nil {
		t.Fatal(err)
	}
	for request, want := range map[UserTaskPageRequest]string{
		{WorkspacePath: root, PackageName: "Usr-Pkg", UserTaskUID: userTaskTestUID, PageName: "P", Caption: "c"}:                                               "package-name must be a valid schema identifier.",
		{WorkspacePath: root, PackageName: "UsrPkg", PageName: "P", Caption: "c"}:                                                                              "workspace-path, user-task-uid and caption are required.",
		{WorkspacePath: root, PackageName: "Other", UserTaskUID: userTaskTestUID, PageName: "P", Caption: "c"}:                                                 "The package must belong to the workspace.",
		{WorkspacePath: root, PackageName: "UsrPkg", UserTaskUID: userTaskTestPackage, PageName: "P", Caption: "c"}:                                            "Exactly one matching user task must exist in the package.",
		{WorkspacePath: root, PackageName: "UsrPkg", UserTaskUID: userTaskTestUID, PageName: "P", Caption: "c", SmallIconPath: filepath.Join(root, "bad.svg")}: "SVG icons must use static basic shapes without styles, animation, event handlers or external references.",
	} {
		if result := CreateUserTaskPage(request); result.ExitCode != 1 || result.Messages[0].Value != want {
			t.Errorf("%#v: %#v", request, result)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "packages/UsrPkg/Schemas/P")); !os.IsNotExist(err) {
		t.Fatal("a refused scaffold wrote files")
	}
}
