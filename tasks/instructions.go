package tasks

import (
	"fmt"
	"os"
	"path"
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

func indexedInstructions(files []indexedFile) map[string]indexedFile {
	instructions := make(map[string]indexedFile)
	for _, file := range files {
		if isInstructionsFile(file) {
			instructions[file.path] = file
		}
	}
	return instructions
}

func nearestInstructionsFile(file indexedFile, instructions map[string]indexedFile) (indexedFile, bool) {
	for directory := path.Dir(file.path); ; directory = path.Dir(directory) {
		candidate := path.Join(directory, instructionsFileName)
		if instructionFile, ok := instructions[candidate]; ok {
			return instructionFile, true
		}
		if directory == "." {
			return indexedFile{}, false
		}
	}
}

func composeInitialInput(instructions string, file indexedFile, content []byte) string {
	return instructions +
		"\n\nCurrent file path: " + strconv.Quote(file.path) +
		"\nFile contents:\n" + string(content)
}
