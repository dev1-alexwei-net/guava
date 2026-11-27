package tasks

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/dev1-alexwei-net/guava/conversations"
	_ "modernc.org/sqlite"
)

func TestLookupConstantUsesReadOnlySQLiteDatabase(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "constants.sqlite")
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`CREATE TABLE constants_fields (field_name TEXT PRIMARY KEY NOT NULL)`,
		`CREATE TABLE constants_values (field_name TEXT PRIMARY KEY NOT NULL, value TEXT NOT NULL)`,
		`INSERT INTO constants_fields (field_name) VALUES ('instructions')`,
		`INSERT INTO constants_values (field_name, value) VALUES ('instructions', 'Output <complete /> only when finished.')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("initialize test database: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	got, err := LookupConstant(context.Background(), databasePath, conversations.InstructionsKey)
	if err != nil || got != "Output <complete /> only when finished." {
		t.Fatalf("LookupConstant() = %q, err=%v", got, err)
	}

	index, err := openFileIndex(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer index.close()
	if _, err := index.db.Exec(`UPDATE constants_values SET value = 'changed'`); err == nil {
		t.Fatal("read-only database connection allowed a write")
	}
}
