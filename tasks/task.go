package tasks

import (
	"fmt"
	"log/slog"
	"time"
)

type FileTask struct {
	Id string
	//PromptFunc PromptFunc

	file   indexedFile
	reader *rootedFileReader
	p      *FileProvider // todo -
}

// ID returns the task's stable identity.
func (t FileTask) ID() string {
	return t.Id
}

// Prompt returns the next prompt to send into the conversation for this
// task. It reports an error if the task was created without a prompt
// function, or if the prompt function fails to build a prompt.
func (t FileTask) Prompt() (string, error) {

	fileReadStarted := time.Now()
	content, err := t.reader.read(t.file)
	instructions := ""
	if err != nil {
		t.p.log(slog.LevelError, "Failed to read indexed file",
			slog.String("path", t.file.path),
			slog.Duration("file_read_elapsed", time.Since(fileReadStarted)),
			slog.Any("error", err),
		)
		return "", fmt.Errorf("read indexed file: %w", err)
	}
	t.p.log(slog.LevelDebug, "Read indexed file",
		slog.String("path", t.file.path),
		slog.Int("bytes", len(content)),
		slog.Duration("file_read_elapsed", time.Since(fileReadStarted)),
	)

	files, err := t.p.index.regularFiles(t.p.ctx)
	if err != nil {
		return "", fmt.Errorf("list indexed files: %w", err)
	}
	if instructionFile, ok := nearestInstructionsFile(t.file, indexedInstructions(files)); ok {
		instructionsReadStarted := time.Now()
		instructionContent, err := t.p.reader.read(instructionFile)
		if err != nil {
			t.p.log(slog.LevelError, "Failed to read indexed instructions",
				slog.String("path", instructionFile.path),
				slog.Duration("file_read_elapsed", time.Since(instructionsReadStarted)),
				slog.Any("error", err),
			)
			return "", fmt.Errorf("read indexed instructions %q: %w", instructionFile.path, err)
		}
		instructions = string(instructionContent)
		t.p.log(slog.LevelDebug, "Read indexed instructions",
			slog.String("path", instructionFile.path),
			slog.Int("bytes", len(instructionContent)),
			slog.Duration("file_read_elapsed", time.Since(instructionsReadStarted)),
		)
		t.p.log(slog.LevelInfo, "Processing indexed file",
			slog.String("path", t.file.path),
			slog.Int("content_bytes", len(content)),
		)
		return composeInitialInput(instructions, t.file, content), nil
	}
	return "", fmt.Errorf("failed to find instrutions.md")
}
