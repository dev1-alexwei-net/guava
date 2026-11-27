package tasks

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
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
