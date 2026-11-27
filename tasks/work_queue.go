package tasks

import (
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// WorkQueue is the in-memory work queue scoped to a single executor run.
// It records every path discovered during the run and keeps all unprocessed
// paths in binary path order, including when new paths are discovered.
// It exists only for one run: it is never shared between runs and never persisted,
// so no shared-database table is involved.
type WorkQueue struct {
	pending    []string
	discovered map[string]struct{}
}

// NewWorkQueue returns an empty work queue for one executor run.
func NewWorkQueue() *WorkQueue {
	return &WorkQueue{discovered: make(map[string]struct{})}
}

// EnqueueNext accepts one file snapshot and adds newly discovered paths to
// pending work. The complete pending set is kept in binary path order so
// resumed and newly discovered files have consistent ordering.
func (q *WorkQueue) EnqueueNext(snapshot []string) (int, error) {
	return q.enqueue(snapshot)
}

func (q *WorkQueue) enqueue(snapshot []string) (int, error) {
	if q == nil {
		return 0, errors.New("work queue is nil")
	}

	inBatch := make(map[string]struct{}, len(snapshot))
	fresh := make([]string, 0, len(snapshot))
	for _, raw := range snapshot {
		path, err := normalizeQueuePath(raw)
		if err != nil {
			return 0, fmt.Errorf("work queue snapshot path %q: %w", raw, err)
		}
		if _, known := q.discovered[path]; known {
			continue
		}
		if _, inSnapshot := inBatch[path]; inSnapshot {
			continue
		}
		inBatch[path] = struct{}{}
		fresh = append(fresh, path)
	}

	for _, path := range fresh {
		q.discovered[path] = struct{}{}
		q.pending = append(q.pending, path)
	}
	sort.Slice(q.pending, func(i, j int) bool { return q.pending[i] < q.pending[j] })
	return len(fresh), nil
}

// Seed sets the queue's initial pending work before any discovery pass. Paths
// are normalized, deduplicated, and sorted in binary path order, like paths
// later merged from manifest snapshots.
//
// All seed paths are validated before the queue is modified: a snapshot
// containing an invalid path fails as a whole and leaves the queue
// unchanged. Duplicate paths are queued once. It returns the number of
// paths queued.
func (q *WorkQueue) Seed(paths []string) (int, error) {
	if q == nil {
		return 0, errors.New("work queue is nil")
	}
	if len(q.pending) > 0 {
		return 0, errors.New("work queue already has pending work")
	}

	seen := make(map[string]struct{}, len(paths))
	queued := 0
	for _, raw := range paths {
		path, err := normalizeQueuePath(raw)
		if err != nil {
			return 0, fmt.Errorf("work queue seed path %q: %w", raw, err)
		}
		if _, inSeed := seen[path]; inSeed {
			continue
		}
		seen[path] = struct{}{}
		if _, known := q.discovered[path]; known {
			continue
		}
		q.discovered[path] = struct{}{}
		q.pending = append(q.pending, path)
		queued++
	}
	sort.Slice(q.pending, func(i, j int) bool { return q.pending[i] < q.pending[j] })
	return queued, nil
}

// Pop removes and returns the first queued path in binary path order.
// The second return value is false when the queue is empty.
func (q *WorkQueue) Pop() (string, bool) {
	if q == nil || len(q.pending) == 0 {
		return "", false
	}
	path := q.pending[0]
	q.pending = q.pending[1:]
	return path, true
}

// Len returns the number of paths currently queued for processing.
func (q *WorkQueue) Len() int {
	if q == nil {
		return 0
	}
	return len(q.pending)
}

// Empty reports whether no paths are currently queued.
func (q *WorkQueue) Empty() bool {
	return q.Len() == 0
}

// Pending returns a copy of the queued paths in binary path order.
func (q *WorkQueue) Pending() []string {
	if q == nil {
		return nil
	}
	out := make([]string, len(q.pending))
	copy(out, q.pending)
	return out
}

// normalizeQueuePath reduces a discovered path to the per-run identity: the
// cleaned, slash-separated relative path. It rejects the empty path, paths
// containing NUL bytes, absolute paths, and paths that escape the
// filesystem root after cleaning.
func normalizeQueuePath(raw string) (string, error) {
	if raw == "" || strings.IndexByte(raw, 0) >= 0 {
		return "", errors.New("path must be a non-empty relative file path")
	}
	slash := filepath.ToSlash(raw)
	if slash[0] == '/' {
		return "", errors.New("path must be relative to the filesystem root")
	}
	cleaned := path.Clean(slash)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", errors.New("path must stay inside the filesystem root")
	}
	return cleaned, nil
}
