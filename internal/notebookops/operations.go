// Package notebookops provides repository-internal, owner-scoped notebook
// operations. It is deliberately not wired to a public HTTP route.
package notebookops

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/dev1-alexwei-net/guava/notebooks"
)

// Store is the persistence surface required by the internal operation service.
type Store interface {
	GetNotebook(context.Context, string) (notebooks.Notebook, error)
	ListFiles(context.Context, string) ([]notebooks.File, error)
	ReadFile(context.Context, string, string) (notebooks.File, io.ReadCloser, error)
	WriteFile(context.Context, string, string, string, int64, int64, io.Reader) (notebooks.File, error)
	DeleteFile(context.Context, string, string, int64) (int64, error)
}

// Service exposes owner-scoped notebook operations to trusted in-process
// callers. ownerID must come from an authenticated identity, never request data.
type Service struct {
	store Store
}

// New creates an operation service backed by durable notebook storage.
func New(store Store) (*Service, error) {
	if store == nil {
		return nil, fmt.Errorf("notebook storage is required")
	}
	return &Service{store: store}, nil
}

// List returns metadata for all files owned by ownerID in notebookID.
func (s *Service) List(ctx context.Context, ownerID, notebookID string) ([]notebooks.File, error) {
	if err := s.authorize(ctx, ownerID, notebookID); err != nil {
		return nil, err
	}
	return s.store.ListFiles(ctx, notebookID)
}

// Read returns file metadata and a stream of its bytes.
func (s *Service) Read(ctx context.Context, ownerID, notebookID, filePath string) (notebooks.File, io.ReadCloser, error) {
	if err := s.authorize(ctx, ownerID, notebookID); err != nil {
		return notebooks.File{}, nil, err
	}
	return s.store.ReadFile(ctx, notebookID, filePath)
}

// Write replaces or creates a file only if expectedRevision is still current.
func (s *Service) Write(ctx context.Context, ownerID, notebookID, filePath, contentType string, size, expectedRevision int64, body io.Reader) (notebooks.File, error) {
	if err := s.authorize(ctx, ownerID, notebookID); err != nil {
		return notebooks.File{}, err
	}
	return s.store.WriteFile(ctx, notebookID, filePath, contentType, size, expectedRevision, body)
}

// Delete removes a file's metadata pointer only if expectedRevision is current.
// It returns the newly committed notebook revision.
func (s *Service) Delete(ctx context.Context, ownerID, notebookID, filePath string, expectedRevision int64) (int64, error) {
	if err := s.authorize(ctx, ownerID, notebookID); err != nil {
		return 0, err
	}
	return s.store.DeleteFile(ctx, notebookID, filePath, expectedRevision)
}

func (s *Service) authorize(ctx context.Context, ownerID, notebookID string) error {
	if strings.TrimSpace(ownerID) == "" {
		return fmt.Errorf("%w: owner ID is required", notebooks.ErrInvalidData)
	}
	notebook, err := s.store.GetNotebook(ctx, notebookID)
	if err != nil {
		return err
	}
	// Hide notebook existence from callers that do not own it.
	if notebook.OwnerID != ownerID {
		return notebooks.ErrNotFound
	}
	return nil
}
