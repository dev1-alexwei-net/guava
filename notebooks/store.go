package notebooks

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
)

var (
	ErrNotFound    = errors.New("notebook or file not found")
	ErrInvalidData = errors.New("invalid notebook data")
	ErrConflict    = errors.New("revision conflict")
)

// ConflictError reports that a mutation was based on an obsolete notebook revision.
type ConflictError struct {
	ExpectedRevision int64
	CurrentRevision  int64
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("%v: expected revision %d, current revision %d", ErrConflict, e.ExpectedRevision, e.CurrentRevision)
}

func (e *ConflictError) Unwrap() error { return ErrConflict }

var schema = []string{
	`CREATE TABLE IF NOT EXISTS notebooks (
		id TEXT PRIMARY KEY,
		owner_id TEXT NOT NULL,
		revision BIGINT NOT NULL DEFAULT 0 CHECK (revision >= 0),
		created_at BIGINT NOT NULL,
		updated_at BIGINT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS notebook_files (
		notebook_id TEXT NOT NULL REFERENCES notebooks(id) ON DELETE CASCADE,
		path TEXT NOT NULL,
		object_id TEXT NOT NULL,
		revision BIGINT NOT NULL CHECK (revision > 0),
		size BIGINT NOT NULL CHECK (size >= 0),
		content_type TEXT NOT NULL,
		updated_at BIGINT NOT NULL,
		PRIMARY KEY (notebook_id, path),
		UNIQUE (notebook_id, object_id)
	)`,
}

