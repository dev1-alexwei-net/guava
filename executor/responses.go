package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const maxResponsesBodySize = 32 << 20

type responsesRequest struct {
	Model              string `json:"model"`
	Input              string `json:"input"`
	Stream             bool   `json:"stream"`
	User               string `json:"user,omitempty"`
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

type responsesAPIError struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

type agentResponse struct {
	id         string
	outputText string
}

type responsesClient struct {
	config Config
}

func (c responsesClient) create(ctx context.Context, input, user, previousResponseID string) (agentResponse, error) {
	endpoint, err := responsesEndpoint(c.config.GatewayURL)
	if err != nil {
		return agentResponse{}, err
	}

	payload := responsesRequest{
		Model:              "openclaw",
		Input:              input,
		Stream:             false,
		User:               user,
		PreviousResponseID: previousResponseID,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return agentResponse{}, fmt.Errorf("encode OpenResponses request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return agentResponse{}, fmt.Errorf("create OpenResponses request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-openclaw-agent-id", c.config.AgentID)
	if c.config.AuthToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.config.AuthToken)
	}

	httpClient := c.config.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return agentResponse{}, fmt.Errorf("send OpenResponses request: %w", err)
	}
	defer resp.Body.Close()

	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponsesBodySize+1))
	if err != nil {
		return agentResponse{}, fmt.Errorf("read OpenResponses response: %w", err)
	}
	if len(responseBody) > maxResponsesBodySize {
		return agentResponse{}, fmt.Errorf("OpenResponses response exceeds %d bytes", maxResponsesBodySize)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		var apiError responsesAPIError
		if json.Unmarshal(responseBody, &apiError) == nil && strings.TrimSpace(apiError.Error.Message) != "" {
			return agentResponse{}, fmt.Errorf("OpenResponses returned HTTP %d: %s", resp.StatusCode, apiError.Error.Message)
		}
		return agentResponse{}, fmt.Errorf("OpenResponses returned HTTP %d", resp.StatusCode)
	}

	var decoded responsesAPIResponse
	if err := json.Unmarshal(responseBody, &decoded); err != nil {
		return agentResponse{}, fmt.Errorf("decode OpenResponses response: %w", err)
	}
	if decoded.ID == "" {
		return agentResponse{}, errors.New("OpenResponses response is missing an id")
	}

	var output strings.Builder
	for _, item := range decoded.Output {
		if item.Type != "message" || item.Role != "assistant" {
			continue
		}
		for _, part := range item.Content {
			if part.Type == "output_text" {
				output.WriteString(part.Text)
			}
		}
	}
	return agentResponse{id: decoded.ID, outputText: output.String()}, nil
}

func responsesEndpoint(gatewayURL string) (string, error) {
	endpoint, err := url.Parse(gatewayURL)
	if err != nil {
		return "", fmt.Errorf("parse Gateway URL: %w", err)
	}
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/v1/responses"
	endpoint.RawPath = ""
	return endpoint.String(), nil
}
