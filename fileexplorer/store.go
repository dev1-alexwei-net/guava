package main

import (
	"context"
	"database/sql"
	"strings"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS files (
	path   TEXT PRIMARY KEY,
	name   TEXT NOT NULL,
	is_dir INTEGER NOT NULL DEFAULT 0,
	size   INTEGER NOT NULL DEFAULT 0,
	mtime  INTEGER NOT NULL DEFAULT 0
);`

// Entry represents a single file or directory in the replica.
type Entry struct {
	Path  string
	Name  string
	IsDir bool
	Size  int64
	Mtime int64
}

// Store is a SQLite-backed storage layer for the file explorer replica.
type Store struct {
	db *sql.DB
}

// Open opens (or creates) the SQLite database at path and applies the schema.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	if _, err := db.ExecContext(context.Background(), schema); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

// Close closes the underlying database.
func (s *Store) Close() error {
	return s.db.Close()
}

// List returns the direct children of dirPath.
// An empty dirPath means the root.
func (s *Store) List(dirPath string) ([]Entry, error) {
	ctx := context.Background()
	var rows *sql.Rows
	var err error

	if dirPath == "" || dirPath == "." {
		rows, err = s.db.QueryContext(ctx,
			`SELECT path, name, is_dir, size, mtime FROM files WHERE instr(path, '/') = 0 ORDER BY is_dir DESC, name`)
	} else {
		prefix := strings.TrimSuffix(dirPath, "/") + "/"
		rows, err = s.db.QueryContext(ctx,
			`SELECT path, name, is_dir, size, mtime FROM files
			 WHERE substr(path, 1, length(?)) = ?
			   AND instr(substr(path, length(?) + 1), '/') = 0
			 ORDER BY is_dir DESC, name`,
			prefix, prefix, prefix)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	entries := make([]Entry, 0, 16)
	for rows.Next() {
		var e Entry
		var isDir int
		if err := rows.Scan(&e.Path, &e.Name, &isDir, &e.Size, &e.Mtime); err != nil {
			return nil, err
		}
		e.IsDir = isDir != 0
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// ReplaceAll atomically replaces all rows in the files table with the given entries.
func (s *Store) ReplaceAll(entries []Entry) error {
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `DELETE FROM files`); err != nil {
		return err
	}

	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO files (path, name, is_dir, size, mtime) VALUES (?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, e := range entries {
		if _, err := stmt.ExecContext(ctx, e.Path, e.Name, boolToInt(e.IsDir), e.Size, e.Mtime); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
