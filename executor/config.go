// Package executor runs OpenClaw Responses conversations over files indexed by fileexplorer.
package executor

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Config describes the file source and OpenClaw Gateway used by an Executor.
type Config struct {
	// DatabasePath is the fileexplorer SQLite database containing the file index.
	DatabasePath string

	// RootPath is the filesystem root that corresponds to paths stored in the index.
	RootPath string

	// InstructionsPath is the fallback instructions file used when the index has no
	// ancestor instructions.md for a target file.
	InstructionsPath string

	// GatewayURL is the OpenClaw Gateway base URL, without the /v1/responses path.
	GatewayURL string

	// FileExplorerURL is the fileexplorer base URL used to refresh the file
	// index, without the internal refresh path. Use a hostname reachable from
	// wherever the executor runs, such as the container name on a container
	// network; the local default works on loopback.
	FileExplorerURL string

	// AgentID selects the OpenClaw agent that receives each request.
	AgentID string

	// AuthToken is an optional Gateway bearer token. Leave it empty when Gateway
	// authentication is supplied by another transport, such as a trusted proxy.
	AuthToken string

	// HTTPClient overrides the default HTTP client. It is useful for custom
	// transports, including tests that use a local mock Gateway.
	HTTPClient *http.Client

	// Logger receives structured progress and Gateway request details. A nil
	// logger disables package logging.
	Logger *slog.Logger

	// CLIArgs is a raw dump of the command line the server was started with,
	// for example strings.Join(os.Args, " "). It is carried as-is in the
	// session summary and never parsed; leave it empty when the executor has
	// no command line to report.
	CLIArgs string
}

// Executor holds validated configuration for a file-processing run.
type Executor struct {
	config Config

	conversationTokens *tokenBucket
	retryWait          func(context.Context, time.Duration) error

	mu        sync.Mutex
	status    string
	startedAt time.Time
}

// New validates config and returns an Executor.
func New(config Config) (*Executor, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}

	config.GatewayURL = strings.TrimRight(strings.TrimSpace(config.GatewayURL), "/")
	config.FileExplorerURL = strings.TrimRight(strings.TrimSpace(config.FileExplorerURL), "/")
	if config.HTTPClient == nil {
		config.HTTPClient = http.DefaultClient
	}

	return &Executor{
		config:             config,
		conversationTokens: newConversationTokenBucket(),
		status:             StatusIdle,
	}, nil
}

// Validate reports missing required values or an invalid Gateway or
// fileexplorer base URL.
// Authentication is optional because a Gateway may use a trusted proxy or
// private no-auth ingress.
func (c Config) Validate() error {
	required := []struct {
		name  string
		value string
	}{
		{name: "database path", value: c.DatabasePath},
		{name: "filesystem root", value: c.RootPath},
		{name: "instructions path", value: c.InstructionsPath},
		{name: "Gateway URL", value: c.GatewayURL},
		{name: "fileexplorer URL", value: c.FileExplorerURL},
		{name: "agent ID", value: c.AgentID},
	}
	for _, field := range required {
		if strings.TrimSpace(field.value) == "" {
			return fmt.Errorf("%s is required", field.name)
		}
	}

	if err := validateBaseURL("Gateway URL", c.GatewayURL); err != nil {
		return err
	}
	if err := validateBaseURL("fileexplorer URL", c.FileExplorerURL); err != nil {
		return err
	}
	return nil
}

// validateBaseURL reports when value is not a usable base URL: the scheme
// must be http or https, a host must be present, and credentials, a query,
// and a fragment are not allowed.
func validateBaseURL(field, value string) error {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil {
		return fmt.Errorf("invalid %s: %w", field, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("%s must use http or https", field)
	}
	if parsed.Hostname() == "" {
		return fmt.Errorf("%s must include a host", field)
	}
	if parsed.User != nil {
		return fmt.Errorf("%s must not contain user information", field)
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("%s must not contain a query or fragment", field)
	}
	return nil
}
