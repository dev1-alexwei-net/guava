package executor

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRunFileConversationContinuesUntilCompletion(t *testing.T) {
	var requests []responsesRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request responsesRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		requests = append(requests, request)

		switch len(requests) {
		case 1:
			if err := json.NewEncoder(w).Encode(responsesAPIResponse{
				ID:     "resp_first",
				Output: assistantText("still working"),
			}); err != nil {
				t.Errorf("encode response: %v", err)
			}
		case 2:
			if err := json.NewEncoder(w).Encode(responsesAPIResponse{
				ID:     "resp_done",
				Output: assistantText("finished <complete", " />"),
			}); err != nil {
				t.Errorf("encode response: %v", err)
			}
		default:
			t.Errorf("unexpected request %d", len(requests))
			http.Error(w, "too many requests", http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	config := validConfig()
	config.GatewayURL = server.URL
	config.HTTPClient = server.Client()
	executor := &Executor{config: config}

	if err := executor.runFileConversation(context.Background(), "instructions and file contents"); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 {
		t.Fatalf("got %d requests, want 2", len(requests))
	}
	if requests[0].Input != "instructions and file contents" {
		t.Errorf("first input = %q", requests[0].Input)
	}
	if requests[0].PreviousResponseID != "" {
		t.Errorf("first previous_response_id = %q, want empty", requests[0].PreviousResponseID)
	}
	if requests[1].Input != continueMessage {
		t.Errorf("follow-up input = %q, want %q", requests[1].Input, continueMessage)
	}
	if requests[1].PreviousResponseID != "resp_first" {
		t.Errorf("follow-up previous_response_id = %q, want resp_first", requests[1].PreviousResponseID)
	}
	if requests[0].User != requests[1].User {
		t.Errorf("user routing changed between turns: %q and %q", requests[0].User, requests[1].User)
	}
}

func TestRunFileConversationContinuesAfterModelCutoff(t *testing.T) {
	var requests []responsesRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request responsesRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		requests = append(requests, request)

		switch len(requests) {
		case 1:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"The model was cut off after reaching the output token limit"}}`))
		case 2:
			if err := json.NewEncoder(w).Encode(responsesAPIResponse{
				ID:     "resp_after_cutoff",
				Output: assistantText(completionMarker),
			}); err != nil {
				t.Errorf("encode response: %v", err)
			}
		default:
			t.Errorf("unexpected request %d", len(requests))
			http.Error(w, "too many requests", http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	config := validConfig()
	config.GatewayURL = server.URL
	config.HTTPClient = server.Client()
	executor := &Executor{config: config}
	if err := executor.runFileConversation(context.Background(), "initial input"); err != nil {
		t.Fatalf("runFileConversation() error = %v", err)
	}
	if len(requests) != 2 {
		t.Fatalf("got %d requests, want 2", len(requests))
	}
	if requests[0].Input != "initial input" {
		t.Errorf("first input = %q, want initial input", requests[0].Input)
	}
	if requests[1].Input != continueMessage {
		t.Errorf("follow-up input = %q, want %q", requests[1].Input, continueMessage)
	}
	if requests[1].PreviousResponseID != "" {
		t.Errorf("follow-up previous_response_id = %q, want empty after an error response", requests[1].PreviousResponseID)
	}
}

func TestRunFileConversationReturnsNonCutoffResponseError(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limit exceeded"}}`))
	}))
	defer server.Close()

	config := validConfig()
	config.GatewayURL = server.URL
	config.HTTPClient = server.Client()
	executor := &Executor{config: config}
	err := executor.runFileConversation(context.Background(), "initial input")
	if err == nil || err.Error() != "OpenResponses returned HTTP 429: rate limit exceeded" {
		t.Fatalf("runFileConversation() error = %v, want the non-cutoff API error", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
}

func TestRunFileConversationCompletesOnFirstResponse(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if err := json.NewEncoder(w).Encode(responsesAPIResponse{
			ID:     "resp_done",
			Output: assistantText(completionMarker),
		}); err != nil {
			t.Errorf("encode response: %v", err)
		}
	}))
	defer server.Close()

	config := validConfig()
	config.GatewayURL = server.URL
	config.HTTPClient = server.Client()
	executor := &Executor{config: config}
	if err := executor.runFileConversation(context.Background(), "initial input"); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
}

func TestRunFileConversationStopsWhenContextIsCanceled(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if err := json.NewEncoder(w).Encode(responsesAPIResponse{
			ID:     "resp_continue",
			Output: assistantText("not complete"),
		}); err != nil {
			t.Errorf("encode response: %v", err)
		}
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	config := validConfig()
	config.GatewayURL = server.URL
	config.HTTPClient = server.Client()
	executor := &Executor{config: config}

	if err := executor.runFileConversation(ctx, "initial input"); !errors.Is(err, context.Canceled) {
		t.Fatalf("runFileConversation() error = %v, want context.Canceled", err)
	}
	if calls != 0 {
		t.Fatalf("calls = %d, want no requests for canceled context", calls)
	}
}

func assistantText(parts ...string) []responsesOutputItem {
	content := make([]responsesContentPart, 0, len(parts))
	for _, part := range parts {
		content = append(content, responsesContentPart{Type: "output_text", Text: part})
	}
	return []responsesOutputItem{{Type: "message", Role: "assistant", Content: content}}
}

func TestConversationUsesExactCompletionMarker(t *testing.T) {
	tests := []struct {
		name string
		text string
		done bool
	}{
		{name: "exact marker", text: completionMarker, done: true},
		{name: "different case", text: "<Complete />", done: false},
		{name: "similar text", text: "<complete>", done: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := containsCompletionMarker(test.text); got != test.done {
				t.Errorf("containsCompletionMarker(%q) = %v, want %v", test.text, got, test.done)
			}
		})
	}
}
