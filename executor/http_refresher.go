package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// HTTPRefresher refreshes the fileexplorer index over HTTP. fileexplorer
// re-synchronizes its SQLite index whenever a directory page is rendered, so
// a successful GET to the base URL is a complete refresh. The HTML response
// body is drained and closed without being parsed or retained.
//
// The adapter depends only on the base URL and an HTTP client; it does not
// touch the queue or SQLite index implementations. Its Refresh method
// satisfies the Refresher contract from story #109, so a coordinator can use
// it directly once the queue is integrated.
type HTTPRefresher struct {
	baseURL *url.URL
	client  *http.Client
}

// NewHTTPRefresher validates baseURL and returns a refresher for it. baseURL
// must be an http or https base URL with a host and without credentials, a
// query, or a fragment, the same rules as the other base URLs. A nil client
// uses http.DefaultClient.
func NewHTTPRefresher(baseURL string, client *http.Client) (*HTTPRefresher, error) {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return nil, fmt.Errorf("parse fileexplorer URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("fileexplorer URL must use http or https")
	}
	if parsed.Hostname() == "" {
		return nil, fmt.Errorf("fileexplorer URL must include a host")
	}
	if parsed.User != nil {
		return nil, fmt.Errorf("fileexplorer URL must not contain user information")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("fileexplorer URL must not contain a query or fragment")
	}
	if client == nil {
		client = http.DefaultClient
	}
	return &HTTPRefresher{baseURL: parsed, client: client}, nil
}

// Refresh issues a context-bound GET to the fileexplorer base URL and treats
// a non-2xx response as a refresh failure, including the response status in
// the returned error. The response body is drained and closed and is never
// parsed or retained. Cancellation of ctx stops the request.
func (r *HTTPRefresher) Refresh(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.baseURL.String(), nil)
	if err != nil {
		return fmt.Errorf("build fileexplorer refresh request: %w", err)
	}

	resp, err := r.client.Do(req)
	if err != nil {
		return fmt.Errorf("refresh fileexplorer index: %w", err)
	}
	defer resp.Body.Close()

	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		return fmt.Errorf("discard fileexplorer response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("refresh fileexplorer index: unexpected status %s", resp.Status)
	}
	return nil
}
