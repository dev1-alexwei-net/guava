package executor

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
)

type testResponsesRequest struct {
	Input              string `json:"input"`
	PreviousResponseID string `json:"previous_response_id"`
}

func TestExecutorRunProcessesFilesSequentially(t *testing.T) {
	rootPath := t.TempDir()
	if err := os.WriteFile(filepath.Join(rootPath, "zeta.txt"), []byte("zeta content"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(rootPath, "folder"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootPath, "folder", "alpha.txt"), []byte("alpha content"), 0o600); err != nil {
		t.Fatal(err)
	}

	databasePath := filepath.Join(t.TempDir(), "fileexplorer.sqlite")
	createIndexFixture(t, databasePath,
		[]indexedFile{
			{path: "zeta.txt", name: "zeta.txt", size: 12},
			{path: "folder/alpha.txt", name: "alpha.txt", size: 13},
		},
		[]string{"folder"},
	)
	instructionsPath := filepath.Join(t.TempDir(), "instructions.md")
	const instructions = "Process every file carefully."
	if err := os.WriteFile(instructionsPath, []byte(instructions), 0o600); err != nil {
		t.Fatal(err)
	}

	var requests []testResponsesRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusOK)
			return
		}
		var request testResponsesRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		requests = append(requests, request)
		response := testResponsesAPIResponse{
			ID:     fmt.Sprintf("resp_%d", len(requests)),
			Output: testAssistantText(testCompletionMarker),
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
	config.HTTPClient = server.Client()
	executor, err := New(config)
	if err != nil {
		t.Fatal(err)
	}

	if err := executor.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 {
		t.Fatalf("got %d requests, want 2", len(requests))
	}
	if strings.Index(requests[0].Input, "folder/alpha.txt") == -1 || strings.Index(requests[1].Input, "zeta.txt") == -1 {
		t.Fatalf("requests were not in stable path order: %#v", requests)
	}
	if !strings.Contains(requests[0].Input, instructions) || !strings.Contains(requests[0].Input, "alpha content") {
		t.Errorf("first input did not include instructions and current file content: %q", requests[0].Input)
	}
	if !strings.Contains(requests[1].Input, instructions) || !strings.Contains(requests[1].Input, "zeta content") {
		t.Errorf("second input did not include instructions and current file content: %q", requests[1].Input)
	}
	for i, request := range requests {
		if request.PreviousResponseID != "" {
			t.Errorf("request %d previous_response_id = %q, want empty for a new file conversation", i, request.PreviousResponseID)
		}
	}
}

func TestExecutorRunScopesToSubdirectory(t *testing.T) {
	rootPath := t.TempDir()
	for rel, content := range map[string]string{
		"zeta.txt":          "root level content",
		"folder/alpha.txt":  "alpha content",
		"folder/beta.txt":   "beta content",
		"folder2/other.txt": "sibling folder content",
	} {
		directory := filepath.Dir(filepath.Join(rootPath, rel))
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(rootPath, rel), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	databasePath := filepath.Join(t.TempDir(), "fileexplorer.sqlite")
	createIndexFixture(t, databasePath,
		[]indexedFile{
			{path: "zeta.txt", name: "zeta.txt", size: 20},
			{path: "folder/alpha.txt", name: "alpha.txt", size: 13},
			{path: "folder/beta.txt", name: "beta.txt", size: 12},
			{path: "folder2/other.txt", name: "other.txt", size: 22},
			{path: "instructions.md", name: "instructions.md", size: 27},
		},
		[]string{"folder", "folder2"},
	)
	// The indexed instructions file lives at the root, outside the scoped subdirectory.
	const indexedInstructions = "Use the indexed root instructions."
	if err := os.WriteFile(filepath.Join(rootPath, "instructions.md"), []byte(indexedInstructions), 0o600); err != nil {
		t.Fatal(err)
	}
	fallbackPath := filepath.Join(t.TempDir(), "fallback-instructions.md")
	const fallbackInstructions = "Use the fallback instructions."
	if err := os.WriteFile(fallbackPath, []byte(fallbackInstructions), 0o600); err != nil {
		t.Fatal(err)
	}

	var requests []testResponsesRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusOK)
			return
		}
		var request testResponsesRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		requests = append(requests, request)
		response := testResponsesAPIResponse{
			ID:     fmt.Sprintf("resp_%d", len(requests)),
			Output: testAssistantText(testCompletionMarker),
		}
		if err := json.NewEncoder(w).Encode(response); err != nil {
			t.Errorf("encode response: %v", err)
		}
	}))
	defer server.Close()

	config := validConfig()
	config.DatabasePath = databasePath
	config.RootPath = rootPath
	config.Subdirectory = "folder/"
	config.InstructionsPath = fallbackPath
	config.GatewayURL = server.URL
	config.FileExplorerURL = server.URL
	config.AgentID = "executor-agent"
	config.HTTPClient = server.Client()
	executor, err := New(config)
	if err != nil {
		t.Fatal(err)
	}

	if err := executor.Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	if len(requests) != 2 {
		t.Fatalf("got %d requests, want only the two files under the subdirectory: %#v", len(requests), requests)
	}
	wantPaths := []string{`Current file path: "folder/alpha.txt"`, `Current file path: "folder/beta.txt"`}
	for i, want := range wantPaths {
		if !strings.Contains(requests[i].Input, want) {
			t.Errorf("request %d does not identify %q: %q", i, want, requests[i].Input)
		}
	}
	for i, request := range requests {
		if strings.Contains(request.Input, `Current file path: "zeta.txt"`) || strings.Contains(request.Input, `folder2/`) {
			t.Errorf("request %d escaped the subdirectory scope: %q", i, request.Input)
		}
		if !strings.Contains(request.Input, indexedInstructions) {
			t.Errorf("request %d did not source the indexed instructions.md outside the subdirectory: %q", i, request.Input)
		}
		if strings.Contains(request.Input, fallbackInstructions) {
			t.Errorf("request %d used the fallback instead of the indexed instructions: %q", i, request.Input)
		}
	}
}

