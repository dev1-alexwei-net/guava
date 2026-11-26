package executor

import (
	"context"

	"github.com/dev1-alexwei-net/guava/tasks"
)

// Refresher and FileLister remain aliases for compatibility; their shared
// contracts now live with the task source package.
type Refresher = tasks.Refresher
type FileLister = tasks.FileLister

// ProcessFile handles a single discovered file path. The coordinator only
// selects work and invokes this callback in order.
type ProcessFile func(ctx context.Context, path string) error
