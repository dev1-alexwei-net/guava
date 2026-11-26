package executor

import "github.com/dev1-alexwei-net/guava/tasks"

// HTTPRefresher and NewHTTPRefresher remain compatibility aliases. The
// fileexplorer adapter is owned by the tasks package.
type HTTPRefresher = tasks.HTTPRefresher

var NewHTTPRefresher = tasks.NewHTTPRefresher
