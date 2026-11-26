package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dev1-alexwei-net/guava/executor"
	"github.com/dev1-alexwei-net/guava/sessions"

	_ "modernc.org/sqlite"
)

// indexedFile is a minimal fileexplorer index entry for dashboard tests.
type indexedFile struct {
	path string
	name string
	size int64
}

// createIndexFixture seeds the fileexplorer database with a complete set of
// files so a fresh run's initial refresh can start processing.
func createIndexFixture(t *testing.T, databasePath string, entries []indexedFile) {
	t.Helper()
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`
		CREATE TABLE files (
			path TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			is_dir INTEGER NOT NULL DEFAULT 0,
			size INTEGER NOT NULL DEFAULT 0,
			mtime INTEGER NOT NULL DEFAULT 0
		)
	`); err != nil {
		t.Fatal(err)
	}
	for _, file := range entries {
		if _, err := db.Exec(`
			INSERT INTO files (path, name, is_dir, size, mtime) VALUES (?, ?, 0, ?, 0)
		`, file.path, file.name, file.size); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTaskSessionManagerOwnsPersistedLaunchAndLifecycleState(t *testing.T) {
	config := testConfig(t, filepath.Join(t.TempDir(), "fresh-index.sqlite"))
	config.RootPath = filepath.Join(t.TempDir(), "fresh-root")
	store, err := sessions.OpenStore(filepath.Join(t.TempDir(), "task-sessions.sqlite"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()
	manager := newTaskSessionManager(config, store, nil)

	runner, err := executor.New(config)
	if err != nil {
		t.Fatalf("executor.New: %v", err)
	}
	runner, err = runner.WithSubdirectory("docs")
	if err != nil {
		t.Fatalf("WithSubdirectory: %v", err)
	}
	bound, record, err := manager.Create(runner)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if record.Status != sessions.StatusActive || record.AgentID != config.AgentID ||
		record.RootDirectory != config.RootPath || record.DatabaseFile != config.DatabasePath ||
		record.Subdirectory != "docs" || record.StartedAt.IsZero() {
		t.Fatalf("created record = %+v, want active record with the fresh launch configuration", record)
	}
	if bound == nil {
		t.Fatal("Create returned a nil runner")
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

func TestTaskSessionManagerResumeSkipsCompletedFilesAndRetriesUnfinished(t *testing.T) {
	config := testConfig(t, filepath.Join(t.TempDir(), "fileexplorer.sqlite"))
	rootPath := config.RootPath
	for _, name := range []string{"a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(rootPath, name), []byte(name+" content"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(config.InstructionsPath, []byte("instructions"), 0o600); err != nil {
		t.Fatal(err)
	}
	createIndexFixture(t, config.DatabasePath, []indexedFile{
		{path: "a.txt", name: "a.txt", size: 11},
		{path: "b.txt", name: "b.txt", size: 11},
	})

	// The gateway completes a.txt but denies b.txt with a non-2xx response,
	// which turns the first run into a failed one. After the session is
	// resumed the gateway accepts every file.
	var mu sync.Mutex
	phase := 1
	rejectB := true
	requests := map[int][]string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusOK)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read body", http.StatusBadRequest)
			return
		}
		path := ""
		for _, name := range []string{"a.txt", "b.txt"} {
			if strings.Contains(string(body), `Current file path: \"`+name+`\"`) {
				path = name
			}
		}
		mu.Lock()
		if path == "b.txt" && rejectB {
			mu.Unlock()
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"error":{"message":"denied"}}`)
			return
		}
		requests[phase] = append(requests[phase], path)
		mu.Unlock()
		response := map[string]any{
			"id": "resp_1",
			"output": []map[string]any{{
				"type": "message", "role": "assistant",
				"content": []map[string]any{{"type": "output_text", "text": "<complete />"}},
			}},
		}
		if err := json.NewEncoder(w).Encode(response); err != nil {
			t.Errorf("encode response: %v", err)
		}
	}))
	defer server.Close()
	config.GatewayURL = server.URL
	config.FileExplorerURL = server.URL
	config.HTTPClient = server.Client()

	store, err := sessions.OpenStore(filepath.Join(t.TempDir(), "task-sessions.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	manager := newTaskSessionManager(config, store, nil)

	runner, err := executor.New(config)
	if err != nil {
		t.Fatal(err)
	}
	bound, record, err := manager.Create(runner)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// First run: a fresh run attempts every file; a.txt completes and b.txt is
	// denied, so the run fails with b.txt left unfinished.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	if err := bound.Run(ctx); err == nil {
		t.Fatal("first Run() succeeded, want the denied file to fail the run")
	}
	cancel()
	if want := []string{"a.txt"}; !reflect.DeepEqual(requests[1], want) {
		t.Fatalf("fresh run conversations = %#v, want only the completed file; b.txt was denied", requests[1])
	}
	if err := manager.Finish(record, sessions.StatusFailed, time.Now().UTC(), "gateway denied b.txt"); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	progress, err := store.ListFileProgress(context.Background(), record.ID)
	if err != nil {
		t.Fatalf("ListFileProgress: %v", err)
	}
	if want := []sessions.FileProgress{
		{SessionID: record.ID, Path: "a.txt", Status: sessions.ProgressCompleted},
		{SessionID: record.ID, Path: "b.txt", Status: sessions.ProgressAborted},
	}; !progressMatches(progress, want) {
		t.Fatalf("persisted progress = %#v, want completed kept and unfinished aborted", progress)
	}

	// Resumed run: the completed file is skipped and the unfinished one is
	// processed again.
	mu.Lock()
	phase = 2
	rejectB = false
	mu.Unlock()
	resumed, resumedRecord, err := manager.Resume(record.ID)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if resumedRecord.ID != record.ID {
		t.Fatalf("resumed record ID = %q, want %q", resumedRecord.ID, record.ID)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 300*time.Millisecond)
	if err := resumed.Run(ctx); err != nil {
		t.Fatalf("resumed Run: %v", err)
	}
	cancel()
	if want := []string{"b.txt"}; !reflect.DeepEqual(requests[2], want) {
		t.Fatalf("resumed run conversations = %#v, want only the unfinished file", requests[2])
	}
	if err := manager.Finish(resumedRecord, sessions.StatusEnded, time.Now().UTC(), ""); err != nil {
		t.Fatalf("Finish resumed: %v", err)
	}
	progress, err = store.ListFileProgress(context.Background(), record.ID)
	if err != nil {
		t.Fatalf("ListFileProgress resumed: %v", err)
	}
	if want := []sessions.FileProgress{
		{SessionID: record.ID, Path: "a.txt", Status: sessions.ProgressCompleted},
		{SessionID: record.ID, Path: "b.txt", Status: sessions.ProgressCompleted},
	}; !progressMatches(progress, want) {
		t.Fatalf("final progress = %#v, want every file completed", progress)
	}
}

// progressMatches compares per-file progress entries, ignoring the server
// maintained UpdatedAt timestamp.
func progressMatches(got, want []sessions.FileProgress) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i].SessionID != want[i].SessionID || got[i].Path != want[i].Path || got[i].Status != want[i].Status {
			return false
		}
	}
	return true
}
