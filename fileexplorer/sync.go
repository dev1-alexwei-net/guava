package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

type scanner struct {
	root *rootedFS
}

func (s *scanner) Scan() ([]Entry, error) {
	if s == nil || s.root == nil {
		return nil, errors.New("rooted filesystem is required")
	}
	entries := []Entry{}
	err := fs.WalkDir(s.root.FS(), ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if name == "." || d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		entry := Entry{Path: filepath.ToSlash(name), Name: d.Name(), IsDir: d.IsDir()}
		if !entry.IsDir {
			entry.Size = info.Size()
		}
		entry.Mtime = info.ModTime().Unix()
		entries = append(entries, entry)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return entries, nil
}

type synchronizer struct {
	scanner *scanner
	store   *Store
}

func newSynchronizer(store *Store, root *rootedFS) *synchronizer {
	return &synchronizer{scanner: &scanner{root: root}, store: store}
}

// Sync scans the filesystem first, then atomically replaces the persisted replica.
func (s *synchronizer) Sync() (int, error) {
	if s == nil || s.scanner == nil || s.store == nil {
		return 0, errors.New("synchronizer dependencies are required")
	}
	entries, err := s.scanner.Scan()
	if err != nil {
		return 0, err
	}
	if err := s.store.ReplaceAll(entries); err != nil {
		return 0, err
	}
	return len(entries), nil
}
