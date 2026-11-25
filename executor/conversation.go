package executor

import (
	"context"
	"errors"
	"log/slog"
	"strings"
)

const completionMarker = "<complete />"
const continueMessage = "Continue"

// conversationSessionKey returns the explicit agent-scoped Gateway session
// key for a file conversation, or "" for a conversation with no file. The
// custom "guava:" namespace keeps the key outside Gateway-reserved
// namespaces, and the same key routes every turn for this file to one agent
// session even when a turn fails without returning a response id.
func conversationSessionKey(agentID, filePath string) string {
	if agentID == "" || filePath == "" {
		return ""
	}
	return "agent:" + agentID + ":guava:" + filePath
}

func (e *Executor) runFileConversation(ctx context.Context, initialInput string) error {
	return e.runFileConversationForFile(ctx, "", initialInput)
}

func (e *Executor) runFileConversationForFile(ctx context.Context, filePath, initialInput string) error {
	if e == nil {
		return errors.New("executor is nil")
	}
	client := responsesClient{config: e.config}
	input := initialInput
	sessionKey := conversationSessionKey(e.config.AgentID, filePath)
	previousResponseID := ""
	turn := 0

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		turn++
		response, err := client.createObserved(ctx, input, sessionKey, previousResponseID, filePath, turn)
		if err != nil && !containsOutputLimitMessage(err.Error()) {
			return err
		}
		attrs := []slog.Attr{slog.Int("turn", turn), slog.String("response_id", response.id)}
		if filePath != "" {
			attrs = append(attrs, slog.String("path", filePath))
		}
		if containsCompletionMarker(response.outputText) {
			logAttrs(e.config.Logger, ctx, slog.LevelDebug, "File conversation completed", attrs...)
			return nil
		}
		logAttrs(e.config.Logger, ctx, slog.LevelDebug, "Continuing file conversation", attrs...)
		input = continueMessage
		previousResponseID = response.id
	}
}

func containsCompletionMarker(text string) bool {
	return strings.Contains(text, completionMarker)
}

func containsOutputLimitMessage(text string) bool {
	return strings.Contains(text, "The model was cut off") // should responses api handle this?
}
