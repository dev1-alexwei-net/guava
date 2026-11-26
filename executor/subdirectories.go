package executor

import (
	"context"

	"github.com/dev1-alexwei-net/guava/tasks"
)

// ListSubdirectories remains available for existing callers; the
// fileexplorer manifest implementation now lives in the tasks package.
func ListSubdirectories(ctx context.Context, databasePath string) ([]string, error) {
	return tasks.ListSubdirectories(ctx, databasePath)
}