func TestExecutorRunPrioritizesFilesCreatedDuringRun(t *testing.T) {
	rootPath := t.TempDir()
	for name, content := range map[string]string{"a.txt": "first content", "z.txt": "already queued content"} {
		if err := os.WriteFile(filepath.Join(rootPath, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	databasePath := filepath.Join(t.TempDir(), "fileexplorer.sqlite")
	createIndexFixture(t, databasePath,
		[]indexedFile{
			{path: "a.txt", name: "a.txt", size: 13},
			{path: "z.txt", name: "z.txt", size: 22},
		}, nil,
	)
	instructionsPath := filepath.Join(t.TempDir(), "instructions.md")
	const instructions = "Follow the fallback instructions."
	if err := os.WriteFile(instructionsPath, []byte(instructions), 0o600); err != nil {
		t.Fatal(err)
	}

	var requests []testResponsesRequest
	var synced bool
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/":
			// This endpoint models fileexplorer's regular GET-triggered index sync.
			if !synced {
				if _, err := os.Stat(filepath.Join(rootPath, "b.txt")); err == nil {
					db, err := sql.Open("sqlite", databasePath)
					if err != nil {
						t.Errorf("open index for sync: %v", err)
						http.Error(w, "sync failed", http.StatusInternalServerError)
						return
					}
					_, err = db.Exec("INSERT INTO files (path, name, is_dir, size, mtime) VALUES (?, ?, 0, ?, 0)", "b.txt", "b.txt", len("created during the first routine"))
					closeErr := db.Close()
					if err != nil || closeErr != nil {
						t.Errorf("sync newly created file: exec=%v close=%v", err, closeErr)
						http.Error(w, "sync failed", http.StatusInternalServerError)
						return
					}
					synced = true
				}
			}
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/responses":
			var request testResponsesRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode request: %v", err)
				http.Error(w, "invalid request", http.StatusBadRequest)
				return
			}
			requests = append(requests, request)
			if strings.Contains(request.Input, `Current file path: "a.txt"`) {
				if err := os.WriteFile(filepath.Join(rootPath, "b.txt"), []byte("created during the first routine"), 0o600); err != nil {
					t.Errorf("create file during routine: %v", err)
					http.Error(w, "create failed", http.StatusInternalServerError)
					return
				}
			}
			response := testResponsesAPIResponse{
				ID:     fmt.Sprintf("resp_%d", len(requests)),
				Output: testAssistantText(testCompletionMarker),
			}
			if err := json.NewEncoder(w).Encode(response); err != nil {
				t.Errorf("encode response: %v", err)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	config := validConfig()
	config.DatabasePath = databasePath
	config.RootPath = rootPath
	config.InstructionsPath = instructionsPath
	config.GatewayURL = server.URL
	config.FileExplorerURL = server.URL
	config.HTTPClient = server.Client()
	runner, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	if len(requests) != 3 {
		t.Fatalf("got %d conversations, want 3: %#v", len(requests), requests)
	}
	wantPaths := []string{`Current file path: "a.txt"`, `Current file path: "b.txt"`, `Current file path: "z.txt"`}
	for i, want := range wantPaths {
		if !strings.Contains(requests[i].Input, want) {
			t.Errorf("conversation %d input does not identify %q: %q", i, want, requests[i].Input)
		}
		if !strings.Contains(requests[i].Input, instructions) {
			t.Errorf("conversation %d input is missing fallback instructions", i)
		}
	}
	if !strings.Contains(requests[1].Input, "created during the first routine") {
		t.Errorf("new file content was not available to the immediately following task: %q", requests[1].Input)
	}
}

func TestExecutorRunStopsOnFirstTaskPromptError(t *testing.T) {
	rootPath := t.TempDir()
	for _, name := range []string{"a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(rootPath, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	databasePath := filepath.Join(t.TempDir(), "fileexplorer.sqlite")
	createIndexFixture(t, databasePath,
		[]indexedFile{
			{path: "a.txt", name: "a.txt"},
			{path: "b.txt", name: "b.txt"},
		},
		nil,
	)
	instructionsPath := filepath.Join(t.TempDir(), "instructions.md")
	if err := os.WriteFile(instructionsPath, []byte("instructions"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Make the first indexed task fail while building its prompt so this test
	// continues to cover stop-on-task-error behavior, not API retry policy.
	if err := os.Remove(filepath.Join(rootPath, "a.txt")); err != nil {
		t.Fatal(err)
	}

	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusOK)
			return
		}
		calls++
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
	config.HTTPClient = server.Client()
	executor, err := New(config)
	if err != nil {
		t.Fatal(err)
	}

	err = executor.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "a.txt") {
		t.Fatalf("Run() error = %v, want first file path", err)
	}
	if calls != 0 {
		t.Fatalf("calls = %d, want no API requests after the first task prompt error", calls)
	}
}

func TestExecutorRunDoesNotStartForCanceledContext(t *testing.T) {
	config := validConfig()
	executor, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := executor.Run(ctx); err != context.Canceled {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}
}

type progressReport struct {
	path      string
	completed bool
}

func TestExecutorRunReportsCompletedFileProgress(t *testing.T) {
	rootPath := t.TempDir()
	for _, name := range []string{"a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(rootPath, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	databasePath := filepath.Join(t.TempDir(), "fileexplorer.sqlite")
	createIndexFixture(t, databasePath,
		[]indexedFile{{path: "a.txt", name: "a.txt"}, {path: "b.txt", name: "b.txt"}},
		nil,
	)
	instructionsPath := filepath.Join(t.TempDir(), "instructions.md")
	if err := os.WriteFile(instructionsPath, []byte("instructions"), 0o600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusOK)
			return
		}
		var request testResponsesRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		response := testResponsesAPIResponse{ID: "resp_1", Output: testAssistantText(testCompletionMarker)}
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
	var reports []progressReport
	var reportsMu sync.Mutex
	config.OnFileProgress = func(path string, completed bool) {
		reportsMu.Lock()
		reports = append(reports, progressReport{path: path, completed: completed})
		reportsMu.Unlock()
	}
	runner, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if want := []progressReport{{path: "a.txt", completed: true}, {path: "b.txt", completed: true}}; !reflect.DeepEqual(reports, want) {
		t.Fatalf("progress reports = %#v, want %#v", reports, want)
	}
}

func TestExecutorRunSkipsCompletedFilesOnResume(t *testing.T) {
	rootPath := t.TempDir()
	for _, name := range []string{"a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(rootPath, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	databasePath := filepath.Join(t.TempDir(), "fileexplorer.sqlite")
	createIndexFixture(t, databasePath,
		[]indexedFile{{path: "a.txt", name: "a.txt"}, {path: "b.txt", name: "b.txt"}},
		nil,
	)
	instructionsPath := filepath.Join(t.TempDir(), "instructions.md")
	if err := os.WriteFile(instructionsPath, []byte("instructions"), 0o600); err != nil {
		t.Fatal(err)
	}

	var requests []testResponsesRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusOK)
			return
		}
		var request testResponsesRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		requests = append(requests, request)
		response := testResponsesAPIResponse{ID: "resp_1", Output: testAssistantText(testCompletionMarker)}
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
	config.CompletedPaths = []string{"a.txt"}
	runner, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 1 {
		t.Fatalf("got %d conversations, want only the uncompleted file: %#v", len(requests), requests)
	}
	if !strings.Contains(requests[0].Input, `Current file path: "b.txt"`) {
		t.Errorf("conversation input = %q, want the uncompleted file b.txt", requests[0].Input)
	}
}

func TestExecutorRunReportsFailedFileAsUnfinished(t *testing.T) {
	rootPath := t.TempDir()
	for _, name := range []string{"a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(rootPath, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	databasePath := filepath.Join(t.TempDir(), "fileexplorer.sqlite")
	createIndexFixture(t, databasePath,
		[]indexedFile{{path: "a.txt", name: "a.txt"}, {path: "b.txt", name: "b.txt"}},
		nil,
	)
	instructionsPath := filepath.Join(t.TempDir(), "instructions.md")
	if err := os.WriteFile(instructionsPath, []byte("instructions"), 0o600); err != nil {
		t.Fatal(err)
	}

	// The gateway refuses the first file with a non-retryable 401 so the run
	// stops after reporting that file as unfinished.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusOK)
			return
		}
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), `Current file path: \"a.txt\"`) {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"error":{"message":"denied"}}`)
			return
		}
		var request testResponsesRequest
		if err := json.Unmarshal(body, &request); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		response := testResponsesAPIResponse{ID: "resp_1", Output: testAssistantText(testCompletionMarker)}
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
	var reports []progressReport
	config.OnFileProgress = func(path string, completed bool) {
		reports = append(reports, progressReport{path: path, completed: completed})
	}
	runner, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	// The conversations loop retries failed HTTP responses until the context
	// ends, so a deadline is what turns the denied first file into a failed
	// run. It must leave that file uncompleted and never start the next one.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if err := runner.Run(ctx); err == nil {
		t.Fatal("Run() succeeded, want the failed conversation to stop the run")
	}
	if len(reports) != 1 || reports[0] != (progressReport{path: "a.txt", completed: false}) {
		t.Fatalf("progress reports = %#v, want the failed file reported unfinished and nothing after it", reports)
	}
}

func TestExecutorConfigCopyCarriesProgressFields(t *testing.T) {
	runner, err := New(validConfig())
	if err != nil {
		t.Fatal(err)
	}
	config := runner.Config()
	if config.CompletedPaths != nil {
		t.Errorf("fresh config CompletedPaths = %#v, want empty", config.CompletedPaths)
	}
	if config.OnFileProgress != nil {
		t.Error("fresh config OnFileProgress is set, want nil")
	}
	if config.AgentID == "" || config.RootPath == "" || config.DatabasePath == "" {
		t.Errorf("Config() did not preserve the validated configuration: %+v", config)
	}
}

func TestExecutorRunProcessesFilesDiscoveredAfterResume(t *testing.T) {
	rootPath := t.TempDir()
	for _, name := range []string{"a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(rootPath, name), []byte(name+" content"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	databasePath := filepath.Join(t.TempDir(), "fileexplorer.sqlite")
	createIndexFixture(t, databasePath,
		[]indexedFile{{path: "a.txt", name: "a.txt"}, {path: "b.txt", name: "b.txt"}},
		nil,
	)
	instructionsPath := filepath.Join(rootPath, "instructions.md")
	if err := os.WriteFile(instructionsPath, []byte("fallback"), 0o600); err != nil {
		t.Fatal(err)
	}

	// The run resumed from a session that already completed a.txt. The next
	// fileexplorer sync reports c.txt, a file first discovered after the
	// resume, and b.txt's conversation is what causes c.txt to appear.
	var mu sync.Mutex
	synced := false
	var requests []testResponsesRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			mu.Lock()
			if !synced {
				if _, err := os.Stat(filepath.Join(rootPath, "c.txt")); err == nil {
					synced = true
					db, err := sql.Open("sqlite", databasePath)
					if err != nil {
						mu.Unlock()
						t.Fatalf("open index for sync: %v", err)
					}
					if _, err := db.Exec(`
						INSERT INTO files (path, name, is_dir, size, mtime) VALUES ('c.txt', 'c.txt', 0, 11, 0)
					`); err != nil {
						mu.Unlock()
						t.Fatalf("sync c.txt into the index: %v", err)
					}
					db.Close()
				}
			}
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
			return
		}
		var request testResponsesRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		mu.Lock()
		requests = append(requests, request)
		mu.Unlock()
		if strings.Contains(request.Input, `Current file path: "b.txt"`) {
			if err := os.WriteFile(filepath.Join(rootPath, "c.txt"), []byte("c.txt content"), 0o600); err != nil {
				t.Errorf("create c.txt: %v", err)
			}
		}
		response := testResponsesAPIResponse{ID: "resp_1", Output: testAssistantText(testCompletionMarker)}
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
	config.CompletedPaths = []string{"a.txt"}
	runner, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 {
		t.Fatalf("got %d conversations, want the unfinished and the newly discovered file: %#v", len(requests), requests)
	}
	if !strings.Contains(requests[0].Input, `Current file path: "b.txt"`) {
		t.Errorf("first conversation input = %q, want the unfinished file b.txt", requests[0].Input)
	}
	if !strings.Contains(requests[1].Input, `Current file path: "c.txt"`) {
		t.Errorf("second conversation input = %q, want the file discovered after the resume", requests[1].Input)
	}
	if !strings.Contains(requests[1].Input, "c.txt content") {
		t.Errorf("second conversation input = %q, want the current content of c.txt", requests[1].Input)
	}
}
