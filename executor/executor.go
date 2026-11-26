package executor

import (
	"context"
	"errors"

	"github.com/dev1-alexwei-net/guava/conversations"
	"github.com/dev1-alexwei-net/guava/tasks"
)

type failedTaskProvider struct{ err error }

func (p failedTaskProvider) Tasks(func(tasks.Task) bool) error { return p.err }

type closingTaskProvider struct{ provider *tasks.FileProvider }

func (p closingTaskProvider) Tasks(fn func(tasks.Task) bool) (err error) {
	defer func() { err = errors.Join(err, p.provider.Close()) }()
	return p.provider.Tasks(fn)
}

// progressTaskProvider records per-file progress around each task: it reports
// the task once, after its processing attempt, with the outcome the
// coordinator observed. A task the coordinator did not complete (failed or
// canceled) is reported as unfinished, so a resume processes it again.
type progressTaskProvider struct {
	provider tasks.TaskProvider
	report   func(taskID string, completed bool)
}

func (p progressTaskProvider) Tasks(fn func(tasks.Task) bool) error {
	return p.provider.Tasks(func(task tasks.Task) bool {
		completed := fn(task)
		if p.report != nil {
			p.report(task.ID(), completed)
		}
		return completed
	})
}

// Run opens the configured file task source and delegates task processing,
// conversation lifecycle, and summaries to the conversations service.
func (e *Executor) Run(ctx context.Context) error {
	if e == nil {
		return errors.New("executor is nil")
	}
	if ctx == nil {
		return errors.New("context is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if e.conversationService == nil {
		return errors.New("conversation service is not configured")
	}
	provider, err := tasks.NewFileProvider(ctx, tasks.FileProviderConfig{
		DatabasePath:     e.config.DatabasePath,
		RootPath:         e.config.RootPath,
		InstructionsPath: e.config.InstructionsPath,
		FileExplorerURL:  e.config.FileExplorerURL,
		Subdirectory:     e.config.Subdirectory,
		CompletedPaths:   e.config.CompletedPaths,
		HTTPClient:       e.config.HTTPClient,
		Logger:           e.config.Logger,
	})
	if err != nil {
		return e.conversationService.Run(ctx, failedTaskProvider{err: err})
	}
	runProvider := tasks.TaskProvider(closingTaskProvider{provider: provider})
	if e.config.OnFileProgress != nil {
		runProvider = progressTaskProvider{provider: runProvider, report: e.config.OnFileProgress}
	}
	return e.conversationService.Run(ctx, runProvider)
}

// SessionSummary returns the conversation service's session snapshot.
func (e *Executor) SessionSummary() conversations.Summary {
	if e == nil || e.conversationService == nil {
		return conversations.Summary{Status: conversations.StatusIdle}
	}
	return e.conversationService.SessionSummary()
}
