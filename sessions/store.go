// Package sessions persists task-session identity, launch configuration, and
// lifecycle state independently of the fileexplorer index database.
package sessions

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// Status describes the resumable lifecycle state of a saved task session.
type Status string

const (
	// StatusActive identifies a session whose executor is currently running.
	StatusActive Status = "active"
	// StatusPaused identifies a session stopped intentionally and available to resume.
	StatusPaused Status = "paused"
	// StatusEnded identifies a session that completed normally and is no longer active.
	StatusEnded Status = "ended"
	// StatusFailed identifies a session that stopped because its run failed.
	StatusFailed Status = "failed"
)

// ErrNotFound is returned when a requested session ID does not exist.
var ErrNotFound = errors.New("task session not found")

// Record is the persisted identity, launch configuration, and latest
// lifecycle snapshot for one task session. ID is generated on Create when
// omitted and remains stable through Update. DatabaseFile refers to the
// fileexplorer index; the store itself uses the path passed to OpenStore.
type Record struct {
	ID            string
	AgentID       string
	RootDirectory string
	Subdirectory  string
	DatabaseFile  string
	Status        Status
	CreatedAt     time.Time
	UpdatedAt     time.Time
	StartedAt     time.Time
	FinishedAt    time.Time
	LastError     string
}

// Store is a SQLite-backed repository for task-session records.
type Store struct {
	db *sql.DB
}

