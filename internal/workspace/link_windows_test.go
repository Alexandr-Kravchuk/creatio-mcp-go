//go:build windows

package workspace

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// On Windows clio's Directory.CreateSymbolicLink makes a directory symbolic link (not a junction), whether
// or not the target exists, and .NET's enumerator counts it as a folder by its own attribute.
func TestWindowsDirectorySymlink(t *testing.T) {
	requireSymlinks(t)
	root := t.TempDir()
	missing := filepath.Join(root, "missing")
	link := filepath.Join(root, "PkgA")
	if err := createDirectorySymlink(link, missing); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(link)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("lstat = %v, %v", info, err)
	}
	if target, err := os.Readlink(link); err != nil || target != missing {
		t.Fatalf("target = %q, %v", target, err)
	}
	names, err := listDirectories(root)
	if err != nil || len(names) != 1 || names[0] != "PkgA" {
		t.Fatalf("a dangling directory link is still a folder to .NET: %q, %v", names, err)
	}
	err = createDirectorySymlink(link, missing)
	if err == nil || err.Error() != "Cannot create '"+link+"' because a file or directory with the same name already exists." {
		t.Fatalf("existing = %v", err)
	}
	if err := deleteDirectory(link); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Fatalf("the link must be gone: %v", err)
	}
}

func TestWindowsLinkReplacesAFolderAndKeepsTheRepository(t *testing.T) {
	requireSymlinks(t)
	root := t.TempDir()
	repo, pkg := filepath.Join(root, "repo"), filepath.Join(root, "pkg")
	writeFile(t, filepath.Join(repo, "PkgA", "descriptor.json"), "{}")
	writeFile(t, filepath.Join(pkg, "PkgA", "old.txt"), "old")
	for range 2 {
		result := Link(context.Background(), LinkRequest{EnvPkgPath: pkg, RepoPath: repo, Packages: "PkgA", SkipPreparation: true}, LinkDeps{})
		assertMessages(t, result, 0, "Info: Skipping preparation (--skip-preparation).",
			"Info: Linking repository package(s) to environment "+pkg+" from "+repo)
	}
	if linkTarget(t, filepath.Join(pkg, "PkgA")) != filepath.Join(repo, "PkgA") || !fileExists(filepath.Join(repo, "PkgA", "descriptor.json")) {
		t.Fatal("PkgA must link to the repository, which keeps its files")
	}
}
