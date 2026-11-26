package executor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

const completionMarker = "<complete />"
const continueMessage = "Continue. Remember to output the completion marker <complete /> only if you have finished."

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
	client := responsesClient{config: e.config, tokens: e.conversationTokenBucket()}
	input := initialInput
	sessionKey := conversationSessionKey(e.config.AgentID, filePath)
	previousResponseID := ""
	turn := 0

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		turn++
		var response agentResponse
		var err error
		consecutiveFailures := 0
		for {
			response, err = client.createObserved(ctx, input, sessionKey, previousResponseID, filePath, turn)
			if err == nil {
				break
			}
			var responseErr *responsesHTTPError
			if errors.As(err, &responseErr) && isRetryableOpenResponsesError(responseErr) {
				consecutiveFailures++
				retryDelay := openResponsesRetryDelay(consecutiveFailures)
				retryAttrs := responseLogAttrs(filePath, turn)
				retryAttrs = append(retryAttrs,
					slog.Int("status", responseErr.statusCode),
					slog.Int("retry_attempt", consecutiveFailures),
					slog.Duration("retry_delay", retryDelay),
					slog.Any("error", err),
				)
				message := fmt.Sprintf("Retrying OpenResponses after HTTP %d", responseErr.statusCode)
				logAttrs(e.config.Logger, ctx, slog.LevelInfo, message, retryAttrs...)
				if err := e.waitBeforeRetry(ctx, retryDelay); err != nil {
					return err
				}
				continue
			}
			if containsOutputLimitMessage(err.Error()) || containsAgentRunCancellationMsg(err.Error()) {
				break
			}
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

func (e *Executor) conversationTokenBucket() *tokenBucket {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.conversationTokens == nil {
		e.conversationTokens = newConversationTokenBucket()
	}
	return e.conversationTokens
}

func (e *Executor) waitBeforeRetry(ctx context.Context, delay time.Duration) error {
	if e.retryWait != nil {
		return e.retryWait(ctx, delay)
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func isRetryableOpenResponsesError(responseErr *responsesHTTPError) bool {
	if responseErr == nil {
		return false
	}
	switch responseErr.statusCode {
	case http.StatusInternalServerError, http.StatusRequestTimeout:
		return true
	case http.StatusNotFound:
		message := strings.ToLower(responseErr.message)
		return strings.Contains(message, "model") && strings.Contains(message, "not found")
	default:
		return false
	}
}

func openResponsesRetryDelay(consecutiveFailures int) time.Duration {
	switch consecutiveFailures {
	case 1:
		return time.Second
	case 2:
		return 5 * time.Second
	default:
		return 30 * time.Second
	}
}

func containsCompletionMarker(text string) bool {
	return strings.Contains(text, completionMarker)
}

func containsOutputLimitMessage(text string) bool {
	return strings.Contains(text, "The model was cut off")
}

func containsAgentRunCancellationMsg(text string) bool {
	return strings.Contains(text, "agent run was cancelled")
}
