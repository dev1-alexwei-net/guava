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

func TestStorePersistsFileProgressAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.sqlite")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}

	record, err := store.Create(context.Background(), Record{
		AgentID: "agent", RootDirectory: "/files", DatabaseFile: "/data/index.sqlite", Status: StatusActive,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	for _, entry := range []FileProgress{
		{SessionID: record.ID, Path: "a.txt", Status: ProgressCompleted},
		{SessionID: record.ID, Path: "b.txt", Status: ProgressPending},
		{SessionID: record.ID, Path: "c.txt", Status: ProgressCompleted},
	} {
		if _, err := store.SaveFileProgress(context.Background(), entry); err != nil {
			t.Fatalf("SaveFileProgress(%s): %v", entry.Path, err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	store, err = OpenStore(path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer store.Close()

	rows, err := store.ListFileProgress(context.Background(), record.ID)
	if err != nil {
		t.Fatalf("ListFileProgress: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("ListFileProgress = %+v, want 3 rows", rows)
	}
	statuses := make(map[string]ProgressStatus, len(rows))
	for _, row := range rows {
		if row.SessionID != record.ID {
			t.Fatalf("row session = %q, want %q", row.SessionID, record.ID)
		}
		statuses[row.Path] = row.Status
	}
	if statuses["a.txt"] != ProgressCompleted || statuses["b.txt"] != ProgressPending || statuses["c.txt"] != ProgressCompleted {
		t.Errorf("statuses = %#v, want a/c completed and b pending", statuses)
	}

	completed, err := store.CompletedPaths(context.Background(), record.ID)
	if err != nil {
		t.Fatalf("CompletedPaths: %v", err)
	}
	if len(completed) != 2 || (completed[0] != "a.txt" && completed[0] != "c.txt") || (completed[1] != "a.txt" && completed[1] != "c.txt") || completed[0] == completed[1] {
		t.Errorf("CompletedPaths = %#v, want a.txt and c.txt in order", completed)
	}
	completed, err = store.CompletedPaths(context.Background(), "missing")
	if err != nil || len(completed) != 0 {
		t.Errorf("CompletedPaths(missing) = %#v, %v, want empty with no error", completed, err)
	}
}

func TestStoreFileProgressValidatesInputAndRespectsSessionScope(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "sessions.sqlite"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	record, err := store.Create(context.Background(), Record{
		AgentID: "agent", RootDirectory: "/files", DatabaseFile: "/data/index.sqlite", Status: StatusActive,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if _, err := store.SaveFileProgress(context.Background(), FileProgress{Path: "a.txt", Status: ProgressPending}); err == nil {
		t.Error("SaveFileProgress(empty session) succeeded")
	}
	if _, err := store.SaveFileProgress(context.Background(), FileProgress{SessionID: record.ID, Status: ProgressPending}); err == nil {
		t.Error("SaveFileProgress(empty path) succeeded")
	}
	if _, err := store.SaveFileProgress(context.Background(), FileProgress{SessionID: record.ID, Path: "a.txt", Status: "bogus"}); err == nil {
		t.Error("SaveFileProgress(invalid status) succeeded")
	}

	if _, err := store.SaveFileProgress(context.Background(), FileProgress{SessionID: record.ID, Path: "a.txt", Status: ProgressPending}); err != nil {
		t.Fatalf("SaveFileProgress(valid): %v", err)
	}
	// The store is a scoped persistence layer: unknown sessions list empty and
	// never leak rows from other sessions.
	if rows, err := store.ListFileProgress(context.Background(), "missing"); err != nil || len(rows) != 0 {
		t.Errorf("ListFileProgress(missing) = %+v, %v, want empty with no error", rows, err)
	}
	if rows, err := store.ListFileProgress(context.Background(), record.ID); err != nil || len(rows) != 1 || rows[0].Path != "a.txt" {
		t.Errorf("ListFileProgress = %+v err=%v, want the single saved row", rows, err)
	}
}

func TestStoreMarkFileStatusTransitionsOnlyMatchingRows(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "sessions.sqlite"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	record, err := store.Create(context.Background(), Record{
		AgentID: "agent", RootDirectory: "/files", DatabaseFile: "/data/index.sqlite", Status: StatusActive,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	for _, status := range []ProgressStatus{ProgressPending, ProgressCompleted} {
		if _, err := store.SaveFileProgress(context.Background(), FileProgress{
			SessionID: record.ID, Path: string(status) + ".txt", Status: status,
		}); err != nil {
			t.Fatalf("SaveFileProgress(%s): %v", status, err)
		}
	}

	if err := store.MarkFileStatus(context.Background(), record.ID, ProgressPending, ProgressAborted); err != nil {
		t.Fatalf("MarkFileStatus: %v", err)
	}
	// Re-running the transition on an already-aborted row must be a no-op.
	if err := store.MarkFileStatus(context.Background(), record.ID, ProgressPending, ProgressAborted); err != nil {
		t.Fatalf("MarkFileStatus second pass: %v", err)
	}

	rows, err := store.ListFileProgress(context.Background(), record.ID)
	if err != nil {
		t.Fatalf("ListFileProgress: %v", err)
	}
	for _, row := range rows {
		switch row.Path {
		case "pending.txt":
			if row.Status != ProgressAborted {
				t.Errorf("pending row = %s, want aborted after transition", row.Status)
			}
		case "completed.txt":
			if row.Status != ProgressCompleted {
				t.Errorf("completed row = %s, want untouched completed status", row.Status)
			}
		}
	}
}

func TestStoreDeleteRemovesFileProgress(t *testing.T) {
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
	if _, err := store.SaveFileProgress(context.Background(), FileProgress{SessionID: record.ID, Path: "a.txt", Status: ProgressAborted}); err != nil {
		t.Fatalf("SaveFileProgress: %v", err)
	}
	if err := store.Delete(context.Background(), record.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	rows, err := store.ListFileProgress(context.Background(), record.ID)
	if err != nil || len(rows) != 0 {
		t.Errorf("ListFileProgress after delete = %+v, %v, want no saved progress", rows, err)
	}
	if completed, err := store.CompletedPaths(context.Background(), record.ID); err != nil || len(completed) != 0 {
		t.Errorf("CompletedPaths after delete = %#v, %v, want empty with no error", completed, err)
	}
}

func TestRecoverInterruptedAbortsOnlyUnfinishedFileProgress(t *testing.T) {
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
	paused, err := store.Create(context.Background(), Record{
		AgentID: "agent", RootDirectory: "/files", DatabaseFile: "/data/index.sqlite", Status: StatusPaused,
	})
	if err != nil {
		t.Fatalf("Create paused: %v", err)
	}
	for _, entry := range []FileProgress{
		{SessionID: active.ID, Path: "done.txt", Status: ProgressCompleted},
		{SessionID: active.ID, Path: "left-off.txt", Status: ProgressPending},
		{SessionID: paused.ID, Path: "stranded.txt", Status: ProgressPending},
	} {
		if _, err := store.SaveFileProgress(context.Background(), entry); err != nil {
			t.Fatalf("SaveFileProgress(%s): %v", entry.Path, err)
		}
	}

	if err := store.RecoverInterrupted(context.Background()); err != nil {
		t.Fatalf("RecoverInterrupted: %v", err)
	}

	wantStatus := map[string]ProgressStatus{
		"done.txt":     ProgressCompleted,
		"left-off.txt": ProgressAborted,
		"stranded.txt": ProgressAborted,
	}
	wantCount := map[string]int{active.ID: 2, paused.ID: 1}
	for _, id := range []string{active.ID, paused.ID} {
		rows, err := store.ListFileProgress(context.Background(), id)
		if err != nil {
			t.Fatalf("ListFileProgress(%s): %v", id, err)
		}
		if len(rows) != wantCount[id] {
			t.Fatalf("ListFileProgress(%s) = %+v, want %d rows", id, rows, wantCount[id])
		}
		for _, row := range rows {
			if want := wantStatus[row.Path]; row.Status != want {
				t.Errorf("session %s file %s = %s, want %s", id, row.Path, row.Status, want)
			}
		}
	}
}
