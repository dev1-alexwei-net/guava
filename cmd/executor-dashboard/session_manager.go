package main

import (
	"context"
	"errors"
	"fmt"
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
}

var _ taskSessionStore = (*sessions.Store)(nil)

// taskSessionManager owns the persisted identity, launch configuration, and
// lifecycle snapshot for dashboard runs. The controller owns the live
// goroutine and cancellation state; the dashboard owns opening and closing the
// underlying store.
type taskSessionManager struct {
	baseConfig executor.Config
	store      taskSessionStore
}

func newTaskSessionManager(config executor.Config, store taskSessionStore) *taskSessionManager {
	return &taskSessionManager{baseConfig: config, store: store}
}

// Create records the launch configuration for a fresh run before it starts.
func (m *taskSessionManager) Create(runner *executor.Executor) (*sessions.Record, error) {
	summary := runner.SessionSummary()
	record, err := m.store.Create(context.Background(), sessions.Record{
		AgentID: summary.AgentID, RootDirectory: m.baseConfig.RootPath,
		Subdirectory: summary.Subdirectory, DatabaseFile: m.baseConfig.DatabasePath,
		Status: sessions.StatusActive, StartedAt: time.Now().UTC(),
	})
	if err != nil {
		return nil, err
	}
	return &record, nil
}

// Resume loads an explicitly selected inactive record, rehydrates its runner,
// and marks the record active before returning it to the controller.
func (m *taskSessionManager) Resume(id string) (*executor.Executor, *sessions.Record, error) {
	record, err := m.store.Get(context.Background(), strings.TrimSpace(id))
	if err != nil {
		return nil, nil, err
	}
	if record.Status == sessions.StatusActive {
		return nil, nil, errors.New("task session is still active and cannot be resumed")
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
	return runner, &updated, nil
}

// Finish persists the terminal lifecycle state after a run exits.
func (m *taskSessionManager) Finish(record *sessions.Record, status sessions.Status, finishedAt time.Time, lastError string) error {
	record.Status = status
	record.FinishedAt = finishedAt
	record.LastError = lastError
	_, err := m.store.Update(context.Background(), *record)
	return err
}

// Delete removes an inactive record and its saved history.
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

func executorConfigForSession(base executor.Config, record sessions.Record) executor.Config {
	base.AgentID = record.AgentID
	base.RootPath = record.RootDirectory
	base.Subdirectory = record.Subdirectory
	base.DatabasePath = record.DatabaseFile
	return base
}
