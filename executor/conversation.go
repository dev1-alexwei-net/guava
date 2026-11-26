package executor

import (
	"context"
	"errors"
	"strings"

	"github.com/dev1-alexwei-net/guava/conversations"
)

const completionMarker = "<complete />"
const continueMessage = "Continue. Remember to output the completion marker <complete /> only if you have finished."

// conversationSessionKey remains as a compatibility helper for package callers.
func conversationSessionKey(agentID, taskID string) string {
	if agentID == "" || taskID == "" {
		return ""
	}
	return "agent:" + agentID + ":guava:" + taskID
}

func containsCompletionMarker(text string) bool { return strings.Contains(text, completionMarker) }

// runFileConversation preserves the package's historical helper while routing
// all conversation work through the conversations service.
func (e *Executor) runFileConversation(ctx context.Context, input string) error {
	return e.runFileConversationForFile(ctx, "", input)
}

func (e *Executor) runFileConversationForFile(ctx context.Context, taskID, input string) error {
	if e == nil {
		return errors.New("executor is nil")
	}
	service, err := conversations.NewWithOptions(conversationConfig(e.config), conversations.Options{
		TokenWaiter: e.conversationTokenBucket(), RetryWait: e.retryWait,
	})
	if err != nil {
		return err
	}
	return service.RunPrompt(ctx, taskID, input)
}

func (e *Executor) conversationTokenBucket() *tokenBucket {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.conversationTokens == nil {
		e.conversationTokens = newConversationTokenBucket()
	}
	return e.conversationTokens
}