// OpenStore opens or creates a session metadata database at path. It must be
// separate from the fileexplorer index, which the executor opens read-only.
func OpenStore(path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("session store path is required")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open task session store: %w", err)
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("connect to task session store: %w", err)
	}
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS task_sessions (
			id TEXT PRIMARY KEY,
			agent_id TEXT NOT NULL,
			root_directory TEXT NOT NULL,
			subdirectory TEXT NOT NULL,
			database_file TEXT NOT NULL,
			status TEXT NOT NULL CHECK (status IN ('active', 'paused', 'ended', 'failed')),
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			started_at INTEGER,
			finished_at INTEGER,
			last_error TEXT NOT NULL DEFAULT ''
		);
		CREATE INDEX IF NOT EXISTS task_sessions_updated_at
			ON task_sessions(updated_at DESC, id ASC);
	`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("initialize task session store: %w", err)
	}
	return &Store{db: db}, nil
}

// Create stores a new record and returns it with a stable ID and timestamps.
// A caller-provided ID is preserved, which also allows importing records.
func (s *Store) Create(ctx context.Context, record Record) (Record, error) {
	if s == nil || s.db == nil {
		return Record{}, errors.New("task session store is not open")
	}
	if err := validateRecord(record); err != nil {
		return Record{}, err
	}
	if record.ID == "" {
		id, err := newID()
		if err != nil {
			return Record{}, fmt.Errorf("generate task session ID: %w", err)
		}
		record.ID = id
	}
	now := time.Now().UTC()
	if record.CreatedAt.IsZero() {
		record.CreatedAt = now
	}
	if record.UpdatedAt.IsZero() {
		record.UpdatedAt = now
	}
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO task_sessions
			(id, agent_id, root_directory, subdirectory, database_file, status, created_at, updated_at, started_at, finished_at, last_error)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, record.ID, record.AgentID, record.RootDirectory, record.Subdirectory, record.DatabaseFile,
		string(record.Status), record.CreatedAt.UnixNano(), record.UpdatedAt.UnixNano(), nullableTime(record.StartedAt), nullableTime(record.FinishedAt), record.LastError); err != nil {
		return Record{}, fmt.Errorf("create task session: %w", err)
	}
	return record, nil
}

// Update replaces a record's configuration and latest lifecycle snapshot.
// CreatedAt and ID remain the values assigned at creation; UpdatedAt is set
// by the store to the current UTC time.
func (s *Store) Update(ctx context.Context, record Record) (Record, error) {
	if s == nil || s.db == nil {
		return Record{}, errors.New("task session store is not open")
	}
	if record.ID == "" {
		return Record{}, errors.New("task session ID is required")
	}
	if err := validateRecord(record); err != nil {
		return Record{}, err
	}
	current, err := s.Get(ctx, record.ID)
	if err != nil {
		return Record{}, err
	}
	record.CreatedAt = current.CreatedAt
	record.UpdatedAt = time.Now().UTC()
	result, err := s.db.ExecContext(ctx, `
		UPDATE task_sessions SET
			agent_id = ?, root_directory = ?, subdirectory = ?, database_file = ?,
			status = ?, updated_at = ?, started_at = ?, finished_at = ?, last_error = ?
		WHERE id = ?
	`, record.AgentID, record.RootDirectory, record.Subdirectory, record.DatabaseFile, string(record.Status),
		record.UpdatedAt.UnixNano(), nullableTime(record.StartedAt), nullableTime(record.FinishedAt), record.LastError, record.ID)
	if err != nil {
		return Record{}, fmt.Errorf("update task session: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return Record{}, fmt.Errorf("check task session update: %w", err)
	}
	if rows == 0 {
		return Record{}, fmt.Errorf("%w: %q", ErrNotFound, record.ID)
	}
	return record, nil
}

// Get loads a session by its stable ID.
func (s *Store) Get(ctx context.Context, id string) (Record, error) {
	if s == nil || s.db == nil {
		return Record{}, errors.New("task session store is not open")
	}
	record, err := scanRecord(s.db.QueryRowContext(ctx, `
		SELECT id, agent_id, root_directory, subdirectory, database_file, status,
			created_at, updated_at, started_at, finished_at, last_error
		FROM task_sessions WHERE id = ?
	`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, fmt.Errorf("%w: %q", ErrNotFound, id)
	}
	if err != nil {
		return Record{}, fmt.Errorf("get task session: %w", err)
	}
	return record, nil
}

// Delete removes one saved session record and all of its persisted history.
// Deleting an unknown ID returns ErrNotFound, matching Get and Update.
func (s *Store) Delete(ctx context.Context, id string) error {
	if s == nil || s.db == nil {
		return errors.New("task session store is not open")
	}
	if id == "" {
		return errors.New("task session ID is required")
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM task_sessions WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete task session: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check task session deletion: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("%w: %q", ErrNotFound, id)
	}
	return nil
}

// List returns all records ordered by most recently updated, then by ID.
func (s *Store) List(ctx context.Context) ([]Record, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("task session store is not open")
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, agent_id, root_directory, subdirectory, database_file, status,
			created_at, updated_at, started_at, finished_at, last_error
		FROM task_sessions ORDER BY updated_at DESC, id ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("list task sessions: %w", err)
	}
	defer rows.Close()
	records := make([]Record, 0)
	for rows.Next() {
		record, err := scanRecord(rows)
		if err != nil {
			return nil, fmt.Errorf("read task session: %w", err)
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate task sessions: %w", err)
	}
	return records, nil
}

// RecoverInterrupted marks records left active by a process exit as paused so
// they remain available for an explicit resume. It does not start a run or
// alter records that already reached a terminal state.
func (s *Store) RecoverInterrupted(ctx context.Context) error {
	if s == nil || s.db == nil {
		return errors.New("task session store is not open")
	}
	records, err := s.List(ctx)
	if err != nil {
		return err
	}
	for _, record := range records {
		if record.Status != StatusActive {
			continue
		}
		record.Status = StatusPaused
		record.FinishedAt = time.Now().UTC()
		record.LastError = "interrupted when the dashboard process exited"
		if _, err := s.Update(ctx, record); err != nil {
			return fmt.Errorf("recover interrupted task session %q: %w", record.ID, err)
		}
	}
	return nil
}

// Close releases the SQLite connection. A nil or already-closed Store is safe.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func validateRecord(record Record) error {
	for name, value := range map[string]string{
		"agent ID":       record.AgentID,
		"root directory": record.RootDirectory,
		"database file":  record.DatabaseFile,
	} {
		if value == "" {
			return fmt.Errorf("task session %s is required", name)
		}
	}
	switch record.Status {
	case StatusActive, StatusPaused, StatusEnded, StatusFailed:
		return nil
	default:
		return fmt.Errorf("invalid task session status %q", record.Status)
	}
}

func newID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	encoded := hex.EncodeToString(value[:])
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:], nil
}

func nullableTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value.UTC().UnixNano()
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanRecord(row rowScanner) (Record, error) {
	var record Record
	var status string
	var createdAt, updatedAt int64
	var startedAt, finishedAt sql.NullInt64
	if err := row.Scan(&record.ID, &record.AgentID, &record.RootDirectory, &record.Subdirectory, &record.DatabaseFile,
		&status, &createdAt, &updatedAt, &startedAt, &finishedAt, &record.LastError); err != nil {
		return Record{}, err
	}
	record.Status = Status(status)
	record.CreatedAt = time.Unix(0, createdAt).UTC()
	record.UpdatedAt = time.Unix(0, updatedAt).UTC()
	if startedAt.Valid {
		record.StartedAt = time.Unix(0, startedAt.Int64).UTC()
	}
	if finishedAt.Valid {
		record.FinishedAt = time.Unix(0, finishedAt.Int64).UTC()
	}
	return record, nil
}
