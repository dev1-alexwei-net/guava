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

// ProgressStatus describes the per-file outcome persisted for one session.
type ProgressStatus string

const (
	// ProgressPending identifies a file started (or queued) but not yet completed.
	ProgressPending ProgressStatus = "pending"
	// ProgressCompleted identifies a file whose conversation finished successfully.
	ProgressCompleted ProgressStatus = "completed"
	// ProgressAborted identifies a file left unfinished when a run stopped or failed.
	ProgressAborted ProgressStatus = "aborted"
)

// Origin records where a pending file came from. A carried file was already
// unfinished when the session's latest run started; a new file was reported
// by the file list and is not completed yet.
type Origin string

const (
	// OriginNew identifies a file reported by the file list that has not
	// completed yet.
	OriginNew Origin = "new"
	// OriginCarried identifies a file that was unfinished in an earlier run
	// of the same session.
	OriginCarried Origin = "carried"
)

// PendingEntry is one path in a session's unfinished file set. Carried marks
// files that were unfinished in an earlier run of the same session; every
// other entry is a newly discovered file.
type PendingEntry struct {
	Path    string
	Carried bool
}

// FileProgress is the persisted per-file state of one task session: which file
// reached which outcome, and when. SessionID and Path are stable identity; a
// session has at most one row per file path. Origin is meaningful for
// unfinished rows and records how the file entered the pending set.
type FileProgress struct {
	SessionID string
	Path      string
	Status    ProgressStatus
	Origin    Origin
	UpdatedAt time.Time
}

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
		CREATE TABLE IF NOT EXISTS session_file_progress (
			session_id TEXT NOT NULL,
			file_path TEXT NOT NULL,
			status TEXT NOT NULL CHECK (status IN ('pending', 'completed', 'aborted')),
			origin TEXT NOT NULL DEFAULT 'new' CHECK (origin IN ('new', 'carried')),
			updated_at INTEGER NOT NULL,
			PRIMARY KEY (session_id, file_path)
		);
	`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("initialize task session store: %w", err)
	}
	if err := (&Store{db: db}).migrateFileProgressOrigin(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate task session store: %w", err)
	}
	return &Store{db: db}, nil
}

// migrateFileProgressOrigin upgrades databases created before the origin
// column existed. Existing rows default to newly discovered; a resume
// reclassifies them against the current manifest before it starts, so the
// default only matters for sessions that are inspected without resuming.
func (s *Store) migrateFileProgressOrigin() error {
	hasOrigin := false
	rows, err := s.db.Query(`PRAGMA table_info(session_file_progress)`)
	if err != nil {
		return fmt.Errorf("inspect session_file_progress schema: %w", err)
	}
	for rows.Next() {
		var cid int
		var name, colType string
		var notnull int
		var dflt any
		var pk int
		if err := rows.Scan(&cid, &name, &colType, &notnull, &dflt, &pk); err != nil {
			_ = rows.Close()
			return fmt.Errorf("inspect session_file_progress schema: %w", err)
		}
		if name == "origin" {
			hasOrigin = true
		}
	}
	if err := rows.Err(); err == nil {
		err = rows.Close()
	}
	if err != nil {
		return fmt.Errorf("inspect session_file_progress schema: %w", err)
	}
	if hasOrigin {
		return nil
	}
	if _, err := s.db.Exec(`
		ALTER TABLE session_file_progress
		ADD COLUMN origin TEXT NOT NULL DEFAULT 'new' CHECK (origin IN ('new', 'carried'))
	`); err != nil {
		return fmt.Errorf("add origin column to session_file_progress: %w", err)
	}
	return nil
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

// Delete removes one saved session record, its per-file progress, and all of
// its persisted history. Deleting an unknown ID returns ErrNotFound, matching
// Get and Update.
func (s *Store) Delete(ctx context.Context, id string) error {
	if s == nil || s.db == nil {
		return errors.New("task session store is not open")
	}
	if id == "" {
		return errors.New("task session ID is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin task session deletion: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `DELETE FROM task_sessions WHERE id = ?`, id)
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
	if _, err := tx.ExecContext(ctx, `DELETE FROM session_file_progress WHERE session_id = ?`, id); err != nil {
		return fmt.Errorf("delete task session file progress: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit task session deletion: %w", err)
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
// they remain available for an explicit resume, and records the files their
// interrupted runs left unfinished as aborted. It does not start a run, does
// not alter records that already reached a terminal state, and never touches
// files that completed successfully: those stay resumable from where they left
// off.
func (s *Store) RecoverInterrupted(ctx context.Context) error {
	if s == nil || s.db == nil {
		return errors.New("task session store is not open")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin task session recovery: %w", err)
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	if _, err := tx.ExecContext(ctx, `
		UPDATE task_sessions
		SET status = ?, updated_at = ?, finished_at = ?, last_error = ?
		WHERE status = ?
	`, string(StatusPaused), now.UnixNano(), now.UnixNano(), "interrupted when the dashboard process exited", string(StatusActive)); err != nil {
		return fmt.Errorf("recover interrupted task sessions: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE session_file_progress
		SET status = ?, updated_at = ?
		WHERE status = ? AND session_id IN (SELECT id FROM task_sessions WHERE status = ?)
	`, string(ProgressAborted), now.UnixNano(), string(ProgressPending), string(StatusPaused)); err != nil {
		return fmt.Errorf("recover interrupted task session file progress: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit task session recovery: %w", err)
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

// SaveFileProgress records or replaces the latest per-file outcome for one
// session. A new row's origin defaults to newly discovered; an existing row
// keeps its stored origin, so progress updates never clobber the
// classification set when the pending set was replaced. The store owns the
// timestamp when the caller leaves it zero.
func (s *Store) SaveFileProgress(ctx context.Context, progress FileProgress) (FileProgress, error) {
	if s == nil || s.db == nil {
		return FileProgress{}, errors.New("task session store is not open")
	}
	if err := validateFileProgress(progress); err != nil {
		return FileProgress{}, err
	}
	if progress.Origin == "" {
		progress.Origin = OriginNew
	}
	if progress.UpdatedAt.IsZero() {
		progress.UpdatedAt = time.Now().UTC()
	}
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO session_file_progress (session_id, file_path, status, origin, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (session_id, file_path) DO UPDATE SET
			status = excluded.status,
			updated_at = excluded.updated_at
	`, progress.SessionID, progress.Path, string(progress.Status), string(progress.Origin), progress.UpdatedAt.UnixNano()); err != nil {
		return FileProgress{}, fmt.Errorf("save task session file progress: %w", err)
	}
	return progress, nil
}

// ListFileProgress returns every per-file row for one session ordered by
// path. It is the full progress snapshot used to resume a run.
func (s *Store) ListFileProgress(ctx context.Context, sessionID string) ([]FileProgress, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("task session store is not open")
	}
	if sessionID == "" {
		return nil, errors.New("task session ID is required")
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT session_id, file_path, status, origin, updated_at
		FROM session_file_progress WHERE session_id = ?
		ORDER BY file_path ASC
	`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("list task session file progress: %w", err)
	}
	defer rows.Close()
	progress := make([]FileProgress, 0)
	for rows.Next() {
		row, err := scanFileProgress(rows)
		if err != nil {
			return nil, fmt.Errorf("read task session file progress: %w", err)
		}
		progress = append(progress, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate task session file progress: %w", err)
	}
	return progress, nil
}

// CompletedPaths returns the file paths the session already finished
// successfully, ordered by path. Resumed runs skip exactly these files.
func (s *Store) CompletedPaths(ctx context.Context, sessionID string) ([]string, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("task session store is not open")
	}
	if sessionID == "" {
		return nil, errors.New("task session ID is required")
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT file_path FROM session_file_progress
		WHERE session_id = ? AND status = ?
		ORDER BY file_path ASC
	`, sessionID, string(ProgressCompleted))
	if err != nil {
		return nil, fmt.Errorf("list completed task session files: %w", err)
	}
	defer rows.Close()
	paths := make([]string, 0)
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, fmt.Errorf("read completed task session file: %w", err)
		}
		paths = append(paths, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate completed task session files: %w", err)
	}
	return paths, nil
}

// PendingPaths returns the session's unfinished file paths — the rows whose
// status is pending or aborted — ordered by path. A resumed run merges
// exactly this set with the files the manifest currently reports.
func (s *Store) PendingPaths(ctx context.Context, sessionID string) ([]string, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("task session store is not open")
	}
	if sessionID == "" {
		return nil, errors.New("task session ID is required")
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT file_path FROM session_file_progress
		WHERE session_id = ? AND status IN (?, ?)
		ORDER BY file_path ASC
	`, sessionID, string(ProgressPending), string(ProgressAborted))
	if err != nil {
		return nil, fmt.Errorf("list pending task session files: %w", err)
	}
	defer rows.Close()
	paths := make([]string, 0)
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, fmt.Errorf("read pending task session file: %w", err)
		}
		paths = append(paths, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate pending task session files: %w", err)
	}
	return paths, nil
}

