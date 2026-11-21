package main

import (
	"os"
	"path/filepath"
	"testing"
)

// writeTree creates the files under root described by path -> content.
// Parent directories are created as needed.
func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for path, content := range files {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("MkdirAll %s: %v", full, err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile %s: %v", full, err)
		}
	}
}

// findEntry returns the entry with the given path, or nil if absent.
func findEntry(entries []Entry, path string) *Entry {
	for i := range entries {
		if entries[i].Path == path {
			return &entries[i]
		}
	}
	return nil
}

// countRows returns the total number of rows in the files table.
func countRows(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM files`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

func TestSync_MirrorsTree(t *testing.T) {
	s := openTestStore(t)
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"a.txt":          "abc",
		"sub/b.txt":      "xy",
		"sub/deep/c.txt": "z",
	})

	// Entries: a.txt, sub, sub/b.txt, sub/deep, sub/deep/c.txt
	n, err := s.Sync(root)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if n != 5 {
		t.Fatalf("expected 5 entries, got %d", n)
	}
	if countRows(t, s) != 5 {
		t.Fatalf("expected 5 rows, got %d", countRows(t, s))
	}

	// A file at the root carries its size and is not a dir.
	rootEntries, err := s.List("")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if a := findEntry(rootEntries, "a.txt"); a == nil || a.Size != 3 || a.IsDir {
		t.Fatalf("unexpected a.txt entry: %+v", a)
	}

	// A nested directory entry is present under its parent.
	subEntries, err := s.List("sub")
	if err != nil {
		t.Fatalf("List(sub): %v", err)
	}
	if b := findEntry(subEntries, "sub/b.txt"); b == nil || b.Size != 2 {
		t.Fatalf("unexpected sub/b.txt entry: %+v", b)
	}
	if deep := findEntry(subEntries, "sub/deep"); deep == nil || !deep.IsDir {
		t.Fatalf("expected sub/deep dir entry, got: %+v", deep)
	}
}

func TestSync_Idempotent(t *testing.T) {
	s := openTestStore(t)
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"a.txt":     "one",
		"dir/b.txt": "two",
	})

	n1, err := s.Sync(root)
	if err != nil {
		t.Fatalf("Sync#1: %v", err)
	}
	n2, err := s.Sync(root)
	if err != nil {
		t.Fatalf("Sync#2: %v", err)
	}
	if n1 != n2 {
		t.Fatalf("expected stable entry count, got %d then %d", n1, n2)
	}
	if got := countRows(t, s); got != n1 {
		t.Fatalf("expected %d rows after re-sync, got %d", n1, got)
	}
}

func TestSync_AddRemove(t *testing.T) {
	s := openTestStore(t)
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"keep.txt": "keep",
		"gone.txt": "gone",
	})
	if _, err := s.Sync(root); err != nil {
		t.Fatalf("Sync#1: %v", err)
	}

	// Add one file, remove another, then re-sync.
	if err := os.WriteFile(filepath.Join(root, "new.txt"), []byte("new"), 0o644); err != nil {
		t.Fatalf("WriteFile new.txt: %v", err)
	}
	if err := os.Remove(filepath.Join(root, "gone.txt")); err != nil {
		t.Fatalf("Remove gone.txt: %v", err)
	}
	if _, err := s.Sync(root); err != nil {
		t.Fatalf("Sync#2: %v", err)
	}

	rootEntries, err := s.List("")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if findEntry(rootEntries, "new.txt") == nil {
		t.Fatal("new.txt should be present after re-sync")
	}
	if findEntry(rootEntries, "gone.txt") != nil {
		t.Fatal("gone.txt should be removed after re-sync")
	}
	if findEntry(rootEntries, "keep.txt") == nil {
		t.Fatal("keep.txt should remain after re-sync")
	}
}

func TestSync_SkipsSymlinks(t *testing.T) {
	s := openTestStore(t)
	root := t.TempDir()
	writeTree(t, root, map[string]string{"real.txt": "real"})

	target := filepath.Join(root, "real.txt")
	if err := os.Symlink(target, filepath.Join(root, "link.txt")); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	if _, err := s.Sync(root); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	rootEntries, err := s.List("")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if findEntry(rootEntries, "real.txt") == nil {
		t.Fatal("real.txt should be present")
	}
	if findEntry(rootEntries, "link.txt") != nil {
		t.Fatal("symlink link.txt should not be recorded")
	}
}

func TestSync_NonexistentRoot(t *testing.T) {
	s := openTestStore(t)

	// Seed an existing entry so we can assert it survives a failed sync.
	if err := s.Upsert(Entry{Path: "seed", Name: "seed"}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	bad := filepath.Join(t.TempDir(), "does-not-exist")
	if _, err := s.Sync(bad); err == nil {
		t.Fatal("expected error when syncing a nonexistent root")
	}

	entries, err := s.List("")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if findEntry(entries, "seed") == nil {
		t.Fatal("seed entry should survive a failed sync")
	}
}
