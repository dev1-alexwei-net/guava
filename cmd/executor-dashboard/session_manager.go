package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/dev1-alexwei-net/guava/executor"
	"github.com/dev1-alexwei-net/guava/sessions"
	"github.com/dev1-alexwei-net/guava/tasks"
)

// taskSessionStore is the persistence port used by taskSessionManager. The
// dashboard composition root retains ownership of opening, recovery, listing,
// and closing the persistent store.
type taskSessionStore interface {
	Create(context.Context, sessions.Record) (sessions.Record, error)
	Get(context.Context, string) (sessions.Record, error)
	Update(context.Context, sessions.Record) (sessions.Record, error)
	Delete(context.Context, string) error
	SaveFileProgress(context.Context, sessions.FileProgress) (sessions.FileProgress, error)
	CompletedPaths(context.Context, string) ([]string, error)
	PendingPaths(context.Context, string) ([]string, error)
	SetPendingSet(context.Context, string, []sessions.PendingEntry) error
	MarkFileStatus(context.Context, string, sessions.ProgressStatus, sessions.ProgressStatus) error
}

var _ taskSessionStore = (*sessions.Store)(nil)

// taskSessionManager owns the persisted identity, launch configuration,
// per-file progress, and lifecycle snapshot for dashboard runs. The
// controller owns the live goroutine and cancellation state; the dashboard
// owns opening and closing the underlying store.
type taskSessionManager struct {
	baseConfig executor.Config
	store      taskSessionStore
	logger     *slog.Logger
}

func newTaskSessionManager(config executor.Config, store taskSessionStore, logger *slog.Logger) *taskSessionManager {
	return &taskSessionManager{baseConfig: config, store: store, logger: logger}
}

// Create records the launch configuration for a fresh run before it starts
// and returns the runner bound to the new session's per-file progress
// tracking. A fresh run has no persisted progress, so it processes every
// discovered file exactly as before.
func (m *taskSessionManager) Create(runner *executor.Executor) (*executor.Executor, *sessions.Record, error) {
	summary := runner.SessionSummary()
	record, err := m.store.Create(context.Background(), sessions.Record{
		AgentID: summary.AgentID, RootDirectory: m.baseConfig.RootPath,
		Subdirectory: summary.Subdirectory, DatabaseFile: m.baseConfig.DatabasePath,
		Status: sessions.StatusActive, StartedAt: time.Now().UTC(),
	})
	if err != nil {
		return nil, nil, err
	}
	bound, err := m.bindRunner(runner, record.ID, nil, nil, nil)
	if err != nil {
		return nil, nil, err
	}
	return bound, &record, nil
}

// Resume loads an explicitly selected inactive record, refreshes the
// fileexplorer manifest for the session's root and subdirectory, and merges
// the files the session left unfinished with the files the manifest currently
// reports — dropping any that no longer exist — persisting the merged set
// before marking the record active. The returned runner skips files the
// session already completed, merges surviving unfinished paths with current
// discoveries in the work queue, and processes nothing that no longer exists.
func (m *taskSessionManager) Resume(id string) (*executor.Executor, *sessions.Record, error) {
	ctx := context.Background()
	record, err := m.store.Get(ctx, strings.TrimSpace(id))
	if err != nil {
		return nil, nil, err
	}
	if record.Status == sessions.StatusActive {
		return nil, nil, errors.New("task session is still active and cannot be resumed")
	}

	completed, err := m.store.CompletedPaths(ctx, record.ID)
	if err != nil {
		return nil, nil, fmt.Errorf("load completed task session files: %w", err)
	}
	pending, err := m.store.PendingPaths(ctx, record.ID)
	if err != nil {
		return nil, nil, fmt.Errorf("load pending task session files: %w", err)
	}
	merged, carried, err := m.mergePendingSet(ctx, record, completed, pending)
	if err != nil {
		return nil, nil, err
	}

	config := executorConfigForSession(m.baseConfig, record)
	subdirectory := config.Subdirectory
	config.Subdirectory = ""
	runner, err := executor.New(config)
	if err != nil {
		return nil, nil, fmt.Errorf("restore task session configuration: %w", err)
	}
	if subdirectory != "" {
		runner, err = runner.WithSubdirectory(subdirectory)
		if err != nil {
			return nil, nil, fmt.Errorf("restore task session subdirectory: %w", err)
		}
	}

	record.Status = sessions.StatusActive
	record.StartedAt = time.Now().UTC()
	record.FinishedAt = time.Time{}
	record.LastError = ""
	updated, err := m.store.Update(ctx, record)
	if err != nil {
		return nil, nil, err
	}
	bound, err := m.bindRunner(runner, updated.ID, completed, merged, carried)
	if err != nil {
		return nil, nil, err
	}
	return bound, &updated, nil
}

