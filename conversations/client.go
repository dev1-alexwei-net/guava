package conversations

import (
	"context"
	"errors"
)

var errNilClient = errors.New("OpenResponses client is nil")

// Response is the response ID and assistant text returned by OpenResponses.
type Response struct{ ID, Text string }

// Client sends requests to the OpenResponses endpoint.
type Client struct{ client responsesClient }

// NewClient creates an OpenResponses client. A nil token waiter disables token
// throttling for direct client use.
func NewClient(config Config, tokens TokenWaiter) *Client {
	return &Client{client: responsesClient{config: config, tokens: tokens}}
}

// Create sends one non-streaming OpenResponses request.
func (c *Client) Create(ctx context.Context, input, sessionKey, previousResponseID string) (Response, error) {
	if c == nil {
		return Response{}, errNilClient
	}
	response, err := c.client.create(ctx, input, sessionKey, previousResponseID)
	return Response{ID: response.id, Text: response.outputText}, err
}

// CreateObserved sends a request with task and turn metadata in diagnostic logs.
func (c *Client) CreateObserved(ctx context.Context, input, sessionKey, previousResponseID, taskID string, turn int) (Response, error) {
	if c == nil {
		return Response{}, errNilClient
	}
	response, err := c.client.createObserved(ctx, input, sessionKey, previousResponseID, taskID, turn)
	return Response{ID: response.id, Text: response.outputText}, err
}
