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
// It records every path discovered during the run and keeps unprocessed paths
// in deterministic order, with newly discovered paths optionally prioritized.
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

// Enqueue accepts one ordered file snapshot and appends only the snapshot
// paths that have not already been discovered during this run. Already
// queued items keep their FIFO position; the new batch is appended as a
// group, sorted by binary (byte-wise) path order, the same ordering the
// fileexplorer index uses. Path identity is the normalized relative path, so
// the same file discovered in different raw spellings is queued once.
//
// All snapshot paths are validated before the queue is modified: a snapshot
// containing an absolute path, an empty path, or a path that escapes the
// filesystem root fails as a whole and leaves the queue unchanged. It
// returns the number of paths appended.
func (q *WorkQueue) Enqueue(snapshot []string) (int, error) {
	return q.enqueue(snapshot, false)
}

// EnqueueNext accepts one file snapshot and puts newly discovered paths ahead
// of existing pending work. The new batch is sorted in binary path order and
// existing pending paths retain their relative FIFO order. This lets files
// created by the current task be selected immediately after its completion.
func (q *WorkQueue) EnqueueNext(snapshot []string) (int, error) {
	return q.enqueue(snapshot, true)
}

func (q *WorkQueue) enqueue(snapshot []string, prioritize bool) (int, error) {
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

	sort.Slice(fresh, func(i, j int) bool { return fresh[i] < fresh[j] })
	for _, path := range fresh {
		q.discovered[path] = struct{}{}
	}
	if prioritize {
		q.pending = append(fresh, q.pending...)
		return len(fresh), nil
	}
	for _, path := range fresh {
		q.pending = append(q.pending, path)
	}
	return len(fresh), nil
}

// Pop removes and returns the oldest queued path, preserving FIFO order.
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

// Pending returns a copy of the queued paths in FIFO order.
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
