package handler

import "net/http"

// FileExplorer is the serverless entry point for the file explorer route.
// The application itself is not yet adapted to request-scoped execution.
func FileExplorer(w http.ResponseWriter, _ *http.Request) {
	http.Error(w, "fileexplorer serverless handler is not implemented", http.StatusNotImplemented)
}
