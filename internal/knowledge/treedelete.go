package knowledge

import (
	"os"
	"path/filepath"
	"strings"
)

// deleteTree is clio's KnowledgeManagedTreeDeleter.Delete: read-only bits are cleared, links are removed
// without following them, and the tree is deleted. os.RemoveAll never descends through a link.
func deleteTree(root string) error {
	if _, err := os.Lstat(root); err != nil {
		return nil
	}
	clearReadOnly(root)
	return os.RemoveAll(root)
}

// deleteRecoverably is clio's DeleteRecoverably: abandoned quarantines of the same root are swept, the
// root is renamed to a sibling .clio-deleting-<name>-<guid> (nothing is lost when that fails) and the
// quarantine is deleted.
func deleteRecoverably(root string) error {
	parent := filepath.Dir(root)
	prefix := quarantinePrefix + filepath.Base(root) + "-"
	if entries, err := os.ReadDir(parent); err == nil {
		for _, entry := range entries {
			if entry.IsDir() && strings.HasPrefix(entry.Name(), prefix) {
				_ = deleteTree(filepath.Join(parent, entry.Name()))
			}
		}
	}
	if _, err := os.Lstat(root); err != nil {
		return nil
	}
	quarantine := filepath.Join(parent, prefix+newGUID())
	if err := os.Rename(root, quarantine); err != nil {
		return err
	}
	return deleteTree(quarantine)
}

func clearReadOnly(root string) {
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		info, infoErr := entry.Info()
		if infoErr != nil || isReparsePoint(info) {
			return nil
		}
		if info.Mode().Perm()&0o200 == 0 {
			_ = os.Chmod(path, info.Mode().Perm()|0o200)
		}
		return nil
	})
}
