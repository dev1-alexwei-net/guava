// Command fileexplorer runs the self-contained file explorer HTTP server.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

func main() {
	addr := flag.String("addr", "", "listen address; defaults to $PORT or :8080")
	root := flag.String("root", ".", "directory to serve")
	db := flag.String("db", "", "SQLite database file; defaults to a file in the system temp directory")
	flag.Parse()

	listen := *addr
	if listen == "" {
		listen = os.Getenv("PORT")
	}
	if listen == "" {
		listen = ":8080"
	}
	if listen[0] != ':' {
		listen = ":" + listen
	}

	dbPath := *db
	if dbPath == "" {
		var err error
		dbPath, err = defaultDatabasePath(*root)
		if err != nil {
			log.Fatalf("database path: %v", err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		log.Fatalf("create database directory: %v", err)
	}
	store, err := Open(dbPath)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	defer store.Close()

	app, err := newHandler(*root, store)
	if err != nil {
		log.Fatalf("configure file explorer: %v", err)
	}
	srv := &http.Server{Addr: listen, Handler: app}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		log.Printf("fileexplorer listening on %s (root %s)", listen, *root)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		log.Fatalf("server error: %v", err)
	case <-ctx.Done():
		log.Println("shutting down...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Fatalf("shutdown: %v", err)
		}
		log.Println("stopped")
	}
}

func defaultDatabasePath(root string) (string, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(absRoot))
	return filepath.Join(os.TempDir(), "guava-"+hex.EncodeToString(sum[:8])+".sqlite"), nil
}
