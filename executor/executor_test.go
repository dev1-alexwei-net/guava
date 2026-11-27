package executor

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/dev1-alexwei-net/guava/conversations"
)

const testCompletionMarker = "<complete />"

type testResponsesAPIResponse struct {
	ID     string                    `json:"id"`
	Output []testResponsesOutputItem `json:"output"`
}

type testResponsesOutputItem struct {
	Type    string                     `json:"type"`
	Role    string                     `json:"role"`
	Content []testResponsesContentPart `json:"content"`
}

type testResponsesContentPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// indexedFile is a test fixture shape for building executor integration-test
// manifests; production file-index ownership resides in package tasks.
type indexedFile struct {
	path  string
	name  string
	size  int64
	mtime int64
}

func createIndexFixture(t *testing.T, databasePath string, entries []indexedFile, directories []string) {
	t.Helper()

	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	_, err = db.Exec("CREATE TABLE files (path TEXT PRIMARY KEY, name TEXT NOT NULL, is_dir INTEGER NOT NULL DEFAULT 0, size INTEGER NOT NULL DEFAULT 0, mtime INTEGER NOT NULL DEFAULT 0)")
	if err != nil {
		t.Fatal(err)
	}
	for _, directory := range directories {
		if _, err := db.Exec(
			"INSERT INTO files (path, name, is_dir, size, mtime) VALUES (?, ?, 1, 0, 0)",
			directory, filepath.Base(directory)); err != nil {
			t.Fatal(err)
		}
	}
	for _, file := range entries {
		if _, err := db.Exec(
			"INSERT INTO files (path, name, is_dir, size, mtime) VALUES (?, ?, 0, ?, ?)",
			file.path, file.name, file.size, file.mtime); err != nil {
			t.Fatal(err)
		}
	}
}

// rawCLIArgs models the raw dump of a server command line, including an
// argument with spaces, to prove the summary carries it verbatim.
const rawCLIArgs = `guava-executor -database "/var/lib/fileexplorer.sqlite" -root /srv/files -agent-id executor-agent`

func TestSessionSummaryBeforeFirstRun(t *testing.T) {
	config := validConfig()
	config.AgentID = "executor-agent"
	config.CLIArgs = rawCLIArgs
	runner, err := New(config)
	if err != nil {
		t.Fatal(err)
	}

	summary := runner.SessionSummary()
	if summary.AgentID != "executor-agent" {
		t.Errorf("AgentID = %q, want %q", summary.AgentID, "executor-agent")
	}
	if summary.CLIArgs != rawCLIArgs {
		t.Errorf("CLIArgs = %q, want raw dump %q", summary.CLIArgs, rawCLIArgs)
	}
	if summary.Status != conversations.StatusIdle {
		t.Errorf("Status = %q, want %q", summary.Status, conversations.StatusIdle)
	}
	if !summary.StartedAt.IsZero() {
		t.Errorf("StartedAt = %v, want zero before the first run", summary.StartedAt)
	}
}

func TestSessionSummaryNilExecutor(t *testing.T) {
	var runner *Executor
	summary := runner.SessionSummary()
	if summary.Status != conversations.StatusIdle || !summary.StartedAt.IsZero() {
		t.Errorf("nil Executor SessionSummary = %+v, want idle with zero StartedAt", summary)
	}
}

func TestSessionSummaryDuringAndAfterCanceledRun(t *testing.T) {
	rootPath := t.TempDir()
	if err := os.WriteFile(filepath.Join(rootPath, "a.txt"), []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	databasePath := filepath.Join(t.TempDir(), "fileexplorer.sqlite")
	createIndexFixture(t, databasePath, []indexedFile{{path: "a.txt", name: "a.txt"}}, nil)
	instructionsPath := filepath.Join(t.TempDir(), "instructions.md")
	if err := os.WriteFile(instructionsPath, []byte("instructions"), 0o600); err != nil {
		t.Fatal(err)
	}

	// A refresh GET that blocks until the request context is canceled keeps
	// the run in the running state for the duration of the test.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			<-r.Context().Done()
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	config := validConfig()
	config.DatabasePath = databasePath
	config.RootPath = rootPath
	config.InstructionsPath = instructionsPath
	config.GatewayURL = server.URL
	config.FileExplorerURL = server.URL
	config.AgentID = "executor-agent"
	config.CLIArgs = rawCLIArgs
	config.HTTPClient = server.Client()
	runner, err := New(config)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() {
		runDone <- runner.Run(ctx)
	}()

	summary := waitForSummaryStatus(t, runner, conversations.StatusRunning)
	if summary.AgentID != "executor-agent" {
		t.Errorf("during run AgentID = %q, want %q", summary.AgentID, "executor-agent")
	}
	if summary.CLIArgs != rawCLIArgs {
		t.Errorf("during run CLIArgs = %q, want raw dump %q", summary.CLIArgs, rawCLIArgs)
	}
	if summary.StartedAt.IsZero() {
		t.Error("during run StartedAt = zero, want the run start time")
	}

	// The accessor must stay safe to call from many goroutines while the run
	// is active.
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				if got := runner.SessionSummary(); got.AgentID != "executor-agent" || got.CLIArgs != rawCLIArgs {
					t.Errorf("concurrent SessionSummary = %+v, want agent-id and CLI-args unchanged", got)
					return
				}
			}
		}()
	}
	cancel()
	wg.Wait()

	runErr := <-runDone
	if !errors.Is(runErr, context.Canceled) {
		t.Fatalf("Run() error = %v, want context.Canceled", runErr)
	}
	summary = runner.SessionSummary()
	if summary.Status != conversations.StatusStopped {
		t.Errorf("after canceled run Status = %q, want %q", summary.Status, conversations.StatusStopped)
	}
	if summary.StartedAt.IsZero() {
		t.Error("after canceled run StartedAt = zero, want the run start time")
	}
}

