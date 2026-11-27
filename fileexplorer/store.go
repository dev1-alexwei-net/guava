package main

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/dev1-alexwei-net/guava/conversations"
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
	if err := recreateConstants(context.Background(), db); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

// recreateConstants rebuilds the read-only constants tables during startup.
func recreateConstants(ctx context.Context, db *sql.DB) error {
	instructions, ok := conversations.LookupConstant(conversations.InstructionsKey)
	if !ok {
		return errors.New("default instructions constant is not configured")
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
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
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO constants_fields (field_name) VALUES (?)`, conversations.InstructionsKey); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO constants_values (field_name, value) VALUES (?, ?)`, conversations.InstructionsKey, instructions); err != nil {
		return err
	}
	return tx.Commit()
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

type scanner struct {
	root *rootedFS
}

func (s *scanner) Scan() ([]Entry, error) {
	if s == nil || s.root == nil {
		return nil, errors.New("rooted filesystem is required")
	}
	entries := []Entry{}
	err := fs.WalkDir(s.root.FS(), ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if name == "." || d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		entry := Entry{Path: filepath.ToSlash(name), Name: d.Name(), IsDir: d.IsDir()}
		if !entry.IsDir {
			entry.Size = info.Size()
		}
		entry.Mtime = info.ModTime().Unix()
		entries = append(entries, entry)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return entries, nil
}

type synchronizer struct {
	scanner *scanner
	store   *Store
}

func newSynchronizer(store *Store, root *rootedFS) *synchronizer {
	return &synchronizer{scanner: &scanner{root: root}, store: store}
}

// Sync scans the filesystem first, then atomically replaces the persisted replica.
func (s *synchronizer) Sync() (int, error) {
	if s == nil || s.scanner == nil || s.store == nil {
		return 0, errors.New("synchronizer dependencies are required")
	}
	entries, err := s.scanner.Scan()
	if err != nil {
		return 0, err
	}
	if err := s.store.ReplaceAll(entries); err != nil {
		return 0, err
	}
	return len(entries), nil
}
