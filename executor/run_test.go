package executor

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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

	var requests []responsesRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusOK)
			return
		}
		var request responsesRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		requests = append(requests, request)
		response := responsesAPIResponse{
			ID:     fmt.Sprintf("resp_%d", len(requests)),
			Output: assistantText(completionMarker),
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

	var requests []responsesRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusOK)
			return
		}
		var request responsesRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		requests = append(requests, request)
		response := responsesAPIResponse{
			ID:     fmt.Sprintf("resp_%d", len(requests)),
			Output: assistantText(completionMarker),
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

func TestExecutorRunDiscoversFilesCreatedDuringRun(t *testing.T) {
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

	var requests []responsesRequest
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
			var request responsesRequest
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
			response := responsesAPIResponse{
				ID:     fmt.Sprintf("resp_%d", len(requests)),
				Output: assistantText(completionMarker),
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
	wantPaths := []string{`Current file path: "a.txt"`, `Current file path: "z.txt"`, `Current file path: "b.txt"`}
	for i, want := range wantPaths {
		if !strings.Contains(requests[i].Input, want) {
			t.Errorf("conversation %d input does not identify %q: %q", i, want, requests[i].Input)
		}
		if !strings.Contains(requests[i].Input, instructions) {
			t.Errorf("conversation %d input is missing fallback instructions", i)
		}
	}
	if !strings.Contains(requests[2].Input, "created during the first routine") {
		t.Errorf("new file content was not available in the same run: %q", requests[2].Input)
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
