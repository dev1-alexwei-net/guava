// Command executor-dashboard serves a small web interface for controlling executor runs.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/dev1-alexwei-net/guava/executor"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	flags := flag.NewFlagSet("executor-dashboard", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	addr := flags.String("addr", "127.0.0.1:8081", "dashboard listen address")
	databasePath := flags.String("database", "", "path to the fileexplorer SQLite database (required)")
	rootPath := flags.String("root", "", "filesystem root for paths in the database (required)")
	instructionsPath := flags.String("instructions", "instructions.md", "fallback instructions file")
	gatewayURL := flags.String("gateway-url", "http://127.0.0.1:18789", "OpenClaw Gateway base URL")
	agentID := flags.String("agent-id", "main", "OpenClaw agent ID")
	verifyTLS := flags.Bool("verify-tls", false, "verify the Gateway TLS certificate and hostname (default: skipped)")
	logLevel := flags.String("log-level", "info", "logging verbosity: trace, debug, info, warn, or error")
	if err := flags.Parse(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() > 0 {
		return fmt.Errorf("unexpected positional arguments: %s", strings.Join(flags.Args(), " "))
	}
	if strings.TrimSpace(*databasePath) == "" {
		return errors.New("-database is required")
	}
	if strings.TrimSpace(*rootPath) == "" {
		return errors.New("-root is required")
	}

	parsedLogLevel, err := executor.ParseLogLevel(*logLevel)
	if err != nil {
		return err
	}

	app, err := newDashboard(executor.Config{
		DatabasePath:     *databasePath,
		RootPath:         *rootPath,
		InstructionsPath: *instructionsPath,
		GatewayURL:       *gatewayURL,
		AgentID:          *agentID,
		AuthToken:        os.Getenv("OPENCLAW_AUTH_TOKEN"),
		HTTPClient:       executor.NewGatewayHTTPClient(*verifyTLS),
	}, parsedLogLevel)
	if err != nil {
		return fmt.Errorf("configure executor dashboard: %w", err)
	}
	defer app.Close()

	server := &http.Server{Addr: normalizeAddress(*addr), Handler: app}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		log.Printf("executor dashboard listening on %s", server.Addr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("dashboard server: %w", err)
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown dashboard: %w", err)
		}
		return nil
	}
}

func normalizeAddress(addr string) string {
	if addr == "" {
		return "127.0.0.1:8081"
	}
	if strings.Contains(addr, ":") {
		return addr
	}
	return ":" + addr
}
