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
	Tasks(fn func(Task) error) error
}

type Task interface {
	ID() string
	Prompt() (string, error)
}
