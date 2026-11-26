package tasks

import (
	"errors"
	"testing"

	"github.com/dev1-alexwei-net/guava/conversations"
)

func TestStaticProviderYieldsTasksInOrder(t *testing.T) {
	ids := []string{"alpha/a.txt", "beta/b.txt", "gamma/c.txt"}
	want := make([]conversations.Task, len(ids))
	for n, id := range ids {
		want[n] = NewTask(id, func() (string, error) {
			return "prompt: " + id, nil
		})
	}

	provider := conversations.NewStaticProvider(want...)
	var got []conversations.Task
	if err := provider.Tasks(func(task conversations.Task) bool {
		got = append(got, task)
		return true
	}); err != nil {
		t.Fatalf("Tasks() error = %v", err)
	}
	if len(got) != len(ids) {
		t.Fatalf("Tasks() visited %d tasks, want %d", len(got), len(ids))
	}
	for n := range ids {
		if got[n].ID() != ids[n] {
			t.Errorf("task %d ID() = %q, want %q", n, got[n].ID(), ids[n])
		}
	}
}

func TestStaticProviderYieldsRetrievablePrompts(t *testing.T) {
	provider := conversations.NewStaticProvider(
		NewTask("docs/a.md", func() (string, error) {
			return "work on docs/a.md", nil
		}),
		NewTask("docs/b.md", func() (string, error) {
			return "work on docs/b.md", nil
		}),
	)

	var prompts []string
	if err := provider.Tasks(func(task conversations.Task) bool {
		prompt, err := task.Prompt()
		if err != nil {
			t.Fatalf("Prompt() for %q error = %v", task.ID(), err)
		}
		prompts = append(prompts, prompt)
		return true
	}); err != nil {
		t.Fatalf("Tasks() error = %v", err)
	}
	want := []string{"work on docs/a.md", "work on docs/b.md"}
	if len(prompts) != len(want) {
		t.Fatalf("prompts = %v, want %v", prompts, want)
	}
	for n := range want {
		if prompts[n] != want[n] {
			t.Errorf("prompt %d = %q, want %q", n, prompts[n], want[n])
		}
	}
}

func TestStaticProviderStopsIterationWhenCallbackDeclines(t *testing.T) {
	ids := []string{"one", "two", "three"}
	//tasks := make([]tasks.Task, len(ids))

	var tasksArr []conversations.Task

	for _, id := range ids {
		tasksArr = append(tasksArr, NewTask(id, func() (string, error) {
			return id, nil
		}))
	}

	provider := conversations.NewStaticProvider(tasksArr...)
	visited := 0
	if err := provider.Tasks(func(task conversations.Task) bool {
		visited++
		return task.ID() != "two"
	}); err != nil {
		t.Fatalf("Tasks() error = %v", err)
	}
	if visited != 2 {
		t.Fatalf("callback visited %d tasks, want 2 (stop at the declined task)", visited)
	}
}

func TestStaticProviderWithoutTasksVisitsNothing(t *testing.T) {
	provider := conversations.NewStaticProvider()
	visited := false
	if err := provider.Tasks(func(conversations.Task) bool {
		visited = true
		return true
	}); err != nil {
		t.Fatalf("Tasks() error = %v", err)
	}
	if visited {
		t.Fatal("callback visited a task, want none for an empty provider")
	}
}

func TestNewStaticProviderCopiesTasks(t *testing.T) {
	source := []conversations.Task{
		NewTask("original", func() (string, error) { return "original", nil }),
	}
	provider := conversations.NewStaticProvider(source...)
	source[0] = NewTask("replaced", nil)

	var got []conversations.Task
	if err := provider.Tasks(func(task conversations.Task) bool {
		got = append(got, task)
		return true
	}); err != nil {
		t.Fatalf("Tasks() error = %v", err)
	}
	if len(got) != 1 || got[0].ID() != "original" {
		t.Fatalf("provider tasks = %v, want the original tasks", got)
	}
}

func TestNilStaticProviderReportsError(t *testing.T) {
	var provider *conversations.StaticProvider
	if err := provider.Tasks(func(conversations.Task) bool {
		t.Fatal("callback visited a task, want none for a nil provider")
		return true
	}); err == nil {
		t.Fatal("Tasks() error = nil, want an error for a nil provider")
	}
}

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
