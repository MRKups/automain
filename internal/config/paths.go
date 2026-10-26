package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ValidateDatabasePaths rejects path, symlink, and hard-link aliases, including
// paths whose parent is a symlink but whose database file does not exist yet.
// The composition root rechecks after opening the databases.
func (c Config) ValidateDatabasePaths() error {
	paths := []string{c.DBPath, c.IndexPath()}
	if strings.TrimSpace(c.VectorDBPath) != "" {
		paths = append(paths, c.VectorDBPath)
	}
	resolved := make([]string, len(paths))
	infos := make([]os.FileInfo, len(paths))
	for i, path := range paths {
		absolute, err := filepath.Abs(path)
		if err != nil {
			return fmt.Errorf("resolve database path: %w", err)
		}
		resolved[i], err = resolvePath(absolute)
		if err != nil {
			return fmt.Errorf("resolve database path: %w", err)
		}
		info, err := os.Stat(absolute)
		if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("inspect database path: %w", err)
		}
		infos[i] = info
		for j := 0; j < i; j++ {
			if resolved[i] == resolved[j] || (infos[i] != nil && infos[j] != nil && os.SameFile(infos[i], infos[j])) {
				return fmt.Errorf("source, index catalog, and explicit legacy database paths must refer to separate files")
			}
		}
	}
	return nil
}

func resolvePath(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		return resolved, nil
	}
	if !os.IsNotExist(err) {
		return "", err
	}
	// Resolve the link itself before falling back to its parent, so dangling
	// symlinks cannot alias a file that the factory is about to create.
	info, statErr := os.Lstat(path)
	if statErr != nil && !os.IsNotExist(statErr) {
		return "", statErr
	}
	if statErr == nil && info.Mode()&os.ModeSymlink != 0 {
		target, linkErr := os.Readlink(path)
		if linkErr != nil {
			return "", linkErr
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(path), target)
		}
		return resolvePath(target)
	}
	parent := filepath.Dir(path)
	if parent == path {
		return "", err
	}
	resolved, err = resolvePath(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolved, filepath.Base(path)), nil
}
