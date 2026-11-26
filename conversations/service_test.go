package conversations

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/dev1-alexwei-net/guava/tasks"
)

func TestRunConsumesTaskPromptsAndCompletesSession(t *testing.T) {
	var gotInputs []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Errorf("request path = %q, want /v1/responses", r.URL.Path)
		}
		if got := r.Header.Get("x-openclaw-agent-id"); got != "agent-test" {
			t.Errorf("agent header = %q, want agent-test", got)
		}
		var request struct {
			Input string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		gotInputs = append(gotInputs, request.Input)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"response-1","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"<complete />"}]}]}`))
	}))
	defer server.Close()

	service, err := New(Config{GatewayURL: server.URL, AgentID: "agent-test", CLIArgs: "executor -test", Subdirectory: "docs", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	provider := tasks.NewStaticProvider(
		tasks.NewTask("docs/a.md", func() (string, error) { return "prompt a", nil }),
		tasks.NewTask("docs/b.md", func() (string, error) { return "prompt b", nil }),
	)
	if err := service.Run(context.Background(), provider); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if want := []string{"prompt a", "prompt b"}; !reflect.DeepEqual(gotInputs, want) {
		t.Fatalf("request inputs = %#v, want %#v", gotInputs, want)
	}
	if got := service.SessionSummary(); got.Status != StatusCompleted || got.AgentID != "agent-test" || got.CLIArgs != "executor -test" || got.Subdirectory != "docs" {
		t.Fatalf("SessionSummary() = %+v, want completed session metadata", got)
	}
}
