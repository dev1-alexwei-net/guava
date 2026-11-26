package tasks

import (
	"errors"
	"testing"
)

func TestNewTaskKeepsIdentityAndPrompt(t *testing.T) {
	id := "docs/readme.md"
	task := NewTask(id, func() (string, error) {
		return "prompt for " + id, nil
	})

	if got := task.ID(); got != id {
		t.Fatalf("ID() = %q, want %q", got, id)
	}
	prompt, err := task.Prompt()
	if err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}
	if want := "prompt for " + id; prompt != want {
		t.Fatalf("Prompt() = %q, want %q", prompt, want)
	}
}

func TestTaskPromptReportsPromptFunctionError(t *testing.T) {
	cause := errors.New("read file: no such file or directory")
	task := NewTask("docs/readme.md", func() (string, error) {
		return "", cause
	})

	if _, err := task.Prompt(); !errors.Is(err, cause) {
		t.Fatalf("Prompt() error = %v, want %v", err, cause)
	}
}
