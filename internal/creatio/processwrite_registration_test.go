package creatio

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const (
	processWriteRegistrationTask    = "aaaaaaaa-0000-0000-0000-000000000001"
	processWriteRegistrationPackage = "11111111-2222-3333-4444-555555555555"
)

// processWriteRegistrationWorkspace builds a clio workspace with package UsrPkg owning user task UsrTask1.
func processWriteRegistrationWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		".clio/workspaceSettings.json":                     `{"Packages":["UsrPkg"]}`,
		"packages/UsrPkg/descriptor.json":                  `{"Descriptor":{"Name":"UsrPkg","UId":"` + processWriteRegistrationPackage + `"}}`,
		"packages/UsrPkg/Schemas/UsrTask1/descriptor.json": `{"Descriptor":{"UId":"AAAAAAAA-0000-0000-0000-000000000001","Name":"UsrTask1","ManagerName":"ProcessUserTaskSchemaManager"}}`,
		"packages/UsrPkg/Schemas/UsrTask1/metadata.json":   `{"MetaData":{"Schema":{"ManagerName":"ProcessUserTaskSchemaManager","A2":"UsrTask1","UId":"` + processWriteRegistrationTask + `","B6":"` + processWriteRegistrationPackage + `"}}}`,
	}
	for path, content := range files {
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func processWriteRegistrationRequest(root string) ProcessWriteRegisterRequest {
	return ProcessWriteRegisterRequest{WorkspacePath: root, PackageName: "UsrPkg", UserTaskUID: processWriteRegistrationTask, Caption: `It's a \ test`}
}

func TestRegisterProcessElementWritesBothDialectsAndIsRepeatable(t *testing.T) {
	root := processWriteRegistrationWorkspace(t)
	result := RegisterProcessElement(processWriteRegistrationRequest(root))
	scripts := filepath.Join(root, "packages", "UsrPkg", "SqlScripts")
	postgres := filepath.Join(scripts, "UsrRegisterTaskaaaaaaaa000000000000000000000001PostgreSql", "UsrRegisterTaskaaaaaaaa000000000000000000000001PostgreSql.sql")
	mssql := filepath.Join(scripts, "UsrRegisterTaskaaaaaaaa000000000000000000000001MsSql", "UsrRegisterTaskaaaaaaaa000000000000000000000001MsSql.sql")
	if result.ExitCode != 0 || result.Note != processWriteRegisterNote || len(result.Messages) != 2 ||
		result.Messages[0].Value != "Registration script ready: "+postgres || result.Messages[1].Value != "Registration script ready: "+mssql {
		t.Fatalf("%+v", result)
	}
	sql, _ := os.ReadFile(postgres)
	wantPostgres := "INSERT INTO \"SysProcessUserTask\" (\"SysUserTaskSchemaUId\", \"Caption\")\r\nSELECT s.\"UId\", E'It''s a \\\\ test'\r\n" +
		"FROM \"SysSchema\" s\r\nINNER JOIN \"SysPackage\" p ON p.\"Id\" = s.\"SysPackageId\"\r\n" +
		"WHERE s.\"UId\" = '" + processWriteRegistrationTask + "' AND p.\"UId\" = '" + processWriteRegistrationPackage + "'\r\n" +
		"AND NOT EXISTS (SELECT 1 FROM \"SysProcessUserTask\" t WHERE t.\"SysUserTaskSchemaUId\" = s.\"UId\");"
	if string(sql) != wantPostgres {
		t.Fatalf("postgres sql:\n%q", sql)
	}
	sql, _ = os.ReadFile(mssql)
	if !strings.Contains(string(sql), "SELECT s.[UId], N'It''s a \\ test'\r\n") {
		t.Fatalf("mssql sql:\n%q", sql)
	}
	descriptor, _ := os.ReadFile(filepath.Join(filepath.Dir(mssql), "descriptor.json"))
	pattern := regexp.MustCompile(`^\{\n  "SqlScript": \{\n    "UId": "[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}",\n    "Name": "UsrRegisterTaskaaaaaaaa000000000000000000000001MsSql",\n    "DBEngineType": 0,\n    "InstallType": 1,\n    "ModifiedOnUtc": "/Date\(\d+\)/"\n  \}\n\}$`)
	if !pattern.Match(descriptor) {
		t.Fatalf("descriptor:\n%s", descriptor)
	}
	// A re-run keeps every artifact, an LF copy of the script included.
	_ = os.WriteFile(mssql, []byte(strings.ReplaceAll(string(sql), "\r\n", "\n")), 0o644)
	if again := RegisterProcessElement(processWriteRegistrationRequest(root)); again.ExitCode != 0 {
		t.Fatalf("re-run: %+v", again)
	}
	if kept, _ := os.ReadFile(filepath.Join(filepath.Dir(mssql), "descriptor.json")); string(kept) != string(descriptor) {
		t.Fatal("the descriptor was rewritten")
	}
	changed := processWriteRegistrationRequest(root)
	changed.Caption = "Other"
	if failure := RegisterProcessElement(changed); failure.ExitCode != 1 || failure.Messages[0].Value != "Existing registration SQL differs; it was preserved: "+postgres {
		t.Fatalf("%+v", failure)
	}
}

func TestRegisterProcessElementRefusals(t *testing.T) {
	root := processWriteRegistrationWorkspace(t)
	required := "A workspace path, package name, nonempty user-task UId and caption are required."
	cases := []struct {
		label  string
		mutate func(request *ProcessWriteRegisterRequest)
		setup  func()
		want   string
	}{
		{"bad package name", func(r *ProcessWriteRegisterRequest) { r.PackageName = "Usr-Pkg" }, nil, required},
		{"empty uid", func(r *ProcessWriteRegisterRequest) { r.UserTaskUID = processWriteEmptyGUID }, nil, required},
		{"nul caption", func(r *ProcessWriteRegisterRequest) { r.Caption = "a\x00b" }, nil, required},
		{"blank workspace", func(r *ProcessWriteRegisterRequest) { r.WorkspacePath = " " }, nil, required},
		{"missing workspace", func(r *ProcessWriteRegisterRequest) { r.WorkspacePath = filepath.Join(root, "nope") }, nil,
			"Could not find a part of the path '" + filepath.Join(root, "nope", ".clio", "workspaceSettings.json") + "'."},
		{"not owned", func(r *ProcessWriteRegisterRequest) { r.PackageName = "Other" }, nil, "The package must belong to the selected clio workspace."},
		{"unknown task", func(r *ProcessWriteRegisterRequest) { r.UserTaskUID = "aaaaaaaa-0000-0000-0000-000000000009" }, nil,
			"The requested user-task UId was not found in the workspace package."},
		{"duplicate", nil, func() {
			_ = os.MkdirAll(filepath.Join(root, "packages/UsrPkg/Schemas/UsrTask2"), 0o755)
			_ = os.WriteFile(filepath.Join(root, "packages/UsrPkg/Schemas/UsrTask2/descriptor.json"), []byte(`{"Descriptor":{"UId":"`+processWriteRegistrationTask+`"}}`), 0o644)
		}, "The user-task UId is duplicated in the package."},
		{"metadata mismatch", nil, func() {
			_ = os.WriteFile(filepath.Join(root, "packages/UsrPkg/Schemas/UsrTask1/metadata.json"),
				[]byte(`{"MetaData":{"Schema":{"ManagerName":"ProcessUserTaskSchemaManager","A2":"Other","UId":"`+processWriteRegistrationTask+`","B6":"`+processWriteRegistrationPackage+`"}}}`), 0o644)
		}, "The schema must be a user task with matching descriptor, metadata and package identities."},
	}
	for _, item := range cases {
		request := processWriteRegistrationRequest(root)
		if item.mutate != nil {
			item.mutate(&request)
		}
		if item.setup != nil {
			item.setup()
		}
		result := RegisterProcessElement(request)
		if result.ExitCode != 1 || len(result.Messages) != 1 || result.Messages[0].Value != item.want || result.Note != "" {
			t.Errorf("%s: %+v", item.label, result)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "packages", "UsrPkg", "SqlScripts")); !os.IsNotExist(err) {
		t.Fatal("a refused call wrote scripts")
	}
}

func TestRegisterProcessElementRefusesAConflictingDescriptorBeforeWriting(t *testing.T) {
	root := processWriteRegistrationWorkspace(t)
	directory := filepath.Join(root, "packages", "UsrPkg", "SqlScripts", "UsrRegisterTaskaaaaaaaa000000000000000000000001MsSql")
	_ = os.MkdirAll(directory, 0o755)
	descriptor := filepath.Join(directory, "descriptor.json")
	_ = os.WriteFile(descriptor, []byte(`{"SqlScript":{"UId":"bbbbbbbb-0000-0000-0000-000000000001","Name":"UsrRegisterTaskaaaaaaaa000000000000000000000001MsSql","DBEngineType":2,"InstallType":1}}`), 0o644)
	result := RegisterProcessElement(processWriteRegistrationRequest(root))
	if result.ExitCode != 1 || result.Messages[0].Value != "Existing registration descriptor conflicts with the requested task: "+descriptor {
		t.Fatalf("%+v", result)
	}
	if _, err := os.Stat(filepath.Join(root, "packages", "UsrPkg", "SqlScripts", "UsrRegisterTaskaaaaaaaa000000000000000000000001PostgreSql")); !os.IsNotExist(err) {
		t.Fatal("the PostgreSQL script was written although the SQL Server descriptor conflicts")
	}
}
