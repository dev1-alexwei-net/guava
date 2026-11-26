package conversations

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/dev1-alexwei-net/guava/tasks"
)

// Options supplies optional dependencies useful for embedding and deterministic
// tests. A nil token waiter uses the service's rate-limited token bucket.
type Options struct {
	TokenWaiter TokenWaiter
	RetryWait   RetryWait
}

// Service is the central conversation runner and session-summary owner.
type Service struct {
	config             Config
	mu                 sync.Mutex
	status             string
	startedAt          time.Time
	conversationTokens TokenWaiter
	retryWait          RetryWait
}

// New creates a conversation service with default rate limiting and retry timing.
func New(config Config) (*Service, error) { return NewWithOptions(config, Options{}) }

// NewWithOptions creates a conversation service with optional injected timing
// and token dependencies.
func NewWithOptions(config Config, options Options) (*Service, error) {
	config.GatewayURL = strings.TrimRight(strings.TrimSpace(config.GatewayURL), "/")
	parsed, err := url.Parse(config.GatewayURL)
	if err != nil || parsed == nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("invalid Gateway URL")
	}
	if strings.TrimSpace(config.AgentID) == "" {
		return nil, errors.New("agent ID is required")
	}
	if config.HTTPClient == nil {
		config.HTTPClient = http.DefaultClient
	}
	tokens := options.TokenWaiter
	if tokens == nil {
		tokens = newConversationTokenBucket()
	}
	return &Service{config: config, status: StatusIdle, conversationTokens: tokens, retryWait: options.RetryWait}, nil
}

// Run consumes tasks and prompts in provider order and runs each conversation
// to its completion marker. The provider owns task discovery; this package
// does not know how tasks are stored or enumerated.
func (s *Service) Run(ctx context.Context, provider tasks.TaskProvider) (err error) {
	if s == nil {
		return errors.New("conversation service is nil")
	}
	if ctx == nil {
		return errors.New("context is nil")
	}
	if provider == nil {
		return errors.New("task provider is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.startRun()
	defer func() { s.finishRun(err) }()

	logger := s.config.Logger
	runStarted := time.Now()
	logAttrs(logger, ctx, slog.LevelInfo, "Executor run started",
		slog.String("agent_id", s.config.AgentID), slog.String("gateway_url", s.config.GatewayURL), slog.String("subdirectory", s.config.Subdirectory))
	defer func() {
		attrs := []slog.Attr{slog.Duration("elapsed", time.Since(runStarted))}
		if err != nil {
			attrs = append(attrs, slog.Any("error", err))
			logAttrs(logger, ctx, slog.LevelError, "Executor run failed", attrs...)
			return
		}
		logAttrs(logger, ctx, slog.LevelInfo, "Executor run completed", attrs...)
	}()

	coordinator, err := NewCoordinator(provider, func(ctx context.Context, task tasks.Task) error {
		fileStarted := time.Now()
		if err := s.RunTask(ctx, task); err != nil {
			logAttrs(logger, ctx, slog.LevelError, "Indexed file processing failed", slog.String("path", task.ID()), slog.Duration("elapsed", time.Since(fileStarted)), slog.Any("error", err))
			return fmt.Errorf("process task %q: %w", task.ID(), err)
		}
		logAttrs(logger, ctx, slog.LevelInfo, "Indexed file processing completed", slog.String("path", task.ID()), slog.Duration("elapsed", time.Since(fileStarted)))
		return nil
	})
	if err != nil {
		return err
	}
	return coordinator.Run(ctx)
}

// RunTask obtains a task's prompt and runs its conversation to completion.
func (s *Service) RunTask(ctx context.Context, task tasks.Task) error {
	if s == nil {
		return errors.New("conversation service is nil")
	}
	if ctx == nil {
		return errors.New("context is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	prompt, err := task.Prompt()
	if err != nil {
		return fmt.Errorf("build task prompt: %w", err)
	}
	return s.RunPrompt(ctx, task.ID(), prompt)
}
