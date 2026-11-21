// Package main implements a minimal self-contained file explorer HTTP server.
package main

import (
	"net/http"
	"os"
)

// Handler serves a minimal HTML view of the given root directory.
func handler(root string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entries, err := os.ReadDir(root)
		if err != nil {
			http.Error(w, "unable to read directory", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("<!doctype html><html><body><ul>"))
		for _, entry := range entries {
			_, _ = w.Write([]byte("<li>" + entry.Name() + "</li>"))
		}
		_, _ = w.Write([]byte("</ul></body></html>"))
	})
}
