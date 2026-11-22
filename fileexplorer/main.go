// Command fileexplorer runs the self-contained file explorer HTTP server.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() (err error) {
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
		dbPath, err = defaultDatabasePath(*root)
		if err != nil {
			return fmt.Errorf("database path: %w", err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		return fmt.Errorf("create database directory: %w", err)
	}
	store, err := Open(dbPath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer func() { err = errors.Join(err, store.Close()) }()

	app, err := newApplicationAt(*root, store)
	if err != nil {
		return fmt.Errorf("configure file explorer: %w", err)
	}
	defer func() { err = errors.Join(err, app.Close()) }()

	srv := &http.Server{Addr: listen, Handler: app.Handler()}
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
		return fmt.Errorf("server error: %w", err)
	case <-ctx.Done():
		log.Println("shutting down...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown: %w", err)
		}
		log.Println("stopped")
		return nil
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
