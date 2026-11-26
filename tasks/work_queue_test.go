package tasks

import (
	"reflect"
	"testing"
)

func TestWorkQueueSeedKeepsSeededOrderAheadOfNewBatches(t *testing.T) {
	queue := NewWorkQueue()
	if added, err := queue.Seed([]string{"zeta.txt", "alpha.txt"}); err != nil || added != 2 {
		t.Fatalf("Seed() = (%d, %v), want (2, nil)", added, err)
	}
	checkPending(t, queue, []string{"zeta.txt", "alpha.txt"})

	// A later-discovered file that sorts before every seeded item stays
	// behind them: seeded work keeps its position ahead of new batches.
	if added, err := queue.Enqueue([]string{"beta.txt", "alpha.txt"}); err != nil || added != 1 {
		t.Fatalf("Enqueue() = (%d, %v), want (1, nil)", added, err)
	}
	checkPending(t, queue, []string{"zeta.txt", "alpha.txt", "beta.txt"})
}

func TestWorkQueueSeedValidatesPathsAndStaysEmptyOnFailure(t *testing.T) {
	queue := NewWorkQueue()
	for _, bad := range []string{"", "/absolute.txt", "../outside.txt"} {
		if added, err := queue.Seed([]string{bad}); err == nil {
			t.Errorf("Seed(%q) succeeded, want validation error", bad)
		} else if added != 0 {
			t.Errorf("Seed(%q) added = %d, want 0", bad, added)
		}
	}
	if got := queue.Len(); got != 0 {
		t.Fatalf("Len() = %d after rejected seeds, want 0", got)
	}

	// Seed initializes an empty queue only; a second seed is rejected and the
	// first seed's order is preserved.
	if added, err := queue.Seed([]string{"a.txt"}); err != nil || added != 1 {
		t.Fatalf("Seed() on empty queue = (%d, %v), want (1, nil)", added, err)
	}
	if _, err := queue.Seed([]string{"b.txt"}); err == nil {
		t.Error("second Seed() succeeded, want error for a queue with pending work")
	}
	checkPending(t, queue, []string{"a.txt"})
}

func checkPending(t *testing.T, queue *WorkQueue, want []string) {
	t.Helper()
	got := queue.Pending()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Pending() = %#v, want %#v", got, want)
	}
}
