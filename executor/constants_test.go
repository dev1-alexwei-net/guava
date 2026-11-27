package executor

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/dev1-alexwei-net/guava/conversations"
)

func TestInitializeConstantsRecreatesTablesWithoutReplacingFileIndex(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "executor.sqlite")
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE files (path TEXT PRIMARY KEY)`); err != nil {
		t.Fatalf("create file index: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO files (path) VALUES ('keep.txt')`); err != nil {
		t.Fatalf("seed file index: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	want, ok := conversations.LookupConstant(conversations.InstructionsKey)
	if !ok {
		t.Fatal("instructions constant is not configured")
	}
	if err := initializeConstants(context.Background(), databasePath); err != nil {
		t.Fatalf("initialize constants: %v", err)
	}

	db, err = sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var got string
	if err := db.QueryRow(`
		SELECT constants_values.value
		FROM constants_fields
		JOIN constants_values USING (field_name)
		WHERE constants_fields.field_name = ?`, conversations.InstructionsKey).Scan(&got); err != nil {
		t.Fatalf("read instructions constant: %v", err)
	}
	if got != want {
		t.Fatalf("instructions constant = %q, want %q", got, want)
	}
	if _, err := db.Exec(`UPDATE constants_values SET value = 'stale value'`); err != nil {
		t.Fatalf("make constant stale: %v", err)
	}
	if err := initializeConstants(context.Background(), databasePath); err != nil {
		t.Fatalf("reinitialize constants: %v", err)
	}
	if err := db.QueryRow(`SELECT value FROM constants_values WHERE field_name = ?`, conversations.InstructionsKey).Scan(&got); err != nil {
		t.Fatalf("read recreated instructions constant: %v", err)
	}
	if got != want {
		t.Fatalf("recreated instructions constant = %q, want %q", got, want)
	}

	var files int
	if err := db.QueryRow(`SELECT COUNT(*) FROM files WHERE path = 'keep.txt'`).Scan(&files); err != nil {
		t.Fatalf("read file index: %v", err)
	}
	if files != 1 {
		t.Fatalf("file index row count = %d, want 1", files)
	}
}
