package executor

import (
	"context"
	"errors"
	"strings"
)

const completionMarker = "<complete />"
const continueMessage = "Continue"

func (e *Executor) runFileConversation(ctx context.Context, initialInput string) error {
	if e == nil {
		return errors.New("executor is nil")
	}
	client := responsesClient{config: e.config}
	input := initialInput
	previousResponseID := ""

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		response, err := client.create(ctx, input, "", previousResponseID)
		if err != nil {
			return err
		}
		if containsCompletionMarker(response.outputText) {
			return nil
		}
		input = continueMessage
		previousResponseID = response.id
	}
}

func containsCompletionMarker(text string) bool {
	return strings.Contains(text, completionMarker)
}
