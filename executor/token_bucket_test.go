package executor

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestTokenBucketStartsFullAndRefillsAtConfiguredRate(t *testing.T) {
	currentTime := time.Unix(1_000, 0)
	bucket := newTokenBucket(10, time.Minute, func() time.Time { return currentTime })

	for n := 0; n < 10; n++ {
		if acquired, _ := bucket.tryTake(); !acquired {
			t.Fatalf("tryTake() failed for initial token %d", n+1)
		}
	}
	if acquired, waitFor := bucket.tryTake(); acquired || waitFor != time.Minute {
		t.Fatalf("tryTake() with empty bucket = (%v, %s), want (false, 1m)", acquired, waitFor)
	}

	currentTime = currentTime.Add(59 * time.Second)
	if acquired, waitFor := bucket.tryTake(); acquired || waitFor != time.Second {
		t.Fatalf("tryTake() after 59s = (%v, %s), want (false, 1s)", acquired, waitFor)
	}
	currentTime = currentTime.Add(time.Second)
	if acquired, _ := bucket.tryTake(); !acquired {
		t.Fatal("tryTake() failed after one refill interval")
	}

	// Refill over many intervals, but never exceed the configured capacity.
	currentTime = currentTime.Add(20 * time.Minute)
	for n := 0; n < 10; n++ {
		if acquired, _ := bucket.tryTake(); !acquired {
			t.Fatalf("tryTake() failed for refilled token %d", n+1)
		}
	}
	if acquired, _ := bucket.tryTake(); acquired {
		t.Fatal("tryTake() acquired more than the bucket capacity")
	}
}

func TestTokenBucketConcurrentAcquisitionDoesNotOverdraw(t *testing.T) {
	bucket := newTokenBucket(3, time.Hour, time.Now)
	var acquired atomic.Int32
	var wait sync.WaitGroup
	for n := 0; n < 100; n++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if ok, _ := bucket.tryTake(); ok {
				acquired.Add(1)
			}
		}()
	}
	wait.Wait()
	if got := acquired.Load(); got != 3 {
		t.Fatalf("successful concurrent acquisitions = %d, want 3", got)
	}
}

func TestTokenBucketWaitHonorsCanceledContext(t *testing.T) {
	bucket := newTokenBucket(1, time.Hour, time.Now)
	if acquired, _ := bucket.tryTake(); !acquired {
		t.Fatal("failed to consume initial token")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := bucket.Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait() error = %v, want context.Canceled", err)
	}
}

func TestTokenBucketWaitsUntilRefilled(t *testing.T) {
	bucket := newTokenBucket(1, 25*time.Millisecond, time.Now)
	if acquired, _ := bucket.tryTake(); !acquired {
		t.Fatal("failed to consume initial token")
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := bucket.Wait(ctx); err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
}
