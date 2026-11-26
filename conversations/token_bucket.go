package conversations

import (
	"context"
	"errors"
	"sync"
	"time"
)

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
