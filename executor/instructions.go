package executor

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
)

const instructionsFileName = "instructions.md"

func loadInstructions(path string) (string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read instructions file %q: %w", path, err)
	}
	return string(content), nil
}

func isInstructionsFile(file indexedFile) bool {
	return path.Base(file.path) == instructionsFileName
}

func nearestInstructionsPath(rootPath string, file indexedFile) (string, bool, error) {
	absoluteRoot, err := filepath.Abs(rootPath)
	if err != nil {
		return "", false, fmt.Errorf("resolve filesystem root: %w", err)
	}
	resolvedRoot, err := filepath.EvalSymlinks(absoluteRoot)
	if err != nil {
		return "", false, fmt.Errorf("resolve filesystem root: %w", err)
	}

	directory := filepath.Dir(filepath.Join(resolvedRoot, filepath.FromSlash(file.path)))
	for {
		candidate := filepath.Join(directory, instructionsFileName)
		info, err := os.Stat(candidate)
		if err == nil {
			if !info.Mode().IsRegular() {
				return "", false, fmt.Errorf("instructions path %q is not a regular file", candidate)
			}
			return candidate, true, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", false, fmt.Errorf("inspect instructions file %q: %w", candidate, err)
		}

		parent := filepath.Dir(directory)
		if parent == directory {
			return "", false, nil
		}
		directory = parent
	}
}

func composeInitialInput(instructions string, file indexedFile, content []byte) string {
	return instructions +
		"\n\nCurrent file path: " + strconv.Quote(file.path) +
		"\nFile contents:\n" + string(content)
}
