package tasks

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func createIndexFixture(t *testing.T, databasePath string, entries []indexedFile, directories []string) {
	t.Helper()

	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	_, err = db.Exec("CREATE TABLE files (path TEXT PRIMARY KEY, name TEXT NOT NULL, is_dir INTEGER NOT NULL DEFAULT 0, size INTEGER NOT NULL DEFAULT 0, mtime INTEGER NOT NULL DEFAULT 0)")
	if err != nil {
		t.Fatal(err)
	}
	for _, directory := range directories {
		if _, err := db.Exec(
			"INSERT INTO files (path, name, is_dir, size, mtime) VALUES (?, ?, 1, 0, 0)",
			directory, filepath.Base(directory)); err != nil {
			t.Fatal(err)
		}
	}
	for _, file := range entries {
		if _, err := db.Exec(
			"INSERT INTO files (path, name, is_dir, size, mtime) VALUES (?, ?, 0, ?, ?)",
			file.path, file.name, file.size, file.mtime); err != nil {
			t.Fatal(err)
		}
	}
}

func TestFileIndexReturnsRegularFilesInStablePathOrder(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "fileexplorer.sqlite")
	createIndexFixture(t, databasePath,
		[]indexedFile{
			{path: "zeta.txt", name: "zeta.txt", size: 9, mtime: 3},
			{path: "dir/beta.txt", name: "beta.txt", size: 4, mtime: 2},
			{path: "alpha.txt", name: "alpha.txt", size: 5, mtime: 1},
		},
		[]string{"dir"},
	)

	index, err := openFileIndex(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer index.close()

	got, err := index.regularFiles(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []indexedFile{
		{path: "alpha.txt", name: "alpha.txt", size: 5, mtime: 1},
		{path: "dir/beta.txt", name: "beta.txt", size: 4, mtime: 2},
		{path: "zeta.txt", name: "zeta.txt", size: 9, mtime: 3},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("regularFiles() = %#v, want %#v", got, want)
	}
}

func TestOpenFileIndexDoesNotCreateMissingDatabase(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "missing.sqlite")
	if _, err := openFileIndex(context.Background(), databasePath); err == nil {
		t.Fatal("openFileIndex() succeeded for a missing database")
	}
	if _, err := os.Stat(databasePath); !os.IsNotExist(err) {
		t.Fatalf("database stat error = %v, want not-exist", err)
	}
}

func TestOpenFileIndexRequiresFileexplorerSchemaToList(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "empty.sqlite")
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Ping(); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	index, err := openFileIndex(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer index.close()

	if _, err := index.regularFiles(context.Background()); err == nil {
		t.Fatal("regularFiles() succeeded without the fileexplorer files table")
	}
}
