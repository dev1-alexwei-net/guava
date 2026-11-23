package executor

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// indexedFile is a file row from the fileexplorer replica. Paths are
// slash-separated and relative to the configured filesystem root.
type indexedFile struct {
	path  string
	name  string
	size  int64
	mtime int64
}

type fileIndex struct {
	db *sql.DB
}

func openFileIndex(ctx context.Context, databasePath string) (*fileIndex, error) {
	dsn, err := readOnlySQLiteDSN(databasePath)
	if err != nil {
		return nil, fmt.Errorf("resolve fileexplorer database path: %w", err)
	}

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open fileexplorer database: %w", err)
	}
	db.SetMaxOpenConns(1)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("open fileexplorer database: %w", err)
	}

	return &fileIndex{db: db}, nil
}

func readOnlySQLiteDSN(databasePath string) (string, error) {
	absolutePath, err := filepath.Abs(databasePath)
	if err != nil {
		return "", err
	}
	dsn := &url.URL{
		Scheme: "file",
		Path:   filepath.ToSlash(absolutePath),
	}
	query := dsn.Query()
	query.Set("mode", "ro")
	dsn.RawQuery = query.Encode()
	return dsn.String(), nil
}

func (i *fileIndex) FilePaths(ctx context.Context) ([]string, error) {
	files, err := i.regularFiles(ctx)
	if err != nil {
		return nil, err
	}
	paths := make([]string, len(files))
	for n, file := range files {
		paths[n] = file.path
	}
	return paths, nil
}

func (i *fileIndex) regularFiles(ctx context.Context) ([]indexedFile, error) {
	if i == nil || i.db == nil {
		return nil, fmt.Errorf("fileexplorer index is not open")
	}
	rows, err := i.db.QueryContext(ctx,
		"SELECT path, name, size, mtime FROM files WHERE is_dir = 0 ORDER BY path COLLATE BINARY")
	if err != nil {
		return nil, fmt.Errorf("query fileexplorer index: %w", err)
	}
	defer rows.Close()

	files := make([]indexedFile, 0)
	for rows.Next() {
		var file indexedFile
		if err := rows.Scan(&file.path, &file.name, &file.size, &file.mtime); err != nil {
			return nil, fmt.Errorf("read fileexplorer index row: %w", err)
		}
		files = append(files, file)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate fileexplorer index: %w", err)
	}
	return files, nil
}

func (i *fileIndex) close() error {
	if i == nil || i.db == nil {
		return nil
	}
	return i.db.Close()
}
