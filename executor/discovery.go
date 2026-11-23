package executor

import "context"

// Refresher refreshes fileexplorer's view of its filesystem before
// indexed files are listed. The queue coordinator depends only on this
// method; the concrete implementation performs a plain HTTP GET against
// a configurable fileexplorer URL and treats a non-success response as
// an error.
type Refresher interface {
	Refresh(ctx context.Context) error
}

// FileLister returns the indexed regular-file paths that fileexplorer
// knows about, in fileexplorer's deterministic order
// (ORDER BY path COLLATE BINARY). The queue coordinator depends only on
// this ordered listing, not on the SQLite implementation.
type FileLister interface {
	FilePaths(ctx context.Context) ([]string, error)
}

// ProcessFile handles a single discovered file path. It is deliberately
// separate from discovery: the queue coordinator only selects work and
// invokes this callback in order, while the callback performs the file
// read and conversation for the path.
type ProcessFile func(ctx context.Context, path string) error
