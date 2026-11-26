package conversations

import (
	"context"
	"errors"
	"fmt"

	"github.com/dev1-alexwei-net/guava/tasks"
)

// ProcessTask processes one source-agnostic task.
type ProcessTask func(context.Context, tasks.Task) error

// Coordinator consumes a TaskProvider and invokes the conversation callback
// for tasks in provider order. Dynamic refresh/listing remains behind the
// tasks package's Refresher, FileLister, and TaskProvider seams.
type Coordinator struct {
	provider tasks.TaskProvider
	process  ProcessTask
}

// NewCoordinator validates the task-provider and task-processing dependencies.
func NewCoordinator(provider tasks.TaskProvider, process ProcessTask) (*Coordinator, error) {
	if provider == nil {
		return nil, errors.New("task provider is required")
	}
	if process == nil {
		return nil, errors.New("task processor is required")
	}
	return &Coordinator{provider: provider, process: process}, nil
}

// Run consumes the provider once, returning the first processing or provider error.
func (c *Coordinator) Run(ctx context.Context) error {
	if c == nil {
		return errors.New("coordinator is nil")
	}
	if ctx == nil {
		return errors.New("context is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var processErr error
	providerErr := c.provider.Tasks(func(task tasks.Task) bool {
		if err := ctx.Err(); err != nil {
			processErr = err
			return false
		}
		if err := c.process(ctx, task); err != nil {
			processErr = fmt.Errorf("process task %q: %w", task.ID(), err)
			return false
		}
		if err := ctx.Err(); err != nil {
			processErr = fmt.Errorf("process task %q canceled before completion: %w", task.ID(), err)
			return false
		}
		return true
	})
	if processErr != nil {
		return processErr
	}
	return providerErr
}
