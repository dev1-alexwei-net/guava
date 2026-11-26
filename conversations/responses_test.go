package conversations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestResponsesClientSendsRoutedJSONRequestAndReadsOutput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		if r.URL.Path != "/gateway/v1/responses" {
			t.Errorf("path = %q, want /gateway/v1/responses", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer secret-token" {
			t.Errorf("Authorization = %q, want bearer token", got)
		}
		if got := r.Header.Get("x-openclaw-agent-id"); got != "writer" {
			t.Errorf("x-openclaw-agent-id = %q, want writer", got)
		}
		if got := r.Header.Get("x-openclaw-session-key"); got != "agent:writer:file-session" {
			t.Errorf("x-openclaw-session-key = %q, want agent:writer:file-session", got)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", got)
		}

		var request responsesRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(w, "invalid test request", http.StatusBadRequest)
			return
		}
		if request.Model != "openclaw" || request.Input != "instructions plus file" || request.Stream {
			t.Errorf("request = %#v, want non-streaming OpenClaw input", request)
		}
		if request.PreviousResponseID != "resp_previous" {
			t.Errorf("previous_response_id = %q, want resp_previous", request.PreviousResponseID)
		}

		response := responsesAPIResponse{
			ID: "resp_current",
			Output: []responsesOutputItem{
				{Type: "reasoning", Content: []responsesContentPart{{Type: "reasoning_text", Text: "ignore"}}},
				{Type: "message", Role: "assistant", Content: []responsesContentPart{
					{Type: "output_text", Text: "the agent "},
					{Type: "output_text", Text: "response"},
				}},
				{Type: "message", Role: "user", Content: []responsesContentPart{{Type: "output_text", Text: "ignore"}}},
			},
		}
		if err := json.NewEncoder(w).Encode(response); err != nil {
			t.Errorf("encode response: %v", err)
		}
	}))
	defer server.Close()

	config := testConversationConfig(server)
	config.GatewayURL += "/gateway/"
	config.AgentID = "writer"
	config.AuthToken = "secret-token"

	got, err := (responsesClient{config: config}).create(
		context.Background(), "instructions plus file", "agent:writer:file-session", "resp_previous")
	if err != nil {
		t.Fatal(err)
	}
	if got.id != "resp_current" || got.outputText != "the agent response" {
		t.Fatalf("create() = %#v, want id and assistant output text", got)
	}
}

func TestResponsesClientSupportsGatewayWithoutBearerToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("Authorization = %q, want no bearer header", got)
		}
		if got := r.Header.Get("x-openclaw-session-key"); got != "" {
			t.Errorf("x-openclaw-session-key = %q, want no session header for an unscoped request", got)
		}
		fmt.Fprint(w, `{"id":"resp_1","output":[]}`)
	}))
	defer server.Close()

	config := testConversationConfig(server)
	if _, err := (responsesClient{config: config}).create(context.Background(), "input", "", ""); err != nil {
		t.Fatal(err)
	}
}

func TestResponsesClientReportsHTTPErrorMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":{"type":"invalid_request_error","message":"missing auth"}}`)
	}))
	defer server.Close()

	config := testConversationConfig(server)
	_, err := (responsesClient{config: config}).create(context.Background(), "input", "", "")
	if err == nil || !strings.Contains(err.Error(), "HTTP 401") || !strings.Contains(err.Error(), "missing auth") {
		t.Fatalf("create() error = %v, want HTTP 401 and API message", err)
	}
}

func TestResponsesClientRequiresResponseID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"output":[]}`)
	}))
	defer server.Close()

	config := testConversationConfig(server)
	_, err := (responsesClient{config: config}).create(context.Background(), "input", "", "")
	if err == nil || !strings.Contains(err.Error(), "missing an id") {
		t.Fatalf("create() error = %v, want missing-id error", err)
	}
}

func TestResponsesClientUsesRequestContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	config := testConversationConfig(server)
	_, err := (responsesClient{config: config}).create(ctx, "input", "", "")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("create() error = %v, want context.Canceled", err)
	}
}
