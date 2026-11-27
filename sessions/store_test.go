package sessions

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	_ "modernc.org/sqlite"
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

func TestStorePendingPathsListsOnlyUnfinishedFilesInPathOrder(t *testing.T) {
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
	for _, entry := range []FileProgress{
		{SessionID: record.ID, Path: "a.txt", Status: ProgressCompleted},
		{SessionID: record.ID, Path: "c.txt", Status: ProgressAborted},
		{SessionID: record.ID, Path: "b.txt", Status: ProgressPending},
	} {
		if _, err := store.SaveFileProgress(context.Background(), entry); err != nil {
			t.Fatalf("SaveFileProgress(%s): %v", entry.Path, err)
		}
	}

	got, err := store.PendingPaths(context.Background(), record.ID)
	if err != nil {
		t.Fatalf("PendingPaths: %v", err)
	}
	if want := []string{"b.txt", "c.txt"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("PendingPaths = %#v, want the pending and aborted files in path order %#v", got, want)
	}

	if got, err := store.PendingPaths(context.Background(), "unknown"); err != nil || len(got) != 0 {
		t.Errorf("PendingPaths(unknown) = %#v, %v, want empty with no error", got, err)
	}
	if _, err := store.PendingPaths(context.Background(), ""); err == nil {
		t.Error("PendingPaths(\"\") succeeded, want error")
	}
}

func TestStoreSetPendingSetReplacesUnfinishedSet(t *testing.T) {
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
	for _, entry := range []FileProgress{
		{SessionID: record.ID, Path: "a.txt", Status: ProgressCompleted},
		{SessionID: record.ID, Path: "b.txt", Status: ProgressPending},
		{SessionID: record.ID, Path: "c.txt", Status: ProgressAborted},
	} {
		if _, err := store.SaveFileProgress(context.Background(), entry); err != nil {
			t.Fatalf("SaveFileProgress(%s): %v", entry.Path, err)
		}
	}

	// Surviving unfinished files keep their place and are classified as
	// carried over, new files join the set as newly discovered, and
	// unfinished files left out of the set are dropped while completed
	// files stay completed.
	if err := store.SetPendingSet(context.Background(), record.ID, []PendingEntry{
		{Path: "d.txt"},
		{Path: "b.txt", Carried: true},
	}); err != nil {
		t.Fatalf("SetPendingSet: %v", err)
	}
	if got, err := store.PendingPaths(context.Background(), record.ID); err != nil {
		t.Fatalf("PendingPaths after replace: %v", err)
	} else if want := []string{"b.txt", "d.txt"}; !reflect.DeepEqual(got, want) {
		t.Errorf("PendingPaths = %#v, want %#v", got, want)
	}
	rows, err := store.ListFileProgress(context.Background(), record.ID)
	if err != nil {
		t.Fatalf("ListFileProgress: %v", err)
	}
	wantStatus := map[string]ProgressStatus{"a.txt": ProgressCompleted, "b.txt": ProgressPending, "d.txt": ProgressPending}
	wantOrigin := map[string]Origin{"a.txt": OriginNew, "b.txt": OriginCarried, "d.txt": OriginNew}
	if len(rows) != len(wantStatus) {
		t.Fatalf("ListFileProgress = %+v, want exactly %v", rows, wantStatus)
	}
	for _, row := range rows {
		if want, ok := wantStatus[row.Path]; !ok {
			t.Errorf("ListFileProgress kept file %s = %+v, want it dropped", row.Path, row)
		} else if row.Status != want {
			t.Errorf("file %s = %s, want %s", row.Path, row.Status, want)
		}
		if want := wantOrigin[row.Path]; row.Origin != want {
			t.Errorf("file %s origin = %s, want %s", row.Path, row.Origin, want)
		}
	}

	// A rejected update leaves the persisted set untouched.
	if err := store.SetPendingSet(context.Background(), record.ID, []PendingEntry{{Path: "e.txt"}, {Path: ""}}); err == nil {
		t.Error("SetPendingSet with an empty path succeeded, want error")
	}
	if got, err := store.PendingPaths(context.Background(), record.ID); err != nil {
		t.Fatalf("PendingPaths after rejected update: %v", err)
	} else if want := []string{"b.txt", "d.txt"}; !reflect.DeepEqual(got, want) {
		t.Errorf("PendingPaths after rejected update = %#v, want %#v", got, want)
	}

	// Replacing with an empty set clears the unfinished files and keeps the
	// completed ones.
	if err := store.SetPendingSet(context.Background(), record.ID, nil); err != nil {
		t.Fatalf("SetPendingSet(nil): %v", err)
	}
	if got, err := store.PendingPaths(context.Background(), record.ID); err != nil || len(got) != 0 {
		t.Errorf("PendingPaths after clear = %#v, %v, want empty", got, err)
	}
	if completed, err := store.CompletedPaths(context.Background(), record.ID); err != nil {
		t.Fatalf("CompletedPaths after clear: %v", err)
	} else if want := []string{"a.txt"}; !reflect.DeepEqual(completed, want) {
		t.Errorf("CompletedPaths after clear = %#v, want %#v", completed, want)
	}

	if err := store.SetPendingSet(context.Background(), "", []PendingEntry{{Path: "a.txt"}}); err == nil {
		t.Error(`SetPendingSet("", ...) succeeded, want error`)
	}
}

