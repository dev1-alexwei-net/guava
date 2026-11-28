package notebooks

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

type memoryObjects struct {
	data   map[string][]byte
	putErr error
	puts   int
}

func newMemoryObjects() *memoryObjects { return &memoryObjects{data: make(map[string][]byte)} }

func (m *memoryObjects) Put(_ context.Context, notebookID, objectID, _ string, size int64, body io.Reader) error {
	m.puts++
	if m.putErr != nil {
		return m.putErr
	}
	data, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	if int64(len(data)) != size {
		return errors.New("declared size does not match body")
	}
	m.data[notebookID+"/"+objectID] = data
	return nil
}

func (m *memoryObjects) Get(_ context.Context, notebookID, objectID string) (io.ReadCloser, error) {
	data, ok := m.data[notebookID+"/"+objectID]
	if !ok {
		return nil, ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (m *memoryObjects) Delete(_ context.Context, notebookID, objectID string) error {
	delete(m.data, notebookID+"/"+objectID)
	return nil
}

func newTestStore(t *testing.T, objects *memoryObjects) *Store {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	store, err := NewStore(db, objects)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := store.Migrate(context.Background()); err != nil {
		store.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestStorePersistsMetadataAndKeepsContentInObjectStore(t *testing.T) {
	ctx := context.Background()
	objects := newMemoryObjects()
	store := newTestStore(t, objects)

	notebook, err := store.CreateNotebook(ctx, "owner-7")
	if err != nil {
		t.Fatal(err)
	}
	gotNotebook, err := store.GetNotebook(ctx, notebook.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotNotebook.OwnerID != "owner-7" || gotNotebook.Revision != 0 {
		t.Fatalf("created notebook metadata = %+v", gotNotebook)
	}

	firstBody := "durable note bytes"
	first, err := store.WriteFile(ctx, notebook.ID, "notes/readme.md", "text/markdown", int64(len(firstBody)), 0, strings.NewReader(firstBody))
	if err != nil {
		t.Fatal(err)
	}
	if first.Revision != 1 || first.ObjectID == "" || first.Size != int64(len(firstBody)) {
		t.Fatalf("first file metadata = %+v", first)
	}
	if _, ok := objects.data[notebook.ID+"/"+first.ObjectID]; !ok {
		t.Fatal("file bytes were not stored in the object store")
	}

	metadata, content, err := store.ReadFile(ctx, notebook.ID, "notes/readme.md")
	if err != nil {
		t.Fatal(err)
	}
	defer content.Close()
	gotBody, err := io.ReadAll(content)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotBody) != firstBody || metadata.ObjectID != first.ObjectID || metadata.Revision != 1 {
		t.Fatalf("read result metadata=%+v body=%q", metadata, gotBody)
	}

	secondBody := "new immutable version"
	second, err := store.WriteFile(ctx, notebook.ID, "notes/readme.md", "text/markdown", int64(len(secondBody)), 1, strings.NewReader(secondBody))
	if err != nil {
		t.Fatal(err)
	}
	if second.Revision != 2 || second.ObjectID == first.ObjectID {
		t.Fatalf("overwrite did not create a new revision/object: first=%+v second=%+v", first, second)
	}
	gotNotebook, err = store.GetNotebook(ctx, notebook.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotNotebook.Revision != 2 || gotNotebook.OwnerID != "owner-7" {
		t.Fatalf("updated notebook metadata = %+v", gotNotebook)
	}
	metadata, content, err = store.ReadFile(ctx, notebook.ID, "notes/readme.md")
	if err != nil {
		t.Fatal(err)
	}
	defer content.Close()
	gotBody, err = io.ReadAll(content)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotBody) != secondBody || metadata.Revision != 2 {
		t.Fatalf("latest read metadata=%+v body=%q", metadata, gotBody)
	}
}

func TestWriteFileDoesNotCommitMetadataWhenObjectUploadFails(t *testing.T) {
	ctx := context.Background()
	objects := newMemoryObjects()
	store := newTestStore(t, objects)
	notebook, err := store.CreateNotebook(ctx, "owner-7")
	if err != nil {
		t.Fatal(err)
	}
	objects.putErr = errors.New("object storage unavailable")
	_, err = store.WriteFile(ctx, notebook.ID, "note.txt", "text/plain", 4, 0, strings.NewReader("data"))
	if err == nil {
		t.Fatal("WriteFile succeeded despite object upload failure")
	}
	if _, _, err := store.ReadFile(ctx, notebook.ID, "note.txt"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ReadFile error = %v, want ErrNotFound", err)
	}
	gotNotebook, err := store.GetNotebook(ctx, notebook.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotNotebook.Revision != 0 {
		t.Fatalf("failed upload advanced revision to %d", gotNotebook.Revision)
	}
}

func TestWriteFileRejectsInvalidPathsBeforeObjectWrite(t *testing.T) {
	ctx := context.Background()
	objects := newMemoryObjects()
	store := newTestStore(t, objects)
	notebook, err := store.CreateNotebook(ctx, "owner-7")
	if err != nil {
		t.Fatal(err)
	}
	for _, filePath := range []string{"../outside", "notes/../../outside", "/absolute", `C:\\absolute`, ""} {
		t.Run(filePath, func(t *testing.T) {
			_, err := store.WriteFile(ctx, notebook.ID, filePath, "text/plain", 1, 0, strings.NewReader("x"))
			if !errors.Is(err, ErrInvalidData) {
				t.Fatalf("WriteFile error = %v, want ErrInvalidData", err)
			}
		})
	}
	if objects.puts != 0 {
		t.Fatalf("invalid paths reached object storage %d times", objects.puts)
	}
}

func TestWriteFileForMissingNotebookDoesNotWriteObject(t *testing.T) {
	objects := newMemoryObjects()
	store := newTestStore(t, objects)
	_, err := store.WriteFile(context.Background(), "28c81446-d031-427d-b129-f981173f2001", "note.txt", "text/plain", 1, 0, strings.NewReader("x"))
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("WriteFile error = %v, want ErrNotFound", err)
	}
	if objects.puts != 0 {
		t.Fatalf("missing notebook reached object storage %d times", objects.puts)
	}
}

func TestWriteFileRejectsStaleRevisionWithoutOverwriting(t *testing.T) {
	ctx := context.Background()
	objects := newMemoryObjects()
	store := newTestStore(t, objects)
	notebook, err := store.CreateNotebook(ctx, "owner-7")
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.WriteFile(ctx, notebook.ID, "note.txt", "text/plain", 8, 0, strings.NewReader("original"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.WriteFile(ctx, notebook.ID, "note.txt", "text/plain", 6, 0, strings.NewReader("stale!"))
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("stale WriteFile error = %v, want ErrConflict", err)
	}
	var conflict *ConflictError
	if !errors.As(err, &conflict) || conflict.ExpectedRevision != 0 || conflict.CurrentRevision != 1 {
		t.Fatalf("conflict details = %+v, error = %v", conflict, err)
	}
	if objects.puts != 1 {
		t.Fatalf("stale write uploaded content; puts = %d, want 1", objects.puts)
	}
	metadata, content, err := store.ReadFile(ctx, notebook.ID, "note.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer content.Close()
	body, err := io.ReadAll(content)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "original" || metadata.ObjectID != first.ObjectID || metadata.Revision != 1 {
		t.Fatalf("stale write changed content: metadata=%+v body=%q", metadata, body)
	}
}

func TestListAndDeleteFilesUseNotebookRevision(t *testing.T) {
	ctx := context.Background()
	objects := newMemoryObjects()
	store := newTestStore(t, objects)
	notebook, err := store.CreateNotebook(ctx, "owner-7")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.WriteFile(ctx, notebook.ID, "z.txt", "text/plain", 1, 0, strings.NewReader("z")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.WriteFile(ctx, notebook.ID, "a.txt", "text/plain", 1, 1, strings.NewReader("a")); err != nil {
		t.Fatal(err)
	}
	files, err := store.ListFiles(ctx, notebook.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || files[0].Path != "a.txt" || files[1].Path != "z.txt" {
		t.Fatalf("listed files = %+v, want path order [a.txt z.txt]", files)
	}

	revision, err := store.DeleteFile(ctx, notebook.ID, "z.txt", 2)
	if err != nil {
		t.Fatal(err)
	}
	if revision != 3 {
		t.Fatalf("delete revision = %d, want 3", revision)
	}
	if _, _, err := store.ReadFile(ctx, notebook.ID, "z.txt"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ReadFile after delete error = %v, want ErrNotFound", err)
	}
	files, err = store.ListFiles(ctx, notebook.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Path != "a.txt" {
		t.Fatalf("files after delete = %+v, want only a.txt", files)
	}
	if _, err := store.DeleteFile(ctx, notebook.ID, "a.txt", 2); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale DeleteFile error = %v, want ErrConflict", err)
	}
	files, err = store.ListFiles(ctx, notebook.ID)
	if err != nil || len(files) != 1 || files[0].Path != "a.txt" {
		t.Fatalf("stale delete changed files: files=%+v err=%v", files, err)
	}
}
