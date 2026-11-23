package executor

import (
	"context"
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

func TestExecutorRunStopsOnFirstFileError(t *testing.T) {
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

	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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
	config.HTTPClient = server.Client()
	executor, err := New(config)
	if err != nil {
		t.Fatal(err)
	}

	err = executor.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "a.txt") {
		t.Fatalf("Run() error = %v, want first file path", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want processing to stop after the first file error", calls)
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