// TestStorePendingProgressReportsOrigins checks that the dashboard's pending
// view reads only unfinished rows and reports each file's origin.
func TestStorePendingProgressReportsOrigins(t *testing.T) {
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
	if err := store.SetPendingSet(context.Background(), record.ID, []PendingEntry{
		{Path: "carried.txt", Carried: true},
		{Path: "new.txt"},
	}); err != nil {
		t.Fatalf("SetPendingSet: %v", err)
	}
	if _, err := store.SaveFileProgress(context.Background(), FileProgress{
		SessionID: record.ID, Path: "done.txt", Status: ProgressCompleted,
	}); err != nil {
		t.Fatalf("SaveFileProgress: %v", err)
	}

	got, err := store.PendingProgress(context.Background(), record.ID)
	if err != nil {
		t.Fatalf("PendingProgress: %v", err)
	}
	if len(got) != 2 || got[0].Path != "carried.txt" || got[1].Path != "new.txt" {
		t.Fatalf("PendingProgress = %+v, want carried.txt then new.txt in path order", got)
	}
	if got[0].Origin != OriginCarried {
		t.Errorf("carried.txt origin = %s, want %s", got[0].Origin, OriginCarried)
	}
	if got[1].Origin != OriginNew {
		t.Errorf("new.txt origin = %s, want %s", got[1].Origin, OriginNew)
	}

	if rows, err := store.PendingProgress(context.Background(), "unknown"); err != nil || len(rows) != 0 {
		t.Errorf("PendingProgress(unknown) = %+v, %v, want empty with no error", rows, err)
	}
	if _, err := store.PendingProgress(context.Background(), ""); err == nil {
		t.Error(`PendingProgress("") succeeded, want error`)
	}
}

