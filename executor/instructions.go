package executor

import (
	"fmt"
	"os"
	"strconv"
)

func loadInstructions(path string) (string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read instructions file %q: %w", path, err)
	}
	return string(content), nil
}

func composeInitialInput(instructions string, file indexedFile, content []byte) string {
	return instructions +
		"\n\nCurrent file path: " + strconv.Quote(file.path) +
		"\nFile contents:\n" + string(content)
}
