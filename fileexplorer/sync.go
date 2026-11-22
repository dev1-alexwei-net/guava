package main

import (
	"io/fs"
	"os"
	"path/filepath"
)

// Sync mirrors root into the replica. The filesystem is opened as a rooted
// filesystem so scanning stays confined to the configured directory.
func (s *Store) Sync(rootPath string) (int, error) {
	root, err := openRootedFS(rootPath)
	if err != nil {
		return 0, err
	}
	defer root.Close()
	return s.SyncRoot(root)
}

// SyncRoot atomically replaces the replica with the entries under root.
func (s *Store) SyncRoot(root *rootedFS) (int, error) {
	if root == nil {
		return 0, os.ErrInvalid
	}
	entries := []Entry{}
	walkErr := fs.WalkDir(root.FS(), ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if name == "." {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		e := Entry{
			Path:  filepath.ToSlash(name),
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
