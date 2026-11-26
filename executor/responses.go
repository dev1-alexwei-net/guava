package executor

import (
	"context"

	"github.com/dev1-alexwei-net/guava/conversations"
)

// These wire shapes remain local aliases for compatibility with executor tests
// and callers that historically used package-private helpers.
type responsesRequest struct {
	Model              string `json:"model"`
	Input              string `json:"input"`
	Stream             bool   `json:"stream"`
	PreviousResponseID string `json:"previous_response_id,omitempty"`
}
type responsesAPIResponse struct {
	ID     string                `json:"id"`
	Status string                `json:"status"`
	Output []responsesOutputItem `json:"output"`
}
type responsesOutputItem struct {
	Type    string                 `json:"type"`
	Role    string                 `json:"role"`
	Content []responsesContentPart `json:"content"`
}
type responsesContentPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}
type agentResponse struct{ id, outputText string }
type responsesHTTPError struct {
	statusCode int
	message    string
}

func (e *responsesHTTPError) Error() string {
	if e.message == "" {
		return "OpenResponses returned an error"
	}
	return e.message
}

type responsesClient struct {
	config Config
	tokens TokenWaiter
}
type TokenWaiter = conversations.TokenWaiter

func (c responsesClient) create(ctx context.Context, input, sessionKey, previousID string) (agentResponse, error) {
	return c.createObserved(ctx, input, sessionKey, previousID, "", 0)
}
func (c responsesClient) createObserved(ctx context.Context, input, sessionKey, previousID, taskID string, turn int) (agentResponse, error) {
	client := conversations.NewClient(conversationConfig(c.config), c.tokens)
	response, err := client.CreateObserved(ctx, input, sessionKey, previousID, taskID, turn)
	if err != nil {
		return agentResponse{}, err
	}
	return agentResponse{id: response.ID, outputText: response.Text}, nil
}
