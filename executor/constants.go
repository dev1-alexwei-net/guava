package executor

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/dev1-alexwei-net/guava/conversations"
	_ "modernc.org/sqlite"
)

// initializeConstants rebuilds the constants tables when the executor starts.
// The file explorer owns only its filesystem index; the executor owns the
// values consumed by its task-processing services.
func initializeConstants(ctx context.Context, databasePath string) (err error) {
	instructions, ok := conversations.LookupConstant(conversations.InstructionsKey)
	if !ok {
		return errors.New("default instructions constant is not configured")
	}

	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		return fmt.Errorf("open constants database: %w", err)
	}
	defer func() { err = errors.Join(err, db.Close()) }()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin constants table transaction: %w", err)
	}
	defer tx.Rollback()

	for _, statement := range []string{
		`DROP TABLE IF EXISTS constants_values`,
		`DROP TABLE IF EXISTS constants_fields`,
		`CREATE TABLE constants_fields (field_name TEXT PRIMARY KEY NOT NULL)`,
		`CREATE TABLE constants_values (
			field_name TEXT PRIMARY KEY NOT NULL REFERENCES constants_fields(field_name),
			value TEXT NOT NULL
		)`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("initialize constants table: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO constants_fields (field_name) VALUES (?)`, conversations.InstructionsKey); err != nil {
		return fmt.Errorf("insert constants field: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO constants_values (field_name, value) VALUES (?, ?)`, conversations.InstructionsKey, instructions); err != nil {
		return fmt.Errorf("insert constants value: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit constants table: %w", err)
	}
	return nil
}
