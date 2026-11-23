package executor

import (
	"context"
	"errors"
	"fmt"
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
		content, err := reader.read(file)
		if err != nil {
			return fmt.Errorf("process file %q: %w", file.path, err)
		}
		if err := ctx.Err(); err != nil {
			return err
		}

		instructions := ""
		if instructionFile, found := nearestInstructionsFile(file, instructionsByPath); found {
			var cached bool
			instructions, cached = instructionContents[instructionFile.path]
			if !cached {
				instructionContent, err := reader.read(instructionFile)
				if err != nil {
					return fmt.Errorf("process file %q: read indexed instructions %q: %w", file.path, instructionFile.path, err)
				}
				instructions = string(instructionContent)
				instructionContents[instructionFile.path] = instructions
			}
		} else {
			if !fallbackLoaded {
				fallbackInstructions, err = loadInstructions(e.config.InstructionsPath)
				if err != nil {
					return fmt.Errorf("process file %q: %w", file.path, err)
				}
				fallbackLoaded = true
			}
			instructions = fallbackInstructions
		}
		if err := ctx.Err(); err != nil {
			return err
		}

		input := composeInitialInput(instructions, file, content)
		if err := e.runFileConversation(ctx, input); err != nil {
			return fmt.Errorf("process file %q: %w", file.path, err)
		}
	}
	return nil
}
