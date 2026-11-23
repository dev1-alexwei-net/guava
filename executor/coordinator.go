package executor

import (
	"context"
	"errors"
	"fmt"
)

// Coordinator is the in-memory work queue for a single dynamic
// discovery run. It is independent of HTTP and SQLite: it needs only a
// Refresher, a FileLister, and a ProcessFile callback, so its behavior
// can be exercised with in-memory fakes.
//
// Each loop iteration refreshes the index, appends paths not previously
// discovered to the tail of the queue in listing order, and processes
// the head of the queue. Newly discovered paths are appended after
// already queued work, preserving FIFO order for known work and sorted
// order within each discovery batch. A path is processed at most once
// per run, and refresh, listing, processing, or context-cancellation
// failures stop the run and are returned to the caller.
//
// Queue and discovery state are in memory and scoped to a single run: a
// failed run leaves no persisted state, and a later run starts fresh
// with a new coordinator.
type Coordinator struct {
	refresher Refresher
	lister    FileLister
	process   ProcessFile
	queue     *WorkQueue
}

// NewCoordinator validates the discovery dependencies and returns a
// fresh Coordinator.
func NewCoordinator(refresher Refresher, lister FileLister, process ProcessFile) (*Coordinator, error) {
	if refresher == nil {
		return nil, errors.New("refresher is required")
	}
	if lister == nil {
		return nil, errors.New("file lister is required")
	}
	if process == nil {
		return nil, errors.New("file processor is required")
	}
	return &Coordinator{
		refresher: refresher,
		lister:    lister,
		process:   process,
		queue:     NewWorkQueue(),
	}, nil
}

// Run drives the dynamic discovery loop until a refresh discovers no
// new work and the queue is empty. It returns the first error
// encountered, including a canceled context.
func (c *Coordinator) Run(ctx context.Context) error {
	if c == nil {
		return errors.New("coordinator is nil")
	}
	if ctx == nil {
		return errors.New("context is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	for {
		if err := c.refreshAndDiscover(ctx); err != nil {
			return err
		}
		if c.queue.Empty() {
			return nil
		}

		pending := c.queue.Pending()
		if len(pending) == 0 {
			return nil
		}
		path := pending[0]
		if err := c.process(ctx, path); err != nil {
			return fmt.Errorf("process file %q: %w", path, err)
		}
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("process file %q canceled before completion: %w", path, err)
		}
		if popped, ok := c.queue.Pop(); !ok || popped != path {
			return fmt.Errorf("work queue changed while processing %q", path)
		}
	}
}

// refreshAndDiscover refreshes fileexplorer and appends paths not
// previously discovered from the ordered listing to the tail of the
// queue. Paths already queued or already completed are never requeued.
func (c *Coordinator) refreshAndDiscover(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := c.refresher.Refresh(ctx); err != nil {
		return fmt.Errorf("refresh file index: %w", err)
	}
	paths, err := c.lister.FilePaths(ctx)
	if err != nil {
		return fmt.Errorf("list indexed files: %w", err)
	}
	if _, err := c.queue.Enqueue(paths); err != nil {
		return fmt.Errorf("enqueue discovered files: %w", err)
	}
	return nil
}
