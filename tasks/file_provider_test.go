package tasks

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/dev1-alexwei-net/guava/conversations"
)

func TestNewFileProviderKeepsInitialPendingWork(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "fileexplorer.sqlite")
	createIndexFixture(t, databasePath, nil, nil)

	provider, err := NewFileProvider(context.Background(), FileProviderConfig{
		DatabasePath:    databasePath,
		RootPath:        t.TempDir(),
		FileExplorerURL: "http://127.0.0.1:8080",
		InitialPending:  []string{"zeta.txt", "alpha.txt", "middle.txt", "alpha.txt"},
	})
	if err != nil {
		t.Fatalf("NewFileProvider() error = %v", err)
	}
	defer provider.Close()

	want := []string{"alpha.txt", "middle.txt", "zeta.txt"}
	if got := provider.queue.Pending(); !reflect.DeepEqual(got, want) {
		t.Fatalf("initial pending = %#v, want %#v", got, want)
	}
}

func TestFileProviderPersistsQueueAfterEachRefreshBeforeSelectingWork(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "fileexplorer.sqlite")
	createIndexFixture(t, databasePath, []indexedFile{
		{path: "a.txt", name: "a.txt"},
		{path: "b.txt", name: "b.txt"},
	}, nil)

	refreshes := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		refreshes++
		if refreshes == 2 {
			db, err := sql.Open("sqlite", databasePath)
			if err != nil {
				t.Errorf("open index for refresh update: %v", err)
				return
			}
			defer db.Close()
			if _, err := db.Exec("INSERT INTO files (path, name, is_dir, size, mtime) VALUES (?, ?, 0, 0, 0)", "aa.txt", "aa.txt"); err != nil {
				t.Errorf("add newly discovered file: %v", err)
			}
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	var snapshots [][]string
	provider, err := NewFileProvider(context.Background(), FileProviderConfig{
		DatabasePath:    databasePath,
		RootPath:        t.TempDir(),
		FileExplorerURL: server.URL,
		OnPendingFiles: func(paths []string) error {
			snapshots = append(snapshots, append([]string(nil), paths...))
			return nil
		},
	})
	if err != nil {
		t.Fatalf("NewFileProvider() error = %v", err)
	}
	defer provider.Close()

	var processed []string
	if err := provider.Tasks(func(task conversations.Task) error {
		processed = append(processed, task.ID())
		return nil
	}); err != nil {
		t.Fatalf("Tasks() error = %v", err)
	}

	wantSnapshots := [][]string{{"a.txt", "b.txt"}, {"aa.txt", "b.txt"}, {"b.txt"}, nil}
	if !reflect.DeepEqual(snapshots, wantSnapshots) {
		t.Errorf("pending snapshots = %#v, want %#v", snapshots, wantSnapshots)
	}
	wantProcessed := []string{"a.txt", "aa.txt", "b.txt"}
	if !reflect.DeepEqual(processed, wantProcessed) {
		t.Errorf("processed = %#v, want %#v", processed, wantProcessed)
	}
}
