package conversations

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
func (s *Service) SessionSummary() Summary {
	if s == nil {
		return Summary{Status: StatusIdle}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return Summary{AgentID: s.config.AgentID, CLIArgs: s.config.CLIArgs, Subdirectory: s.config.Subdirectory, Status: s.status, StartedAt: s.startedAt}
}

// startRun records that a run began and when.
func (s *Service) startRun() {
	s.mu.Lock()
	s.status = StatusRunning
	s.startedAt = time.Now()
	s.mu.Unlock()
}

// finishRun records the terminal status for a run that ended with err.
func (s *Service) finishRun(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case err == nil:
		s.status = StatusCompleted
	case errors.Is(err, context.Canceled):
		s.status = StatusStopped
	default:
		s.status = StatusFailed
	}
}
