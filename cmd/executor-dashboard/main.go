// Command executor-dashboard serves a small web interface for controlling executor runs.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/dev1-alexwei-net/guava/conversations"
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
	host := flags.String("host", envOrDefault("HOST", "127.0.0.1"), "bind host; defaults to $HOST or 127.0.0.1")
	port := flags.String("port", envOrDefault("PORT", "8081"), "bind port; defaults to $PORT or 8081")
	addr := flags.String("addr", "", "deprecated: complete listen address or port; overrides -host and -port")
	databasePath := flags.String("database", "", "path to the fileexplorer SQLite database (required)")
	sessionStorePath := flags.String("session-store", "", "path to the task-session metadata SQLite database (defaults to -database + .sessions.sqlite)")
	rootPath := flags.String("root", "", "filesystem root for paths in the database (required)")
	instructionsPath := flags.String("instructions", "", "fallback instructions file")
	gatewayURL := flags.String("gateway-url", "http://127.0.0.1:18789", "OpenClaw Gateway base URL")
	fileExplorerURL := flags.String("fileexplorer-url", "http://127.0.0.1:8080", "fileexplorer base URL for refreshing the file index")
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
	if *instructionsPath != "" {
		return errors.New("-instructions is deprecated")
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
		FileExplorerURL:  *fileExplorerURL,
		AgentID:          *agentID,
		AuthToken:        os.Getenv("OPENCLAW_AUTH_TOKEN"),
		HTTPClient:       conversations.NewGatewayHTTPClient(*verifyTLS),
		CLIArgs:          strings.Join(os.Args, " "),
	}, parsedLogLevel, strings.TrimSpace(*sessionStorePath))
	if err != nil {
		return fmt.Errorf("configure executor dashboard: %w", err)
	}
	defer app.Close()

	listen := net.JoinHostPort(*host, *port)
	if *addr != "" {
		listen = normalizeAddress(*addr)
	}
	server := &http.Server{Addr: listen, Handler: app}
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

func envOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func normalizeAddress(addr string) string {
	addr = strings.TrimSpace(addr)
	if strings.Contains(addr, ":") {
		return addr
	}
	return net.JoinHostPort("", addr)
}
