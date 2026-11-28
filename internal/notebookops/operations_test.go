package notebookops

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/dev1-alexwei-net/guava/notebooks"
)

type fakeStore struct {
	notebook       notebooks.Notebook
	getErr         error
	listCalls      int
	readCalls      int
	writeCalls     int
	deleteCalls    int
	writeRevision  int64
	deleteRevision int64
}

func (f *fakeStore) GetNotebook(context.Context, string) (notebooks.Notebook, error) {
	if f.getErr != nil {
		return notebooks.Notebook{}, f.getErr
	}
	return f.notebook, nil
}

func (f *fakeStore) ListFiles(context.Context, string) ([]notebooks.File, error) {
	f.listCalls++
	return []notebooks.File{}, nil
}

func (f *fakeStore) ReadFile(context.Context, string, string) (notebooks.File, io.ReadCloser, error) {
	f.readCalls++
	return notebooks.File{}, io.NopCloser(strings.NewReader("contents")), nil
}

func (f *fakeStore) WriteFile(_ context.Context, notebookID, filePath, contentType string, size, expectedRevision int64, _ io.Reader) (notebooks.File, error) {
	f.writeCalls++
	f.writeRevision = expectedRevision
	return notebooks.File{NotebookID: notebookID, Path: filePath, ContentType: contentType, Size: size}, nil
}

func (f *fakeStore) DeleteFile(_ context.Context, _, _ string, expectedRevision int64) (int64, error) {
	f.deleteCalls++
	f.deleteRevision = expectedRevision
	return expectedRevision + 1, nil
}

func TestOperationsHideNotebooksFromNonOwners(t *testing.T) {
	store := &fakeStore{notebook: notebooks.Notebook{ID: "notebook-1", OwnerID: "owner-a"}}
	service, err := New(store)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := service.List(ctx, "owner-b", "notebook-1"); !errors.Is(err, notebooks.ErrNotFound) {
		t.Fatalf("List error = %v, want ErrNotFound", err)
	}
	if _, _, err := service.Read(ctx, "owner-b", "notebook-1", "note.txt"); !errors.Is(err, notebooks.ErrNotFound) {
		t.Fatalf("Read error = %v, want ErrNotFound", err)
	}
	if _, err := service.Write(ctx, "owner-b", "notebook-1", "note.txt", "text/plain", 1, 0, strings.NewReader("x")); !errors.Is(err, notebooks.ErrNotFound) {
		t.Fatalf("Write error = %v, want ErrNotFound", err)
	}
	if _, err := service.Delete(ctx, "owner-b", "notebook-1", "note.txt", 0); !errors.Is(err, notebooks.ErrNotFound) {
		t.Fatalf("Delete error = %v, want ErrNotFound", err)
	}
	if store.listCalls+store.readCalls+store.writeCalls+store.deleteCalls != 0 {
		t.Fatal("operation reached storage after owner mismatch")
	}
}

func TestOperationsPassExpectedRevisionForMutations(t *testing.T) {
	store := &fakeStore{notebook: notebooks.Notebook{ID: "notebook-1", OwnerID: "owner-a"}}
	service, err := New(store)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := service.Write(ctx, "owner-a", "notebook-1", "note.txt", "text/plain", 4, 12, strings.NewReader("data")); err != nil {
		t.Fatal(err)
	}
	if store.writeCalls != 1 || store.writeRevision != 12 {
		t.Fatalf("write call/revision = %d/%d, want 1/12", store.writeCalls, store.writeRevision)
	}
	if revision, err := service.Delete(ctx, "owner-a", "notebook-1", "note.txt", 13); err != nil || revision != 14 {
		t.Fatalf("Delete = revision %d, error %v; want 14, nil", revision, err)
	}
	if store.deleteCalls != 1 || store.deleteRevision != 13 {
		t.Fatalf("delete call/revision = %d/%d, want 1/13", store.deleteCalls, store.deleteRevision)
	}
}

func TestOperationsRequireOwnerIdentity(t *testing.T) {
	store := &fakeStore{notebook: notebooks.Notebook{ID: "notebook-1", OwnerID: "owner-a"}}
	service, err := New(store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.List(context.Background(), "  ", "notebook-1"); !errors.Is(err, notebooks.ErrInvalidData) {
		t.Fatalf("List with empty owner error = %v, want ErrInvalidData", err)
	}
}
