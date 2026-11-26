package tasks

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
)

func TestFileProviderYieldsOrderedScopedFileTasksWithNearestInstructions(t *testing.T) {
	root := t.TempDir()
	fallbackPath := filepath.Join(t.TempDir(), "fallback.md")
	if err := os.WriteFile(fallbackPath, []byte("fallback instructions"), 0o600); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"a.txt":                     "root file",
		"alpha/instructions.md":     "alpha instructions",
		"alpha/a.txt":               "alpha file",
		"alpha/sub/instructions.md": "nested instructions",
		"alpha/sub/b.txt":           "nested file",
		"beta/c.txt":                "beta file",
		"instructions.md":           "root indexed instructions",
	}
	entries := make([]indexedFile, 0, len(files))
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		entries = append(entries, indexedFile{path: name, name: filepath.Base(name)})
	}
	databasePath := filepath.Join(t.TempDir(), "fileexplorer.sqlite")
	createIndexFixture(t, databasePath, entries, []string{"alpha", "alpha/sub", "beta"})

	var refreshes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refreshes.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	provider, err := NewFileProvider(context.Background(), FileProviderConfig{
		DatabasePath: databasePath, RootPath: root, InstructionsPath: fallbackPath,
		FileExplorerURL: server.URL, Subdirectory: "alpha",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer provider.Close()

	type prompt struct{ id, text string }
	var got []prompt
	if err := provider.Tasks(func(task Task) bool {
		value, err := task.Prompt()
		if err != nil {
			t.Errorf("Prompt(%q) error = %v", task.ID(), err)
			return false
		}
		got = append(got, prompt{task.ID(), value})
		return true
	}); err != nil {
		t.Fatalf("Tasks() error = %v", err)
	}

	want := []prompt{
		{"alpha/a.txt", "alpha instructions\n\nCurrent file path: \"alpha/a.txt\"\nFile contents:\nalpha file"},
		{"alpha/sub/b.txt", "nested instructions\n\nCurrent file path: \"alpha/sub/b.txt\"\nFile contents:\nnested file"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tasks = %#v, want %#v", got, want)
	}
	// The two scoped instructions rows are discovered queue entries too, so
	// the provider refreshes before skipping each one, just as the old runner did.
	if got := refreshes.Load(); got != 5 {
		t.Fatalf("refresh count = %d, want one refresh per task, instruction row, and final empty discovery", got)
	}
}

func TestFileProviderUsesFallbackInstructionsAndExcludesInstructionTasks(t *testing.T) {
	root := t.TempDir()
	fallbackPath := filepath.Join(t.TempDir(), "fallback.md")
	if err := os.WriteFile(fallbackPath, []byte("configured instructions"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"file.txt": "file body",
	} {
		filePath := filepath.Join(root, name)
		if err := os.WriteFile(filePath, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	databasePath := filepath.Join(t.TempDir(), "fileexplorer.sqlite")
	createIndexFixture(t, databasePath, []indexedFile{
		{path: "file.txt", name: "file.txt"},
	}, nil)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	provider, err := NewFileProvider(context.Background(), FileProviderConfig{
		DatabasePath: databasePath, RootPath: root, InstructionsPath: fallbackPath,
		FileExplorerURL: server.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer provider.Close()

	var got []string
	var prompt string
	if err := provider.Tasks(func(task Task) bool {
		got = append(got, task.ID())
		var promptErr error
		prompt, promptErr = task.Prompt()
		if promptErr != nil {
			t.Errorf("Prompt() error = %v", promptErr)
			return false
		}
		return true
	}); err != nil {
		t.Fatalf("Tasks() error = %v", err)
	}
	if want := []string{"file.txt"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("task IDs = %#v, want %#v", got, want)
	}
	if want := "configured instructions\n\nCurrent file path: \"file.txt\"\nFile contents:\nfile body"; prompt != want {
		t.Fatalf("Prompt() = %q, want %q", prompt, want)
	}
}

func TestFileProviderKeepsDeclinedTaskPending(t *testing.T) {
	root := t.TempDir()
	fallbackPath := filepath.Join(t.TempDir(), "fallback.md")
	if err := os.WriteFile(fallbackPath, []byte("instructions"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	databasePath := filepath.Join(t.TempDir(), "fileexplorer.sqlite")
	createIndexFixture(t, databasePath, []indexedFile{{path: "a.txt", name: "a.txt"}}, nil)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	provider, err := NewFileProvider(context.Background(), FileProviderConfig{
		DatabasePath: databasePath, RootPath: root, InstructionsPath: fallbackPath,
		FileExplorerURL: server.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer provider.Close()

	var first, second string
	if err := provider.Tasks(func(task Task) bool { first = task.ID(); return false }); err != nil {
		t.Fatal(err)
	}
	if err := provider.Tasks(func(task Task) bool { second = task.ID(); return false }); err != nil {
		t.Fatal(err)
	}
	if first != "a.txt" || second != first {
		t.Fatalf("declined task IDs = %q then %q, want the same pending task", first, second)
	}
}
