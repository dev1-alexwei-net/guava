package main

import (
	"io/fs"
	"os"
	"path/filepath"
)

// Sync walks root and atomically replaces the replica in the files table
// with the current directory tree under root.
//
// Paths are stored relative to root (slash-separated), so no path outside
// root is ever recorded. The root directory itself is not recorded; only
// its children and descendants are. Symlinks are skipped by default.
//
// If the walk fails, the previous replica is left untouched.
func (s *Store) Sync(root string) (int, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return 0, err
	}
	abs = filepath.Clean(abs)

	entries := []Entry{}
	walkErr := filepath.WalkDir(abs, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == abs {
			return nil // skip the root itself
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil // skip symlinks
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(abs, path)
		if err != nil {
			return err
		}
		e := Entry{
			Path:  filepath.ToSlash(rel),
			Name:  d.Name(),
			IsDir: d.IsDir(),
		}
		if !d.IsDir() {
			e.Size = info.Size()
		}
		e.Mtime = info.ModTime().Unix()
		entries = append(entries, e)
		return nil
	})
	if walkErr != nil {
		return 0, walkErr
	}

	if err := s.ReplaceAll(entries); err != nil {
		return 0, err
	}
	return len(entries), nil
}
