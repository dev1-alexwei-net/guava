package main

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func makeTestHandler(t *testing.T, root string) http.Handler {
	t.Helper()
	h, err := newHandler(root, openTestStore(t))
	if err != nil {
		t.Fatalf("newHandler: %v", err)
	}
	return h
}

func formRequest(t *testing.T, h http.Handler, method, target string, values url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(values.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recorder := httptest.NewRecorder()
	h.ServeHTTP(recorder, req)
	return recorder
}

func TestHandlerSyncsAndSortsDirectoriesFirst(t *testing.T) {
	root := t.TempDir()
	h := makeTestHandler(t, root)
	if err := os.Mkdir(filepath.Join(root, "z-dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	h.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	body := recorder.Body.String()
	dirAt, fileAt := strings.Index(body, "z-dir/"), strings.Index(body, "a.txt")
	if recorder.Code != http.StatusOK || dirAt < 0 || fileAt < 0 || dirAt > fileAt {
		t.Fatalf("status=%d directory index=%d file index=%d body=%s", recorder.Code, dirAt, fileAt, body)
	}
	if got := recorder.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Fatalf("Content-Type = %q", got)
	}

	fileResponse := httptest.NewRecorder()
	h.ServeHTTP(fileResponse, httptest.NewRequest(http.MethodGet, "/f/a.txt", nil))
	if fileResponse.Code != http.StatusOK || fileResponse.Body.String() != "a" {
		t.Fatalf("file response: status=%d body=%q", fileResponse.Code, fileResponse.Body.String())
	}
}

func TestHandlerMutationRoutes(t *testing.T) {
	root := t.TempDir()
	h := makeTestHandler(t, root)

	created := formRequest(t, h, http.MethodPost, "/mkdir", url.Values{"name": {"docs"}})
	if created.Code != http.StatusOK || !strings.Contains(created.Body.String(), "docs/") {
		t.Fatalf("mkdir response: status=%d body=%s", created.Code, created.Body.String())
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("parent", "docs"); err != nil {
		t.Fatal(err)
	}
	part, err := writer.CreateFormFile("file", "note.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(part, "hello"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	upload := httptest.NewRequest(http.MethodPost, "/upload", &body)
	upload.Header.Set("Content-Type", writer.FormDataContentType())
	uploadResponse := httptest.NewRecorder()
	h.ServeHTTP(uploadResponse, upload)
	if uploadResponse.Code != http.StatusOK {
		t.Fatalf("upload status=%d body=%s", uploadResponse.Code, uploadResponse.Body.String())
	}

	renamed := formRequest(t, h, http.MethodPut, "/rename", url.Values{"old": {"docs/note.txt"}, "new": {"renamed.txt"}})
	if renamed.Code != http.StatusOK {
		t.Fatalf("rename status=%d body=%s", renamed.Code, renamed.Body.String())
	}
	if _, err := os.Stat(filepath.Join(root, "docs", "renamed.txt")); err != nil {
		t.Fatalf("renamed file: %v", err)
	}

	deleted := formRequest(t, h, http.MethodPost, "/delete", url.Values{"path": {"docs/renamed.txt"}})
	if deleted.Code != http.StatusOK {
		t.Fatalf("delete status=%d body=%s", deleted.Code, deleted.Body.String())
	}
	if _, err := os.Stat(filepath.Join(root, "docs", "renamed.txt")); !os.IsNotExist(err) {
		t.Fatalf("file still exists after delete: err=%v", err)
	}

	putCreated := formRequest(t, h, http.MethodPut, "/mkdir", url.Values{"parent": {"docs"}, "name": {"via-put"}})
	if putCreated.Code != http.StatusOK {
		t.Fatalf("PUT mkdir status=%d body=%s", putCreated.Code, putCreated.Body.String())
	}
}

func TestHandlerRejectsTraversalAndSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(outsideFile, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	h := makeTestHandler(t, root)

	for _, test := range []struct {
		name string
		run  func() *httptest.ResponseRecorder
		want int
	}{
		{
			name: "encoded URL traversal",
			run: func() *httptest.ResponseRecorder {
				recorder := httptest.NewRecorder()
				h.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/d/%2e%2e%2foutside", nil))
				return recorder
			},
			want: http.StatusBadRequest,
		},
		{
			name: "form traversal",
			run: func() *httptest.ResponseRecorder {
				return formRequest(t, h, http.MethodPost, "/mkdir", url.Values{"parent": {"../outside"}, "name": {"bad"}})
			},
			want: http.StatusBadRequest,
		},
		{
			name: "absolute path",
			run: func() *httptest.ResponseRecorder {
				return formRequest(t, h, http.MethodPost, "/delete", url.Values{"path": {outsideFile}})
			},
			want: http.StatusBadRequest,
		},
		{
			name: "symlink directory browse",
			run: func() *httptest.ResponseRecorder {
				recorder := httptest.NewRecorder()
				h.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/d/escape", nil))
				return recorder
			},
			want: http.StatusForbidden,
		},
		{
			name: "symlink editor read",
			run: func() *httptest.ResponseRecorder {
				recorder := httptest.NewRecorder()
				h.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/edit/escape/secret.txt", nil))
				return recorder
			},
			want: http.StatusForbidden,
		},
		{
			name: "symlink mutation",
			run: func() *httptest.ResponseRecorder {
				return formRequest(t, h, http.MethodPost, "/delete", url.Values{"path": {"escape/secret.txt"}})
			},
			want: http.StatusForbidden,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := test.run()
			if response.Code != test.want {
				t.Fatalf("status=%d, want %d; body=%s", response.Code, test.want, response.Body.String())
			}
			if got := response.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
				t.Fatalf("Content-Type=%q, want server-rendered HTML", got)
			}
		})
	}
	if contents, err := os.ReadFile(outsideFile); err != nil || string(contents) != "secret" {
		t.Fatalf("outside file changed: contents=%q err=%v", contents, err)
	}
}
