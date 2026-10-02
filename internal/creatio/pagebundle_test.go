package creatio

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mustJNode(t *testing.T, text string) *jnode {
	t.Helper()
	node, err := parseJNode([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	return node
}

func TestViewConfigDiffAppliesInsertMergeMoveRemoveAcrossLayers(t *testing.T) {
	base := mustJNode(t, `[
		{"operation":"insert","name":"Main","values":{"type":"crt.FlexContainer","items":[]}},
		{"operation":"insert","name":"Side","values":{"type":"crt.FlexContainer","items":[]}},
		{"operation":"insert","name":"Title","parentName":"Main","propertyName":"items","values":{"type":"crt.Label","caption":"A"}},
		{"operation":"insert","name":"Gone","parentName":"Main","propertyName":"items","values":{"type":"crt.Label"}}]`)
	layer := mustJNode(t, `[
		{"operation":"merge","name":"Title","values":{"caption":"B"}},
		{"operation":"move","name":"Title","parentName":"Side","propertyName":"items","index":0},
		{"operation":"remove","name":"Gone"}]`)
	result := newJSONDiffApplier(false).applyDiff(newArray(), []*jnode{base, layer}, nil)
	got := string(result.newtonsoftJSON())
	want := `[{"type":"crt.FlexContainer","items":[],"name":"Main"},{"type":"crt.FlexContainer","items":[{"type":"crt.Label","caption":"B","name":"Title"}],"name":"Side"}]`
	if got != want {
		t.Fatalf("view config =\n%s\nwant\n%s", got, want)
	}
}

func TestViewConfigDiffRejectsLoopDependency(t *testing.T) {
	_, err := safePageBundle([]pageBundlePart{{schema: pageLayer{Name: "P", Parameters: newArray(), LocalizableStrings: newArray(), OptionalProperties: newArray()},
		parsed: parsedWith(t, `[{"operation":"insert","name":"A","parentName":"A"}]`)}}, "P")
	if err == nil || !strings.Contains(err.Error(), `Cyclic dependency exists for object "A"`) || !strings.HasPrefix(err.Error(), "Failed to resolve page bundle for 'P'") {
		t.Fatalf("err = %v", err)
	}
}

func parsedWith(t *testing.T, viewConfigDiff string) parsedPageBody {
	parsed := emptyParsedPageBody()
	parsed.viewConfigDiff = mustJNode(t, viewConfigDiff)
	return parsed
}

func TestPathDiffMergesViewModelConfigByPath(t *testing.T) {
	source := mustJNode(t, `{"attributes":{"Name":{"modelConfig":{"path":"PDS.Name"}}}}`)
	diff := mustJNode(t, `[{"operation":"merge","path":["attributes"],"values":{"Phone":{"modelConfig":{"path":"PDS.Phone"}}}},
		{"operation":"merge","path":["attributes","Name"],"values":{"modelConfig":{"path":"PDS.Title"}}}]`)
	got := string(newJSONDiffApplier(true).apply(source, diff, nil).newtonsoftJSON())
	want := `{"attributes":{"Name":{"modelConfig":{"path":"PDS.Title"}},"Phone":{"modelConfig":{"path":"PDS.Phone"}}}}`
	if got != want {
		t.Fatalf("config = %s, want %s", got, want)
	}
}

func TestSTJSerializationEscapesLikeSystemTextJSON(t *testing.T) {
	node := mustJNode(t, `{"a":"It's <b> & \"q\" + ü 😀\n","n":1.50,"e":1e20,"i":7}`)
	got := string(node.stjJSON())
	want := `{"a":"It\u0027s \u003Cb\u003E \u0026 \u0022q\u0022 \u002B \u00FC \uD83D\uDE00\n","n":1.5,"e":1E+20,"i":7}`
	if got != want {
		t.Fatalf("stj =\n%s\nwant\n%s", got, want)
	}
	if dotnetDouble(2) != "2.0" || dotnetDouble(1e-7) != "1E-07" || dotnetDouble(0.0001) != "0.0001" {
		t.Fatalf("double formatting: %s %s %s", dotnetDouble(2), dotnetDouble(1e-7), dotnetDouble(0.0001))
	}
}

func TestWritePageFilesReplacesSchemaDirectoryAndReturnsPaths(t *testing.T) {
	dir := t.TempDir()
	client := newFormsTestClient(t, "http://example.invalid")
	result := PageGetResult{Success: true, Page: &PageMetadata{SchemaName: "P"}, fullPage: &PageMetadata{SchemaName: "P", SchemaType: "web"},
		Editable: &PageEditableInfo{}, bundle: mustJNode(t, `{"name":"P"}`), rawBody: "body"}
	stale := filepath.Join(dir, ".clio-pages", "P", "stale.txt")
	_ = os.MkdirAll(filepath.Dir(stale), 0o755)
	_ = os.WriteFile(stale, []byte("x"), 0o644)
	written := client.WritePageFiles(result, "P", dir)
	if !written.Success || written.Files == nil || written.Files.MetaFile != filepath.Join(dir, ".clio-pages", "P", "meta.json") {
		t.Fatalf("written = %#v", written)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("previous generation was not replaced: %v", err)
	}
	body, _ := os.ReadFile(written.Files.BodyFile)
	meta, _ := os.ReadFile(written.Files.MetaFile)
	ignore, _ := os.ReadFile(filepath.Join(dir, ".clio-pages", ".gitignore"))
	if string(body) != "body" || string(ignore) != "*\n!.gitignore\n" ||
		!strings.Contains(string(meta), `"page":{"schemaName":"P","schemaUId":"","packageName":"","currentLeafPackageName":"","packageUId":"","parentSchemaName":"","willCreateReplacingInDesignPackage":false,"schema-type":"web"},"baseline":{"schemaName":"P","environmentUri":"http://example.invalid","editableSchemaExists":false,"capturedAt":`) {
		t.Fatalf("body = %q, ignore = %q, meta = %s", body, ignore, meta)
	}
	if invalid := client.WritePageFiles(result, "../P", dir); invalid.Success || !strings.HasPrefix(invalid.Error, "Invalid schema name") {
		t.Fatalf("invalid = %#v", invalid)
	}
}

func TestPageOutputAnchorPrefersWorkspaceRoot(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "a", "b")
	_ = os.MkdirAll(nested, 0o755)
	_ = os.MkdirAll(filepath.Join(root, ".clio"), 0o755)
	_ = os.WriteFile(filepath.Join(root, ".clio", "workspaceSettings.json"), []byte("{}"), 0o644)
	t.Chdir(nested)
	anchor, err := pageOutputAnchor("")
	resolvedRoot, _ := filepath.EvalSymlinks(root)
	resolvedAnchor, _ := filepath.EvalSymlinks(anchor)
	if err != nil || resolvedAnchor != resolvedRoot {
		t.Fatalf("anchor = %q, want %q, err = %v", anchor, root, err)
	}
	if explicit, _ := pageOutputAnchor("rel"); explicit != filepath.Join(nested, "rel") && !strings.HasSuffix(explicit, filepath.Join("b", "rel")) {
		t.Fatalf("explicit anchor = %q", explicit)
	}
}