// mergePendingSet refreshes the fileexplorer manifest for the session's root
// and subdirectory, then merges the session's persisted unfinished files with
// the files the manifest currently reports. Unfinished files that no longer
// exist under the session's root and subdirectory are dropped; surviving
// unfinished files keep their persisted position ahead of the newly reported
// files, which keep the manifest's binary path order. Each entry is
// classified against the latest run: surviving unfinished files are carried
// over, newly reported files are newly discovered. The merged set replaces
// the session's persisted unfinished set, including its origins, before the
// run starts.
func (m *taskSessionManager) mergePendingSet(ctx context.Context, record sessions.Record, completed, pending []string) ([]string, []string, error) {
	if err := m.refreshFileExplorer(ctx); err != nil {
		return nil, nil, fmt.Errorf("refresh fileexplorer for resume: %w", err)
	}
	manifest, err := tasks.ListFilePaths(ctx, record.DatabaseFile, record.Subdirectory)
	if err != nil {
		return nil, nil, fmt.Errorf("list files for resume: %w", err)
	}

	completedSet := make(map[string]struct{}, len(completed))
	for _, path := range completed {
		completedSet[path] = struct{}{}
	}
	pendingSet := make(map[string]struct{}, len(pending))
	for _, path := range pending {
		pendingSet[path] = struct{}{}
	}
	manifestSet := make(map[string]struct{}, len(manifest))
	for _, path := range manifest {
		manifestSet[path] = struct{}{}
	}

	merged := make([]string, 0, len(manifest))
	carried := make([]string, 0, len(pending))
	entries := make([]sessions.PendingEntry, 0, len(manifest))
	for _, path := range pending {
		if _, gone := manifestSet[path]; !gone {
			continue
		}
		if _, done := completedSet[path]; done {
			continue
		}
		merged = append(merged, path)
		carried = append(carried, path)
		entries = append(entries, sessions.PendingEntry{Path: path, Carried: true})
	}
	for _, path := range manifest {
		if _, wasPending := pendingSet[path]; wasPending {
			continue
		}
		if _, done := completedSet[path]; done {
			continue
		}
		merged = append(merged, path)
		entries = append(entries, sessions.PendingEntry{Path: path})
	}

	if err := m.store.SetPendingSet(ctx, record.ID, entries); err != nil {
		return nil, nil, fmt.Errorf("persist merged pending set: %w", err)
	}
	return merged, carried, nil
}

// refreshFileExplorer asks fileexplorer to rescan the session's root before
// a resume reads the manifest, so files created or removed since the last
// run are visible to the merge.
func (m *taskSessionManager) refreshFileExplorer(ctx context.Context) error {
	refresher, err := tasks.NewHTTPRefresher(m.baseConfig.FileExplorerURL, m.baseConfig.HTTPClient)
	if err != nil {
		return err
	}
	return refresher.Refresh(ctx)
}

