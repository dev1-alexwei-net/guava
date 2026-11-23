package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/dev1-alexwei-net/guava/executor"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "executor:", err)
		os.Exit(1)
	}
}

func run() error {
	flags := flag.NewFlagSet("executor", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)

	databasePath := flags.String("database", "", "path to the fileexplorer SQLite database (required)")
	rootPath := flags.String("root", "", "filesystem root for paths in the database (required)")
	instructionsPath := flags.String("instructions", "instructions.md", "fallback instructions file when no indexed ancestor instructions.md exists")
	gatewayURL := flags.String("gateway-url", "http://127.0.0.1:18789", "OpenClaw Gateway base URL")
	agentID := flags.String("agent-id", "main", "OpenClaw agent ID")
	verifyTLS := flags.Bool("verify-tls", false, "verify the Gateway TLS certificate and hostname (default: skipped)")
	logLevel := flags.String("log-level", "info", "logging verbosity: trace, debug, info, warn, or error")

	flags.Usage = func() {
		fmt.Fprintf(flags.Output(), "Run an OpenClaw executor conversation for each indexed file.\n\nUsage: %s [flags]\n\nFlags:\n", os.Args[0])
		flags.PrintDefaults()
	}

	if err := flags.Parse(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() > 0 {
		return fmt.Errorf("unexpected positional arguments: %s", strings.Join(flags.Args(), " "))
	}
	parsedLogLevel, err := parseLogLevel(*logLevel)
	if err != nil {
		return err
	}

	runner, err := executor.New(executor.Config{
		DatabasePath:     *databasePath,
		RootPath:         *rootPath,
		InstructionsPath: *instructionsPath,
		GatewayURL:       *gatewayURL,
		AgentID:          *agentID,
		AuthToken:        os.Getenv("OPENCLAW_AUTH_TOKEN"),
		HTTPClient:       newGatewayHTTPClient(*verifyTLS),
		Logger:           newLogger(os.Stderr, parsedLogLevel),
	})
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := runner.Run(ctx); err != nil {
		return fmt.Errorf("run failed: %w", err)
	}
	return nil
}

func parseLogLevel(value string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "trace":
		return executor.LevelTrace, nil
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("invalid log level %q (want trace, debug, info, warn, or error)", value)
	}
}

func newLogger(output io.Writer, level slog.Level) *slog.Logger {
	return slog.New(slog.NewTextHandler(output, &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(_ []string, attr slog.Attr) slog.Attr {
			if attr.Key == slog.LevelKey && attr.Value.Kind() == slog.KindAny {
				if actualLevel, ok := attr.Value.Any().(slog.Level); ok && actualLevel == executor.LevelTrace {
					return slog.String(slog.LevelKey, "TRACE")
				}
			}
			return attr
		},
	}))
}

func newGatewayHTTPClient(verifyTLS bool) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	tlsConfig := transport.TLSClientConfig
	if tlsConfig == nil {
		tlsConfig = &tls.Config{}
	} else {
		tlsConfig = tlsConfig.Clone()
	}
	tlsConfig.InsecureSkipVerify = !verifyTLS // #nosec G402 -- verification is an explicit CLI opt-in.
	transport.TLSClientConfig = tlsConfig
	return &http.Client{Transport: transport}
}
