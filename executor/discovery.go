package executor

import (
	"context"

	"github.com/dev1-alexwei-net/guava/tasks"
)

// Refresher and FileLister remain aliases for compatibility; their shared
// contracts now live with the task source package.
type Refresher = tasks.Refresher
type FileLister = tasks.FileLister

// ProcessFile remains available for callers that used the historical executor
// discovery seam; production task processing is now owned by conversations.
type ProcessFile func(ctx context.Context, path string) error
