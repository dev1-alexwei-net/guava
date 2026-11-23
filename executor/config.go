// Package executor runs OpenClaw Responses conversations over files indexed by fileexplorer.
package executor

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// Config describes the file source and OpenClaw Gateway used by an Executor.
type Config struct {
	// DatabasePath is the fileexplorer SQLite database containing the file index.
	DatabasePath string

	// RootPath is the filesystem root that corresponds to paths stored in the index.
	RootPath string

	// InstructionsPath names the instructions.md file supplied to each file conversation.
	InstructionsPath string

	// GatewayURL is the OpenClaw Gateway base URL, without the /v1/responses path.
	GatewayURL string

	// AgentID selects the OpenClaw agent that receives each request.
	AgentID string

	// AuthToken is an optional Gateway bearer token. Leave it empty when Gateway
	// authentication is supplied by another transport, such as a trusted proxy.
	AuthToken string

	// HTTPClient overrides the default HTTP client. It is useful for custom
	// transports, including tests that use a local mock Gateway.
	HTTPClient *http.Client
}

// Executor holds validated configuration for a file-processing run.
type Executor struct {
	config Config
}

// New validates config and returns an Executor.
func New(config Config) (*Executor, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}

	config.GatewayURL = strings.TrimRight(strings.TrimSpace(config.GatewayURL), "/")
	if config.HTTPClient == nil {
		config.HTTPClient = http.DefaultClient
	}

	return &Executor{config: config}, nil
}

// Validate reports missing required values or an invalid Gateway base URL.
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
		{name: "agent ID", value: c.AgentID},
	}
	for _, field := range required {
		if strings.TrimSpace(field.value) == "" {
			return fmt.Errorf("%s is required", field.name)
		}
	}

	parsed, err := url.Parse(strings.TrimSpace(c.GatewayURL))
	if err != nil {
		return fmt.Errorf("invalid Gateway URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("Gateway URL must use http or https")
	}
	if parsed.Hostname() == "" {
		return fmt.Errorf("Gateway URL must include a host")
	}
	if parsed.User != nil {
		return fmt.Errorf("Gateway URL must not contain user information")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("Gateway URL must not contain a query or fragment")
	}
	return nil
}
