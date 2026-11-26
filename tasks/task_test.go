package tasks

import (
	"errors"
	"strings"
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

func TestTaskPromptIsStableAcrossCalls(t *testing.T) {
	task := NewTask("src/app.go", func() (string, error) {
		return "edit src/app.go", nil
	})

	first, err := task.Prompt()
	if err != nil {
		t.Fatalf("first Prompt() error = %v", err)
	}
	second, err := task.Prompt()
	if err != nil {
		t.Fatalf("second Prompt() error = %v", err)
	}
	if first != "edit src/app.go" || second != first {
		t.Fatalf("Prompt() = %q then %q, want stable %q", first, second, "edit src/app.go")
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

func TestTaskWithoutPromptFunctionReportsError(t *testing.T) {
	task := FileTask{}
	if _, err := task.Prompt(); err == nil || !strings.Contains(err.Error(), "prompt function") {
		t.Fatalf("Prompt() error = %v, want an error about the missing prompt function", err)
	}
}

func TestNewTaskWithNilPromptFunctionReportsError(t *testing.T) {
	task := NewTask("a/b.txt", nil)
	if _, err := task.Prompt(); err == nil {
		t.Fatal("Prompt() error = nil, want an error for a task created without a prompt function")
	}
}
