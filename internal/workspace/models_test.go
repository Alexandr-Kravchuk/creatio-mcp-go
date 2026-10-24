package workspace

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
)

// modelFixtures is the fake site of the add-item-model parity scenario.
const modelFixtures = "../../scripts/workspace-scenarios/fixtures/codegen"

func modelFixtureClient(t *testing.T) *creatio.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/AuthService.svc/Login") {
			_, _ = w.Write([]byte(`{"Code":0}`))
			return
		}
		body, _ := io.ReadAll(r.Body)
		file := "entity-schemas.json"
		if strings.HasSuffix(r.URL.Path, "/RuntimeEntitySchemaRequest") {
			name := strings.TrimSuffix(strings.TrimPrefix(string(body), `{"Name" : "`), `"}`)
			file = "runtime-" + strings.ToLower(name) + ".json"
		}
		data, err := os.ReadFile(filepath.Join(modelFixtures, file))
		if err != nil {
			t.Errorf("unexpected request %s %s", r.URL.Path, body)
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(data)
	}))
	t.Cleanup(server.Close)
	client, _ := creatio.NewClient(creatio.Config{BaseURL: server.URL, Login: "example-user", Password: "replace-me", IsNetCore: true})
	return client
}

// TestGenerateModelsMatchesClioByteForByte compares every file with what clio 8.1.0.142 wrote for the same
// site (testdata/models, from the add-item-model parity run). Class files use the platform's line break;
// BaseModelExtensions.cs is CRLF everywhere.
func TestGenerateModelsMatchesClioByteForByte(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "models")
	result := GenerateModels(context.Background(), modelFixtureClient(t), "Acme.Models", destination)
	want := []creatio.LogMessage{
		{MessageType: "Info", Value: "Generating models..."},
		{MessageType: "None", Value: "Schema Country not found"},
		{MessageType: "None", Value: "Models will be generated in directory: " + destination},
		{MessageType: "Info", Value: "Generated 5 models; requested filter: none."},
	}
	if result.ExitCode != 0 || len(result.Messages) != len(want) {
		t.Fatalf("result = %#v", result)
	}
	for index := range want {
		if result.Messages[index] != want[index] {
			t.Fatalf("message %d = %#v, want %#v", index, result.Messages[index], want[index])
		}
	}
	golden, _ := filepath.Glob(filepath.Join("testdata", "models", "*.cs"))
	written, _ := filepath.Glob(filepath.Join(destination, "*"))
	if len(golden) != 6 || len(written) != len(golden) {
		t.Fatalf("golden %v, written %v", golden, written)
	}
	for _, path := range golden {
		expected, _ := os.ReadFile(path)
		text := strings.ReplaceAll(string(expected), "\r\n", "\n")
		if filepath.Base(path) == modelExtensionsFileName {
			text = strings.ReplaceAll(text, "\n", "\r\n")
		} else {
			text = strings.ReplaceAll(text, "\n", newLine())
			// A column description's own line break stays "\n": clio appends the host newline only after it.
			text = strings.ReplaceAll(text, "Notes about the contact."+newLine()+"Free text.", "Notes about the contact.\nFree text.")
		}
		actual, err := os.ReadFile(filepath.Join(destination, filepath.Base(path)))
		if err != nil || string(actual) != text {
			t.Errorf("%s differs (err %v):\n%s", filepath.Base(path), err, actual)
		}
	}
}

type modelSourceFunc func() ([]*creatio.ModelSchema, []creatio.LogMessage, error)

func (f modelSourceFunc) EntityModelSchemas(context.Context, string) ([]*creatio.ModelSchema, []creatio.LogMessage, error) {
	return f()
}

