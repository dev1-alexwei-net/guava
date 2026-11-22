package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func postPreview(t *testing.T, h http.Handler, rel, action string) *httptest.ResponseRecorder {
	t.Helper()
	form := url.Values{"path": {rel}, "action": {action}}
	req := httptest.NewRequest(http.MethodPost, "/preview", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestPreviewShowsSelectedFileContent(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("previewable content"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := makeTestHandler(t, root)

	rec := postPreview(t, h, "a.txt", "show")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{`id="preview-heading"`, "Preview · a.txt", "previewable content", "Hide preview"} {
		if !strings.Contains(body, want) {
			t.Errorf("rendered page does not contain %q", want)
		}
	}
	if strings.Contains(body, "<script") {
		t.Fatal("preview markup contains client-side JavaScript")
	}
}

func TestPreviewHideRemovesPane(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("previewable content"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := makeTestHandler(t, root)

	show := postPreview(t, h, "a.txt", "show")
	if show.Code != http.StatusOK || !strings.Contains(show.Body.String(), "previewable content") {
		t.Fatalf("show failed: status=%d", show.Code)
	}

	hide := postPreview(t, h, "a.txt", "hide")
	if hide.Code != http.StatusOK {
		t.Fatalf("hide status = %d, want %d", hide.Code, http.StatusOK)
	}
	if strings.Contains(hide.Body.String(), `id="preview-heading"`) {
		t.Fatal("preview pane is still rendered after hiding")
	}
	// The row button must offer "Preview" again.
	if !strings.Contains(hide.Body.String(), ">Preview<") {
		t.Fatal("row does not offer the Preview mutation again after hiding")
	}
}

func TestPreviewSwitchesBetweenFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("alpha content"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "b.txt"), []byte("beta content"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := makeTestHandler(t, root)

	first := postPreview(t, h, "a.txt", "show")
	if !strings.Contains(first.Body.String(), "alpha content") {
		t.Fatalf("first preview does not show alpha: %s", first.Body.String())
	}
	second := postPreview(t, h, "b.txt", "show")
	if second.Code != http.StatusOK {
		t.Fatalf("second preview status = %d", second.Code)
	}
	body := second.Body.String()
	if !strings.Contains(body, "Preview · b.txt") || !strings.Contains(body, "beta content") {
		t.Fatal("preview did not switch to b.txt")
	}
	if strings.Contains(body, "alpha content") {
		t.Fatal("old preview content is still rendered")
	}
}

func TestPreviewRejectsDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	h := makeTestHandler(t, root)

	rec := postPreview(t, h, "sub", "show")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if !strings.Contains(rec.Body.String(), "Preview requires a regular file") {
		t.Fatal("expected directory rejection message")
	}
}

func TestPreviewRejectsMissingFile(t *testing.T) {
	h := makeTestHandler(t, t.TempDir())
	rec := postPreview(t, h, "nope.txt", "show")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestPreviewShowsNoteForBinaryFile(t *testing.T) {
	root := t.TempDir()
	binary := append([]byte{0x00, 0x01, 0x02}, 'x')
	if err := os.WriteFile(filepath.Join(root, "blob.bin"), binary, 0o600); err != nil {
		t.Fatal(err)
	}
	h := makeTestHandler(t, root)

	rec := postPreview(t, h, "blob.bin", "show")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `id="preview-heading"`) {
		t.Fatal("preview pane is not rendered for a binary file")
	}
	if !strings.Contains(body, "does not appear to be UTF-8 text") {
		t.Fatal("expected the non-text note")
	}
}

func TestPreviewSelectionClearsWhenFileDeleted(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("temp"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := makeTestHandler(t, root)

	if rec := postPreview(t, h, "a.txt", "show"); rec.Code != http.StatusOK {
		t.Fatalf("show status = %d", rec.Code)
	}
	if err := os.Remove(filepath.Join(root, "a.txt")); err != nil {
		t.Fatal(err)
	}

	page := httptest.NewRecorder()
	h.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/", nil))
	if page.Code != http.StatusOK {
		t.Fatalf("GET / status = %d", page.Code)
	}
	if strings.Contains(page.Body.String(), `id="preview-heading"`) {
		t.Fatal("preview pane still rendered after the selected file was deleted")
	}
}

func TestPreviewAcceptsPutLikeOtherMutations(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("put content"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := makeTestHandler(t, root)

	form := url.Values{"path": {"a.txt"}, "action": {"show"}}
	req := httptest.NewRequest(http.MethodPut, "/preview", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, want %d", rec.Code, http.StatusOK)
	}
	if !strings.Contains(rec.Body.String(), "put content") {
		t.Fatal("PUT mutation did not render the preview")
	}
}
