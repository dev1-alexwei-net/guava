package executor

import (
	"context"
	"errors"
	"time"
)

// Session status values reported by Summary.
const (
	StatusIdle      = "idle"
	StatusRunning   = "running"
	StatusCompleted = "completed"
	StatusFailed    = "failed"
	StatusStopped   = "stopped"
)

// Summary is a read-only snapshot of an executor session: the agent-id the
// executor was configured with, the subdirectory the run is scoped to
// (empty for the whole root), the raw command line the server was started
// with, and the basic state of the current or most recent run. CLIArgs is
// carried exactly as it was configured, without parsing.
type Summary struct {
	AgentID      string
	CLIArgs      string
	Subdirectory string
	Status       string
	StartedAt    time.Time
}

// SessionSummary returns the executor's session summary: the configured
// agent-id, the configured subdirectory (empty for the whole root), the
// raw CLI-args dump, the current status, and the started-at time. It is
// safe to call concurrently, and before, during, or after a run. Before the
// first run it reports StatusIdle and a zero StartedAt.
func (e *Executor) SessionSummary() Summary {
	if e == nil {
		return Summary{Status: StatusIdle}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return Summary{
		AgentID:      e.config.AgentID,
		CLIArgs:      e.config.CLIArgs,
		Subdirectory: e.config.Subdirectory,
		Status:       e.status,
		StartedAt:    e.startedAt,
	}
}

// startRun records that a run began and when.
func (e *Executor) startRun() {
	e.mu.Lock()
	e.status = StatusRunning
	e.startedAt = time.Now()
	e.mu.Unlock()
}

// finishRun records the terminal status for a run that ended with err.
func (e *Executor) finishRun(err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	switch {
	case err == nil:
		e.status = StatusCompleted
	case errors.Is(err, context.Canceled):
		e.status = StatusStopped
	default:
		e.status = StatusFailed
	}
}
