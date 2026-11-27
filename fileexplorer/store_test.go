package main

import (
	"os"
	"path/filepath"
	"testing"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestStoreListsDirectChildrenInOrder(t *testing.T) {
	s := openTestStore(t)
	if err := s.ReplaceAll([]Entry{
		{Path: "z.txt", Name: "z.txt"},
		{Path: "z-dir", Name: "z-dir", IsDir: true},
		{Path: "a.txt", Name: "a.txt"},
		{Path: "a-dir", Name: "a-dir", IsDir: true},
		{Path: "a-dir/child.txt", Name: "child.txt"},
	}); err != nil {
		t.Fatal(err)
	}
	root, err := s.List("")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a-dir", "z-dir", "a.txt", "z.txt"}
	if len(root) != len(want) {
		t.Fatalf("root entries=%+v, want %v", root, want)
	}
	for i, name := range want {
		if root[i].Name != name {
			t.Fatalf("root entry %d=%q, want %q", i, root[i].Name, name)
		}
	}
	children, err := s.List("a-dir")
	if err != nil || len(children) != 1 || children[0].Path != "a-dir/child.txt" {
		t.Fatalf("direct children=%+v, err=%v", children, err)
	}
}

func TestReplaceAllRollsBackOnFailure(t *testing.T) {
	s := openTestStore(t)
	if err := s.ReplaceAll([]Entry{{Path: "old", Name: "old"}}); err != nil {
		t.Fatal(err)
	}
	err := s.ReplaceAll([]Entry{{Path: "new", Name: "new"}, {Path: "new", Name: "duplicate"}})
	if err == nil {
		t.Fatal("duplicate path should fail the replacement")
	}
	entries, err := s.List("")
	if err != nil || len(entries) != 1 || entries[0].Path != "old" {
		t.Fatalf("failed replacement changed prior snapshot: entries=%+v err=%v", entries, err)
	}
}

func TestListTreatsWildcardCharactersLiterally(t *testing.T) {
	s := openTestStore(t)
	if err := s.ReplaceAll([]Entry{
		{Path: "literal%_/wanted.txt", Name: "wanted.txt"},
		{Path: "literalAA/other.txt", Name: "other.txt"},
		{Path: "literal%_/nested/hidden.txt", Name: "hidden.txt"},
	}); err != nil {
		t.Fatalf("ReplaceAll: %v", err)
	}

	got, err := s.List("literal%_")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].Path != "literal%_/wanted.txt" {
		t.Fatalf("List returned unexpected entries: %+v", got)
	}
}

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

func syncTestRoot(t *testing.T, s *Store, root string) (int, error) {
	t.Helper()
	filesystem, err := openRootedFS(root)
	if err != nil {
		return 0, err
	}
	defer filesystem.Close()
	return newSynchronizer(s, filesystem).Sync()
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
	n, err := syncTestRoot(t, s, root)
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

func TestSync_AddRemove(t *testing.T) {
	s := openTestStore(t)
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"keep.txt": "keep",
		"gone.txt": "gone",
	})
	firstCount, err := syncTestRoot(t, s, root)
	if err != nil {
		t.Fatalf("Sync#1: %v", err)
	}
	if count, err := syncTestRoot(t, s, root); err != nil || count != firstCount || countRows(t, s) != firstCount {
		t.Fatalf("unchanged sync changed the replica: count=%d err=%v", count, err)
	}

	// Add one file, remove another, then re-sync.
	if err := os.WriteFile(filepath.Join(root, "new.txt"), []byte("new"), 0o644); err != nil {
		t.Fatalf("WriteFile new.txt: %v", err)
	}
	if err := os.Remove(filepath.Join(root, "gone.txt")); err != nil {
		t.Fatalf("Remove gone.txt: %v", err)
	}
	if _, err := syncTestRoot(t, s, root); err != nil {
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
	if _, err := syncTestRoot(t, s, root); err != nil {
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
	if err := s.ReplaceAll([]Entry{{Path: "seed", Name: "seed"}}); err != nil {
		t.Fatalf("ReplaceAll: %v", err)
	}

	bad := filepath.Join(t.TempDir(), "does-not-exist")
	if _, err := syncTestRoot(t, s, bad); err == nil {
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

func TestOpenDoesNotCreateConstantsTables(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "fileexplorer.sqlite"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	var count int
	err = store.db.QueryRow(`
		SELECT COUNT(*) FROM sqlite_master
		WHERE type = 'table' AND name IN ('constants_fields', 'constants_values')`).Scan(&count)
	if err != nil {
		t.Fatalf("count constants tables: %v", err)
	}
	if count != 0 {
		t.Fatalf("fileexplorer created %d constants tables; constants are owned by executor", count)
	}
}