func TestSessionSummaryAfterCompletedRun(t *testing.T) {
	rootPath := t.TempDir()
	if err := os.WriteFile(filepath.Join(rootPath, "a.txt"), []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	databasePath := filepath.Join(t.TempDir(), "fileexplorer.sqlite")
	createIndexFixture(t, databasePath, []indexedFile{{path: "a.txt", name: "a.txt"}, {path: "instructions.md", name: "instructions.md"}}, nil)
	instructionsPath := filepath.Join(rootPath, "instructions.md")
	if err := os.WriteFile(instructionsPath, []byte("instructions"), 0o600); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusOK)
			return
		}
		var parts []string = []string{testCompletionMarker}
		content := make([]testResponsesContentPart, 0, len(parts))
		for _, part := range parts {
			content = append(content, testResponsesContentPart{Type: "output_text", Text: part})
		}
		response := testResponsesAPIResponse{
			ID:     "resp_1",
			Output: []testResponsesOutputItem{{Type: "message", Role: "assistant", Content: content}},
		}
		if err := json.NewEncoder(w).Encode(response); err != nil {
			t.Errorf("encode response: %v", err)
		}
	}))
	defer server.Close()

	config := validConfig()
	config.DatabasePath = databasePath
	config.RootPath = rootPath
	config.InstructionsPath = instructionsPath
	config.GatewayURL = server.URL
	config.FileExplorerURL = server.URL
	config.AgentID = "executor-agent"
	config.CLIArgs = rawCLIArgs
	config.HTTPClient = server.Client()
	runner, err := New(config)
	if err != nil {
		t.Fatal(err)
	}

	if err := runner.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	summary := runner.SessionSummary()
	if summary.Status != conversations.StatusCompleted {
		t.Errorf("Status = %q, want %q", summary.Status, conversations.StatusCompleted)
	}
	if summary.StartedAt.IsZero() {
		t.Error("StartedAt = zero, want the run start time")
	}
	if summary.AgentID != "executor-agent" || summary.CLIArgs != rawCLIArgs {
		t.Errorf("after completed run summary = %+v, want agent-id and CLI-args preserved", summary)
	}

	firstStartedAt := summary.StartedAt
	if err := runner.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	second := runner.SessionSummary()
	if second.Status != conversations.StatusCompleted {
		t.Errorf("second run Status = %q, want %q", second.Status, conversations.StatusCompleted)
	}
	if second.StartedAt.Before(firstStartedAt) {
		t.Errorf("second run StartedAt = %v, want at or after the first run's %v", second.StartedAt, firstStartedAt)
	}
}

func TestSessionSummaryAfterFailedRun(t *testing.T) {
	rootPath := t.TempDir()
	if err := os.WriteFile(filepath.Join(rootPath, "a.txt"), []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	databasePath := filepath.Join(t.TempDir(), "fileexplorer.sqlite")
	createIndexFixture(t, databasePath, []indexedFile{{path: "a.txt", name: "a.txt"}}, nil)
	// Use a task prompt failure here; OpenResponses HTTP errors are retried by design.
	if err := os.Remove(filepath.Join(rootPath, "a.txt")); err != nil {
		t.Fatal(err)
	}
	instructionsPath := filepath.Join(t.TempDir(), "instructions.md")
	if err := os.WriteFile(instructionsPath, []byte("instructions"), 0o600); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":{"message":"denied"}}`)
	}))
	defer server.Close()

	config := validConfig()
	config.DatabasePath = databasePath
	config.RootPath = rootPath
	config.InstructionsPath = instructionsPath
	config.GatewayURL = server.URL
	config.FileExplorerURL = server.URL
	config.AgentID = "executor-agent"
	config.CLIArgs = rawCLIArgs
	config.HTTPClient = server.Client()
	runner, err := New(config)
	if err != nil {
		t.Fatal(err)
	}

	if err := runner.Run(context.Background()); err == nil {
		t.Fatal("Run() succeeded, want a task prompt failure")
	}
	summary := runner.SessionSummary()
	if summary.Status != conversations.StatusFailed {
		t.Errorf("Status = %q, want %q", summary.Status, conversations.StatusFailed)
	}
	if summary.StartedAt.IsZero() {
		t.Error("StartedAt = zero, want the run start time")
	}
}

func TestSessionSummaryStaysIdleWhenRunNeverStarts(t *testing.T) {
	runner, err := New(validConfig())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := runner.Run(ctx); err != context.Canceled {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}
	summary := runner.SessionSummary()
	if summary.Status != conversations.StatusIdle || !summary.StartedAt.IsZero() {
		t.Errorf("summary = %+v, want idle with zero StartedAt", summary)
	}
}

func waitForSummaryStatus(t *testing.T, runner *Executor, want string) conversations.Summary {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if summary := runner.SessionSummary(); summary.Status == want {
			return summary
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for status %q; last = %+v", want, runner.SessionSummary())
	return conversations.Summary{}
}
