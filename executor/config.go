// Package executor runs OpenClaw Responses conversations over files indexed by fileexplorer.
package executor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/dev1-alexwei-net/guava/conversations"
)

// Config describes the file source and OpenClaw Gateway used by an Executor.
type Config struct {
	// DatabasePath is the fileexplorer SQLite database containing the file index.
	DatabasePath string

	// RootPath is the filesystem root that corresponds to paths stored in the index.
	RootPath string

	// Subdirectory optionally scopes a run to the files under one folder
	// inside RootPath. Paths are relative and slash-separated, matching the
	// index; the match is by exact folder prefix, so a sibling folder that
	// merely shares a name prefix is out of scope. Leave it empty to keep the
	// whole-root behavior. Instruction resolution is unaffected and still
	// sees the full index.
	Subdirectory string

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

// Executor joins the file-backed task source to a conversations service for
// each session. Task discovery belongs to tasks; turn coordination and session
// summaries belong to conversations.
type Executor struct {
	config Config

	conversationTokens *tokenBucket
	retryWait          func(context.Context, time.Duration) error

	mu                  sync.Mutex
	conversationService *conversations.Service
}

// New validates config and returns an Executor.
func New(config Config) (*Executor, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}

	config.GatewayURL = strings.TrimRight(strings.TrimSpace(config.GatewayURL), "/")
	config.FileExplorerURL = strings.TrimRight(strings.TrimSpace(config.FileExplorerURL), "/")
	config.Subdirectory = normalizeSubdirectory(config.Subdirectory)
	if config.HTTPClient == nil {
		config.HTTPClient = http.DefaultClient
	}

	return newExecutor(config)
}

// WithSubdirectory returns an executor identical to e except that its run
// is scoped to subdirectory: a folder inside the configured filesystem
// root, or empty for the whole root. The override is normalized and
// validated exactly like the startup -subdirectory flag, and every other
// field — in particular DatabasePath and RootPath — is preserved
// unchanged, so the derived executor always targets the same fileexplorer
// database and root as e.
//
// The returned executor is independent of e: e keeps its configured
// subdirectory and the two share no run state. Because the derived
// executor is the one that runs, its session summary reports the
// subdirectory, and its CLI args carry the -subdirectory flag when a
// command line was configured, so the summary matches the run that
// actually happens.
func (e *Executor) WithSubdirectory(subdirectory string) (*Executor, error) {
	if e == nil {
		return nil, errors.New("executor is nil")
	}
	config := e.config
	config.Subdirectory = normalizeSubdirectory(subdirectory)
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if config.Subdirectory != "" {
		if base := strings.TrimSpace(config.CLIArgs); base != "" {
			config.CLIArgs = base + " -subdirectory " + config.Subdirectory
		}
	}
	return newExecutor(config)
}

// newExecutor wires the session manager to the conversation implementation.
// Keeping construction here ensures the base and subdirectory-scoped executor
// use the same conversation dependencies and lifecycle owner.
func newExecutor(config Config) (*Executor, error) {
	service, err := conversations.New(conversationConfig(config))
	if err != nil {
		return nil, err
	}
	return &Executor{config: config, conversationTokens: newConversationTokenBucket(), conversationService: service}, nil
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
	if err := validateSubdirectory(c.Subdirectory); err != nil {
		return err
	}
	return nil
}

// validateSubdirectory reports when a non-empty subdirectory is not a valid
// folder inside the filesystem root: it must be relative, stay inside the
// root after cleaning, and not be the root itself. An empty value is valid
// and keeps the whole-root behavior.
func validateSubdirectory(value string) error {
	if value = strings.TrimSpace(value); value == "" {
		return nil
	}
	if strings.IndexByte(value, 0) >= 0 {
		return fmt.Errorf("subdirectory must not contain NUL bytes")
	}
	if slash := filepath.ToSlash(value); slash[0] == '/' {
		return fmt.Errorf("subdirectory %q must be a folder inside the filesystem root", value)
	}
	if cleaned := path.Clean(filepath.ToSlash(value)); cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return fmt.Errorf("subdirectory %q must be a folder inside the filesystem root", value)
	}
	return nil
}

// normalizeSubdirectory returns the trimmed, cleaned, slash-separated form of
// a subdirectory. Empty values stay empty, which means the whole root.
func normalizeSubdirectory(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	return path.Clean(filepath.ToSlash(value))
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

func conversationConfig(config Config) conversations.Config {
	return conversations.Config{GatewayURL: config.GatewayURL, AgentID: config.AgentID, AuthToken: config.AuthToken, HTTPClient: config.HTTPClient, Logger: config.Logger, CLIArgs: config.CLIArgs, Subdirectory: config.Subdirectory}
}
