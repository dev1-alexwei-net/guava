package executor

import (
	"context"
	"errors"

	"github.com/dev1-alexwei-net/guava/tasks"
)

type failedTaskProvider struct{ err error }

func (p failedTaskProvider) Tasks(func(tasks.Task) bool) error { return p.err }

type closingTaskProvider struct{ provider *tasks.FileProvider }

func (p closingTaskProvider) Tasks(fn func(tasks.Task) bool) (err error) {
	defer func() { err = errors.Join(err, p.provider.Close()) }()
	return p.provider.Tasks(fn)
}

// Run preserves the existing executor entry point while delegating task
// processing, conversation lifecycle, and summaries to the conversations
// service.
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
		HTTPClient:       e.config.HTTPClient,
		Logger:           e.config.Logger,
	})
	if err != nil {
		return e.conversationService.Run(ctx, failedTaskProvider{err: err})
	}
	return e.conversationService.Run(ctx, closingTaskProvider{provider: provider})
}
