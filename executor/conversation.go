package executor

import (
	"context"
	"errors"
	"log/slog"
	"strings"
)

const completionMarker = "<complete />"
const continueMessage = "Continue"

// conversationUser returns the stable OpenResponses "user" routing key for a
// file conversation, or "" for a conversation with no file. The Gateway
// derives a stable session key from the user string, so every turn of the
// same file conversation shares one agent session even when a turn fails
// without returning a response id, such as an output-token cutoff.
func conversationUser(filePath string) string {
	if filePath == "" {
		return ""
	}
	return "guava:" + filePath
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
	user := conversationUser(filePath)
	previousResponseID := ""
	turn := 0

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		turn++
		response, err := client.createObserved(ctx, input, user, previousResponseID, filePath, turn)
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