// Finish persists the terminal lifecycle state after a run exits. A run that
// ended without completing (stopped or failed) leaves its unfinished files
// marked aborted; files that completed keep their status, so a later resume
// starts from where the run left off.
func (m *taskSessionManager) Finish(record *sessions.Record, status sessions.Status, finishedAt time.Time, lastError string) error {
	record.Status = status
	record.FinishedAt = finishedAt
	record.LastError = lastError
	updated, err := m.store.Update(context.Background(), *record)
	if err != nil {
		return err
	}
	if status == sessions.StatusEnded {
		return nil
	}
	if err := m.store.MarkFileStatus(context.Background(), updated.ID, sessions.ProgressPending, sessions.ProgressAborted); err != nil {
		return fmt.Errorf("mark unfinished task session files aborted: %w", err)
	}
	return nil
}

// Delete removes an inactive record and its saved history, including its
// per-file progress.
func (m *taskSessionManager) Delete(id string) error {
	record, err := m.store.Get(context.Background(), id)
	if err != nil {
		return err
	}
	if record.Status == sessions.StatusActive {
		return errors.New("task session is still active and cannot be deleted")
	}
	return m.store.Delete(context.Background(), id)
}

// bindRunner returns an executor derived from runner that reports each
// file's progress for sessionID into the session store, skips the files an
// earlier run of this session already finished when completedPaths is
// non-empty, and — when initialPending is non-empty — seeds the session's
// unfinished files into the provider queue alongside manifest discoveries. runner
// itself is left untouched, so the original stays usable for other sessions.
func (m *taskSessionManager) bindRunner(runner *executor.Executor, sessionID string, completedPaths, initialPending, carriedPaths []string) (*executor.Executor, error) {
	config := runner.Config()
	config.CompletedPaths = completedPaths
	config.InitialPending = initialPending
	config.OnFileProgress = m.fileReporter(sessionID)
	config.OnPendingFiles = m.pendingReporter(sessionID, carriedPaths)
	bound, err := executor.New(config)
	if err != nil {
		return nil, fmt.Errorf("bind task session progress tracking: %w", err)
	}
	return bound, nil
}

// fileReporter returns the OnFileProgress hook for one session. It persists
// each file's outcome without failing the run: a persistence failure is
// logged, and the file is simply retried on the next resume because it was
// never recorded as completed.
func (m *taskSessionManager) fileReporter(sessionID string) func(taskID string, completed bool) {
	store := m.store
	logger := m.logger
	return func(taskID string, completed bool) {
		status := sessions.ProgressPending
		if completed {
			status = sessions.ProgressCompleted
		}
		if _, err := store.SaveFileProgress(context.Background(), sessions.FileProgress{
			SessionID: sessionID,
			Path:      taskID,
			Status:    status,
		}); err != nil {
			if logger != nil {
				logger.Error("Could not persist task session file progress",
					slog.String("session_id", sessionID),
					slog.String("path", taskID),
					slog.String("status", string(status)),
					slog.String("error", err.Error()),
				)
			}
		}
	}
}

// pendingReporter persists the queue snapshot from each file-index refresh,
// preserving the origin assigned when the run began. The task provider invokes
// it before selecting work, so the dashboard never gets behind the executor.
func (m *taskSessionManager) pendingReporter(sessionID string, carriedPaths []string) func([]string) error {
	carried := make(map[string]struct{}, len(carriedPaths))
	for _, path := range carriedPaths {
		carried[path] = struct{}{}
	}
	return func(paths []string) error {
		entries := make([]sessions.PendingEntry, 0, len(paths))
		for _, path := range paths {
			_, isCarried := carried[path]
			entries = append(entries, sessions.PendingEntry{Path: path, Carried: isCarried})
		}
		return m.store.SetPendingSet(context.Background(), sessionID, entries)
	}
}

func executorConfigForSession(base executor.Config, record sessions.Record) executor.Config {
	base.AgentID = record.AgentID
	base.RootPath = record.RootDirectory
	base.Subdirectory = record.Subdirectory
	base.DatabasePath = record.DatabaseFile
	return base
}
