package tasks

import "context"

// Refresher refreshes fileexplorer's view before indexed files are listed.
type Refresher interface {
	Refresh(ctx context.Context) error
}

// FileLister returns indexed regular-file paths in deterministic manifest order.
type FileLister interface {
	FilePaths(ctx context.Context) ([]string, error)
}
