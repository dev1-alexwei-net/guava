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
	createIndexFixture(t, config.DatabasePath, nil)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	config.FileExplorerURL = server.URL
	config.HTTPClient = server.Client()
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

func TestTaskSessionManagerResumeRetriesAbortedAndUnstartedFiles(t *testing.T) {
	config := testConfig(t, filepath.Join(t.TempDir(), "fileexplorer.sqlite"))
	rootPath := config.RootPath
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
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
		{path: "c.txt", name: "c.txt", size: 11},
	})

	// The gateway completes a.txt, then denies b.txt with a non-2xx response,
	// which stops the first run with b.txt unfinished and c.txt queued but
	// never started. After the session is resumed the gateway accepts every
	// file, so both the unfinished file and the file that never started must
	// be processed again while a.txt is skipped.
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
		for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
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

	// First run: a.txt completes, b.txt is denied so the run stops, and c.txt
	// is queued but never started.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	if err := bound.Run(ctx); err == nil {
		t.Fatal("first Run() succeeded, want the denied file to fail the run")
	}
	cancel()
	if want := []string{"a.txt"}; !reflect.DeepEqual(requests[1], want) {
		t.Fatalf("fresh run conversations = %#v, want only the completed file; b.txt was denied and c.txt never started", requests[1])
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
		t.Fatalf("persisted progress = %#v, want a.txt completed, b.txt aborted, and c.txt not yet recorded", progress)
	}

	// Resumed run: the completed file is skipped, and both the unfinished file
	// (b.txt) and the file that never started before the interruption (c.txt)
	// are processed again.
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
	if want := []string{"b.txt", "c.txt"}; !reflect.DeepEqual(requests[2], want) {
		t.Fatalf("resumed run conversations = %#v, want the unfinished file and the never-started file reprocessed", requests[2])
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
		{SessionID: record.ID, Path: "c.txt", Status: sessions.ProgressCompleted},
	}; !progressMatches(progress, want) {
		t.Fatalf("final progress = %#v, want every file completed", progress)
	}
}

