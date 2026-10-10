package creatio

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The golden tree testdata/databinding-clio/Data is what clio 8.1.0.134 wrote for exactly this sequence of
// create-data-binding / add-data-binding-row / remove-data-binding-row calls (offline templates only); only
// the descriptor UId and ModifiedOnUtc are generated per run.
func TestDataWriteLocalBindingsMatchClioFiles(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{".clio", "packages/UsrParityPkg", "img"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(root, ".clio", "workspaceSettings.json"), []byte("{}"), 0o600)
	os.WriteFile(filepath.Join(root, "img", "a.png"), []byte("PNGDATA"), 0o600)
	ctx := context.Background()
	request := func(r DataWriteLocalBindingRequest) DataWriteLocalBindingRequest {
		r.PackageName, r.WorkspacePath = "UsrParityPkg", root
		return r
	}
	steps := []func() CommandResult{
		func() CommandResult {
			return CreateDataBindingLocal(ctx, nil, request(DataWriteLocalBindingRequest{SchemaName: "SysSettings", BindingName: "TplSettings"}))
		},
		func() CommandResult {
			return CreateDataBindingLocal(ctx, nil, request(DataWriteLocalBindingRequest{SchemaName: "SysSettings", BindingName: "Vals",
				ValuesJSON:        `{"Id":"11111111-1111-1111-1111-11111111111A","Code":"UsrParityCode","Name":"Parity é <x>","IsCacheable":"true","ReferenceSchemaUId":null}`,
				LocalizationsJSON: `{"uk-UA":{"Name":"Назва","Description":5},"en-US":{"Name":"Name"}}`}))
		},
		func() CommandResult {
			return AddDataBindingRowLocal(ctx, request(DataWriteLocalBindingRequest{BindingName: "Vals",
				ValuesJSON: `{"Id":"22222222-2222-2222-2222-222222222222","Code":"Second","IsCacheable":false}`, LocalizationsJSON: `{"uk-UA":{"Name":"Друга"}}`}))
		},
		func() CommandResult {
			return AddDataBindingRowLocal(ctx, request(DataWriteLocalBindingRequest{BindingName: "Vals", ValuesJSON: `{"Id":"{22222222-2222-2222-2222-222222222222}","Code":"Second2"}`}))
		},
		func() CommandResult {
			return AddDataBindingRowLocal(ctx, request(DataWriteLocalBindingRequest{BindingName: "Vals", ValuesJSON: `{"Id":"33333333-3333-3333-3333-333333333333","Code":""}`}))
		},
		func() CommandResult {
			return RemoveDataBindingRowLocal(ctx, request(DataWriteLocalBindingRequest{BindingName: "Vals", KeyValue: "11111111-1111-1111-1111-11111111111a"}))
		},
		func() CommandResult {
			return CreateDataBindingLocal(ctx, nil, request(DataWriteLocalBindingRequest{SchemaName: "SysModule", BindingName: "Mod",
				ValuesJSON: `{"Id":"44444444-4444-4444-4444-444444444444","Code":"UsrX","Type":"3","FolderMode":{"value":"b659d704-3955-e011-981f-00155d043204","displayValue":"Folders"},` +
					`"SysModuleVisa":null,"Image16":"img/a.png","Logo":{"value":"cccccccc-0000-0000-0000-000000000001","displayValue":"logo"}}`}))
		},
	}
	for index, step := range steps {
		if result := step(); result.ExitCode != 0 || result.Messages[0].Value != "Done" {
			t.Fatalf("step %d: %#v", index, result)
		}
	}
	generated := regexp.MustCompile(`("UId": "[0-9a-f-]+",\s+"Name": "(?:TplSettings|Vals|Mod)",\s+"ModifiedOnUtc": ")[^"]*"`)
	golden := "testdata/databinding-clio/Data"
	err := filepath.Walk(golden, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		relative, _ := filepath.Rel(golden, path)
		want, _ := os.ReadFile(path)
		got, readErr := os.ReadFile(filepath.Join(root, "packages", "UsrParityPkg", "Data", relative))
		if readErr != nil {
			t.Errorf("%s: %v", relative, readErr)
			return nil
		}
		normalize := func(content []byte) string {
			text := strings.ReplaceAll(string(content), "\r\n", "\n")
			return regexp.MustCompile(`"UId": "[0-9a-f-]+",(\s+"Name": "(?:TplSettings|Vals|Mod)")`).ReplaceAllString(
				generated.ReplaceAllString(text, `$1"`), `"UId": "",$1`)
		}
		if normalize(got) != normalize(want) {
			t.Errorf("%s differs:\n%s\nwant:\n%s", relative, got, want)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	refusals := map[string]CommandResult{
		"Column 'Nope' is not part of binding 'Vals'.": AddDataBindingRowLocal(ctx, request(DataWriteLocalBindingRequest{BindingName: "Vals", ValuesJSON: `{"Nope":1}`})),
		"Column 'IsCacheable' value '5' is not valid for data type 'Boolean'.": AddDataBindingRowLocal(ctx,
			request(DataWriteLocalBindingRequest{BindingName: "Vals", ValuesJSON: `{"IsCacheable":5}`})),
		"Column 'Id' value 'nope' is not valid for data type 'Guid'.": RemoveDataBindingRowLocal(ctx,
			request(DataWriteLocalBindingRequest{BindingName: "Vals", KeyValue: "nope"})),
		"Binding 'Mod' already exists for schema 'SysModule'.": CreateDataBindingLocal(ctx, nil,
			request(DataWriteLocalBindingRequest{SchemaName: "SysSettings", BindingName: "Mod"})),
		"Column 'FolderMode' requires displayValue. Pass the value as an object like {\"value\":\"b659d704-3955-e011-981f-00155d043204\",\"displayValue\":\"...\"}.": CreateDataBindingLocal(ctx, nil,
			request(DataWriteLocalBindingRequest{SchemaName: "SysModule", BindingName: "Mod2", ValuesJSON: `{"Code":"UsrX","FolderMode":"b659d704-3955-e011-981f-00155d043204"}`})),
		"create-data-binding requires --environment or --uri.": CreateDataBindingLocal(ctx, nil, request(DataWriteLocalBindingRequest{SchemaName: "Contact"})),
	}
	for want, result := range refusals {
		if result.ExitCode != 1 || result.Messages[0].Value != want {
			t.Errorf("want %q, got %#v", want, result)
		}
	}
	if _, err := DataWriteValidateWorkspacePath("relative/ws"); err == nil || !strings.HasPrefix(err.Error(), "Workspace path must be absolute: relative/ws.") {
		t.Fatalf("relative path err=%v", err)
	}
}
