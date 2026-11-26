package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxResponsesBodySize = 32 << 20

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

type responsesAPIError struct {
	Error json.RawMessage `json:"error"`
}

type responsesAPIErrorDetails struct {
	Message string `json:"message"`
}

func responsesAPIErrorMessage(body []byte) string {
	var response responsesAPIError
	if err := json.Unmarshal(body, &response); err != nil {
		return ""
	}

	// Gateways may encode error as a plain string, while OpenResponses error
	// objects use {message, type}. Accept both so retry classification can
	// distinguish a missing model from an unrelated 404.
	var message string
	if err := json.Unmarshal(response.Error, &message); err == nil && strings.TrimSpace(message) != "" {
		return strings.TrimSpace(message)
	}
	var details responsesAPIErrorDetails
	if err := json.Unmarshal(response.Error, &details); err == nil {
		return strings.TrimSpace(details.Message)
	}
	return ""
}

type responsesHTTPError struct {
	statusCode int
	message    string
}

func (e *responsesHTTPError) Error() string {
	if e.message == "" {
		return fmt.Sprintf("OpenResponses returned HTTP %d", e.statusCode)
	}
	return fmt.Sprintf("OpenResponses returned HTTP %d: %s", e.statusCode, e.message)
}

type agentResponse struct {
	id         string
	outputText string
}

type responsesClient struct {
	config Config
	tokens *tokenBucket
}

func (c responsesClient) create(ctx context.Context, input, sessionKey, previousResponseID string) (agentResponse, error) {
	return c.createObserved(ctx, input, sessionKey, previousResponseID, "", 0)
}

func (c responsesClient) createObserved(ctx context.Context, input, sessionKey, previousResponseID, filePath string, turn int) (agentResponse, error) {
	endpoint, err := responsesEndpoint(c.config.GatewayURL)
	if err != nil {
		return agentResponse{}, err
	}

	payload := responsesRequest{
		Model:              "openclaw",
		Input:              input,
		Stream:             false,
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
	if sessionKey != "" {
		req.Header.Set("x-openclaw-session-key", sessionKey)
	}
	if c.config.AuthToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.config.AuthToken)
	}
	if c.tokens != nil {
		if err := c.tokens.Wait(ctx); err != nil {
			return agentResponse{}, fmt.Errorf("wait for OpenResponses token: %w", err)
		}
		tokenAttrs := responseLogAttrs(filePath, turn)
		tokenAttrs = append(tokenAttrs, slog.Int("tokens_spent", 1))
		logAttrs(c.config.Logger, ctx, slog.LevelInfo, "Spent conversation token for OpenResponses request", tokenAttrs...)
	}

	httpClient := c.config.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	fetchStarted := time.Now()
	resp, err := httpClient.Do(req)
	fetchElapsed := time.Since(fetchStarted)
	if err != nil {
		attrs := responseLogAttrs(filePath, turn)
		attrs = append(attrs,
			slog.String("method", req.Method),
			slog.String("url", endpoint),
			slog.Duration("fetch_elapsed", fetchElapsed),
			slog.Any("error", err),
		)
		level := slog.LevelError
		message := "OpenResponses fetch failed"
		if errors.Is(err, context.Canceled) {
			level = slog.LevelInfo
			message = "OpenResponses fetch canceled"
		}
		logAttrs(c.config.Logger, ctx, level, message, attrs...)
		return agentResponse{}, fmt.Errorf("send OpenResponses request: %w", err)
	}
	defer resp.Body.Close()

	responseReadStarted := time.Now()
	responseBody, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponsesBodySize+1))
	responseReadElapsed := time.Since(responseReadStarted)
	responseAttrs := responseLogAttrs(filePath, turn)
	responseAttrs = append(responseAttrs,
		slog.String("method", req.Method),
		slog.String("url", endpoint),
		slog.Int("status", resp.StatusCode),
		slog.Int("response_bytes", len(responseBody)),
		slog.Duration("fetch_elapsed", fetchElapsed),
		slog.Duration("response_read_elapsed", responseReadElapsed),
		slog.Duration("elapsed", time.Since(fetchStarted)),
	)
	if readErr != nil {
		responseAttrs = append(responseAttrs, slog.Any("error", readErr))
		level := slog.LevelError
		message := "Failed to read OpenResponses response"
		if errors.Is(readErr, context.Canceled) {
			level = slog.LevelInfo
			message = "OpenResponses response read canceled"
		}
		logAttrs(c.config.Logger, ctx, level, message, responseAttrs...)
	} else if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		logAttrs(c.config.Logger, ctx, slog.LevelError, "OpenResponses returned an error response", responseAttrs...)
	} else {
		logAttrs(c.config.Logger, ctx, slog.LevelInfo, "OpenResponses response received", responseAttrs...)
	}
	if c.config.Logger != nil && c.config.Logger.Enabled(ctx, LevelTrace) {
		rawAttrs := responseLogAttrs(filePath, turn)
		rawAttrs = append(rawAttrs,
			slog.Int("status", resp.StatusCode),
			slog.String("body", string(responseBody)),
		)
		c.config.Logger.LogAttrs(ctx, LevelTrace, "Raw OpenResponses response body received", rawAttrs...)
	}
	if readErr != nil {
		return agentResponse{}, fmt.Errorf("read OpenResponses response: %w", readErr)
	}
	if len(responseBody) > maxResponsesBodySize {
		return agentResponse{}, fmt.Errorf("OpenResponses response exceeds %d bytes", maxResponsesBodySize)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		if message := responsesAPIErrorMessage(responseBody); message != "" {
			return agentResponse{}, &responsesHTTPError{statusCode: resp.StatusCode, message: message}
		}
		return agentResponse{}, &responsesHTTPError{statusCode: resp.StatusCode}
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

func responseLogAttrs(filePath string, turn int) []slog.Attr {
	attrs := make([]slog.Attr, 0, 2)
	if filePath != "" {
		attrs = append(attrs, slog.String("path", filePath))
	}
	if turn > 0 {
		attrs = append(attrs, slog.Int("turn", turn))
	}
	return attrs
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
