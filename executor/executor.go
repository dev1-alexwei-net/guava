package executor

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/dev1-alexwei-net/guava/conversations"
	"github.com/dev1-alexwei-net/guava/tasks"
)

const completionMarker = "<complete />"
const continueMessage = "Continue. Remember to output the completion marker <complete /> only if you have finished."

// conversationSessionKey remains as a compatibility helper for package callers.
func conversationSessionKey(agentID, taskID string) string {
	if agentID == "" || taskID == "" {
		return ""
	}
	return "agent:" + agentID + ":guava:" + taskID
}

func containsCompletionMarker(text string) bool { return strings.Contains(text, completionMarker) }

// runFileConversation preserves the package's historical helper while routing
// all conversation work through the conversations service.
func (e *Executor) runFileConversation(ctx context.Context, input string) error {
	return e.runFileConversationForFile(ctx, "", input)
}

func (e *Executor) runFileConversationForFile(ctx context.Context, taskID, input string) error {
	if e == nil {
		return errors.New("executor is nil")
	}
	service, err := conversations.NewWithOptions(conversationConfig(e.config), conversations.Options{
		TokenWaiter: e.conversationTokenBucket(), RetryWait: e.retryWait,
	})
	if err != nil {
		return err
	}
	return service.RunPrompt(ctx, taskID, input)
}

func (e *Executor) conversationTokenBucket() *tokenBucket {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.conversationTokens == nil {
		e.conversationTokens = newConversationTokenBucket()
	}
	return e.conversationTokens
}

// These wire shapes remain local aliases for compatibility with executor tests
// and callers that historically used package-private helpers.
type responsesRequest struct {
	Model              string `json:"model"`
	Input              string `json:"input"`
	Stream             bool   `json:"stream"`
	PreviousResponseID string `json:"previous_response_id,omitempty"`
}
type responsesAPIResponse struct {
	ID     string                `json:"id"`
	Status string                `json:"status"`
	Output []responsesOutputItem `json:"output"`
}
type responsesOutputItem struct {
	Type    string                 `json:"type"`
	Role    string                 `json:"role"`
	Content []responsesContentPart `json:"content"`
}
type responsesContentPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}
type agentResponse struct{ id, outputText string }
type responsesHTTPError struct {
	statusCode int
	message    string
}

func (e *responsesHTTPError) Error() string {
	if e.message == "" {
		return "OpenResponses returned an error"
	}
	return e.message
}

type responsesClient struct {
	config Config
	tokens TokenWaiter
}
type TokenWaiter = conversations.TokenWaiter

func (c responsesClient) create(ctx context.Context, input, sessionKey, previousID string) (agentResponse, error) {
	return c.createObserved(ctx, input, sessionKey, previousID, "", 0)
}
func (c responsesClient) createObserved(ctx context.Context, input, sessionKey, previousID, taskID string, turn int) (agentResponse, error) {
	client := conversations.NewClient(conversationConfig(c.config), c.tokens)
	response, err := client.CreateObserved(ctx, input, sessionKey, previousID, taskID, turn)
	if err != nil {
		return agentResponse{}, err
	}
	return agentResponse{id: response.ID, outputText: response.Text}, nil
}

type failedTaskProvider struct{ err error }

func (p failedTaskProvider) Tasks(func(tasks.Task) bool) error { return p.err }

type closingTaskProvider struct{ provider *tasks.FileProvider }

func (p closingTaskProvider) Tasks(fn func(tasks.Task) bool) (err error) {
	defer func() { err = errors.Join(err, p.provider.Close()) }()
	return p.provider.Tasks(fn)
}

// Run preserves the existing executor entry point while delegating task
// processing, conversation lifecycle, and summaries to the conversations
// service.
func (e *Executor) Run(ctx context.Context) error {
	if e == nil {
		return errors.New("executor is nil")
	}
	if ctx == nil {
		return errors.New("context is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if e.conversationService == nil {
		return errors.New("conversation service is not configured")
	}
	provider, err := tasks.NewFileProvider(ctx, tasks.FileProviderConfig{
		DatabasePath:     e.config.DatabasePath,
		RootPath:         e.config.RootPath,
		InstructionsPath: e.config.InstructionsPath,
		FileExplorerURL:  e.config.FileExplorerURL,
		Subdirectory:     e.config.Subdirectory,
		HTTPClient:       e.config.HTTPClient,
		Logger:           e.config.Logger,
	})
	if err != nil {
		return e.conversationService.Run(ctx, failedTaskProvider{err: err})
	}
	return e.conversationService.Run(ctx, closingTaskProvider{provider: provider})
}

const (
	StatusIdle      = conversations.StatusIdle
	StatusRunning   = conversations.StatusRunning
	StatusCompleted = conversations.StatusCompleted
	StatusFailed    = conversations.StatusFailed
	StatusStopped   = conversations.StatusStopped
)

type Summary = conversations.Summary

// SessionSummary returns the conversation service's session snapshot.
func (e *Executor) SessionSummary() Summary {
	if e == nil || e.conversationService == nil {
		return Summary{Status: StatusIdle}
	}
	return e.conversationService.SessionSummary()
}

const (
	conversationTokenCapacity       = 10
	conversationTokenRefillInterval = time.Minute
)

// tokenBucket is a thread-safe, in-memory token bucket. It starts full and
// replenishes one token per refill interval up to its configured capacity.
type tokenBucket struct {
	mu          sync.Mutex
	capacity    int
	tokens      float64
	refillEvery time.Duration
	lastRefill  time.Time
	now         func() time.Time
}

func newConversationTokenBucket() *tokenBucket {
	return newTokenBucket(conversationTokenCapacity, conversationTokenRefillInterval, time.Now)
}

func newTokenBucket(capacity int, refillEvery time.Duration, now func() time.Time) *tokenBucket {
	if capacity < 1 {
		panic("token bucket capacity must be positive")
	}
	if refillEvery <= 0 {
		panic("token bucket refill interval must be positive")
	}
	if now == nil {
		now = time.Now
	}
	createdAt := now()
	return &tokenBucket{
		capacity:    capacity,
		tokens:      float64(capacity),
		refillEvery: refillEvery,
		lastRefill:  createdAt,
		now:         now,
	}
}

// Wait blocks until one token is available and consumes it. Cancellation
// interrupts a wait so shutdown does not leave a conversation stuck.
func (b *tokenBucket) Wait(ctx context.Context) error {
	if b == nil {
		return errors.New("token bucket is nil")
	}
	if ctx == nil {
		return errors.New("context is nil")
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		acquired, waitFor := b.tryTake()
		if acquired {
			return nil
		}
		timer := time.NewTimer(waitFor)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// tryTake refills from elapsed time and consumes a token when one is ready.
// It returns the duration until the next token otherwise.
func (b *tokenBucket) tryTake() (bool, time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := b.now()
	if elapsed := now.Sub(b.lastRefill); elapsed > 0 {
		b.tokens += float64(elapsed) / float64(b.refillEvery)
		if b.tokens > float64(b.capacity) {
			b.tokens = float64(b.capacity)
		}
		b.lastRefill = now
	}
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	waitFor := time.Duration((1 - b.tokens) * float64(b.refillEvery))
	if waitFor < time.Nanosecond {
		waitFor = time.Nanosecond
	}
	return false, waitFor
}
