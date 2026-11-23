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

	instructionContents := make(map[string]string)
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
		instructionsPath, found, err := nearestInstructionsPath(e.config.RootPath, file)
		if err != nil {
			return fmt.Errorf("process file %q: %w", file.path, err)
		}
		if found {
			var cached bool
			instructions, cached = instructionContents[instructionsPath]
			if !cached {
				instructions, err = loadInstructions(instructionsPath)
				if err != nil {
					return fmt.Errorf("process file %q: %w", file.path, err)
				}
				instructionContents[instructionsPath] = instructions
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