// TestStoreOpenStoreMigratesOriginColumn checks that a store opened over a
// database created before the origin column existed gains the column, with
// existing rows defaulting to newly discovered.
func TestStoreOpenStoreMigratesOriginColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.sqlite")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open legacy store: %v", err)
	}
	if _, err := legacy.Exec(`
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
		CREATE TABLE IF NOT EXISTS session_file_progress (
			session_id TEXT NOT NULL,
			file_path TEXT NOT NULL,
			status TEXT NOT NULL CHECK (status IN ('pending', 'completed', 'aborted')),
			updated_at INTEGER NOT NULL,
			PRIMARY KEY (session_id, file_path)
		);
		INSERT INTO session_file_progress (session_id, file_path, status, updated_at)
		VALUES ('legacy', 'old.txt', 'pending', 1)
	`); err != nil {
		legacy.Close()
		t.Fatalf("seed legacy schema: %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("close legacy store: %v", err)
	}

	store, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	rows, err := store.db.Query(`SELECT file_path, status, origin FROM session_file_progress WHERE session_id = 'legacy'`)
	if err != nil {
		t.Fatalf("query legacy rows: %v", err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatal("legacy row missing after migration")
	}
	var filePath, status, origin string
	if err := rows.Scan(&filePath, &status, &origin); err != nil {
		t.Fatalf("scan legacy row: %v", err)
	}
	if filePath != "old.txt" || status != "pending" {
		t.Errorf("legacy row = %s/%s, want old.txt/pending", filePath, status)
	}
	if origin != string(OriginNew) {
		t.Errorf("legacy row origin = %q, want %q", origin, OriginNew)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate legacy rows: %v", err)
	}
}

func TestEnsureAccountInitializesOnceAndPreservesExistingRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metadata.sqlite")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`CREATE TABLE legacy_data (value TEXT); INSERT INTO legacy_data VALUES ('keep')`); err != nil {
		t.Fatal(err)
	}
	_ = legacy.Close()

	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	initial := AccountSettings{Host: "127.0.0.2", Port: "8090", DatabasePath: "/data/index.sqlite", RootPath: "/data/files", GatewayURL: "http://gateway:18789", AgentID: "writer"}
	first, err := store.EnsureAccount(context.Background(), initial)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.ID) != 36 || first.ID[8] != '-' || first.ID[13] != '-' || first.ID[18] != '-' || first.ID[23] != '-' {
		t.Errorf("account ID %q is not a UUID", first.ID)
	}
	if first.Host != initial.Host || first.Port != initial.Port || first.DatabasePath != initial.DatabasePath || first.RootPath != initial.RootPath || first.GatewayURL != initial.GatewayURL || first.AgentID != initial.AgentID || first.FileExplorerURL != DefaultAccountSettings().FileExplorerURL || first.Instructions != DefaultAccountSettings().Instructions || !first.AppendNearestInstructions {
		t.Errorf("initialized settings = %+v, want supplied values and missing-field defaults", first)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	changed := AccountSettings{Host: "0.0.0.0", Port: "80", DatabasePath: "/changed.sqlite", RootPath: "/changed"}
	second, err := store.EnsureAccount(context.Background(), changed)
	if err != nil {
		t.Fatal(err)
	}
	if second != first {
		t.Errorf("reopened account = %+v, want persisted account %+v", second, first)
	}
	var value string
	if err := store.db.QueryRow(`SELECT value FROM legacy_data`).Scan(&value); err != nil || value != "keep" {
		t.Errorf("existing data = %q, error %v; want preserved row", value, err)
	}
}

