package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
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

	runner, err := executor.New(executor.Config{
		DatabasePath:     *databasePath,
		RootPath:         *rootPath,
		InstructionsPath: *instructionsPath,
		GatewayURL:       *gatewayURL,
		AgentID:          *agentID,
		AuthToken:        os.Getenv("OPENCLAW_AUTH_TOKEN"),
		HTTPClient:       newGatewayHTTPClient(*verifyTLS),
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
