// Command fileexplorer is a minimal self-contained file explorer HTTP server.
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	addr := flag.String("addr", "", "listen address; defaults to $PORT or :8080")
	root := flag.String("root", ".", "directory to serve")
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

	mux := http.NewServeMux()
	mux.Handle("/", handler(*root))

	srv := &http.Server{
		Addr:    listen,
		Handler: mux,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		log.Printf("fileexplorer listening on %s", listen)
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
