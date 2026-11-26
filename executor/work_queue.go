package executor

import "github.com/dev1-alexwei-net/guava/tasks"

// WorkQueue and NewWorkQueue remain compatibility aliases. The implementation
// is owned by the tasks package.
type WorkQueue = tasks.WorkQueue

var NewWorkQueue = tasks.NewWorkQueue
