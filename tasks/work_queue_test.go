package tasks

import (
	"reflect"
	"testing"
)

func TestWorkQueueKeepsSeededAndNewPathsSorted(t *testing.T) {
	queue := NewWorkQueue()
	if added, err := queue.Seed([]string{"zeta.txt", "middle.txt", "alpha.txt", "middle.txt"}); err != nil {
		t.Fatalf("Seed() error = %v", err)
	} else if added != 3 {
		t.Fatalf("Seed() added = %d, want 3 unique paths", added)
	}
	want := []string{"alpha.txt", "middle.txt", "zeta.txt"}
	if got := queue.Pending(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Pending() after Seed = %#v, want %#v", got, want)
	}

	if added, err := queue.EnqueueNext([]string{"new.txt", "beta.txt", "zeta.txt"}); err != nil {
		t.Fatalf("EnqueueNext() error = %v", err)
	} else if added != 2 {
		t.Fatalf("EnqueueNext() added = %d, want 2 new paths", added)
	}
	want = []string{"alpha.txt", "beta.txt", "middle.txt", "new.txt", "zeta.txt"}
	if got := queue.Pending(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Pending() after merge = %#v, want sorted merged paths %#v", got, want)
	}

	if added, err := queue.EnqueueNext([]string{"aardvark.txt"}); err != nil {
		t.Fatalf("EnqueueNext() for a later discovery error = %v", err)
	} else if added != 1 {
		t.Fatalf("EnqueueNext() for a later discovery added = %d, want 1", added)
	}
	want = []string{"aardvark.txt", "alpha.txt", "beta.txt", "middle.txt", "new.txt", "zeta.txt"}
	if got := queue.Pending(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Pending() after later discovery = %#v, want sorted merged paths %#v", got, want)
	}
}
