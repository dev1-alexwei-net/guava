package main

import (
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

func TestOpenCreatesSchema(t *testing.T) {
	s := openTestStore(t)
	// Inserting should work because schema was applied.
	if err := s.Upsert(Entry{Path: "a.txt", Name: "a.txt", IsDir: false, Size: 42}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
}

func TestListRoot(t *testing.T) {
	s := openTestStore(t)
	entries := []Entry{
		{Path: "a.txt", Name: "a.txt", Size: 10},
		{Path: "b.txt", Name: "b.txt", Size: 20},
		{Path: "dir/c.txt", Name: "c.txt", Size: 30},
	}
	if err := s.ReplaceAll(entries); err != nil {
		t.Fatalf("ReplaceAll: %v", err)
	}

	got, err := s.List("")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 root entries, got %d", len(got))
	}
	if got[0].Name != "a.txt" || got[1].Name != "b.txt" {
		t.Fatalf("unexpected root entries: %+v", got)
	}
}

func TestListSubdir(t *testing.T) {
	s := openTestStore(t)
	entries := []Entry{
		{Path: "root.txt", Name: "root.txt", Size: 1},
		{Path: "docs/readme.md", Name: "readme.md", Size: 100},
		{Path: "docs/img/logo.png", Name: "logo.png", Size: 500},
	}
	if err := s.ReplaceAll(entries); err != nil {
		t.Fatalf("ReplaceAll: %v", err)
	}

	got, err := s.List("docs")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 entry under docs, got %d: %+v", len(got), got)
	}
	if got[0].Path != "docs/readme.md" {
		t.Fatalf("unexpected path: %q", got[0].Path)
	}
}

func TestUpsertOverwrite(t *testing.T) {
	s := openTestStore(t)
	if err := s.Upsert(Entry{Path: "x", Name: "x", Size: 1}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := s.Upsert(Entry{Path: "x", Name: "x", Size: 999}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	entries, err := s.List("")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 || entries[0].Size != 999 {
		t.Fatalf("unexpected: %+v", entries)
	}
}

func TestDelete(t *testing.T) {
	s := openTestStore(t)
	if err := s.ReplaceAll([]Entry{{Path: "a", Name: "a"}, {Path: "b", Name: "b"}}); err != nil {
		t.Fatalf("ReplaceAll: %v", err)
	}
	if err := s.Delete("a"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	entries, _ := s.List("")
	if len(entries) != 1 || entries[0].Path != "b" {
		t.Fatalf("unexpected: %+v", entries)
	}
}

func TestReplaceAllIsAtomic(t *testing.T) {
	s := openTestStore(t)
	// Seed with one entry.
	if err := s.Upsert(Entry{Path: "old", Name: "old"}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	// Replace with a different set.
	new := []Entry{
		{Path: "new1", Name: "new1", Size: 10},
		{Path: "new2", Name: "new2", Size: 20},
	}
	if err := s.ReplaceAll(new); err != nil {
		t.Fatalf("ReplaceAll: %v", err)
	}

	entries, _ := s.List("")
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries after replace, got %d", len(entries))
	}
	for _, e := range entries {
		if e.Path == "old" {
			t.Fatal("old entry should have been removed")
		}
	}
}

func TestListIsDir(t *testing.T) {
	s := openTestStore(t)
	if err := s.ReplaceAll([]Entry{
		{Path: "sub", Name: "sub", IsDir: true},
		{Path: "file.txt", Name: "file.txt"},
	}); err != nil {
		t.Fatalf("ReplaceAll: %v", err)
	}
	entries, _ := s.List("")
	if len(entries) != 2 {
		t.Fatalf("expected 2, got %d", len(entries))
	}
	// Find the dir entry.
	var dir *Entry
	for i := range entries {
		if entries[i].IsDir {
			dir = &entries[i]
		}
	}
	if dir == nil {
		t.Fatal("expected at least one directory entry")
	}
	if dir.Path != "sub" {
		t.Fatalf("unexpected dir path: %q", dir.Path)
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