func TestOpenStoreMigratesAccountInstructionPreferences(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-account.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE executor_accounts (
		id TEXT PRIMARY KEY, singleton INTEGER NOT NULL UNIQUE CHECK (singleton = 1),
		host TEXT NOT NULL, port TEXT NOT NULL, database_path TEXT NOT NULL, root_path TEXT NOT NULL,
		fileexplorer_url TEXT NOT NULL, gateway_url TEXT NOT NULL, agent_id TEXT NOT NULL,
		subdirectory TEXT NOT NULL, created_at INTEGER NOT NULL
	);
	INSERT INTO executor_accounts VALUES ('legacy', 1, '127.0.0.1', '8081', 'index.sqlite', '.',
		'http://127.0.0.1:8080', 'http://127.0.0.1:18789', 'main', '', 1);`)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	account, err := store.EnsureAccount(context.Background(), AccountSettings{})
	if err != nil {
		t.Fatal(err)
	}
	defaults := DefaultAccountSettings()
	if account.Instructions != defaults.Instructions || !account.AppendNearestInstructions {
		t.Errorf("migrated instruction settings = %q/%t, want %q/true", account.Instructions, account.AppendNearestInstructions, defaults.Instructions)
	}
}

func TestDeleteAccountRemovesOwnedSessionAndProgress(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "sessions.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	legacy, err := store.Create(context.Background(), Record{AgentID: "agent", RootDirectory: "/files", DatabaseFile: "/data/index.sqlite", Status: StatusPaused})
	if err != nil {
		t.Fatal(err)
	}
	account, err := store.EnsureAccount(context.Background(), AccountSettings{})
	if err != nil {
		t.Fatal(err)
	}
	legacy, err = store.Get(context.Background(), legacy.ID)
	if err != nil || legacy.AccountID != account.ID {
		t.Fatalf("legacy session after account initialization = %+v, error %v", legacy, err)
	}
	record, err := store.Create(context.Background(), Record{AgentID: "agent", RootDirectory: "/files", DatabaseFile: "/data/index.sqlite", Status: StatusPaused})
	if err != nil || record.AccountID != account.ID {
		t.Fatalf("Create account-scoped session = %+v, error %v", record, err)
	}
	if _, err := store.SaveFileProgress(context.Background(), FileProgress{SessionID: record.ID, Path: "task.md", Status: ProgressCompleted}); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteAccount(context.Background(), account.ID); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{legacy.ID, record.ID} {
		if _, err := store.Get(context.Background(), id); !errors.Is(err, ErrNotFound) {
			t.Errorf("Get session %q after account deletion = %v, want ErrNotFound", id, err)
		}
	}
	var progress int
	if err := store.db.QueryRow(`SELECT count(*) FROM session_file_progress`).Scan(&progress); err != nil || progress != 0 {
		t.Errorf("session progress after account deletion = %d, error %v; want none", progress, err)
	}
}

func TestDefaultAccountSettingsUseLoopbackAndLocalPaths(t *testing.T) {
	settings := DefaultAccountSettings()
	if settings.Host != "127.0.0.1" || settings.Port != "8081" || settings.DatabasePath != "fileexplorer.sqlite" || settings.RootPath != "." || settings.FileExplorerURL != "http://127.0.0.1:8080" || settings.GatewayURL != "http://127.0.0.1:18789" || settings.AgentID != "main" || settings.Subdirectory != "" || settings.Instructions != "Output <complete /> only when finished." || !settings.AppendNearestInstructions {
		t.Errorf("DefaultAccountSettings() = %+v", settings)
	}
}

func TestReplaceAccountChangesIdentityAndRemovesSessions(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "sessions.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	old, err := store.EnsureAccount(ctx, AccountSettings{})
	if err != nil {
		t.Fatal(err)
	}
	session, err := store.Create(ctx, Record{AgentID: old.AgentID, RootDirectory: old.RootPath, DatabaseFile: old.DatabasePath, Status: StatusPaused})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := store.ReplaceAccount(ctx, old.ID, AccountSettings{Host: "0.0.0.0", Port: "8082", AgentID: "writer", Instructions: "Review each file carefully.", AppendNearestInstructions: false})
	if err != nil {
		t.Fatal(err)
	}
	if updated.ID == old.ID || updated.Host != "0.0.0.0" || updated.Port != "8082" || updated.AgentID != "writer" || updated.Instructions != "Review each file carefully." || updated.AppendNearestInstructions {
		t.Errorf("replacement account = %+v, want new identity and submitted settings", updated)
	}
	if _, err := store.Get(ctx, session.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("old account session lookup = %v, want ErrNotFound", err)
	}
	current, err := store.EnsureAccount(ctx, AccountSettings{})
	if err != nil || current.ID != updated.ID {
		t.Errorf("current account = %+v, %v; want replacement %q", current, err, updated.ID)
	}
}
