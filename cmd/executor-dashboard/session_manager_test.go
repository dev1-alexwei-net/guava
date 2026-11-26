package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/dev1-alexwei-net/guava/executor"
	"github.com/dev1-alexwei-net/guava/sessions"
)

func TestTaskSessionManagerOwnsPersistedLaunchAndLifecycleState(t *testing.T) {
	config := testConfig(t, filepath.Join(t.TempDir(), "fresh-index.sqlite"))
	config.RootPath = filepath.Join(t.TempDir(), "fresh-root")
	store, err := sessions.OpenStore(filepath.Join(t.TempDir(), "task-sessions.sqlite"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()
	manager := newTaskSessionManager(config, store)

	runner, err := executor.New(config)
	if err != nil {
		t.Fatalf("executor.New: %v", err)
	}
	runner, err = runner.WithSubdirectory("docs")
	if err != nil {
		t.Fatalf("WithSubdirectory: %v", err)
	}
	record, err := manager.Create(runner)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if record.Status != sessions.StatusActive || record.AgentID != config.AgentID ||
		record.RootDirectory != config.RootPath || record.DatabaseFile != config.DatabasePath ||
		record.Subdirectory != "docs" || record.StartedAt.IsZero() {
		t.Fatalf("created record = %+v, want active record with the fresh launch configuration", record)
	}

	finishedAt := time.Now().UTC()
	if err := manager.Finish(record, sessions.StatusFailed, finishedAt, "gateway unavailable"); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	resumedRunner, resumedRecord, err := manager.Resume(record.ID)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	summary := resumedRunner.SessionSummary()
	if summary.AgentID != config.AgentID || summary.Subdirectory != "docs" ||
		summary.CLIArgs == config.CLIArgs {
		t.Errorf("resumed runner summary = %+v, want saved agent/subdirectory and selected launch args", summary)
	}
	if resumedRecord.Status != sessions.StatusActive || resumedRecord.StartedAt.IsZero() ||
		!resumedRecord.FinishedAt.IsZero() || resumedRecord.LastError != "" {
		t.Errorf("resumed record = %+v, want a clean active lifecycle snapshot", resumedRecord)
	}
	persisted, err := store.Get(context.Background(), record.ID)
	if err != nil {
		t.Fatalf("Get resumed record: %v", err)
	}
	if persisted.Status != sessions.StatusActive || persisted.RootDirectory != config.RootPath ||
		persisted.DatabaseFile != config.DatabasePath || persisted.Subdirectory != "docs" {
		t.Errorf("persisted resumed record = %+v, want saved launch configuration marked active", persisted)
	}
}
