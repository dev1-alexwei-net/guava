package handler

import "net/http"

// Executor is the serverless entry point for the executor dashboard route.
// The application itself is not yet adapted to request-scoped execution.
func Executor(w http.ResponseWriter, _ *http.Request) {
	http.Error(w, "executor serverless handler is not implemented", http.StatusNotImplemented)
}
