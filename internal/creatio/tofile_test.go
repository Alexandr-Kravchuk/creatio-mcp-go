package creatio

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestToFileExtractHeadingsFollowsClio(t *testing.T) {
	markdown := strings.Join([]string{
		"# Title",
		"Text # not a heading",
		"   ## Indented three ##",
		"    # four spaces is code",
		"#NoSpace",
		"####### seven",
		"## C#",
		"### Closing ###   ",
		"```js",
		"# comment in code",
		"~~~",
		"# still code: a ~~~ fence does not close ```",
		"```",
		"## After fence\r",
		"````",
		"```",
		"# inside the longer fence",
		"````",
		"###### Six",
		"# ",
		"#",
	}, "\n")
	want := []string{"# Title", "## Indented three", "## C#", "### Closing", "## After fence", "###### Six", "#"}
	if got := ToFileExtractHeadings(markdown); !reflect.DeepEqual(got, want) {
		t.Fatalf("headings = %q, want %q", got, want)
	}
	if got := ToFileExtractHeadings("no headings"); got == nil || len(got) != 0 {
		t.Fatalf("no headings = %#v, want an empty list", got)
	}
}

func TestToFileResolveConfinesAndRefusesExistingTargets(t *testing.T) {
	workspace := t.TempDir()
	t.Chdir(workspace)
	t.Setenv("CLIO_HOME", filepath.Join(t.TempDir(), "clio-home"))
	if _, refusal := ToFileResolve("  "); refusal != "output-file is required." {
		t.Fatalf("blank = %q", refusal)
	}
	outside := "/etc/t21b.json"
	if runtime.GOOS == "windows" {
		outside = `C:\Windows\t21b.json`
	}
	if _, refusal := ToFileResolve(outside); refusal != "output-file '"+outside+"' resolves outside the allowed locations; it must be inside the workspace or the OS temp directory." {
		t.Fatalf("outside = %q", refusal)
	}
	path, refusal := ToFileResolve(filepath.Join("a", "b", "out.json"))
	if refusal != "" {
		t.Fatalf("relative path refused: %s", refusal)
	}
	realWorkspace, _ := filepath.EvalSymlinks(workspace)
	if want := filepath.Join(realWorkspace, "a", "b", "out.json"); !strings.EqualFold(path, want) {
		t.Fatalf("canonical path = %s, want %s", path, want)
	}
	if refusal := ToFileWriteNew(path, []byte("first")); refusal != "" {
		t.Fatalf("write: %s", refusal)
	}
	if content, _ := os.ReadFile(path); string(content) != "first" {
		t.Fatalf("content = %q", content)
	}
	if runtime.GOOS != "windows" {
		info, _ := os.Stat(path)
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("file mode = %v, want 0600", info.Mode().Perm())
		}
	}
	// The pre-check refuses an existing file and an existing directory with the caller's spelling.
	if _, refusal := ToFileResolve(filepath.Join("a", "b", "out.json")); !strings.HasPrefix(refusal, "output-file '"+filepath.Join("a", "b", "out.json")+"' already exists; refusing to overwrite it.") {
		t.Fatalf("existing = %q", refusal)
	}
	if _, refusal := ToFileResolve("a"); !strings.Contains(refusal, "already exists") {
		t.Fatalf("existing directory = %q", refusal)
	}
	// The publish itself never replaces a file created after the check, and leaves no staging file.
	if refusal := ToFileWriteNew(path, []byte("second")); !strings.Contains(refusal, "already exists; refusing to overwrite it") {
		t.Fatalf("race = %q", refusal)
	}
	if content, _ := os.ReadFile(path); string(content) != "first" {
		t.Fatalf("content after refused write = %q", content)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatalf("leftover files: %v", entries)
	}
	// clio's own configuration directory is refused even inside the workspace.
	t.Setenv("CLIO_HOME", filepath.Join(workspace, "clio"))
	if _, refusal := ToFileResolve(filepath.Join("clio", "x.json")); !strings.Contains(refusal, "resolves inside clio's own configuration directory") {
		t.Fatalf("clio home = %q", refusal)
	}
}

func TestToFileProjectDocumentation(t *testing.T) {
	t.Chdir(t.TempDir())
	type answer struct {
		Success       bool    `json:"success"`
		Mode          string  `json:"mode"`
		Documentation *string `json:"documentation,omitempty"`
		Source        *string `json:"documentationSource,omitempty"`
	}
	path, _ := ToFileResolve("doc.md")
	docs, source := "# A\n\n```\n# not\n```\n## B ##\n", "cdn"
	projected := ToFileProjectDocumentation(answer{true, "detail", &docs, &source}, &docs, path)
	var decoded map[string]any
	if err := json.Unmarshal(projected, &decoded); err != nil {
		t.Fatal(err)
	}
	if _, has := decoded["documentation"]; has || decoded["documentationFile"] != path ||
		!reflect.DeepEqual(decoded["documentationSections"], []any{"# A", "## B"}) || decoded["documentationSource"] != "cdn" {
		t.Fatalf("projected = %s", projected)
	}
	// documentationFile and documentationSections come last, after every inline member.
	if !strings.HasSuffix(string(projected), `"documentationFile":"`+strings.ReplaceAll(path, `\`, `\\`)+`","documentationSections":["# A","## B"]}`) {
		t.Fatalf("member order: %s", projected)
	}
	if content, _ := os.ReadFile(path); string(content) != docs {
		t.Fatalf("file = %q", content)
	}
	// Without documentation nothing is written and the inline answer comes back unchanged.
	other, _ := ToFileResolve("none.md")
	unchanged := ToFileProjectDocumentation(answer{true, "list", nil, nil}, nil, other)
	if string(unchanged) != `{"success":true,"mode":"list"}` {
		t.Fatalf("unchanged = %s", unchanged)
	}
	if _, err := os.Stat(other); !os.IsNotExist(err) {
		t.Fatalf("a file was written without documentation")
	}
}
