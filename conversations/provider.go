package conversations

import (
	"errors"
)

// errNilProvider reports a call to Tasks on a nil *StaticProvider.
var errNilProvider = errors.New("task provider is nil")

// TaskProvider exposes an ordered iterator over the tasks it provides.
//
// TaskProvider is the seam between a task source and the conversation
// framework: the framework consumes tasks and their prompts through this
// interface without learning where the work comes from, and a new task
// source plugs in by implementing the same interface.
type TaskProvider interface {
	// Tasks visits each provided task in provider order, calling fn once
	// per task. If fn returns false, iteration stops and Tasks returns
	// nil without visiting the remaining tasks. It returns the first
	// error encountered while producing tasks; a provider whose source
	// requires I/O to enumerate may report it here, while a provider that
	// holds a fixed set of tasks always returns nil.
	Tasks(fn func(Task) bool) error
}

// StaticProvider is a TaskProvider backed by a fixed, ordered set of
// tasks. It is the reference implementation of the seam: it shows how a
// task source hands its tasks to the conversation framework, and it is
// convenient for tests and embedded use.
type StaticProvider struct {
	tasks []Task
}

// NewStaticProvider returns a provider that yields the given tasks in the
// given order. The tasks are copied, so later mutations of the caller's
// slice do not change what the provider yields.
func NewStaticProvider(t ...Task) *StaticProvider {
	provider := make([]Task, len(t))
	copy(provider, t)
	return &StaticProvider{tasks: provider}
}

// Tasks implements TaskProvider by visiting the provider's tasks in the
// order they were given.
func (p *StaticProvider) Tasks(fn func(Task) bool) error {
	if p == nil {
		return errNilProvider
	}
	for _, task := range p.tasks {
		if !fn(task) {
			return nil
		}
	}
	return nil
}

type Task interface {
	ID() string
	Prompt() (string, error)
}
