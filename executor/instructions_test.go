package executor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadInstructionsReadsFileContents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "instructions.md")
	want := "# Instructions\nHandle this file carefully.\n"
	if err := os.WriteFile(path, []byte(want), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := loadInstructions(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("loadInstructions() = %q, want %q", got, want)
	}
}

func TestLoadInstructionsReportsMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.md")
	if _, err := loadInstructions(path); err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("loadInstructions() error = %v, want an error mentioning the path", err)
	}
}

func TestComposeInitialInputIncludesInstructionsPathAndContents(t *testing.T) {
	instructions := "# Instructions\nEdit only the requested file."
	file := indexedFile{path: "docs/file.md", name: "file.md"}
	content := []byte("line 1\nline 2")

	got := composeInitialInput(instructions, file, content)
	for _, want := range []string{
		instructions,
		"Current file path: \"docs/file.md\"",
		"File contents:\nline 1\nline 2",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("composed input %q does not contain %q", got, want)
		}
	}
}