// Notebook contains durable ownership and the latest committed revision.
type Notebook struct {
	ID        string
	OwnerID   string
	Revision  int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

// File contains metadata only. Its bytes live in ObjectStore under ObjectID.
type File struct {
	NotebookID  string
	Path        string
	ObjectID    string
	Revision    int64
	Size        int64
	ContentType string
	UpdatedAt   time.Time
}

// ObjectStore keeps content outside the function's local filesystem. Object
// identifiers are opaque, and every operation is explicitly scoped by notebook.
type ObjectStore interface {
	Put(ctx context.Context, notebookID, objectID, contentType string, size int64, body io.Reader) error
	Get(ctx context.Context, notebookID, objectID string) (io.ReadCloser, error)
	Delete(ctx context.Context, notebookID, objectID string) error
}

// Store coordinates durable notebook metadata with separately stored bytes.
type Store struct {
	db      *sql.DB
	objects ObjectStore
}

// OpenPostgres opens a durable PostgreSQL metadata database and applies its
// schema. The DSN should come from deployment configuration such as DATABASE_URL.
func OpenPostgres(ctx context.Context, dsn string, objects ObjectStore) (*Store, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, fmt.Errorf("database URL is required")
	}
	if objects == nil {
		return nil, fmt.Errorf("object storage is required")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("open metadata database: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("connect metadata database: %w", err)
	}
	store, err := NewStore(db, objects)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := store.Migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

// NewStore wraps a database/sql connection and an object store. Call Migrate
// before using it; this constructor also permits deterministic adapter tests.
func NewStore(db *sql.DB, objects ObjectStore) (*Store, error) {
	if db == nil {
		return nil, fmt.Errorf("metadata database is required")
	}
	if objects == nil {
		return nil, fmt.Errorf("object storage is required")
	}
	return &Store{db: db, objects: objects}, nil
}

// Migrate creates the durable metadata schema. It is safe to run on startup.
func (s *Store) Migrate(ctx context.Context) error {
	for _, statement := range schema {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("migrate notebook metadata: %w", err)
		}
	}
	return nil
}

// Close closes the metadata database connection pool.
func (s *Store) Close() error { return s.db.Close() }

// CreateNotebook durably records the notebook owner before any file content is
// accepted for it.
func (s *Store) CreateNotebook(ctx context.Context, ownerID string) (Notebook, error) {
	if strings.TrimSpace(ownerID) == "" {
		return Notebook{}, fmt.Errorf("%w: owner ID is required", ErrInvalidData)
	}
	now := time.Now().UTC()
	notebook := Notebook{
		ID:        uuid.NewString(),
		OwnerID:   ownerID,
		CreatedAt: now,
		UpdatedAt: now,
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO notebooks (id, owner_id, revision, created_at, updated_at) VALUES ($1, $2, 0, $3, $3)`,
		notebook.ID, notebook.OwnerID, now.UnixMilli())
	if err != nil {
		return Notebook{}, fmt.Errorf("create notebook metadata: %w", err)
	}
	return notebook, nil
}

// GetNotebook returns durable ownership and revision metadata.
func (s *Store) GetNotebook(ctx context.Context, notebookID string) (Notebook, error) {
	id, err := canonicalUUID(notebookID)
	if err != nil {
		return Notebook{}, err
	}
	var notebook Notebook
	var createdAt, updatedAt int64
	err = s.db.QueryRowContext(ctx,
		`SELECT id, owner_id, revision, created_at, updated_at FROM notebooks WHERE id = $1`, id,
	).Scan(&notebook.ID, &notebook.OwnerID, &notebook.Revision, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Notebook{}, ErrNotFound
	}
	if err != nil {
		return Notebook{}, fmt.Errorf("read notebook metadata: %w", err)
	}
	notebook.CreatedAt = time.UnixMilli(createdAt).UTC()
	notebook.UpdatedAt = time.UnixMilli(updatedAt).UTC()
	return notebook, nil
}

// WriteFile stores bytes as a new immutable object, then commits only its
// metadata pointer if expectedRevision is still current. The object key is
// opaque and notebook-scoped; no durable content is staged on function-local
// disk. A failed metadata commit may leave an unreferenced object for cleanup.
func (s *Store) WriteFile(ctx context.Context, notebookID, filePath, contentType string, size, expectedRevision int64, body io.Reader) (File, error) {
	id, err := canonicalUUID(notebookID)
	if err != nil {
		return File{}, err
	}
	filePath, err = cleanFilePath(filePath)
	if err != nil {
		return File{}, err
	}
	if body == nil || size < 0 || expectedRevision < 0 {
		return File{}, fmt.Errorf("%w: file body, non-negative size, and non-negative expected revision are required", ErrInvalidData)
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	if err := s.checkRevision(ctx, id, expectedRevision); err != nil {
		return File{}, err
	}
	objectID := uuid.NewString()
	if err := s.objects.Put(ctx, id, objectID, contentType, size, body); err != nil {
		return File{}, fmt.Errorf("store notebook object: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return File{}, fmt.Errorf("begin notebook metadata transaction: %w", err)
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	var revision int64
	err = tx.QueryRowContext(ctx,
		`UPDATE notebooks SET revision = revision + 1, updated_at = $3 WHERE id = $1 AND revision = $2 RETURNING revision`,
		id, expectedRevision, now.UnixMilli(),
	).Scan(&revision)
	if errors.Is(err, sql.ErrNoRows) {
		_ = tx.Rollback()
		return File{}, s.revisionConflict(ctx, id, expectedRevision)
	}
	if err != nil {
		return File{}, fmt.Errorf("advance notebook revision: %w", err)
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO notebook_files (notebook_id, path, object_id, revision, size, content_type, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (notebook_id, path) DO UPDATE SET
			object_id = EXCLUDED.object_id,
			revision = EXCLUDED.revision,
			size = EXCLUDED.size,
			content_type = EXCLUDED.content_type,
			updated_at = EXCLUDED.updated_at`,
		id, filePath, objectID, revision, size, contentType, now.UnixMilli())
	if err != nil {
		return File{}, fmt.Errorf("save file metadata: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return File{}, fmt.Errorf("commit file metadata: %w", err)
	}
	return File{
		NotebookID:  id,
		Path:        filePath,
		ObjectID:    objectID,
		Revision:    revision,
		Size:        size,
		ContentType: contentType,
		UpdatedAt:   now,
	}, nil
}

// ReadFile resolves content through durable metadata and streams it from the
// notebook-scoped object store.
func (s *Store) ReadFile(ctx context.Context, notebookID, filePath string) (File, io.ReadCloser, error) {
	id, err := canonicalUUID(notebookID)
	if err != nil {
		return File{}, nil, err
	}
	filePath, err = cleanFilePath(filePath)
	if err != nil {
		return File{}, nil, err
	}
	var file File
	var updatedAt int64
	err = s.db.QueryRowContext(ctx, `
		SELECT notebook_id, path, object_id, revision, size, content_type, updated_at
		FROM notebook_files WHERE notebook_id = $1 AND path = $2`, id, filePath,
	).Scan(&file.NotebookID, &file.Path, &file.ObjectID, &file.Revision, &file.Size, &file.ContentType, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return File{}, nil, ErrNotFound
	}
	if err != nil {
		return File{}, nil, fmt.Errorf("read file metadata: %w", err)
	}
	file.UpdatedAt = time.UnixMilli(updatedAt).UTC()
	content, err := s.objects.Get(ctx, id, file.ObjectID)
	if err != nil {
		return File{}, nil, fmt.Errorf("read notebook object: %w", err)
	}
	return file, content, nil
}

// ListFiles returns file metadata for a notebook in stable path order.
func (s *Store) ListFiles(ctx context.Context, notebookID string) ([]File, error) {
	id, err := canonicalUUID(notebookID)
	if err != nil {
		return nil, err
	}
	if _, err := s.GetNotebook(ctx, id); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT notebook_id, path, object_id, revision, size, content_type, updated_at
		FROM notebook_files WHERE notebook_id = $1 ORDER BY path`, id)
	if err != nil {
		return nil, fmt.Errorf("list notebook files: %w", err)
	}
	defer rows.Close()
	files := make([]File, 0)
	for rows.Next() {
		var file File
		var updatedAt int64
		if err := rows.Scan(&file.NotebookID, &file.Path, &file.ObjectID, &file.Revision, &file.Size, &file.ContentType, &updatedAt); err != nil {
			return nil, fmt.Errorf("read notebook file metadata: %w", err)
		}
		file.UpdatedAt = time.UnixMilli(updatedAt).UTC()
		files = append(files, file)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list notebook files: %w", err)
	}
	return files, nil
}

// DeleteFile removes the authoritative metadata pointer if expectedRevision is
// current and advances the notebook revision. The now-unreferenced object is
// left for storage lifecycle cleanup, as metadata and object storage cannot be
// changed atomically.
func (s *Store) DeleteFile(ctx context.Context, notebookID, filePath string, expectedRevision int64) (int64, error) {
	id, err := canonicalUUID(notebookID)
	if err != nil {
		return 0, err
	}
	filePath, err = cleanFilePath(filePath)
	if err != nil {
		return 0, err
	}
	if expectedRevision < 0 {
		return 0, fmt.Errorf("%w: expected revision must be non-negative", ErrInvalidData)
	}
	if err := s.checkRevision(ctx, id, expectedRevision); err != nil {
		return 0, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin notebook metadata transaction: %w", err)
	}
	defer tx.Rollback()
	err = tx.QueryRowContext(ctx,
		`SELECT 1 FROM notebook_files WHERE notebook_id = $1 AND path = $2`, id, filePath,
	).Scan(new(int))
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("read file metadata for deletion: %w", err)
	}
	var revision int64
	err = tx.QueryRowContext(ctx,
		`UPDATE notebooks SET revision = revision + 1, updated_at = $3 WHERE id = $1 AND revision = $2 RETURNING revision`,
		id, expectedRevision, time.Now().UTC().UnixMilli(),
	).Scan(&revision)
	if errors.Is(err, sql.ErrNoRows) {
		_ = tx.Rollback()
		return 0, s.revisionConflict(ctx, id, expectedRevision)
	}
	if err != nil {
		return 0, fmt.Errorf("advance notebook revision: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM notebook_files WHERE notebook_id = $1 AND path = $2`, id, filePath); err != nil {
		return 0, fmt.Errorf("delete file metadata: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit file deletion: %w", err)
	}
	return revision, nil
}

func (s *Store) checkRevision(ctx context.Context, notebookID string, expectedRevision int64) error {
	notebook, err := s.GetNotebook(ctx, notebookID)
	if err != nil {
		return err
	}
	if notebook.Revision != expectedRevision {
		return &ConflictError{ExpectedRevision: expectedRevision, CurrentRevision: notebook.Revision}
	}
	return nil
}

func (s *Store) revisionConflict(ctx context.Context, notebookID string, expectedRevision int64) error {
	notebook, err := s.GetNotebook(ctx, notebookID)
	if err != nil {
		return err
	}
	return &ConflictError{ExpectedRevision: expectedRevision, CurrentRevision: notebook.Revision}
}

func canonicalUUID(value string) (string, error) {
	id, err := uuid.Parse(value)
	if err != nil {
		return "", fmt.Errorf("%w: ID must be a UUID", ErrInvalidData)
	}
	return id.String(), nil
}

func cleanFilePath(value string) (string, error) {
	if value == "" || strings.IndexByte(value, 0) >= 0 || strings.HasPrefix(value, "/") || strings.HasPrefix(value, `\`) {
		return "", fmt.Errorf("%w: file path must be relative", ErrInvalidData)
	}
	normalized := strings.ReplaceAll(value, `\`, "/")
	if len(normalized) >= 2 && normalized[1] == ':' {
		return "", fmt.Errorf("%w: absolute file paths are not allowed", ErrInvalidData)
	}
	for _, component := range strings.Split(normalized, "/") {
		if component == ".." {
			return "", fmt.Errorf("%w: parent path components are not allowed", ErrInvalidData)
		}
	}
	clean := path.Clean(normalized)
	if clean == "." || clean == "" {
		return "", fmt.Errorf("%w: file path is required", ErrInvalidData)
	}
	return clean, nil
}
