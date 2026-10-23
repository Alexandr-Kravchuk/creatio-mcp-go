//go:build !windows

package workspace

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// The .NET texts on Unix, as clio 8.1.0.142 printed them on macOS (scripts/workspace-scenarios/link.json).
func TestLinkUnixFailureTexts(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	writeFile(t, filepath.Join(repo, "PkgA", "descriptor.json"), "{}")
	writeFile(t, filepath.Join(root, "pkg", "PkgA"), "a file named like the package")
	writeFile(t, filepath.Join(root, "afile"), "")
	skipping := "Info: Skipping preparation (--skip-preparation)."
	result := Link(context.Background(), LinkRequest{EnvPkgPath: filepath.Join(root, "pkg"), RepoPath: repo, Packages: "PkgA", SkipPreparation: true}, LinkDeps{})
	assertMessages(t, result, 1, skipping, "Error: Error during linking: The file '"+filepath.Join(root, "pkg", "PkgA")+"' already exists.")
	result = Link(context.Background(), LinkRequest{EnvPkgPath: filepath.Join(root, "afile"), RepoPath: repo, Packages: "PkgA", SkipPreparation: true}, LinkDeps{})
	assertMessages(t, result, 1, skipping, "Error: Error during linking: Could not find a part of the path '"+filepath.Join(root, "afile")+"'.")
}

// A link to a folder is a folder to .NET's enumerator, a dangling one is not; the order is the file system's.
func TestListDirectoriesFollowsLinks(t *testing.T) {
	root := t.TempDir()
	mkdir(t, filepath.Join(root, "real"))
	writeFile(t, filepath.Join(root, "file"), "")
	if err := os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "gone"), filepath.Join(root, "dangling")); err != nil {
		t.Fatal(err)
	}
	names, err := listDirectories(root)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, name := range names {
		found[name] = true
	}
	if len(names) != 2 || !found["real"] || !found["linked"] {
		t.Fatalf("names = %q", names)
	}
	// Deleting the link leaves its target.
	if err := deleteDirectory(filepath.Join(root, "linked")); err != nil || !directoryExists(filepath.Join(root, "real")) {
		t.Fatalf("delete link: %v", err)
	}
}
