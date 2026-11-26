package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dev1-alexwei-net/guava/executor"
	"github.com/dev1-alexwei-net/guava/sessions"
)

// rawCLIArgs models the raw dump of a server command line, including an
// argument with spaces, to prove the page data carries it verbatim.
const rawCLIArgs = `executor-dashboard -database "/var/lib/fileexplorer.sqlite" -root /srv/files -agent-id executor-agent`

func testConfig(t *testing.T, databasePath string) executor.Config {
	t.Helper()
	rootPath := t.TempDir()
	return executor.Config{
		DatabasePath:     databasePath,
		RootPath:         rootPath,
		InstructionsPath: filepath.Join(rootPath, "instructions.md"),
		GatewayURL:       "http://127.0.0.1:18789",
		FileExplorerURL:  "http://127.0.0.1:8080",
		AgentID:          "executor-agent",
		CLIArgs:          rawCLIArgs,
	}
}

func newTestController(t *testing.T, databasePath string) *executorController {
	t.Helper()
	config := testConfig(t, databasePath)
	store, err := sessions.OpenStore(filepath.Join(t.TempDir(), "task-sessions.sqlite"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	controller, err := newExecutorController(config, slog.LevelInfo, store)
	if err != nil {
		t.Fatalf("newExecutorController: %v", err)
	}
	t.Cleanup(controller.Close)
	return controller
}

func TestIdleSnapshotCarriesSessionSummary(t *testing.T) {
	controller := newTestController(t, filepath.Join(t.TempDir(), "missing.sqlite"))
	snapshot := controller.Snapshot()

	if snapshot.Status != "idle" {
		t.Errorf("Status = %q, want %q", snapshot.Status, "idle")
	}
	if snapshot.Logs != "" || snapshot.LastErr != "" {
		t.Errorf("idle snapshot = %+v, want no logs and no last error", snapshot)
	}
	if snapshot.AgentID != "executor-agent" {
		t.Errorf("AgentID = %q, want %q", snapshot.AgentID, "executor-agent")
	}
	if snapshot.CLIArgs != rawCLIArgs {
		t.Errorf("CLIArgs = %q, want raw dump %q", snapshot.CLIArgs, rawCLIArgs)
	}
	if !snapshot.StartedAt.IsZero() {
		t.Errorf("StartedAt = %v, want zero before the first run", snapshot.StartedAt)
	}
}

func TestFailedRunSnapshotCarriesSessionSummary(t *testing.T) {
	// The database file does not exist, so the run fails while opening the
	// index, before any network traffic.
	controller := newTestController(t, filepath.Join(t.TempDir(), "missing.sqlite"))
	if err := controller.Start(""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	snapshot := waitForControllerStatus(t, controller, "failed")

	if snapshot.LastErr == "" {
		t.Error("LastErr empty, want the run failure message")
	}
	if snapshot.AgentID != "executor-agent" {
		t.Errorf("AgentID = %q, want %q", snapshot.AgentID, "executor-agent")
	}
	if snapshot.CLIArgs != rawCLIArgs {
		t.Errorf("CLIArgs = %q, want raw dump %q", snapshot.CLIArgs, rawCLIArgs)
	}
	if snapshot.StartedAt.IsZero() {
		t.Error("StartedAt = zero, want the run start time")
	}
}

func TestSnapshotStaysSafeToCallConcurrently(t *testing.T) {
	controller := newTestController(t, filepath.Join(t.TempDir(), "missing.sqlite"))
	if err := controller.Start(""); err != nil {
		t.Fatalf("Start: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				snapshot := controller.Snapshot()
				if snapshot.AgentID != "executor-agent" || snapshot.CLIArgs != rawCLIArgs {
					t.Errorf("concurrent Snapshot = %+v, want agent-id and CLI-args unchanged", snapshot)
					return
				}
			}
		}()
	}
	wg.Wait()
}

func TestPageDataCarriesSessionSummaryForEveryStatus(t *testing.T) {
	startedAt := time.Date(2026, 11, 24, 12, 30, 5, 0, time.UTC)
	statuses := []struct {
		status  string
		running bool
		busy    bool
	}{
		{status: "idle", running: false, busy: false},
		{status: "running", running: true, busy: true},
		{status: "stopping", running: false, busy: true},
		{status: "completed", running: false, busy: false},
		{status: "failed", running: false, busy: false},
	}
	query := url.Values{"refresh": {"5"}, "theme": {"dark"}, "error": {"boom"}}
	for _, tc := range statuses {
		t.Run(tc.status, func(t *testing.T) {
			snapshot := executorSnapshot{
				Status:    tc.status,
				Logs:      "log line",
				LastErr:   "last error",
				AgentID:   "executor-agent",
				CLIArgs:   rawCLIArgs,
				StartedAt: startedAt,
			}
			data := pageData(snapshot, query, []string{"docs", "docs/deep"}, nil)

			if data.Status != tc.status {
				t.Errorf("Status = %q, want %q", data.Status, tc.status)
			}
			if data.Logs != "log line" || data.LastErr != "last error" || data.Error != "boom" {
				t.Errorf("logs/last-error/error = %q/%q/%q, want the snapshot values", data.Logs, data.LastErr, data.Error)
			}
			if data.Refresh != 5 || data.Theme != "dark" || data.NextTheme != "light" {
				t.Errorf("refresh/theme/next-theme = %d/%q/%q, want 5/dark/light", data.Refresh, data.Theme, data.NextTheme)
			}
			if data.Running != tc.running || data.Busy != tc.busy {
				t.Errorf("running/busy = %v/%v, want %v/%v for status %q", data.Running, data.Busy, tc.running, tc.busy, tc.status)
			}
			if data.AgentID != "executor-agent" {
				t.Errorf("AgentID = %q, want %q", data.AgentID, "executor-agent")
			}
			if data.CLIArgs != rawCLIArgs {
				t.Errorf("CLIArgs = %q, want raw dump %q", data.CLIArgs, rawCLIArgs)
			}
			if !data.StartedAt.Equal(startedAt) {
				t.Errorf("StartedAt = %v, want %v", data.StartedAt, startedAt)
			}
			if !data.HasSession {
				t.Error("HasSession = false, want true")
			}
			if want := "2026-11-24 12:30:05"; data.StartedFormatted != want {
				t.Errorf("StartedFormatted = %q, want %q", data.StartedFormatted, want)
			}
			if data.Duration == "" {
				t.Error("Duration empty, want the live elapsed time")
			}
			if want := []string{"docs", "docs/deep"}; !reflect.DeepEqual(data.Subdirectories, want) {
				t.Errorf("Subdirectories = %#v, want %#v", data.Subdirectories, want)
			}
			if data.SubdirectoryHint != "" {
				t.Errorf("SubdirectoryHint = %q, want none", data.SubdirectoryHint)
			}
		})
	}
}

func TestPageDataWithoutSessionShowsEmptyState(t *testing.T) {
	snapshot := executorSnapshot{
		Status:  "idle",
		AgentID: "executor-agent",
		CLIArgs: rawCLIArgs,
	}
	data := pageData(snapshot, url.Values{}, nil, nil)

	if data.HasSession {
		t.Error("HasSession = true, want false before the first run")
	}
	if data.Duration != "" {
		t.Errorf("Duration = %q, want empty before the first run", data.Duration)
	}
	if data.StartedFormatted != "" {
		t.Errorf("StartedFormatted = %q, want empty before the first run", data.StartedFormatted)
	}
	if !data.FinishedAt.IsZero() {
		t.Errorf("FinishedAt = %v, want zero before the first run", data.FinishedAt)
	}
}

func TestPageDataShowsAcceptedSessionBeforeRunStarts(t *testing.T) {
	snapshot := executorSnapshot{
		Status:       "running",
		AgentID:      "executor-agent",
		CLIArgs:      rawCLIArgs,
		Subdirectory: "docs/deep",
	}
	data := pageData(snapshot, url.Values{}, nil, nil)

	if !data.HasSession {
		t.Fatal("HasSession = false, want true once a run is accepted")
	}
	if data.Subdirectory != "docs/deep" || data.CLIArgs != rawCLIArgs {
		t.Errorf("session details = subdirectory %q, CLI args %q; want selected subdirectory and configured args", data.Subdirectory, data.CLIArgs)
	}
	if data.StartedFormatted != "" || data.Duration != "" {
		t.Errorf("start time/duration = %q/%q, want blank until the executor records StartedAt", data.StartedFormatted, data.Duration)
	}
}

func TestSessionDuration(t *testing.T) {
	startedAt := time.Date(2026, 11, 24, 12, 30, 0, 0, time.UTC)
	cases := []struct {
		name     string
		snapshot executorSnapshot
		want     string
	}{
		{name: "before first run", snapshot: executorSnapshot{}, want: ""},
		{
			name:     "finished run",
			snapshot: executorSnapshot{StartedAt: startedAt, FinishedAt: startedAt.Add(3*time.Minute + 24*time.Second)},
			want:     "3m24s",
		},
		{
			name:     "clamps negative elapsed to zero",
			snapshot: executorSnapshot{StartedAt: startedAt, FinishedAt: startedAt.Add(-time.Minute)},
			want:     "0s",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sessionDuration(tc.snapshot); got != tc.want {
				t.Errorf("sessionDuration = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestShowRendersDashboardPage(t *testing.T) {
	app, err := newDashboard(testConfig(t, filepath.Join(t.TempDir(), "missing.sqlite")), slog.LevelInfo, "")
	if err != nil {
		t.Fatalf("newDashboard: %v", err)
	}
	t.Cleanup(app.Close)

	req := httptest.NewRequest("GET", "/?refresh=5&theme=dark", nil)
	rec := httptest.NewRecorder()
	app.show(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if contentType := rec.Header().Get("Content-Type"); contentType != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q, want text/html; charset=utf-8", contentType)
	}
	body, err := io.ReadAll(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Executor dashboard", "idle", "refresh", "dark", "Session summary", "Pending files", "No session yet"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("rendered page missing %q", want)
		}
	}
}

func TestShowRendersSessionSummaryCard(t *testing.T) {
	app, err := newDashboard(testConfig(t, filepath.Join(t.TempDir(), "missing.sqlite")), slog.LevelInfo, "")
	if err != nil {
		t.Fatalf("newDashboard: %v", err)
	}
	t.Cleanup(app.Close)

	if err := app.controller.Start(""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForControllerStatus(t, app.controller, "failed")

	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	app.show(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		"Session summary",
		"Agent ID",
		"executor-agent",
		"CLI args",
		"fileexplorer.sqlite",
		"-root /srv/files",
		"Status",
		"failed",
		"Started",
		"Duration",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("rendered page missing %q", want)
		}
	}
	if strings.Contains(body, "No session yet") {
		t.Error("rendered page shows the idle empty state, want the session values")
	}
}

func waitForControllerStatus(t *testing.T, controller *executorController, want string) executorSnapshot {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		snapshot := controller.Snapshot()
		if snapshot.Status == want {
			return snapshot
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for status %q; last = %+v", want, controller.Snapshot())
	return executorSnapshot{}
}

func TestShowRendersPendingFilesView(t *testing.T) {
	cases := []struct {
		name    string
		entries []sessions.PendingEntry
		want    []string
		unwant  []string
	}{
		{
			name:   "empty pending set",
			want:   []string{"Pending files", "No pending files"},
			unwant: []string{"Carried over", "Newly discovered"},
		},
		{
			name: "partial pending set",
			entries: []sessions.PendingEntry{
				{Path: "carried/report.txt", Carried: true},
				{Path: "fresh/data.txt"},
			},
			want: []string{
				"Pending files", "2 unfinished",
				"carried/report.txt", "Carried over",
				"fresh/data.txt", "Newly discovered",
			},
			unwant: []string{"No pending files"},
		},
		{
			name: "full pending set",
			entries: []sessions.PendingEntry{
				{Path: "carried/report.txt", Carried: true},
				{Path: "fresh/data.txt"},
				{Path: "fresh/notes.txt"},
			},
			want: []string{
				"Pending files", "3 unfinished",
				"carried/report.txt", "Carried over",
				"fresh/data.txt", "Newly discovered",
				"fresh/notes.txt", "Newly discovered",
			},
			unwant: []string{"No pending files"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app, err := newDashboard(testConfig(t, filepath.Join(t.TempDir(), "missing.sqlite")), slog.LevelInfo, "")
			if err != nil {
				t.Fatalf("newDashboard: %v", err)
			}
			t.Cleanup(app.Close)

			if err := app.controller.Start(""); err != nil {
				t.Fatalf("Start: %v", err)
			}
			snapshot := waitForControllerStatus(t, app.controller, "failed")
			if snapshot.SessionID == "" {
				t.Fatal("Start: session ID is empty")
			}
			if err := app.store.SetPendingSet(context.Background(), snapshot.SessionID, tc.entries); err != nil {
				t.Fatalf("SetPendingSet: %v", err)
			}

			req := httptest.NewRequest("GET", "/", nil)
			rec := httptest.NewRecorder()
			app.show(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
			}
			body := rec.Body.String()
			for _, want := range tc.want {
				if !strings.Contains(body, want) {
					t.Errorf("rendered page missing %q", want)
				}
			}
			for _, absent := range tc.unwant {
				if strings.Contains(body, absent) {
					t.Errorf("rendered page unexpectedly contains %q", absent)
				}
			}
		})
	}
}

func TestResumeFailedSessionAfterDashboardRestartUsesSavedConfiguration(t *testing.T) {
	dir := t.TempDir()
	savedDatabase := filepath.Join(dir, "saved-fileexplorer.sqlite")
	createManifestFixture(t, savedDatabase, nil, nil)
	savedRoot := filepath.Join(dir, "saved-root")
	if err := os.MkdirAll(savedRoot, 0o755); err != nil {
		t.Fatalf("create saved root: %v", err)
	}
	storePath := filepath.Join(dir, "task-sessions.sqlite")
	store, err := sessions.OpenStore(storePath)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	saved, err := store.Create(context.Background(), sessions.Record{
		AgentID: "saved-agent", RootDirectory: savedRoot, Subdirectory: "docs",
		DatabaseFile: savedDatabase, Status: sessions.StatusFailed,
		LastError: "gateway unavailable", FinishedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("Create saved session: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close initial store: %v", err)
	}

	refreshServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer refreshServer.Close()
	base := testConfig(t, filepath.Join(dir, "wrong-fileexplorer.sqlite"))
	base.FileExplorerURL = refreshServer.URL
	base.AgentID = "fresh-agent"
	app, err := newDashboard(base, slog.LevelInfo, storePath)
	if err != nil {
		t.Fatalf("newDashboard: %v", err)
	}
	t.Cleanup(app.Close)
	if snapshot := app.controller.Snapshot(); snapshot.Status != "idle" || snapshot.AgentID != "fresh-agent" {
		t.Fatalf("dashboard implicitly resumed saved session: %+v", snapshot)
	}

	form := url.Values{"session_id": {saved.ID}}
	req := httptest.NewRequest("POST", "/start", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	app.start(rec, req)
	if rec.Code != http.StatusSeeOther || strings.Contains(rec.Header().Get("Location"), "error=") {
		t.Fatalf("selected-session response = %d %q, want a successful redirect", rec.Code, rec.Header().Get("Location"))
	}

	snapshot := waitForControllerStatus(t, app.controller, "completed")
	if snapshot.AgentID != "saved-agent" || snapshot.Subdirectory != "docs" {
		t.Errorf("resumed session summary = %+v, want saved agent and subdirectory", snapshot)
	}
	persisted, err := app.store.Get(context.Background(), saved.ID)
	if err != nil {
		t.Fatalf("Get resumed record: %v", err)
	}
	if persisted.Status != sessions.StatusEnded || persisted.AgentID != "saved-agent" ||
		persisted.RootDirectory != savedRoot || persisted.Subdirectory != "docs" || persisted.DatabaseFile != savedDatabase {
		t.Errorf("persisted resumed session = %+v, want ended record with original configuration", persisted)
	}
}

func TestFreshAndResumedSessionsDiscoverFilesAndStopThroughLifecycle(t *testing.T) {
	for _, resumed := range []bool{false, true} {
		name := "fresh"
		if resumed {
			name = "resumed"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			freshRoot := filepath.Join(dir, "fresh-root")
			freshDatabase := filepath.Join(dir, "fresh-fileexplorer.sqlite")
			createIndexedFile(t, freshRoot, freshDatabase, "fresh.txt", "fresh file content")
			if err := os.WriteFile(filepath.Join(freshRoot, "instructions.md"), []byte("Process the indexed file."), 0o600); err != nil {
				t.Fatalf("write fallback instructions: %v", err)
			}
			savedRoot := filepath.Join(dir, "saved-root")
			savedDatabase := filepath.Join(dir, "saved-fileexplorer.sqlite")
			createIndexedFile(t, savedRoot, savedDatabase, "docs/resumed.txt", "resumed file content")

			storePath := filepath.Join(dir, "task-sessions.sqlite")
			var saved sessions.Record
			if resumed {
				store, err := sessions.OpenStore(storePath)
				if err != nil {
					t.Fatalf("OpenStore: %v", err)
				}
				saved, err = store.Create(context.Background(), sessions.Record{
					AgentID: "saved-agent", RootDirectory: savedRoot,
					Subdirectory: "docs", DatabaseFile: savedDatabase,
					Status: sessions.StatusPaused,
				})
				if err != nil {
					t.Fatalf("Create saved session: %v", err)
				}
				if err := store.Close(); err != nil {
					t.Fatalf("Close initial store: %v", err)
				}
			}

			requestInputs := make(chan string, 1)
			gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					w.WriteHeader(http.StatusOK)
					return
				}
				var request struct {
					Input string `json:"input"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Errorf("decode Gateway request: %v", err)
					return
				}
				requestInputs <- request.Input
				<-r.Context().Done()
			}))
			defer gateway.Close()

			config := testConfig(t, freshDatabase)
			config.RootPath = freshRoot
			config.InstructionsPath = filepath.Join(freshRoot, "instructions.md")
			config.AgentID = "fresh-agent"
			config.GatewayURL = gateway.URL
			config.FileExplorerURL = gateway.URL
			config.HTTPClient = gateway.Client()
			app, err := newDashboard(config, slog.LevelInfo, storePath)
			if err != nil {
				t.Fatalf("newDashboard: %v", err)
			}
			t.Cleanup(app.Close)

			if resumed {
				form := url.Values{"session_id": {saved.ID}}
				req := httptest.NewRequest(http.MethodPost, "/resume", strings.NewReader(form.Encode()))
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				rec := httptest.NewRecorder()
				app.ServeHTTP(rec, req)
				if rec.Code != http.StatusSeeOther || strings.Contains(rec.Header().Get("Location"), "error=") {
					t.Fatalf("resume response = %d %q, want a successful redirect", rec.Code, rec.Header().Get("Location"))
				}
			} else if err := app.controller.Start(""); err != nil {
				t.Fatalf("Start fresh session: %v", err)
			}

			var input string
			select {
			case input = <-requestInputs:
			case <-time.After(5 * time.Second):
				t.Fatalf("timed out waiting for a discovered file task to reach the Gateway; snapshot: %+v", app.controller.Snapshot())
			}
			wantContent := "fresh file content"
			otherContent := "resumed file content"
			if resumed {
				wantContent, otherContent = otherContent, wantContent
			}
			if !strings.Contains(input, wantContent) {
				t.Errorf("task input = %q, want it to include %q from the configured file root", input, wantContent)
			}
			if strings.Contains(input, otherContent) {
				t.Errorf("task input = %q, unexpectedly includes content from the other session configuration", input)
			}

			stop := httptest.NewRecorder()
			app.ServeHTTP(stop, httptest.NewRequest(http.MethodPost, "/stop", nil))
			if stop.Code != http.StatusSeeOther || strings.Contains(stop.Header().Get("Location"), "error=") {
				t.Fatalf("stop response = %d %q, want a successful redirect", stop.Code, stop.Header().Get("Location"))
			}
			snapshot := waitForControllerStatus(t, app.controller, "stopped")
			if resumed && (snapshot.AgentID != "saved-agent" || snapshot.Subdirectory != "docs") {
				t.Errorf("resumed snapshot = %+v, want saved agent and subdirectory", snapshot)
			}

			persisted, err := app.store.Get(context.Background(), snapshot.SessionID)
			if err != nil {
				t.Fatalf("Get stopped session: %v", err)
			}
			if persisted.Status != sessions.StatusPaused {
				t.Errorf("stopped session status = %q, want paused", persisted.Status)
			}
			wantRoot, wantDatabase, wantAgent, wantSubdirectory := freshRoot, freshDatabase, "fresh-agent", ""
			if resumed {
				wantRoot, wantDatabase, wantAgent, wantSubdirectory = savedRoot, savedDatabase, "saved-agent", "docs"
			}
			if persisted.RootDirectory != wantRoot || persisted.DatabaseFile != wantDatabase ||
				persisted.AgentID != wantAgent || persisted.Subdirectory != wantSubdirectory {
				t.Errorf("persisted stopped session = %+v, want agent/root/subdirectory/database %q/%q/%q/%q",
					persisted, wantAgent, wantRoot, wantSubdirectory, wantDatabase)
			}
		})
	}
}

func createIndexedFile(t *testing.T, root, databasePath, relativePath, content string) {
	t.Helper()
	filePath := filepath.Join(root, filepath.FromSlash(relativePath))
	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		t.Fatalf("create file directory: %v", err)
	}
	if err := os.WriteFile(filePath, []byte(content), 0o600); err != nil {
		t.Fatalf("write indexed file: %v", err)
	}
	createManifestFixture(t, databasePath, []string{filepath.ToSlash(relativePath)}, nil)
}

func postStart(t *testing.T, app *dashboard, subdirectory string) *httptest.ResponseRecorder {
	t.Helper()
	form := url.Values{}
	if subdirectory != "" {
		form.Set("subdirectory", subdirectory)
	}
	req := httptest.NewRequest("POST", "/start", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	app.start(rec, req)
	return rec
}

func redirectLocation(t *testing.T, rec *httptest.ResponseRecorder) url.Values {
	t.Helper()
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusSeeOther)
	}
	location := rec.Header().Get("Location")
	parsed, err := url.Parse(location)
	if err != nil {
		t.Fatalf("Location %q is not a URL: %v", location, err)
	}
	return parsed.Query()
}

func TestStartRejectsUnknownSubdirectory(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "fileexplorer.sqlite")
	createManifestFixture(t, databasePath,
		[]string{"root.txt", "docs/notes.md"},
		[]string{"docs"},
	)
	app, err := newDashboard(testConfig(t, databasePath), slog.LevelInfo, "")
	if err != nil {
		t.Fatalf("newDashboard: %v", err)
	}
	t.Cleanup(app.Close)

	query := redirectLocation(t, postStart(t, app, "does-not-exist"))
	if got := query.Get("error"); !strings.Contains(got, "not in the file list") {
		t.Errorf("redirect error = %q, want it to say the subdirectory is not in the file list", got)
	}
	if snapshot := app.controller.Snapshot(); snapshot.Status != "idle" {
		t.Errorf("status = %q, want idle: a rejected selection must not start a run", snapshot.Status)
	}
}

func TestStartRejectsSubdirectoryWhenManifestUnavailable(t *testing.T) {
	app, err := newDashboard(testConfig(t, filepath.Join(t.TempDir(), "missing.sqlite")), slog.LevelInfo, "")
	if err != nil {
		t.Fatalf("newDashboard: %v", err)
	}
	t.Cleanup(app.Close)

	query := redirectLocation(t, postStart(t, app, "docs"))
	if got := query.Get("error"); !strings.Contains(got, "file list is unavailable") {
		t.Errorf("redirect error = %q, want the unavailable-file-list message", got)
	}
	if snapshot := app.controller.Snapshot(); snapshot.Status != "idle" {
		t.Errorf("status = %q, want idle: an unverifiable selection must not start a run", snapshot.Status)
	}
}

func TestStartAppliesSelectedSubdirectory(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "fileexplorer.sqlite")
	createManifestFixture(t, databasePath,
		[]string{"root.txt", "docs/notes.md", "docs/deep/page.md"},
		[]string{"docs", "docs/deep"},
	)
	app, err := newDashboard(testConfig(t, databasePath), slog.LevelInfo, "")
	if err != nil {
		t.Fatalf("newDashboard: %v", err)
	}
	t.Cleanup(app.Close)

	query := redirectLocation(t, postStart(t, app, "docs/deep"))
	if got := query.Get("error"); got != "" {
		t.Fatalf("redirect error = %q, want none for a manifest-listed subdirectory", got)
	}

	snapshot := app.controller.Snapshot()
	if snapshot.Status != "running" {
		t.Fatalf("status = %q, want running after a valid start", snapshot.Status)
	}
	if snapshot.Subdirectory != "docs/deep" {
		t.Errorf("snapshot Subdirectory = %q, want the selection %q", snapshot.Subdirectory, "docs/deep")
	}
	if !strings.HasSuffix(snapshot.CLIArgs, " -subdirectory docs/deep") {
		t.Errorf("snapshot CLIArgs = %q, want it to carry the -subdirectory flag for the run", snapshot.CLIArgs)
	}

	rec := httptest.NewRecorder()
	app.show(rec, httptest.NewRequest("GET", "/", nil))
	body := rec.Body.String()
	if !strings.Contains(body, `aria-label="Subdirectory: docs/deep"`) {
		t.Error("rendered session summary missing the applied subdirectory")
	}
	if !strings.Contains(body, " -subdirectory docs/deep") {
		t.Error("rendered CLI args missing the applied -subdirectory flag")
	}
}

func TestStartEmptySelectionKeepsWholeRoot(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "fileexplorer.sqlite")
	createManifestFixture(t, databasePath,
		[]string{"root.txt", "docs/notes.md"},
		[]string{"docs"},
	)
	app, err := newDashboard(testConfig(t, databasePath), slog.LevelInfo, "")
	if err != nil {
		t.Fatalf("newDashboard: %v", err)
	}
	t.Cleanup(app.Close)

	query := redirectLocation(t, postStart(t, app, ""))
	if got := query.Get("error"); got != "" {
		t.Fatalf("redirect error = %q, want none for the root selection", got)
	}

	snapshot := app.controller.Snapshot()
	if snapshot.Status != "running" {
		t.Fatalf("status = %q, want running after a valid start", snapshot.Status)
	}
	if snapshot.Subdirectory != "" {
		t.Errorf("snapshot Subdirectory = %q, want empty for the whole-root behavior", snapshot.Subdirectory)
	}
	if strings.Contains(snapshot.CLIArgs, "-subdirectory") {
		t.Errorf("snapshot CLIArgs = %q, must not gain a -subdirectory flag for the whole root", snapshot.CLIArgs)
	}
}

func TestStartWhileRunningIsRejected(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "fileexplorer.sqlite")
	rootPath := t.TempDir()
	createIndexedFile(t, rootPath, databasePath, "docs/notes.md", "Process the indexed file.")
	if err := os.WriteFile(filepath.Join(rootPath, "instructions.md"), []byte("Process the indexed file."), 0o600); err != nil {
		t.Fatalf("write instructions: %v", err)
	}

	requestStarted := make(chan struct{}, 1)
	releaseRequest := make(chan struct{})
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusOK)
			return
		}
		requestStarted <- struct{}{}
		<-releaseRequest
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"response-1","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"<complete />"}]}]}`)
	}))
	config := testConfig(t, databasePath)
	config.RootPath = rootPath
	config.InstructionsPath = filepath.Join(rootPath, "instructions.md")
	config.GatewayURL = gateway.URL
	config.FileExplorerURL = gateway.URL
	config.HTTPClient = gateway.Client()
	app, err := newDashboard(config, slog.LevelInfo, "")
	if err != nil {
		gateway.Close()
		t.Fatalf("newDashboard: %v", err)
	}
	t.Cleanup(func() {
		close(releaseRequest)
		app.Close()
		gateway.Close()
	})

	if err := app.controller.Start(""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	select {
	case <-requestStarted:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for the first run to reach the Gateway; snapshot: %+v", app.controller.Snapshot())
	}

	query := redirectLocation(t, postStart(t, app, "docs"))
	if got := query.Get("error"); !strings.Contains(got, "already running") {
		t.Errorf("redirect error = %q, want the already-running rejection", got)
	}
	if snapshot := app.controller.Snapshot(); snapshot.Status != "running" {
		t.Errorf("status after rejected start = %q, want running while the first Gateway request is blocked", snapshot.Status)
	}
}

func createManifestFixture(t *testing.T, databasePath string, files []string, directories []string) {
	t.Helper()

	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := db.Exec("CREATE TABLE files (path TEXT PRIMARY KEY, name TEXT NOT NULL, is_dir INTEGER NOT NULL DEFAULT 0, size INTEGER NOT NULL DEFAULT 0, mtime INTEGER NOT NULL DEFAULT 0)"); err != nil {
		t.Fatal(err)
	}
	for _, directory := range directories {
		if _, err := db.Exec("INSERT INTO files (path, name, is_dir, size, mtime) VALUES (?, ?, 1, 0, 0)", directory, filepath.Base(directory)); err != nil {
			t.Fatal(err)
		}
	}
	for _, file := range files {
		if _, err := db.Exec("INSERT INTO files (path, name, is_dir, size, mtime) VALUES (?, ?, 0, 1, 1)", file, filepath.Base(file)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestShowRendersSavedSessionsWithFreshChoiceSelected(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "fileexplorer.sqlite")
	createManifestFixture(t, databasePath, nil, nil)
	app, err := newDashboard(testConfig(t, databasePath), slog.LevelInfo, "")
	if err != nil {
		t.Fatalf("newDashboard: %v", err)
	}
	t.Cleanup(app.Close)

	for _, record := range []sessions.Record{
		{ID: "paused-session", AgentID: "agent-paused", RootDirectory: "/saved/files", Subdirectory: "docs", DatabaseFile: "/saved/index.sqlite", Status: sessions.StatusPaused},
		{ID: "active-session", AgentID: "agent-active", RootDirectory: "/active/files", DatabaseFile: "/active/index.sqlite", Status: sessions.StatusActive},
	} {
		if _, err := app.store.Create(context.Background(), record); err != nil {
			t.Fatalf("Create saved session %q: %v", record.ID, err)
		}
	}

	rec := httptest.NewRecorder()
	app.show(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("show status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		`<link href="/static/bootstrap.min.css" rel="stylesheet">`,
		`id="session-fresh" type="radio" name="session_id" value="" checked`,
		`value="paused-session"`, `agent-paused`, `paused`, `/docs`,
		`value="active-session"`, `agent-active`, `active`,
		`id="saved-session-active-session" type="radio" name="session_id" value="active-session" disabled`,
		`col-12 col-md-6`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("rendered page missing %q", want)
		}
	}
	if !strings.Contains(body, `name="confirm" value="yes" form="delete-session-paused-session" required`) {
		t.Error("rendered page missing explicit saved-history deletion confirmation")
	}
	if strings.Contains(body, `id="confirm-delete-active-session"`) {
		t.Error("active session must not offer a delete confirmation")
	}
	if strings.Contains(strings.ToLower(body), "<script") {
		t.Error("rendered selector must not include client-side JavaScript")
	}
}

func TestDeleteSessionRequiresConfirmationAndRemovesHistory(t *testing.T) {
	app, err := newDashboard(testConfig(t, filepath.Join(t.TempDir(), "fileexplorer.sqlite")), slog.LevelInfo, "")
	if err != nil {
		t.Fatalf("newDashboard: %v", err)
	}
	t.Cleanup(app.Close)

	saved, err := app.store.Create(context.Background(), sessions.Record{
		ID: "delete-me", AgentID: "saved-agent", RootDirectory: "/saved/root",
		DatabaseFile: "/saved/index.sqlite", Status: sessions.StatusPaused,
	})
	if err != nil {
		t.Fatalf("Create saved session: %v", err)
	}

	unconfirmed := url.Values{"session_id": {saved.ID}}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/sessions/delete", strings.NewReader(unconfirmed.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	app.ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther || !strings.Contains(response.Header().Get("Location"), "confirm+permanent+deletion") {
		t.Fatalf("unconfirmed delete response = %d %q, want confirmation error redirect", response.Code, response.Header().Get("Location"))
	}
	if _, err := app.store.Get(context.Background(), saved.ID); err != nil {
		t.Fatalf("unconfirmed deletion removed the record: %v", err)
	}

	missingID := url.Values{"confirm": {"yes"}}
	response = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/sessions/delete", strings.NewReader(missingID.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	app.ServeHTTP(response, request)
	if got := redirectLocation(t, response).Get("error"); !strings.Contains(got, "session ID is required") {
		t.Errorf("missing-ID delete error = %q, want ID validation", got)
	}

	active, err := app.store.Create(context.Background(), sessions.Record{
		ID: "active-session", AgentID: "active-agent", RootDirectory: "/active/root",
		DatabaseFile: "/active/index.sqlite", Status: sessions.StatusActive,
	})
	if err != nil {
		t.Fatalf("Create active session: %v", err)
	}
	activeForm := url.Values{"session_id": {active.ID}, "confirm": {"yes"}}
	response = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/sessions/delete", strings.NewReader(activeForm.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	app.ServeHTTP(response, request)
	if got := redirectLocation(t, response).Get("error"); !strings.Contains(got, "still active") {
		t.Errorf("active-session delete error = %q, want active-session validation", got)
	}
	if _, err := app.store.Get(context.Background(), active.ID); err != nil {
		t.Errorf("active session was deleted: %v", err)
	}

	confirmed := url.Values{"session_id": {saved.ID}, "confirm": {"yes"}}
	response = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/sessions/delete?refresh=5&theme=dark", strings.NewReader(confirmed.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	app.ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther || !strings.Contains(response.Header().Get("Location"), "notice=session_deleted") {
		t.Fatalf("confirmed delete response = %d %q, want successful redirect", response.Code, response.Header().Get("Location"))
	}
	if _, err := app.store.Get(context.Background(), saved.ID); !errors.Is(err, sessions.ErrNotFound) {
		t.Errorf("Get deleted session error = %v, want sessions.ErrNotFound", err)
	}
	page := httptest.NewRecorder()
	app.ServeHTTP(page, httptest.NewRequest(http.MethodGet, response.Header().Get("Location"), nil))
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "Saved session history deleted.") {
		t.Errorf("dashboard after deletion = %d; body missing deletion notice", page.Code)
	}
	if strings.Contains(page.Body.String(), "delete-me") {
		t.Error("deleted session remains available in dashboard history")
	}
	resumeForm := url.Values{"session_id": {saved.ID}}
	resumeResponse := httptest.NewRecorder()
	resumeRequest := httptest.NewRequest(http.MethodPost, "/start", strings.NewReader(resumeForm.Encode()))
	resumeRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	app.ServeHTTP(resumeResponse, resumeRequest)
	if got := redirectLocation(t, resumeResponse).Get("error"); !strings.Contains(got, "task session not found") {
		t.Errorf("resume deleted session error = %q, want missing-session rejection", got)
	}
}

func TestShowRendersSubdirectorySelectFromManifest(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "fileexplorer.sqlite")
	createManifestFixture(t, databasePath,
		[]string{"root.txt", "docs/notes.md", "docs/deep/page.md", "misc/file.txt"},
		[]string{"docs", "docs/deep"},
	)
	app, err := newDashboard(testConfig(t, databasePath), slog.LevelInfo, "")
	if err != nil {
		t.Fatalf("newDashboard: %v", err)
	}

	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	app.show(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	body := rec.Body.String()

	for _, want := range []string{
		`<select class="form-select" id="subdirectory" name="subdirectory">`,
		`<option value="" selected>All files (root)</option>`,
		`<option value="docs">docs</option>`,
		`<option value="docs/deep">docs/deep</option>`,
		`<option value="misc">misc</option>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("rendered page missing %q", want)
		}
	}
	if strings.Contains(body, "The subdirectory list is unavailable") {
		t.Error("degradation hint rendered, want none")
	}

	startForm := strings.Index(body, `action="/start`)
	if startForm == -1 {
		t.Fatal("start form missing from the page")
	}
	rest := body[startForm:]
	selectIdx := strings.Index(rest, `name="subdirectory"`)
	formClose := strings.Index(rest, "</form>")
	if selectIdx == -1 || formClose == -1 || selectIdx > formClose {
		t.Fatalf("select not inside the /start form (select=%d close=%d)", selectIdx, formClose)
	}

	if want := `<option value="" selected>All files (root)</option>`; strings.Index(body, want) != strings.Index(body, "<option value=") {
		t.Error("first option is not the selected root option")
	}
}

func TestShowRefreshesSubdirectorySelectFromManifestOnEveryRequest(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "fileexplorer.sqlite")
	createManifestFixture(t, databasePath,
		[]string{"docs/notes.md"},
		[]string{"docs"},
	)
	app, err := newDashboard(testConfig(t, databasePath), slog.LevelInfo, "")
	if err != nil {
		t.Fatalf("newDashboard: %v", err)
	}
	t.Cleanup(app.Close)

	render := func() string {
		t.Helper()
		rec := httptest.NewRecorder()
		app.show(rec, httptest.NewRequest("GET", "/?refresh=2", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("show status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
		}
		return rec.Body.String()
	}

	first := render()
	if !strings.Contains(first, `name="subdirectory"`) || !strings.Contains(first, `<option value="docs">docs</option>`) {
		t.Fatalf("first refreshed page did not populate the subdirectory control: %s", first)
	}
	if strings.Contains(first, `<option value="archive">archive</option>`) {
		t.Fatal("first page unexpectedly contains a directory not yet in the manifest")
	}

	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO files (path, name, is_dir, size, mtime) VALUES (?, ?, 1, 0, 0)", "archive", "archive"); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO files (path, name, is_dir, size, mtime) VALUES (?, ?, 0, 1, 1)", "archive/readme.md", "readme.md"); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	second := render()
	if !strings.Contains(second, `name="subdirectory"`) || !strings.Contains(second, `<option value="docs">docs</option>`) || !strings.Contains(second, `<option value="archive">archive</option>`) {
		t.Fatalf("subdirectory control was not refreshed from the changed manifest: %s", second)
	}
}

func TestShowRendersRootOnlyWhenManifestCannotBeRead(t *testing.T) {
	app, err := newDashboard(testConfig(t, filepath.Join(t.TempDir(), "missing.sqlite")), slog.LevelInfo, "")
	if err != nil {
		t.Fatalf("newDashboard: %v", err)
	}

	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	app.show(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	body := rec.Body.String()

	if !strings.Contains(body, `name="subdirectory"`) {
		t.Error("subdirectory select missing")
	}
	if !strings.Contains(body, `<option value="" selected>All files (root)</option>`) {
		t.Error("root option missing")
	}
	if strings.Contains(body, `<option value="docs"`) {
		t.Error("extra option rendered without a readable manifest")
	}
	if !strings.Contains(body, "The subdirectory list is unavailable") {
		t.Error("degradation hint missing")
	}
}
