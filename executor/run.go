package executor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
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

	index, err := openFileIndex(ctx, e.config.DatabasePath)
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, index.close())
	}()

	reader, err := openRootedFileReader(e.config.RootPath)
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, reader.close())
	}()

	refresher, err := NewHTTPRefresher(e.config.FileExplorerURL, e.config.HTTPClient)
	if err != nil {
		return err
	}
	logAttrs(logger, ctx, slog.LevelDebug, "Starting dynamic file discovery")

	fallbackInstructions := ""
	fallbackLoaded := false
	processFile := func(ctx context.Context, path string) error {
		files, err := index.regularFiles(ctx)
		if err != nil {
			return fmt.Errorf("list indexed files: %w", err)
		}
		var file indexedFile
		found := false
		for _, candidate := range files {
			if candidate.path == path {
				file = candidate
				found = true
				break
			}
		}
		if !found {
			logAttrs(logger, ctx, slog.LevelDebug, "Skipping file no longer in index", slog.String("path", path))
			return nil
		}
		// Instruction files are inputs to other files, not work items themselves.
		if isInstructionsFile(file) {
			logAttrs(logger, ctx, slog.LevelDebug, "Skipping indexed instructions file", slog.String("path", path))
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}

		fileStarted := time.Now()
		fileReadStarted := time.Now()
		content, err := reader.read(file)
		if err != nil {
			logAttrs(logger, ctx, slog.LevelError, "Failed to read indexed file",
				slog.String("path", file.path),
				slog.Duration("file_read_elapsed", time.Since(fileReadStarted)),
				slog.Any("error", err),
			)
			return fmt.Errorf("read indexed file: %w", err)
		}
		logAttrs(logger, ctx, slog.LevelDebug, "Read indexed file",
			slog.String("path", file.path),
			slog.Int("bytes", len(content)),
			slog.Duration("file_read_elapsed", time.Since(fileReadStarted)),
		)
		if err := ctx.Err(); err != nil {
			return err
		}

		instructions := ""
		if instructionFile, ok := nearestInstructionsFile(file, indexedInstructions(files)); ok {
			instructionsReadStarted := time.Now()
			instructionContent, err := reader.read(instructionFile)
			if err != nil {
				logAttrs(logger, ctx, slog.LevelError, "Failed to read indexed instructions",
					slog.String("path", instructionFile.path),
					slog.Duration("file_read_elapsed", time.Since(instructionsReadStarted)),
					slog.Any("error", err),
				)
				return fmt.Errorf("read indexed instructions %q: %w", instructionFile.path, err)
			}
			instructions = string(instructionContent)
			logAttrs(logger, ctx, slog.LevelDebug, "Read indexed instructions",
				slog.String("path", instructionFile.path),
				slog.Int("bytes", len(instructionContent)),
				slog.Duration("file_read_elapsed", time.Since(instructionsReadStarted)),
			)
		} else {
			if !fallbackLoaded {
				instructionsReadStarted := time.Now()
				fallbackInstructions, err = loadInstructions(e.config.InstructionsPath)
				if err != nil {
					logAttrs(logger, ctx, slog.LevelError, "Failed to read fallback instructions",
						slog.String("path", e.config.InstructionsPath),
						slog.Duration("file_read_elapsed", time.Since(instructionsReadStarted)),
						slog.Any("error", err),
					)
					return err
				}
				fallbackLoaded = true
				logAttrs(logger, ctx, slog.LevelDebug, "Read fallback instructions",
					slog.String("path", e.config.InstructionsPath),
					slog.Int("bytes", len(fallbackInstructions)),
					slog.Duration("file_read_elapsed", time.Since(instructionsReadStarted)),
				)
			}
			instructions = fallbackInstructions
		}
		if err := ctx.Err(); err != nil {
			return err
		}

		input := composeInitialInput(instructions, file, content)
		logAttrs(logger, ctx, slog.LevelInfo, "Processing indexed file",
			slog.String("path", file.path),
			slog.Int("content_bytes", len(content)),
		)
		if err := e.runFileConversationForFile(ctx, file.path, input); err != nil {
			logAttrs(logger, ctx, slog.LevelError, "Indexed file processing failed",
				slog.String("path", file.path),
				slog.Duration("elapsed", time.Since(fileStarted)),
				slog.Any("error", err),
			)
			return fmt.Errorf("run file conversation: %w", err)
		}
		logAttrs(logger, ctx, slog.LevelInfo, "Indexed file processing completed",
			slog.String("path", file.path),
			slog.Duration("elapsed", time.Since(fileStarted)),
		)
		return nil
	}

	coordinator, err := NewCoordinator(refresher, index, processFile)
	if err != nil {
		return err
	}
	return coordinator.Run(ctx)
}
