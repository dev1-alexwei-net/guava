package executor

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// indexedFile is a test fixture shape for building executor integration-test
// manifests; production file-index ownership resides in package tasks.
type indexedFile struct {
	path  string
	name  string
	size  int64
	mtime int64
}

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
