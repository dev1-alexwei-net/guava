package main

import (
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dev1-alexwei-net/guava/executor"
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
	controller, err := newExecutorController(testConfig(t, databasePath), slog.LevelInfo)
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
	app, err := newDashboard(testConfig(t, filepath.Join(t.TempDir(), "missing.sqlite")), slog.LevelInfo)
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
	for _, want := range []string{"Executor dashboard", "idle", "refresh", "dark", "Session summary", "No session yet"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("rendered page missing %q", want)
		}
	}
}

func TestShowRendersSessionSummaryCard(t *testing.T) {
	app, err := newDashboard(testConfig(t, filepath.Join(t.TempDir(), "missing.sqlite")), slog.LevelInfo)
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

func TestPageDataCarriesSubdirectorySelection(t *testing.T) {
	snapshot := executorSnapshot{Status: "completed", Subdirectory: "docs/deep"}
	data := pageData(snapshot, url.Values{}, nil, nil)

	if data.Subdirectory != "docs/deep" {
		t.Errorf("Subdirectory = %q, want %q", data.Subdirectory, "docs/deep")
	}
	snapshot = executorSnapshot{Status: "completed"}
	if got := pageData(snapshot, url.Values{}, nil, nil).Subdirectory; got != "" {
		t.Errorf("Subdirectory = %q, want empty for the whole root", got)
	}
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
	app, err := newDashboard(testConfig(t, databasePath), slog.LevelInfo)
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
	app, err := newDashboard(testConfig(t, filepath.Join(t.TempDir(), "missing.sqlite")), slog.LevelInfo)
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
	app, err := newDashboard(testConfig(t, databasePath), slog.LevelInfo)
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
	app, err := newDashboard(testConfig(t, databasePath), slog.LevelInfo)
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
	createManifestFixture(t, databasePath,
		[]string{"root.txt", "docs/notes.md"},
		[]string{"docs"},
	)
	app, err := newDashboard(testConfig(t, databasePath), slog.LevelInfo)
	if err != nil {
		t.Fatalf("newDashboard: %v", err)
	}
	t.Cleanup(app.Close)

	if err := app.controller.Start(""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	query := redirectLocation(t, postStart(t, app, "docs"))
	if got := query.Get("error"); !strings.Contains(got, "already running") {
		t.Errorf("redirect error = %q, want the already-running rejection", got)
	}
}

func TestPageDataSurfacesSubdirectoryDegradation(t *testing.T) {
	data := pageData(executorSnapshot{}, url.Values{}, nil, errors.New("manifest unreadable"))

	if data.Subdirectories != nil {
		t.Fatalf("Subdirectories = %#v, want nil when the manifest cannot be read", data.Subdirectories)
	}
	if data.SubdirectoryHint == "" {
		t.Error("SubdirectoryHint empty, want the degradation note")
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

func TestShowRendersSubdirectorySelectFromManifest(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "fileexplorer.sqlite")
	createManifestFixture(t, databasePath,
		[]string{"root.txt", "docs/notes.md", "docs/deep/page.md", "misc/file.txt"},
		[]string{"docs", "docs/deep"},
	)
	app, err := newDashboard(testConfig(t, databasePath), slog.LevelInfo)
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

func TestShowRendersRootOnlyWhenManifestCannotBeRead(t *testing.T) {
	app, err := newDashboard(testConfig(t, filepath.Join(t.TempDir(), "missing.sqlite")), slog.LevelInfo)
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
