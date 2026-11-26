// Package tasks defines the source-agnostic task abstractions shared by the
// executor's task side and its conversation framework.
//
// A Task is a unit of work with a stable identity and a prompt function that
// yields the next prompt to send into the conversation. A TaskProvider
// exposes an ordered iterator over the tasks it provides.
//
// The abstractions are deliberately source-agnostic: this package knows
// nothing about the fileexplorer manifest, files on disk, or the OpenResponses
// API. A provider can back tasks with files today and with any other task
// source later, and the conversation framework that consumes tasks never
// learns where the work comes from.
package tasks

import "errors"

// errNoPrompt reports a call to Prompt on a task created without a prompt
// function.
var errNoPrompt = errors.New("task has no prompt function")

// PromptFunc yields the next prompt to send into the conversation for one
// task. A prompt function may perform I/O to build its prompt, for example
// reading the instructions and content the prompt is composed from, and it
// must return any error it encounters. It must be safe to call more than
// once.
type PromptFunc func() (string, error)

// Task is a single unit of work consumed by the conversation framework.
//
// A Task carries a stable identity and a prompt function. The identity is
// source-specific: for file-backed tasks it is the index-relative file
// path, and for other task sources it is whatever stable identifier that
// source uses. Consumers rely on the identity being stable across provider
// iterations so they can deduplicate, route, and log work by it.
type Task struct {
	id     string
	prompt PromptFunc
}

// NewTask returns a Task with the given stable identity and prompt function.
// A nil prompt function is accepted; calling Prompt on the resulting task
// reports an error.
func NewTask(id string, prompt PromptFunc) Task {
	return Task{id: id, prompt: prompt}
}

// ID returns the task's stable identity.
func (t Task) ID() string {
	return t.id
}

// Prompt returns the next prompt to send into the conversation for this
// task. It reports an error if the task was created without a prompt
// function, or if the prompt function fails to build a prompt.
func (t Task) Prompt() (string, error) {
	if t.prompt == nil {
		return "", errNoPrompt
	}
	return t.prompt()
}
