package tasks

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func checkPending(t *testing.T, queue *WorkQueue, want []string) {
	t.Helper()
	got := queue.Pending()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Pending() = %#v, want %#v", got, want)
	}
}

func TestWorkQueueAppendsNewBatchSortedByBinaryPathOrder(t *testing.T) {
	queue := NewWorkQueue()
	if !queue.Empty() {
		t.Fatal("new work queue is not empty")
	}

	added, err := queue.Enqueue([]string{"zeta.txt", "alpha.txt", "dir/beta.txt"})
	if err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	if added != 3 {
		t.Fatalf("Enqueue() added = %d, want 3", added)
	}
	checkPending(t, queue, []string{"alpha.txt", "dir/beta.txt", "zeta.txt"})
	if queue.Len() != 3 {
		t.Fatalf("Len() = %d, want 3", queue.Len())
	}

	for i, want := range []string{"alpha.txt", "dir/beta.txt", "zeta.txt"} {
		got, ok := queue.Pop()
		if !ok {
			t.Fatalf("Pop() %d ok = false, want true", i)
		}
		if got != want {
			t.Fatalf("Pop() %d = %q, want %q", i, got, want)
		}
	}
	if got, ok := queue.Pop(); ok || got != "" {
		t.Fatalf("Pop() on empty queue = (%q, true), want (\"\", false)", got)
	}
	if !queue.Empty() {
		t.Fatal("work queue is not empty after draining")
	}
}

func TestWorkQueueDuplicateSnapshotAppendsNothing(t *testing.T) {
	snapshot := []string{"zeta.txt", "dir/b.txt"}
	queue := NewWorkQueue()
	if added, err := queue.Enqueue(snapshot); err != nil || added != 2 {
		t.Fatalf("first Enqueue() = (%d, %v), want (2, nil)", added, err)
	}
	if added, err := queue.Enqueue(snapshot); err != nil || added != 0 {
		t.Fatalf("duplicate Enqueue() = (%d, %v), want (0, nil)", added, err)
	}
	if added, err := queue.Enqueue([]string{"dir/b.txt", "zeta.txt", "a.txt"}); err != nil || added != 1 {
		t.Fatalf("partial-overlap Enqueue() = (%d, %v), want (1, nil)", added, err)
	}
	checkPending(t, queue, []string{"dir/b.txt", "zeta.txt", "a.txt"})
}

func TestWorkQueueNewNamesSortingBeforePendingStayQueuedLast(t *testing.T) {
	queue := NewWorkQueue()
	if added, err := queue.Enqueue([]string{"b.txt", "z.txt"}); err != nil || added != 2 {
		t.Fatalf("first Enqueue() = (%d, %v), want (2, nil)", added, err)
	}
	// "a.txt" sorts before every pending item, but it was discovered later,
	// so it must stay last and preserve the FIFO order of the first batch.
	if added, err := queue.Enqueue([]string{"a.txt"}); err != nil || added != 1 {
		t.Fatalf("second Enqueue() = (%d, %v), want (1, nil)", added, err)
	}
	checkPending(t, queue, []string{"b.txt", "z.txt", "a.txt"})

	if got, ok := queue.Pop(); !ok || got != "b.txt" {
		t.Fatalf("Pop() = (%q, %v), want (\"b.txt\", true)", got, ok)
	}
}

func TestWorkQueueTreatsNormalizedPathsAsOneIdentity(t *testing.T) {
	queue := NewWorkQueue()
	// Different raw spellings of the same relative path.
	if added, err := queue.Enqueue([]string{"./zeta.txt", "dir/../zeta.txt"}); err != nil || added != 1 {
		t.Fatalf("Enqueue() = (%d, %v), want (1, nil)", added, err)
	}
	if added, err := queue.Enqueue([]string{"zeta.txt"}); err != nil || added != 0 {
		t.Fatalf("normalized duplicate Enqueue() = (%d, %v), want (0, nil)", added, err)
	}
	checkPending(t, queue, []string{"zeta.txt"})
}

func TestWorkQueueRejectsPathsOutsideTheRoot(t *testing.T) {
	queue := NewWorkQueue()
	if added, err := queue.Enqueue([]string{"ok.txt"}); err != nil || added != 1 {
		t.Fatalf("seed Enqueue() = (%d, %v), want (1, nil)", added, err)
	}

	for _, bad := range []string{
		"",
		"abc\x00def",
		"/etc/passwd",
		"//host/share.txt",
		"../outside.txt",
		"a/../../outside.txt",
	} {
		added, err := queue.Enqueue([]string{"ok.txt", bad})
		if err == nil {
			t.Errorf("Enqueue(%q) error = nil, want error", bad)
		} else if !strings.Contains(err.Error(), fmt.Sprintf("%q", bad)) {
			t.Errorf("Enqueue(%q) error = %v, want the offending path named", bad, err)
		}
		if added != 0 {
			t.Errorf("Enqueue(%q) added = %d, want 0", bad, added)
		}
	}
	// The failed batches must not have mutated the queue.
	checkPending(t, queue, []string{"ok.txt"})
	if queue.Len() != 1 {
		t.Fatalf("Len() = %d, want 1 after rejected batches", queue.Len())
	}
}