func TestGenerateModelsRefusalsAndEmptyList(t *testing.T) {
	never := modelSourceFunc(func() ([]*creatio.ModelSchema, []creatio.LogMessage, error) {
		t.Fatal("no read expected")
		return nil, nil, nil
	})
	if result := GenerateModels(context.Background(), never, "  ", t.TempDir()); result.ExitCode != 1 ||
		result.Messages[0].Value != "Namespace is required for model generation." || len(result.Messages) != 1 {
		t.Fatalf("blank namespace = %#v", result)
	}
	failing := modelSourceFunc(func() ([]*creatio.ModelSchema, []creatio.LogMessage, error) {
		return nil, []creatio.LogMessage{{MessageType: "None", Value: "Schema A not found"}}, errors.New("boom")
	})
	result := GenerateModels(context.Background(), failing, "M", t.TempDir())
	if result.ExitCode != 1 || len(result.Messages) != 3 || result.Messages[1].Value != "Schema A not found" || result.Messages[2] != (creatio.LogMessage{MessageType: "Error", Value: "boom"}) {
		t.Fatalf("failure = %#v", result)
	}
	empty := modelSourceFunc(func() ([]*creatio.ModelSchema, []creatio.LogMessage, error) { return nil, nil, nil })
	folder := t.TempDir()
	for _, name := range []string{"Old.cs", "Other.CS", "notes.txt", "baseModelExtensions.cs"} {
		if err := os.WriteFile(filepath.Join(folder, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	result = GenerateModels(context.Background(), empty, "M", folder)
	if last := result.Messages[len(result.Messages)-1].Value; result.ExitCode != 0 || last != "Generated 2 models; requested filter: none." {
		t.Fatalf("empty list = %#v", result)
	}
	if text, _ := os.ReadFile(filepath.Join(folder, modelExtensionsFileName)); !strings.HasPrefix(string(text), "using System.Collections.Generic;\r\n") {
		t.Fatalf("extension class = %q", text)
	}
}

func TestModelWriterDetailsAndColumns(t *testing.T) {
	text := func(value string) *string { return &value }
	schemas := []*creatio.ModelSchema{
		{Name: "M", Columns: []creatio.ModelColumn{{Name: "Id", DataValueType: 0}}},
		{Name: "D", Columns: []creatio.ModelColumn{
			{Name: "UsrM", DataValueType: 10, ReferenceSchemaName: text("M")},
			{Name: "Usr_M", DataValueType: 10, ReferenceSchemaName: text("M")},
			{Name: "Gone", DataValueType: 10, ReferenceSchemaName: text("Missing")},
			{Name: "Blank", DataValueType: 1, ReferenceSchemaName: text(" ")},
		}},
	}
	writer := newModelWriter("N", schemas)
	var names []string
	for _, detail := range writer.sortedDetails("M") {
		names = append(names, detail.name)
	}
	if strings.Join(names, ",") != "CollectionOfDByUsr_M,CollectionOfDByUsrM" || len(writer.details["Missing"]) != 0 {
		t.Fatalf("details = %v / %#v", names, writer.details)
	}
	class := writer.classText(schemas[1])
	for _, want := range []string{"public virtual   Blank { get; set; }", "public string BlankId { get; set; }", "public virtual Missing Gone { get; set; }"} {
		if !strings.Contains(class, want) {
			t.Fatalf("class lacks %q:\n%s", want, class)
		}
	}
	types := map[int]string{0: "Guid", 1: "string", 4: "int", 5: "Single", 6: "decimal", 9: "DateTime", 11: "Guid", 12: "bool",
		13: "byte[]", 14: "string", 23: "String", 24: "String", 34: "decimal", 40: "decimal", 42: "string", 99: "string"}
	keys := make([]int, 0, len(types))
	for key := range types {
		keys = append(keys, key)
	}
	sort.Ints(keys)
	for _, key := range keys {
		if got := modelType(key); got != types[key] {
			t.Errorf("type %d = %s, want %s", key, got, types[key])
		}
	}
}

func TestModelFolderChecks(t *testing.T) {
	cases := map[string]string{"": "Folder is required.", "  ": "Folder is required.",
		"//server/share": "Folder path must be a local absolute path: //server/share",
		`\\server\share`: `Folder path must be a local absolute path: \\server\share`,
		"models":         "Folder path must be absolute: models"}
	for folder, want := range cases {
		if got := ValidateModelFolder(folder); got != want {
			t.Errorf("%q: %q", folder, got)
		}
	}
	root := t.TempDir()
	if runtime.GOOS != "windows" {
		if got := ValidateModelFolder(root); got != "" {
			t.Fatalf("absolute folder refused: %q", got)
		}
	}
	file := filepath.Join(root, "afile")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, message := PrepareModelFolder(file); message != "Failed to create output folder '"+file+"': The file '"+file+"' already exists." {
		t.Fatalf("file in the way: %q", message)
	}
	// Windows reports a path under a file with its own error, not ENOTDIR; clio's Windows text is unmeasured.
	if runtime.GOOS != "windows" {
		below := filepath.Join(file, "sub")
		if _, message := PrepareModelFolder(below); message != "Failed to create output folder '"+below+"': Could not find a part of the path '"+below+"'." {
			t.Fatalf("below a file: %q", message)
		}
	}
	created, message := PrepareModelFolder(filepath.Join(root, "x", "..", "made") + string(filepath.Separator))
	if message != "" || created != filepath.Join(root, "made")+string(filepath.Separator) || !directoryExists(created) {
		t.Fatalf("created %q, %q", created, message)
	}
}
