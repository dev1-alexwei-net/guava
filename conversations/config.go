// Package conversations runs task prompts to completion through OpenResponses.
// It depends on the source-agnostic tasks package and never reads a task source
// or fileexplorer manifest directly.
package conversations

import (
	"context"
	"log/slog"
	"net/http"
	"time"
)

// Config configures OpenResponses and the session metadata for a conversation
// service. Task source configuration belongs to the caller that creates a
// tasks.TaskProvider.
type Config struct {
	GatewayURL   string
	AgentID      string
	AuthToken    string
	HTTPClient   *http.Client
	Logger       *slog.Logger
	CLIArgs      string
	Subdirectory string
}

// TokenWaiter is the rate-limiter seam used by the OpenResponses client.
type TokenWaiter interface{ Wait(context.Context) error }

// RetryWait allows callers to inject retry timing; a nil function uses a timer.
type RetryWait func(context.Context, time.Duration) error
