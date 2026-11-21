package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEmbeddedBootstrapStylesheetIsServedLocally(t *testing.T) {
	h, err := newHandler(t.TempDir(), openTestStore(t))
	if err != nil {
		t.Fatalf("newHandler: %v", err)
	}
	recorder := httptest.NewRecorder()
	h.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/static/bootstrap.min.css", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); got != "text/css; charset=utf-8" {
		t.Fatalf("Content-Type=%q", got)
	}
	if got := recorder.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options=%q", got)
	}
	for _, want := range []string{"Bootstrap  v5.3.8", ".container", ".row"} {
		if !strings.Contains(recorder.Body.String(), want) {
			t.Errorf("stylesheet does not contain %q", want)
		}
	}
}
