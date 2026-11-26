package tasks

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewHTTPRefresherValidatesBaseURL(t *testing.T) {
	for _, value := range []string{
		"",
		"ftp://example.com",
		"http://",
		"http://user:pass@example.com",
		"http://example.com/?q=1",
		"http://example.com#fragment",
	} {
		t.Run(value, func(t *testing.T) {
			if _, err := NewHTTPRefresher(value, nil); err == nil {
				t.Fatalf("NewHTTPRefresher(%q) succeeded, want an error", value)
			}
		})
	}

	for _, value := range []string{
		"http://127.0.0.1:8080",
		"https://files.example.com/explorer",
	} {
		refresher, err := NewHTTPRefresher(value, nil)
		if err != nil {
			t.Fatalf("NewHTTPRefresher(%q) error = %v", value, err)
		}
		if refresher.baseURL.String() != value {
			t.Errorf("baseURL = %q, want %q", refresher.baseURL.String(), value)
		}
	}
}

func TestHTTPRefresherRefreshSucceedsOn2xx(t *testing.T) {
	var gotRequest http.Request
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRequest = *r
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, "<html><body>directory listing</body></html>")
	}))
	defer server.Close()

	refresher, err := NewHTTPRefresher(server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := refresher.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if gotRequest.Method != http.MethodGet {
		t.Errorf("request method = %q, want GET", gotRequest.Method)
	}
	if gotRequest.URL.String() != "/" {
		t.Errorf("request URL = %q, want the base path", gotRequest.URL.String())
	}
}

func TestHTTPRefresherRefreshFailsOnNon2xx(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusInternalServerError, http.StatusServiceUnavailable} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "explorer unavailable", status)
			}))
			defer server.Close()

			refresher, err := NewHTTPRefresher(server.URL, nil)
			if err != nil {
				t.Fatal(err)
			}
			err = refresher.Refresh(context.Background())
			if err == nil {
				t.Fatalf("Refresh() succeeded for a %d response, want an error", status)
			}
			if !strings.Contains(err.Error(), http.StatusText(status)) {
				t.Errorf("Refresh() error = %q, want the %d status text", err, status)
			}
		})
	}
}

func TestHTTPRefresherRefreshFailsOnTransportError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	server.Close()

	refresher, err := NewHTTPRefresher(server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := refresher.Refresh(context.Background()); err == nil {
		t.Fatal("Refresh() succeeded against a closed listener, want an error")
	}
}

func TestHTTPRefresherRefreshStopsOnCancellation(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()

	refresher, err := NewHTTPRefresher(server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- refresher.Refresh(ctx)
	}()

	select {
	case <-started:
	case <-done:
		t.Fatal("Refresh() returned before the server saw the request")
	}
	cancel()

	err = <-done
	if err == nil {
		t.Fatal("Refresh() succeeded after cancellation")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Refresh() error = %v, want context.Canceled", err)
	}
}

func TestHTTPRefresherRefreshRejectsCanceledContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("the server handled a request for an already-canceled context")
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	refresher, err := NewHTTPRefresher(server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = refresher.Refresh(ctx)
	if err == nil {
		t.Fatal("Refresh() succeeded for a canceled context")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Refresh() error = %v, want context.Canceled", err)
	}
}
