package executor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/dev1-alexwei-net/guava/tasks"
)

func (e *Executor) Run(ctx context.Context) (err error) {
	if e == nil {
		return errors.New("executor is nil")
	}
	if ctx == nil {
		return errors.New("context is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	e.startRun()
	defer func() { e.finishRun(err) }()

	logger := e.config.Logger
	runStarted := time.Now()
	logAttrs(logger, ctx, slog.LevelInfo, "Executor run started",
		slog.String("agent_id", e.config.AgentID),
		slog.String("gateway_url", e.config.GatewayURL),
		slog.String("fileexplorer_url", e.config.FileExplorerURL),
		slog.String("subdirectory", e.config.Subdirectory),
	)
	defer func() {
		attrs := []slog.Attr{slog.Duration("elapsed", time.Since(runStarted))}
		if err != nil {
			if errors.Is(err, context.Canceled) {
				logAttrs(logger, ctx, slog.LevelInfo, "Executor run canceled", attrs...)
				return
			}
			attrs = append(attrs, slog.Any("error", err))
			logAttrs(logger, ctx, slog.LevelError, "Executor run failed", attrs...)
			return
		}
		logAttrs(logger, ctx, slog.LevelInfo, "Executor run completed", attrs...)
	}()

	provider, err := tasks.NewFileProvider(ctx, tasks.FileProviderConfig{
		DatabasePath:     e.config.DatabasePath,
		RootPath:         e.config.RootPath,
		InstructionsPath: e.config.InstructionsPath,
		FileExplorerURL:  e.config.FileExplorerURL,
		Subdirectory:     e.config.Subdirectory,
		HTTPClient:       e.config.HTTPClient,
		Logger:           logger,
	})
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, provider.Close())
	}()

	if e.config.Subdirectory != "" {
		logAttrs(logger, ctx, slog.LevelInfo, "Scoping run to subdirectory",
			slog.String("subdirectory", e.config.Subdirectory),
		)
	}
	logAttrs(logger, ctx, slog.LevelDebug, "Starting dynamic file discovery")

	var processErr error
	providerErr := provider.Tasks(func(task tasks.Task) bool {
		if err := ctx.Err(); err != nil {
			processErr = err
			return false
		}

		fileStarted := time.Now()
		input, err := task.Prompt()
		if err != nil {
			processErr = fmt.Errorf("process file %q: build initial prompt: %w", task.ID(), err)
			logAttrs(logger, ctx, slog.LevelError, "Failed to build file prompt",
				slog.String("path", task.ID()),
				slog.Any("error", err),
			)
			return false
		}
		if err := e.runFileConversationForFile(ctx, task.ID(), input); err != nil {
			logAttrs(logger, ctx, slog.LevelError, "Indexed file processing failed",
				slog.String("path", task.ID()),
				slog.Duration("elapsed", time.Since(fileStarted)),
				slog.Any("error", err),
			)
			processErr = fmt.Errorf("process file %q: run file conversation: %w", task.ID(), err)
			return false
		}
		if err := ctx.Err(); err != nil {
			processErr = fmt.Errorf("process file %q canceled before completion: %w", task.ID(), err)
			return false
		}
		logAttrs(logger, ctx, slog.LevelInfo, "Indexed file processing completed",
			slog.String("path", task.ID()),
			slog.Duration("elapsed", time.Since(fileStarted)),
		)
		return true
	})
	if processErr != nil {
		return processErr
	}
	return providerErr
}
