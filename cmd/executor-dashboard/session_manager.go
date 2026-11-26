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
	bound, err := m.bindRunner(runner, record.ID, nil)
	if err != nil {
		return nil, nil, err
	}
	return bound, &record, nil
}

// Resume loads an explicitly selected inactive record, rehydrates its runner,
// skips files the session already completed so the run starts from where it
// left off, and marks the record active before returning it to the controller.
func (m *taskSessionManager) Resume(id string) (*executor.Executor, *sessions.Record, error) {
	record, err := m.store.Get(context.Background(), strings.TrimSpace(id))
	if err != nil {
		return nil, nil, err
	}
	if record.Status == sessions.StatusActive {
		return nil, nil, errors.New("task session is still active and cannot be resumed")
	}

	completed, err := m.store.CompletedPaths(context.Background(), record.ID)
	if err != nil {
		return nil, nil, fmt.Errorf("load task session file progress: %w", err)
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
	updated, err := m.store.Update(context.Background(), record)
	if err != nil {
		return nil, nil, err
	}
	bound, err := m.bindRunner(runner, updated.ID, completed)
	if err != nil {
		return nil, nil, err
	}
	return bound, &updated, nil
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
// file's progress for sessionID into the session store and, when
// completedPaths is non-empty, skips the files an earlier run of this session
// already finished. runner itself is left untouched, so the original stays
// usable for other sessions.
func (m *taskSessionManager) bindRunner(runner *executor.Executor, sessionID string, completedPaths []string) (*executor.Executor, error) {
	config := runner.Config()
	config.CompletedPaths = completedPaths
	config.OnFileProgress = m.fileReporter(sessionID)
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

func executorConfigForSession(base executor.Config, record sessions.Record) executor.Config {
	base.AgentID = record.AgentID
	base.RootPath = record.RootDirectory
	base.Subdirectory = record.Subdirectory
	base.DatabasePath = record.DatabaseFile
	return base
}
