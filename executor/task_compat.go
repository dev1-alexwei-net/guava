// Compatibility declarations for task-source APIs now implemented by tasks.
package executor

import (
	"context"

	"github.com/dev1-alexwei-net/guava/tasks"
)

// ProcessFile remains available for callers that used the historical executor
// discovery seam; production task processing is now owned by conversations.
type ProcessFile func(ctx context.Context, path string) error

// HTTPRefresher and NewHTTPRefresher remain compatibility aliases. The
// fileexplorer adapter is owned by the tasks package.
type HTTPRefresher = tasks.HTTPRefresher

var NewHTTPRefresher = tasks.NewHTTPRefresher

// ListSubdirectories remains available for existing callers; the
// fileexplorer manifest implementation now lives in the tasks package.
func ListSubdirectories(ctx context.Context, databasePath string) ([]string, error) {
	return tasks.ListSubdirectories(ctx, databasePath)
}

// WorkQueue and NewWorkQueue remain compatibility aliases. The implementation
// is owned by the tasks package.
type WorkQueue = tasks.WorkQueue

var NewWorkQueue = tasks.NewWorkQueue
