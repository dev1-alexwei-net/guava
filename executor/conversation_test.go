package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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

func TestConversationSessionKeyIncludesAgentAndPath(t *testing.T) {
	tests := []struct {
		name    string
		agentID string
		path    string
		want    string
	}{
		{name: "empty file path", agentID: "writer", path: "", want: ""},
		{
			name:    "writer file session",
			agentID: "writer",
			path:    "project/0-135-reuse-conversation-bug.md",
			want:    "agent:writer:guava:project/0-135-reuse-conversation-bug.md",
		},
		{
			name:    "same file under another agent",
			agentID: "reader",
			path:    "project/0-135-reuse-conversation-bug.md",
			want:    "agent:reader:guava:project/0-135-reuse-conversation-bug.md",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := conversationSessionKey(test.agentID, test.path); got != test.want {
				t.Errorf("conversationSessionKey(%q, %q) = %q, want %q", test.agentID, test.path, got, test.want)
			}
		})
	}
}

func TestRunFileConversationReusesSessionAfterModelCutoff(t *testing.T) {
	var requests []responsesRequest
	var sessionKeys []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request responsesRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		requests = append(requests, request)
		sessionKeys = append(sessionKeys, r.Header.Get("x-openclaw-session-key"))

		switch len(requests) {
		case 1:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
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
	executor.retryWait = func(context.Context, time.Duration) error { return nil }
	if err := executor.runFileConversationForFile(context.Background(), "project/0-135-reuse-conversation-bug.md", "initial input"); err != nil {
		t.Fatalf("runFileConversationForFile() error = %v", err)
	}
	if len(requests) != 2 {
		t.Fatalf("got %d requests, want 2", len(requests))
	}
	if requests[1].Input != "initial input" {
		t.Errorf("retry input = %q, want initial input", requests[1].Input)
	}
	wantSessionKey := "agent:default:guava:project/0-135-reuse-conversation-bug.md"
	if sessionKeys[0] != wantSessionKey || sessionKeys[1] != wantSessionKey {
		t.Errorf("session keys = %q, %q; want %q on both requests", sessionKeys[0], sessionKeys[1], wantSessionKey)
	}
	if requests[1].PreviousResponseID != "" {
		t.Errorf("follow-up previous_response_id = %q, want empty after an error response", requests[1].PreviousResponseID)
	}
}

func TestRunFileConversationIsolatesSessionsPerFile(t *testing.T) {
	var seen []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request responsesRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		seen = append(seen, r.Header.Get("x-openclaw-session-key"))
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
	if err := executor.runFileConversationForFile(context.Background(), "project/0-135-reuse-conversation-bug.md", "first file"); err != nil {
		t.Fatalf("runFileConversationForFile() first file error = %v", err)
	}
	if err := executor.runFileConversationForFile(context.Background(), "project/0-136-another-bug.md", "second file"); err != nil {
		t.Fatalf("runFileConversationForFile() second file error = %v", err)
	}
	if len(seen) != 2 {
		t.Fatalf("got %d requests, want 2", len(seen))
	}
	if seen[0] == "" || seen[0] == seen[1] {
		t.Errorf("sessions = %v, want distinct stable keys per file", seen)
	}
	if seen[0] != "agent:default:guava:project/0-135-reuse-conversation-bug.md" || seen[1] != "agent:default:guava:project/0-136-another-bug.md" {
		t.Errorf("sessions = %v, want keys derived from each file path", seen)
	}
}

func TestRunFileConversationSpendsTokenForEveryRequest(t *testing.T) {
	var executor *Executor
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		executor.conversationTokens.mu.Lock()
		tokens := executor.conversationTokens.tokens
		executor.conversationTokens.mu.Unlock()
		wantTokens := float64(10 - requests)
		if tokens != wantTokens {
			t.Errorf("tokens after request %d = %v, want %v", requests, tokens, wantTokens)
		}
		output := "keep going"
		if requests == 2 {
			output = completionMarker
		}
		if err := json.NewEncoder(w).Encode(responsesAPIResponse{
			ID:     "resp",
			Output: assistantText(output),
		}); err != nil {
			t.Errorf("encode response: %v", err)
		}
	}))
	defer server.Close()

	config := validConfig()
	config.GatewayURL = server.URL
	config.HTTPClient = server.Client()
	fixedNow := time.Now()
	executor = &Executor{
		config:             config,
		conversationTokens: newTokenBucket(10, time.Hour, func() time.Time { return fixedNow }),
	}
	if err := executor.runFileConversation(context.Background(), "initial input"); err != nil {
		t.Fatalf("runFileConversation() error = %v", err)
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
}

func TestRunFileConversationRetriesHTTP500WithBackoffAndTokens(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		if requests <= 3 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"message":"internal error"}}`))
			return
		}
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
	var logOutput bytes.Buffer
	config.Logger = NewTextLogger(&logOutput, slog.LevelInfo)
	fixedNow := time.Now()
	executor := &Executor{
		config:             config,
		conversationTokens: newTokenBucket(10, time.Hour, func() time.Time { return fixedNow }),
	}
	var delays []time.Duration
	executor.retryWait = func(_ context.Context, delay time.Duration) error {
		delays = append(delays, delay)
		return nil
	}

	if err := executor.runFileConversation(context.Background(), "initial input"); err != nil {
		t.Fatalf("runFileConversation() error = %v", err)
	}
	if requests != 4 {
		t.Fatalf("requests = %d, want 4", requests)
	}
	wantDelays := []time.Duration{time.Second, 5 * time.Second, 30 * time.Second}
	if len(delays) != len(wantDelays) {
		t.Fatalf("retry delays = %v, want %v", delays, wantDelays)
	}
	for i := range wantDelays {
		if delays[i] != wantDelays[i] {
			t.Errorf("retry delay %d = %v, want %v", i+1, delays[i], wantDelays[i])
		}
	}
	executor.conversationTokens.mu.Lock()
	remainingTokens := executor.conversationTokens.tokens
	executor.conversationTokens.mu.Unlock()
	if remainingTokens != 6 {
		t.Errorf("remaining tokens = %v, want 6 after four requests", remainingTokens)
	}
	logs := logOutput.String()
	if got := strings.Count(logs, `msg="Spent conversation token for OpenResponses request"`); got != 4 {
		t.Errorf("token-spending log count = %d, want 4; logs:\n%s", got, logs)
	}
	for _, want := range []string{
		"tokens_spent=1",
		`msg="Retrying OpenResponses after HTTP 500"`,
		"retry_attempt=1",
		"retry_attempt=2",
		"retry_attempt=3",
		"retry_delay=1s",
		"retry_delay=5s",
		"retry_delay=30s",
	} {
		if !strings.Contains(logs, want) {
			t.Errorf("logs do not contain %q:\n%s", want, logs)
		}
	}
}

func TestRunFileConversationCancellationStopsHTTP500Retry(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"internal error"}}`))
	}))
	defer server.Close()

	config := validConfig()
	config.GatewayURL = server.URL
	config.HTTPClient = server.Client()
	executor := &Executor{config: config}
	executor.retryWait = func(context.Context, time.Duration) error { return context.Canceled }

	if err := executor.runFileConversation(context.Background(), "initial input"); !errors.Is(err, context.Canceled) {
		t.Fatalf("runFileConversation() error = %v, want context.Canceled", err)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
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
