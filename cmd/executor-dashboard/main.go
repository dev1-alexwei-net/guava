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
	"sync"
	"syscall"
	"time"

	"github.com/dev1-alexwei-net/guava/conversations"
	"github.com/dev1-alexwei-net/guava/executor"
	"github.com/dev1-alexwei-net/guava/sessions"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	flags := flag.NewFlagSet("executor-dashboard", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	defaults := sessions.DefaultAccountSettings()
	host := flags.String("host", envOrDefault("HOST", defaults.Host), "bind host; defaults to $HOST or 127.0.0.1")
	port := flags.String("port", envOrDefault("PORT", defaults.Port), "bind port; defaults to $PORT or 8081")
	addr := flags.String("addr", "", "deprecated: complete listen address or port; overrides -host and -port")
	databasePath := flags.String("database", defaults.DatabasePath, "path to the fileexplorer SQLite database")
	sessionStorePath := flags.String("session-store", "", "path to the task-session metadata SQLite database (defaults to -database + .sessions.sqlite)")
	rootPath := flags.String("root", defaults.RootPath, "filesystem root for paths in the database")
	instructionsPath := flags.String("instructions", "", "fallback instructions file")
	gatewayURL := flags.String("gateway-url", defaults.GatewayURL, "OpenClaw Gateway base URL")
	fileExplorerURL := flags.String("fileexplorer-url", defaults.FileExplorerURL, "fileexplorer base URL for refreshing the file index")
	agentID := flags.String("agent-id", defaults.AgentID, "OpenClaw agent ID")
	subdirectory := flags.String("subdirectory", defaults.Subdirectory, "optional folder under -root to scope runs to")
	verifyTLS := flags.Bool("verify-tls", false, "verify the Gateway TLS certificate and hostname (default: skipped)")
	accountFlow := flags.Bool("account-flow", false, "open account settings on the first dashboard request")
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
	if *instructionsPath != "" {
		return errors.New("-instructions is deprecated")
	}

	parsedLogLevel, err := executor.ParseLogLevel(*logLevel)
	if err != nil {
		return err
	}

	config := executor.Config{
		DatabasePath:     *databasePath,
		RootPath:         *rootPath,
		Subdirectory:     *subdirectory,
		InstructionsPath: *instructionsPath,
		GatewayURL:       *gatewayURL,
		FileExplorerURL:  *fileExplorerURL,
		AgentID:          *agentID,
		AuthToken:        os.Getenv("OPENCLAW_AUTH_TOKEN"),
		HTTPClient:       conversations.NewGatewayHTTPClient(*verifyTLS),
		CLIArgs:          strings.Join(os.Args, " "),
	}
	initialSettings := sessions.AccountSettings{
		Host: *host, Port: *port, DatabasePath: *databasePath, RootPath: *rootPath,
		FileExplorerURL: *fileExplorerURL, GatewayURL: *gatewayURL,
		AgentID: *agentID, Subdirectory: *subdirectory,
	}
	app, err := newDashboardWithAccountSettings(config, parsedLogLevel, strings.TrimSpace(*sessionStorePath), initialSettings)
	if err != nil {
		return fmt.Errorf("configure executor dashboard: %w", err)
	}
	defer app.Close()

	listen := net.JoinHostPort(app.account.Host, app.account.Port)
	if *addr != "" {
		listen = normalizeAddress(*addr)
	}
	server := &http.Server{Addr: listen, Handler: startupLandingHandler(app, *accountFlow)}
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

func startupLandingHandler(next http.Handler, accountFlow bool) http.Handler {
	if !accountFlow {
		return next
	}
	var firstDashboardRequest sync.Once
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/" {
			first := false
			firstDashboardRequest.Do(func() { first = true })
			if first {
				location := "/settings"
				if r.URL.RawQuery != "" {
					location += "?" + r.URL.RawQuery
				}
				http.Redirect(w, r, location, http.StatusFound)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
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