func TestTaskSessionManagerResumeAfterCleanRunReprocessesNothing(t *testing.T) {
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

	// The gateway accepts every file, so the first run completes cleanly and
	// the session reaches its ended state. A later resume must not process any
	// file again.
	var mu sync.Mutex
	phase := 1
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

	// First run: both files complete, so the run ends cleanly.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	if err := bound.Run(ctx); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	cancel()
	if want := []string{"a.txt", "b.txt"}; !reflect.DeepEqual(requests[1], want) {
		t.Fatalf("fresh run conversations = %#v, want every file processed", requests[1])
	}
	if err := manager.Finish(record, sessions.StatusEnded, time.Now().UTC(), ""); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	// Resumed run: every file already completed, so nothing is processed again.
	mu.Lock()
	phase = 2
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
	if got := requests[2]; len(got) != 0 {
		t.Fatalf("resumed run conversations = %#v, want no files reprocessed after a clean run", got)
	}
	if err := manager.Finish(resumedRecord, sessions.StatusEnded, time.Now().UTC(), ""); err != nil {
		t.Fatalf("Finish resumed: %v", err)
	}
	progress, err := store.ListFileProgress(context.Background(), record.ID)
	if err != nil {
		t.Fatalf("ListFileProgress resumed: %v", err)
	}
	if want := []sessions.FileProgress{
		{SessionID: record.ID, Path: "a.txt", Status: sessions.ProgressCompleted},
		{SessionID: record.ID, Path: "b.txt", Status: sessions.ProgressCompleted},
	}; !progressMatches(progress, want) {
		t.Fatalf("final progress = %#v, want every file completed and unchanged", progress)
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

func TestTaskSessionManagerResumeDropsVanishedPendingFiles(t *testing.T) {
	config := testConfig(t, filepath.Join(t.TempDir(), "fileexplorer.sqlite"))
	rootPath := config.RootPath
	for _, name := range []string{"m.txt", "z.txt"} {
		if err := os.WriteFile(filepath.Join(rootPath, name), []byte(name+" content"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(config.InstructionsPath, []byte("instructions"), 0o600); err != nil {
		t.Fatal(err)
	}
	createIndexFixture(t, config.DatabasePath, []indexedFile{
		{path: "m.txt", name: "m.txt", size: 9},
		{path: "z.txt", name: "z.txt", size: 9},
	})

	// The gateway completes m.txt but denies z.txt, leaving it unfinished.
	// Before the resume z.txt is moved out of the session's root, so it no
	// longer resolves in the refreshed manifest and must be dropped.
	var mu sync.Mutex
	phase := 1
	rejectZ := true
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
		for _, name := range []string{"m.txt", "z.txt"} {
			if strings.Contains(string(body), `Current file path: \"`+name+`\"`) {
				path = name
			}
		}
		mu.Lock()
		if path == "z.txt" && rejectZ {
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

	// First run: m.txt completes and z.txt is denied, so the run fails with
	// z.txt left unfinished.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	if err := bound.Run(ctx); err == nil {
		t.Fatal("first Run() succeeded, want the denied file to fail the run")
	}
	cancel()
	if want := []string{"m.txt"}; !reflect.DeepEqual(requests[1], want) {
		t.Fatalf("fresh run conversations = %#v, want only the completed file; z.txt was denied", requests[1])
	}
	if err := manager.Finish(record, sessions.StatusFailed, time.Now().UTC(), "gateway denied z.txt"); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	// z.txt leaves the session's root and manifest before the resume.
	index, err := sql.Open("sqlite", config.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := index.Exec(`DELETE FROM files WHERE path = 'z.txt'`); err != nil {
		t.Fatal(err)
	}
	_ = index.Close()
	if err := os.Remove(filepath.Join(rootPath, "z.txt")); err != nil {
		t.Fatal(err)
	}

	// Resumed run: the vanished file is dropped from the persisted set and is
	// never selected again, while the completed file stays completed.
	mu.Lock()
	phase = 2
	rejectZ = false
	mu.Unlock()
	resumed, resumedRecord, err := manager.Resume(record.ID)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if resumedRecord.ID != record.ID {
		t.Fatalf("resumed record ID = %q, want %q", resumedRecord.ID, record.ID)
	}
	pending, err := store.PendingPaths(context.Background(), record.ID)
	if err != nil {
		t.Fatalf("PendingPaths after Resume: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("persisted pending set = %#v, want the vanished file dropped", pending)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 300*time.Millisecond)
	if err := resumed.Run(ctx); err != nil {
		t.Fatalf("resumed Run: %v", err)
	}
	cancel()
	if got := requests[2]; len(got) != 0 {
		t.Fatalf("resumed run conversations = %#v, want the vanished file never selected", got)
	}
	if err := manager.Finish(resumedRecord, sessions.StatusEnded, time.Now().UTC(), ""); err != nil {
		t.Fatalf("Finish resumed: %v", err)
	}
	progress, err := store.ListFileProgress(context.Background(), record.ID)
	if err != nil {
		t.Fatalf("ListFileProgress resumed: %v", err)
	}
	if want := []sessions.FileProgress{
		{SessionID: record.ID, Path: "m.txt", Status: sessions.ProgressCompleted},
	}; !progressMatches(progress, want) {
		t.Fatalf("final progress = %#v, want only the still-present completed file", progress)
	}
}

func TestTaskSessionManagerResumeMergesPendingAndNewlyDiscoveredFiles(t *testing.T) {
	config := testConfig(t, filepath.Join(t.TempDir(), "fileexplorer.sqlite"))
	rootPath := config.RootPath
	for _, name := range []string{"m.txt", "z.txt"} {
		if err := os.WriteFile(filepath.Join(rootPath, name), []byte(name+" content"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(config.InstructionsPath, []byte("instructions"), 0o600); err != nil {
		t.Fatal(err)
	}
	createIndexFixture(t, config.DatabasePath, []indexedFile{
		{path: "m.txt", name: "m.txt", size: 9},
		{path: "z.txt", name: "z.txt", size: 9},
	})

	// The gateway completes m.txt but denies z.txt, leaving it unfinished.
	// After the resume the gateway also reports two newly discovered files,
	// a.txt and c.txt, which sort before z.txt, so the wanted order only
	// holds if the surviving unfinished file stays ahead of them.
	var mu sync.Mutex
	phase := 1
	rejectZ := true
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
		for _, name := range []string{"m.txt", "z.txt", "a.txt", "c.txt"} {
			if strings.Contains(string(body), `Current file path: \"`+name+`\"`) {
				path = name
			}
		}
		mu.Lock()
		if path == "z.txt" && rejectZ {
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

	// First run: m.txt completes and z.txt is denied, so the run fails with
	// z.txt left unfinished.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	if err := bound.Run(ctx); err == nil {
		t.Fatal("first Run() succeeded, want the denied file to fail the run")
	}
	cancel()
	if want := []string{"m.txt"}; !reflect.DeepEqual(requests[1], want) {
		t.Fatalf("fresh run conversations = %#v, want only the completed file; z.txt was denied", requests[1])
	}
	if err := manager.Finish(record, sessions.StatusFailed, time.Now().UTC(), "gateway denied z.txt"); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	// a.txt and c.txt are discovered after the failed run.
	for _, name := range []string{"a.txt", "c.txt"} {
		if err := os.WriteFile(filepath.Join(rootPath, name), []byte(name+" content"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	index, err := sql.Open("sqlite", config.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range []indexedFile{{path: "a.txt", name: "a.txt", size: 9}, {path: "c.txt", name: "c.txt", size: 9}} {
		if _, err := index.Exec(`INSERT INTO files (path, name, is_dir, size, mtime) VALUES (?, ?, 0, ?, 0)`, file.path, file.name, file.size); err != nil {
			t.Fatal(err)
		}
	}
	_ = index.Close()

	// Resumed run: the surviving unfinished file stays ahead of the newly
	// discovered files, the merged set is persisted, and the completed file
	// is skipped.
	mu.Lock()
	phase = 2
	rejectZ = false
	mu.Unlock()
	resumed, resumedRecord, err := manager.Resume(record.ID)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if resumedRecord.ID != record.ID {
		t.Fatalf("resumed record ID = %q, want %q", resumedRecord.ID, record.ID)
	}
	pending, err := store.PendingPaths(context.Background(), record.ID)
	if err != nil {
		t.Fatalf("PendingPaths after Resume: %v", err)
	}
	if want := []string{"a.txt", "c.txt", "z.txt"}; !reflect.DeepEqual(pending, want) {
		t.Fatalf("persisted pending set = %#v, want the unfinished and newly discovered files %#v", pending, want)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 300*time.Millisecond)
	if err := resumed.Run(ctx); err != nil {
		t.Fatalf("resumed Run: %v", err)
	}
	cancel()
	if err := manager.Finish(resumedRecord, sessions.StatusEnded, time.Now().UTC(), ""); err != nil {
		t.Fatalf("Finish resumed: %v", err)
	}
	progress, err := store.ListFileProgress(context.Background(), record.ID)
	if err != nil {
		t.Fatalf("ListFileProgress resumed: %v", err)
	}
	if want := []sessions.FileProgress{
		{SessionID: record.ID, Path: "a.txt", Status: sessions.ProgressCompleted},
		{SessionID: record.ID, Path: "c.txt", Status: sessions.ProgressCompleted},
		{SessionID: record.ID, Path: "m.txt", Status: sessions.ProgressCompleted},
		{SessionID: record.ID, Path: "z.txt", Status: sessions.ProgressCompleted},
	}; !progressMatches(progress, want) {
		t.Fatalf("final progress = %#v, want every file completed", progress)
	}
}

func TestTaskSessionManagerResumeProcessesOnlyNewlyDiscoveredFiles(t *testing.T) {
	config := testConfig(t, filepath.Join(t.TempDir(), "fileexplorer.sqlite"))
	rootPath := config.RootPath
	if err := os.WriteFile(filepath.Join(rootPath, "m.txt"), []byte("m.txt content"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.InstructionsPath, []byte("instructions"), 0o600); err != nil {
		t.Fatal(err)
	}
	createIndexFixture(t, config.DatabasePath, []indexedFile{
		{path: "m.txt", name: "m.txt", size: 9},
	})

	// The gateway accepts every file, so the first run completes cleanly.
	// After the resume the gateway reports one newly discovered file, a.txt,
	// which must be the only file the resumed run processes.
	var mu sync.Mutex
	phase := 1
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
		for _, name := range []string{"m.txt", "a.txt"} {
			if strings.Contains(string(body), `Current file path: \"`+name+`\"`) {
				path = name
			}
		}
		mu.Lock()
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

	// First run: m.txt completes, so the run ends cleanly.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	if err := bound.Run(ctx); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	cancel()
	if want := []string{"m.txt"}; !reflect.DeepEqual(requests[1], want) {
		t.Fatalf("fresh run conversations = %#v, want every file processed", requests[1])
	}
	if err := manager.Finish(record, sessions.StatusEnded, time.Now().UTC(), ""); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	// a.txt is discovered after the clean run.
	if err := os.WriteFile(filepath.Join(rootPath, "a.txt"), []byte("a.txt content"), 0o600); err != nil {
		t.Fatal(err)
	}
	index, err := sql.Open("sqlite", config.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := index.Exec(`INSERT INTO files (path, name, is_dir, size, mtime) VALUES ('a.txt', 'a.txt', 0, 9, 0)`); err != nil {
		t.Fatal(err)
	}
	_ = index.Close()

	// Resumed run: with nothing left unfinished, only the newly discovered
	// file is processed while the completed one is skipped.
	mu.Lock()
	phase = 2
	mu.Unlock()
	resumed, resumedRecord, err := manager.Resume(record.ID)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if resumedRecord.ID != record.ID {
		t.Fatalf("resumed record ID = %q, want %q", resumedRecord.ID, record.ID)
	}
	pending, err := store.PendingPaths(context.Background(), record.ID)
	if err != nil {
		t.Fatalf("PendingPaths after Resume: %v", err)
	}
	if want := []string{"a.txt"}; !reflect.DeepEqual(pending, want) {
		t.Fatalf("persisted pending set = %#v, want only the newly discovered file", pending)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 300*time.Millisecond)
	if err := resumed.Run(ctx); err != nil {
		t.Fatalf("resumed Run: %v", err)
	}
	cancel()
	if want := []string{"a.txt"}; !reflect.DeepEqual(requests[2], want) {
		t.Fatalf("resumed run conversations = %#v, want only the newly discovered file", requests[2])
	}
	if err := manager.Finish(resumedRecord, sessions.StatusEnded, time.Now().UTC(), ""); err != nil {
		t.Fatalf("Finish resumed: %v", err)
	}
	progress, err := store.ListFileProgress(context.Background(), record.ID)
	if err != nil {
		t.Fatalf("ListFileProgress resumed: %v", err)
	}
	if want := []sessions.FileProgress{
		{SessionID: record.ID, Path: "a.txt", Status: sessions.ProgressCompleted},
		{SessionID: record.ID, Path: "m.txt", Status: sessions.ProgressCompleted},
	}; !progressMatches(progress, want) {
		t.Fatalf("final progress = %#v, want every file completed", progress)
	}
}
