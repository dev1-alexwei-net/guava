package tasks

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"
)

func TestListSubdirectoriesUnionsDirectoryRowsAndFilePrefixes(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "fileexplorer.sqlite")
	createIndexFixture(t, databasePath,
		[]indexedFile{
			{path: "zeta/a.txt", name: "a.txt", size: 1, mtime: 1},
			{path: "alpha/beta/deep.txt", name: "deep.txt", size: 2, mtime: 2},
			{path: "sibling/file.txt", name: "file.txt", size: 3, mtime: 3},
			{path: "root-file.txt", name: "root-file.txt", size: 4, mtime: 4},
		},
		[]string{"zeta", "alpha", "alpha/beta"},
	)

	got, err := ListSubdirectories(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"alpha", "alpha/beta", "sibling", "zeta"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ListSubdirectories() = %#v, want %#v", got, want)
	}
}

func TestListSubdirectoriesExcludesFoldersWithoutFiles(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "fileexplorer.sqlite")
	createIndexFixture(t, databasePath,
		[]indexedFile{{path: "used/leaf.txt", name: "leaf.txt", size: 1, mtime: 1}},
		[]string{"empty", "empty/nested", "used"},
	)

	got, err := ListSubdirectories(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"used"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ListSubdirectories() = %#v, want %#v", got, want)
	}
}

func TestListSubdirectoriesReturnsEmptyListForAnEmptyManifest(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "fileexplorer.sqlite")
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE files (path TEXT PRIMARY KEY, name TEXT NOT NULL, is_dir INTEGER NOT NULL DEFAULT 0, size INTEGER NOT NULL DEFAULT 0, mtime INTEGER NOT NULL DEFAULT 0)"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	got, err := ListSubdirectories(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || len(got) != 0 {
		t.Fatalf("ListSubdirectories() = %#v, want an empty non-nil list", got)
	}
}
