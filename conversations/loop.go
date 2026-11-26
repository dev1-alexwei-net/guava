package conversations

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
func conversationSessionKey(agentID, taskID string) string {
	if agentID == "" || taskID == "" {
		return ""
	}
	return "agent:" + agentID + ":guava:" + taskID
}

func (s *Service) RunPrompt(ctx context.Context, taskID, initialInput string) error {
	if s == nil {
		return errors.New("conversation service is nil")
	}
	if ctx == nil {
		return errors.New("context is nil")
	}
	client := responsesClient{config: s.config, tokens: s.conversationTokenBucket()}
	input := initialInput
	sessionKey := conversationSessionKey(s.config.AgentID, taskID)
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
			response, err = client.createObserved(ctx, input, sessionKey, previousResponseID, taskID, turn)
			if err == nil {
				break
			}
			var responseErr *responsesHTTPError
			if errors.As(err, &responseErr) && isRetryableOpenResponsesError(responseErr) {
				consecutiveFailures++
				retryDelay := openResponsesRetryDelay(consecutiveFailures)
				retryAttrs := responseLogAttrs(taskID, turn)
				retryAttrs = append(retryAttrs,
					slog.Int("status", responseErr.statusCode),
					slog.Int("retry_attempt", consecutiveFailures),
					slog.Duration("retry_delay", retryDelay),
					slog.Any("error", err),
				)
				message := fmt.Sprintf("Retrying OpenResponses after HTTP %d", responseErr.statusCode)
				logAttrs(s.config.Logger, ctx, slog.LevelInfo, message, retryAttrs...)
				if err := s.waitBeforeRetry(ctx, retryDelay); err != nil {
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
		if taskID != "" {
			attrs = append(attrs, slog.String("path", taskID))
		}
		if containsCompletionMarker(response.outputText) {
			logAttrs(s.config.Logger, ctx, slog.LevelDebug, "File conversation completed", attrs...)
			return nil
		}
		logAttrs(s.config.Logger, ctx, slog.LevelDebug, "Continuing file conversation", attrs...)
		input = continueMessage
		previousResponseID = response.id
	}
}

func (s *Service) conversationTokenBucket() TokenWaiter {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conversationTokens == nil {
		s.conversationTokens = newConversationTokenBucket()
	}
	return s.conversationTokens
}

func (s *Service) waitBeforeRetry(ctx context.Context, delay time.Duration) error {
	if s.retryWait != nil {
		return s.retryWait(ctx, delay)
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
