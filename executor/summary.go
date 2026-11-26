package executor

import "github.com/dev1-alexwei-net/guava/conversations"

const (
	StatusIdle      = conversations.StatusIdle
	StatusRunning   = conversations.StatusRunning
	StatusCompleted = conversations.StatusCompleted
	StatusFailed    = conversations.StatusFailed
	StatusStopped   = conversations.StatusStopped
)

type Summary = conversations.Summary

// SessionSummary returns the conversation service's session snapshot.
func (e *Executor) SessionSummary() Summary {
	if e == nil || e.conversationService == nil {
		return Summary{Status: StatusIdle}
	}
	return e.conversationService.SessionSummary()
}
