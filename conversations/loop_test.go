package conversations

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func testConversationConfig(server *httptest.Server) Config {
	return Config{GatewayURL: server.URL, AgentID: "default", HTTPClient: server.Client()}
}

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

	config := testConversationConfig(server)
	executor, err := New(config)
	if err != nil {
		t.Fatal(err)
	}

	if err := executor.RunPrompt(context.Background(), "", "instructions and file contents"); err != nil {
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

	config := testConversationConfig(server)
	executor, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := executor.RunPrompt(context.Background(), "", "initial input"); err != nil {
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

	config := testConversationConfig(server)
	executor, err := NewWithOptions(config, Options{
		RetryWait: func(context.Context, time.Duration) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := executor.RunPrompt(context.Background(), "project/0-135-reuse-conversation-bug.md", "initial input"); err != nil {
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

	config := testConversationConfig(server)
	executor, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := executor.RunPrompt(context.Background(), "project/0-135-reuse-conversation-bug.md", "first file"); err != nil {
		t.Fatalf("runFileConversationForFile() first file error = %v", err)
	}
	if err := executor.RunPrompt(context.Background(), "project/0-136-another-bug.md", "second file"); err != nil {
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

func TestRunFileConversationRetriesHTTPFailuresWithBackoff(t *testing.T) {
	tests := []struct {
		name        string
		statusCode  int
		message     string
		stringError bool
	}{
		{name: "gateway authority error", statusCode: http.StatusForbidden, message: "Gateway requester authority changed"},
		{name: "string-shaped model error", statusCode: http.StatusNotFound, message: `404 {"error":"model 'qwen3.8:27b' not found"}`, stringError: true},
		{name: "server error", statusCode: http.StatusInternalServerError, message: "internal error"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
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
				if len(requests) <= 3 {
					w.WriteHeader(test.statusCode)
					var body any = map[string]any{"error": map[string]string{"message": test.message}}
					if test.stringError {
						body = map[string]string{"error": test.message}
					}
					if err := json.NewEncoder(w).Encode(body); err != nil {
						t.Errorf("encode error response: %v", err)
					}
					return
				}
				if err := json.NewEncoder(w).Encode(responsesAPIResponse{
					ID: "resp_done", Output: assistantText(completionMarker),
				}); err != nil {
					t.Errorf("encode response: %v", err)
				}
			}))
			defer server.Close()

			config := testConversationConfig(server)
			tokens := &countingTokenWaiter{}
			var delays []time.Duration
			service, err := NewWithOptions(config, Options{
				TokenWaiter: tokens,
				RetryWait: func(_ context.Context, delay time.Duration) error {
					delays = append(delays, delay)
					return nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}

			const taskID = "project/example.md"
			if err := service.RunPrompt(context.Background(), taskID, "initial input"); err != nil {
				t.Fatalf("RunPrompt() error = %v", err)
			}
			if len(requests) != 4 {
				t.Fatalf("requests = %d, want 4", len(requests))
			}
			for i, request := range requests {
				if request.Input != "initial input" || request.PreviousResponseID != "" {
					t.Errorf("request %d = input %q, previous ID %q; want original input and no response ID", i+1, request.Input, request.PreviousResponseID)
				}
				if want := conversationSessionKey(config.AgentID, taskID); sessionKeys[i] != want {
					t.Errorf("request %d session key = %q, want %q", i+1, sessionKeys[i], want)
				}
			}
			if tokens.calls != len(requests) {
				t.Errorf("token waits = %d, want one per request (%d)", tokens.calls, len(requests))
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
		})
	}
}

func TestRunFileConversationCancellationStopsRetryingHTTPFailure(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"internal error"}}`))
	}))
	defer server.Close()

	service, err := NewWithOptions(testConversationConfig(server), Options{
		RetryWait: func(context.Context, time.Duration) error { return context.Canceled },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.RunPrompt(context.Background(), "", "initial input"); !errors.Is(err, context.Canceled) {
		t.Fatalf("RunPrompt() error = %v, want context.Canceled", err)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
}

func TestRunFileConversationRetriesAPIErrorWithoutLosingContinuationContext(t *testing.T) {
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
			if err := json.NewEncoder(w).Encode(responsesAPIResponse{ID: "response-1", Output: assistantText("continue")}); err != nil {
				t.Errorf("encode first response: %v", err)
			}
		case 2:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"message":"Gateway requester authority changed"}}`))
		default:
			if err := json.NewEncoder(w).Encode(responsesAPIResponse{ID: "response-2", Output: assistantText(completionMarker)}); err != nil {
				t.Errorf("encode completed response: %v", err)
			}
		}
	}))
	defer server.Close()

	config := testConversationConfig(server)
	var delays []time.Duration
	executor, err := NewWithOptions(config, Options{
		RetryWait: func(_ context.Context, delay time.Duration) error {
			delays = append(delays, delay)
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	filePath := "project/example.md"
	if err := executor.RunPrompt(context.Background(), filePath, "initial prompt"); err != nil {
		t.Fatalf("runFileConversationForFile() error = %v", err)
	}
	if len(requests) != 3 {
		t.Fatalf("requests = %d, want 3", len(requests))
	}
	wantInputs := []string{"initial prompt", continueMessage, continueMessage}
	wantPreviousIDs := []string{"", "response-1", "response-1"}
	wantSessionKey := conversationSessionKey(config.AgentID, filePath)
	for i := range requests {
		if requests[i].Input != wantInputs[i] {
			t.Errorf("request %d input = %q, want %q", i+1, requests[i].Input, wantInputs[i])
		}
		if requests[i].PreviousResponseID != wantPreviousIDs[i] {
			t.Errorf("request %d previous_response_id = %q, want %q", i+1, requests[i].PreviousResponseID, wantPreviousIDs[i])
		}
		if sessionKeys[i] != wantSessionKey {
			t.Errorf("request %d session key = %q, want %q", i+1, sessionKeys[i], wantSessionKey)
		}
	}
	if len(delays) != 1 || delays[0] != time.Second {
		t.Errorf("retry delays = %v, want [1s]", delays)
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
	config := testConversationConfig(server)
	executor, err := New(config)
	if err != nil {
		t.Fatal(err)
	}

	if err := executor.RunPrompt(ctx, "", "initial input"); !errors.Is(err, context.Canceled) {
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

type countingTokenWaiter struct{ calls int }

func (w *countingTokenWaiter) Wait(context.Context) error {
	w.calls++
	return nil
}
