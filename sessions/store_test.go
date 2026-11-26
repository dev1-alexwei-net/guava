package sessions

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestStorePersistsSessionConfigurationAndLifecycleAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.sqlite")
	startedAt := time.Date(2026, 11, 25, 12, 13, 14, 123, time.UTC)
	initial := Record{
		AgentID:       "writer",
		RootDirectory: "/srv/guava",
		Subdirectory:  "project/tasks-session-persistence",
		DatabaseFile:  "/data/fileexplorer.sqlite",
		Status:        StatusActive,
		StartedAt:     startedAt,
	}
	store, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	created, err := store.Create(context.Background(), initial)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.ID == "" {
		t.Fatal("Create returned empty ID")
	}
	if created.CreatedAt.IsZero() || created.UpdatedAt.IsZero() {
		t.Fatalf("Create returned unset timestamps: %+v", created)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	store, err = OpenStore(path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer store.Close()

	loaded, err := store.Get(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("Get after reopen: %v", err)
	}
	if loaded.ID != created.ID || loaded.AgentID != initial.AgentID ||
		loaded.RootDirectory != initial.RootDirectory ||
		loaded.Subdirectory != initial.Subdirectory ||
		loaded.DatabaseFile != initial.DatabaseFile || loaded.Status != initial.Status ||
		!loaded.StartedAt.Equal(startedAt) ||
		!loaded.CreatedAt.Equal(created.CreatedAt) ||
		!loaded.UpdatedAt.Equal(created.UpdatedAt) {
		t.Errorf("loaded record = %+v, want stored session identity, configuration, lifecycle, and timestamps", loaded)
	}
}

func TestStoreUpdatesLifecycleAndListsRecords(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "sessions.sqlite"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	first, err := store.Create(context.Background(), Record{
		AgentID: "agent", RootDirectory: "/files", DatabaseFile: "/data/index.sqlite", Status: StatusActive,
		StartedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	finishedAt := time.Now().UTC().Add(time.Second)
	updated, err := store.Update(context.Background(), Record{
		ID: first.ID, AgentID: "agent", RootDirectory: "/files", Subdirectory: "docs",
		DatabaseFile: "/data/index.sqlite", Status: StatusFailed, StartedAt: first.StartedAt,
		FinishedAt: finishedAt, LastError: "gateway unavailable",
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Status != StatusFailed || updated.Subdirectory != "docs" || updated.LastError != "gateway unavailable" {
		t.Errorf("Update returned %+v, want the updated config and failure state", updated)
	}
	if updated.UpdatedAt.Before(first.CreatedAt) {
		t.Errorf("UpdatedAt = %v, before CreatedAt %v", updated.UpdatedAt, first.CreatedAt)
	}

	loaded, err := store.Get(context.Background(), first.ID)
	if err != nil {
		t.Fatalf("Get updated record: %v", err)
	}
	if loaded.Status != StatusFailed || !loaded.FinishedAt.Equal(finishedAt) || loaded.LastError != "gateway unavailable" {
		t.Errorf("Get after update = %+v, want the latest lifecycle state", loaded)
	}

	records, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(records) != 1 || records[0].ID != first.ID {
		t.Errorf("List = %+v, want one updated record with ID %q", records, first.ID)
	}
}

func TestStoreDeleteRemovesSavedSessionHistory(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "sessions.sqlite"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	record, err := store.Create(context.Background(), Record{
		AgentID: "agent", RootDirectory: "/files", DatabaseFile: "/data/index.sqlite", Status: StatusPaused,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := store.Delete(context.Background(), record.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := store.Get(context.Background(), record.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get deleted record error = %v, want ErrNotFound", err)
	}
	records, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(records) != 0 {
		t.Errorf("List after delete = %+v, want no saved history", records)
	}
	if err := store.Delete(context.Background(), record.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("Delete missing record error = %v, want ErrNotFound", err)
	}
	if err := store.Delete(context.Background(), ""); err == nil {
		t.Error("Delete empty ID succeeded, want validation error")
	}
}

func TestStoreAcceptsEachLifecycleStatus(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "sessions.sqlite"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	for _, status := range []Status{StatusActive, StatusPaused, StatusEnded, StatusFailed} {
		t.Run(string(status), func(t *testing.T) {
			record, err := store.Create(context.Background(), Record{
				AgentID: "agent", RootDirectory: "/files", DatabaseFile: "/data/index.sqlite", Status: status,
			})
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			loaded, err := store.Get(context.Background(), record.ID)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if loaded.Status != status {
				t.Errorf("Status = %q, want %q", loaded.Status, status)
			}
		})
	}
}

func TestRecoverInterruptedPausesOnlyActiveSessions(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "sessions.sqlite"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	active, err := store.Create(context.Background(), Record{
		AgentID: "agent", RootDirectory: "/files", DatabaseFile: "/data/index.sqlite", Status: StatusActive,
	})
	if err != nil {
		t.Fatalf("Create active: %v", err)
	}
	failed, err := store.Create(context.Background(), Record{
		AgentID: "agent", RootDirectory: "/files", DatabaseFile: "/data/index.sqlite", Status: StatusFailed, LastError: "gateway timeout",
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	if err := store.RecoverInterrupted(context.Background()); err != nil {
		t.Fatalf("RecoverInterrupted: %v", err)
	}
	gotActive, err := store.Get(context.Background(), active.ID)
	if err != nil {
		t.Fatalf("Get recovered session: %v", err)
	}
	if gotActive.Status != StatusPaused || gotActive.LastError == "" || gotActive.FinishedAt.IsZero() {
		t.Errorf("recovered active session = %+v, want paused with interruption details", gotActive)
	}
	gotFailed, err := store.Get(context.Background(), failed.ID)
	if err != nil {
		t.Fatalf("Get failed session: %v", err)
	}
	if gotFailed.Status != StatusFailed || gotFailed.LastError != "gateway timeout" {
		t.Errorf("failed session changed during recovery = %+v, want its prior failure preserved", gotFailed)
	}
}

func TestStoreRejectsInvalidRecordsAndReportsMissingIDs(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "sessions.sqlite"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	if _, err := store.Create(context.Background(), Record{}); err == nil {
		t.Fatal("Create(empty) succeeded, want required configuration validation")
	}
	if _, err := store.Create(context.Background(), Record{
		AgentID: "agent", RootDirectory: "/files", DatabaseFile: "/data/index.sqlite", Status: "unknown",
	}); err == nil {
		t.Fatal("Create(invalid status) succeeded")
	}
	if _, err := store.Get(context.Background(), "missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get(missing) error = %v, want ErrNotFound", err)
	}
	if _, err := store.Update(context.Background(), Record{
		ID: "missing", AgentID: "agent", RootDirectory: "/files", DatabaseFile: "/data/index.sqlite", Status: StatusPaused,
	}); !errors.Is(err, ErrNotFound) {
		t.Errorf("Update(missing) error = %v, want ErrNotFound", err)
	}
	records, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(records) != 0 {
		t.Errorf("List after rejected creates = %d records, want 0", len(records))
	}
}
