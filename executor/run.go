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

	files, err := index.regularFiles(ctx)
	if err != nil {
		return err
	}
	logAttrs(logger, ctx, slog.LevelDebug, "Loaded indexed files", slog.Int("count", len(files)))

	instructionsByPath := indexedInstructions(files)
	instructionContents := make(map[string]string, len(instructionsByPath))
	fallbackInstructions := ""
	fallbackLoaded := false

	for _, file := range files {
		// Instruction files are inputs to other files, not work items themselves.
		if isInstructionsFile(file) {
			continue
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
			return fmt.Errorf("process file %q: %w", file.path, err)
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
		if instructionFile, found := nearestInstructionsFile(file, instructionsByPath); found {
			var cached bool
			instructions, cached = instructionContents[instructionFile.path]
			if !cached {
				instructionsReadStarted := time.Now()
				instructionContent, err := reader.read(instructionFile)
				if err != nil {
					logAttrs(logger, ctx, slog.LevelError, "Failed to read indexed instructions",
						slog.String("path", instructionFile.path),
						slog.Duration("file_read_elapsed", time.Since(instructionsReadStarted)),
						slog.Any("error", err),
					)
					return fmt.Errorf("process file %q: read indexed instructions %q: %w", file.path, instructionFile.path, err)
				}
				instructions = string(instructionContent)
				instructionContents[instructionFile.path] = instructions
				logAttrs(logger, ctx, slog.LevelDebug, "Read indexed instructions",
					slog.String("path", instructionFile.path),
					slog.Int("bytes", len(instructionContent)),
					slog.Duration("file_read_elapsed", time.Since(instructionsReadStarted)),
				)
			}
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
					return fmt.Errorf("process file %q: %w", file.path, err)
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
			return fmt.Errorf("process file %q: %w", file.path, err)
		}
		logAttrs(logger, ctx, slog.LevelInfo, "Indexed file processing completed",
			slog.String("path", file.path),
			slog.Duration("elapsed", time.Since(fileStarted)),
		)
	}
	return nil
}
