package tasks

import (
	"errors"
	"testing"

	"github.com/dev1-alexwei-net/guava/conversations"
)

func TestTaskProviderReportsErrorsWhileProducingTasks(t *testing.T) {
	provider := erroringProvider{cause: errors.New("manifest refresh failed")}

	var visited int
	err := provider.Tasks(func(conversations.Task) bool {
		visited++
		return true
	})
	if !errors.Is(err, provider.cause) {
		t.Fatalf("Tasks() error = %v, want %v", err, provider.cause)
	}
	if visited != 0 {
		t.Fatalf("callback visited %d tasks, want none when production fails", visited)
	}
}

// erroringProvider is a test provider whose task source fails while
// producing tasks, exercising the error path of the TaskProvider seam.
type erroringProvider struct {
	cause error
}

func (p erroringProvider) Tasks(func(conversations.Task) bool) error {
	return p.cause
}
