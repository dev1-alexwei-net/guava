package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func get(t *testing.T, h http.Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

func TestPreviewPageShowsSelectedFileContent(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("previewable content"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := makeTestHandler(t, root)

	rec := get(t, h, "/preview/a.txt")
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

func TestPreviewPageBackLinkTargetsParentDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "docs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs", "a.txt"), []byte("nested content"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "top.txt"), []byte("top content"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := makeTestHandler(t, root)

	nested := get(t, h, "/preview/docs/a.txt")
	if nested.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", nested.Code, http.StatusOK)
	}
	if !strings.Contains(nested.Body.String(), `href="/d/docs">Hide preview`) {
		t.Errorf("back link does not target the parent directory: %s", nested.Body.String())
	}

	top := get(t, h, "/preview/top.txt")
	if top.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", top.Code, http.StatusOK)
	}
	if !strings.Contains(top.Body.String(), `href="/d/">Hide preview`) {
		t.Errorf("back link does not target the root directory: %s", top.Body.String())
	}
}

func TestPlainDirectoryPageHasNoPreviewPane(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("previewable content"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := makeTestHandler(t, root)

	// Visiting a preview first must not leak state into the plain directory.
	if rec := get(t, h, "/preview/a.txt"); rec.Code != http.StatusOK {
		t.Fatalf("preview status = %d", rec.Code)
	}

	rec := get(t, h, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / status = %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, `id="preview-heading"`) {
		t.Fatal("plain directory page renders the preview pane")
	}
	if !strings.Contains(body, `href="/preview/a.txt">Preview`) {
		t.Fatal("row does not offer the preview link")
	}
}

func TestPreviewSelectionFollowsTheURL(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("alpha content"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "b.txt"), []byte("beta content"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := makeTestHandler(t, root)

	second := get(t, h, "/preview/b.txt")
	if second.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", second.Code, http.StatusOK)
	}
	body := second.Body.String()
	if !strings.Contains(body, "Preview · b.txt") || !strings.Contains(body, "beta content") {
		t.Fatal("preview does not follow the requested URL")
	}
	if strings.Contains(body, "alpha content") {
		t.Fatal("unrelated file content is rendered")
	}
	// b.txt is previewing, so its row offers Hide preview while a.txt offers Preview.
	if !strings.Contains(body, `href="/preview/a.txt">Preview`) {
		t.Fatal("non-previewed row does not offer the preview link")
	}
	if !strings.Contains(body, `href="/d/">Hide preview`) {
		t.Fatal("previewed row does not offer the hide link")
	}
}

func TestPreviewRejectsDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	h := makeTestHandler(t, root)

	rec := get(t, h, "/preview/sub")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if !strings.Contains(rec.Body.String(), "Preview requires a regular file") {
		t.Fatal("expected directory rejection message")
	}
}

func TestPreviewRejectsMissingFile(t *testing.T) {
	h := makeTestHandler(t, t.TempDir())
	rec := get(t, h, "/preview/nope.txt")
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

	rec := get(t, h, "/preview/blob.bin")
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

func TestPreviewShowsNoteForOversizedFile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "big.txt"), make([]byte, maxPreviewSize+1), 0o600); err != nil {
		t.Fatal(err)
	}
	h := makeTestHandler(t, root)

	rec := get(t, h, "/preview/big.txt")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `id="preview-heading"`) {
		t.Fatal("preview pane is not rendered for an oversized file")
	}
	if !strings.Contains(body, "too large to preview") {
		t.Fatal("expected the size note")
	}
}

func TestPreviewClearsWhenFileDeleted(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("temp"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := makeTestHandler(t, root)

	if rec := get(t, h, "/preview/a.txt"); rec.Code != http.StatusOK {
		t.Fatalf("preview status = %d", rec.Code)
	}
	if err := os.Remove(filepath.Join(root, "a.txt")); err != nil {
		t.Fatal(err)
	}

	rec := get(t, h, "/preview/a.txt")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d after deletion", rec.Code, http.StatusNotFound)
	}
}