// PendingProgress returns the session's unfinished rows — the files whose
// status is pending or aborted — with each file's origin, ordered by path.
// The dashboard renders exactly this view; a resumed run reclassifies these
// files against the current manifest before it starts.
func (s *Store) PendingProgress(ctx context.Context, sessionID string) ([]FileProgress, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("task session store is not open")
	}
	if sessionID == "" {
		return nil, errors.New("task session ID is required")
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT session_id, file_path, status, origin, updated_at
		FROM session_file_progress
		WHERE session_id = ? AND status IN (?, ?)
		ORDER BY file_path ASC
	`, sessionID, string(ProgressPending), string(ProgressAborted))
	if err != nil {
		return nil, fmt.Errorf("list pending task session files: %w", err)
	}
	defer rows.Close()
	progress := make([]FileProgress, 0)
	for rows.Next() {
		row, err := scanFileProgress(rows)
		if err != nil {
			return nil, fmt.Errorf("read pending task session file: %w", err)
		}
		progress = append(progress, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate pending task session files: %w", err)
	}
	return progress, nil
}

// SetPendingSet replaces the session's unfinished (pending/aborted) file
// set with entries, storing every path as a pending row with the entry's
// origin: carried entries were unfinished in an earlier run of the same
// session, uncarried entries were reported by the file list. Unfinished
// rows for any other paths — for example files that no longer exist under
// the session's root — are dropped, and completed rows are left untouched.
func (s *Store) SetPendingSet(ctx context.Context, sessionID string, entries []PendingEntry) error {
	if s == nil || s.db == nil {
		return errors.New("task session store is not open")
	}
	if sessionID == "" {
		return errors.New("task session ID is required")
	}
	for _, entry := range entries {
		if entry.Path == "" {
			return errors.New("task session pending file path is required")
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin task session pending set update: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM session_file_progress
		WHERE session_id = ? AND status IN (?, ?)
	`, sessionID, string(ProgressPending), string(ProgressAborted)); err != nil {
		return fmt.Errorf("drop unfinished task session files: %w", err)
	}
	now := time.Now().UTC().UnixNano()
	for _, entry := range entries {
		origin := OriginNew
		if entry.Carried {
			origin = OriginCarried
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO session_file_progress (session_id, file_path, status, origin, updated_at)
			VALUES (?, ?, ?, ?, ?)
		`, sessionID, entry.Path, string(ProgressPending), string(origin), now); err != nil {
			return fmt.Errorf("persist task session pending file: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit task session pending set: %w", err)
	}
	return nil
}

// MarkFileStatus transitions every per-file row for one session from one
// status to another, without touching any other file or session. It is used
// to record unfinished files when a run ends without completing.
func (s *Store) MarkFileStatus(ctx context.Context, sessionID string, from, to ProgressStatus) error {
	if s == nil || s.db == nil {
		return errors.New("task session store is not open")
	}
	if sessionID == "" {
		return errors.New("task session ID is required")
	}
	if _, err := s.db.ExecContext(ctx, `
		UPDATE session_file_progress
		SET status = ?, updated_at = ?
		WHERE session_id = ? AND status = ?
	`, string(to), time.Now().UTC().UnixNano(), sessionID, string(from)); err != nil {
		return fmt.Errorf("mark task session file progress: %w", err)
	}
	return nil
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

func validateFileProgress(progress FileProgress) error {
	if progress.SessionID == "" {
		return errors.New("task session ID is required")
	}
	if progress.Path == "" {
		return errors.New("task session file path is required")
	}
	switch progress.Status {
	case ProgressPending, ProgressCompleted, ProgressAborted:
	default:
		return fmt.Errorf("invalid task session file progress status %q", progress.Status)
	}
	switch progress.Origin {
	case "", OriginNew, OriginCarried:
	default:
		return fmt.Errorf("invalid task session file progress origin %q", progress.Origin)
	}
	return nil
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

func scanFileProgress(row rowScanner) (FileProgress, error) {
	var progress FileProgress
	var status, origin string
	var updatedAt int64
	if err := row.Scan(&progress.SessionID, &progress.Path, &status, &origin, &updatedAt); err != nil {
		return FileProgress{}, err
	}
	progress.Status = ProgressStatus(status)
	progress.Origin = Origin(origin)
	progress.UpdatedAt = time.Unix(0, updatedAt).UTC()
	return progress, nil
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
