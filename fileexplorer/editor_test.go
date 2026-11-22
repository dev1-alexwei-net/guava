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

func TestEditorReadsAndSavesTextFile(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	filePath := filepath.Join(root, "docs", "note%.txt")
	if err := os.WriteFile(filePath, []byte("original\ntext & symbols\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := makeTestHandler(t, root)
	target := editorURL("docs/note%.txt")

	view := httptest.NewRecorder()
	h.ServeHTTP(view, httptest.NewRequest(http.MethodGet, target, nil))
	if view.Code != http.StatusOK || !strings.Contains(view.Body.String(), "text &amp; symbols") || !strings.Contains(view.Body.String(), "name=\"content\"") {
		t.Fatalf("editor response: status=%d body=%s", view.Code, view.Body.String())
	}

	content := "saved\nsecond line & more\n"
	saved := formRequest(t, h, http.MethodPost, target, url.Values{"content": {content}})
	if saved.Code != http.StatusSeeOther {
		t.Fatalf("save status=%d body=%s", saved.Code, saved.Body.String())
	}
	if got, err := os.ReadFile(filePath); err != nil || string(got) != content {
		t.Fatalf("saved contents=%q err=%v", got, err)
	}
	if info, err := os.Stat(filePath); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("file mode=%v err=%v; want 0600", info.Mode().Perm(), err)
	}

	after := httptest.NewRecorder()
	h.ServeHTTP(after, httptest.NewRequest(http.MethodGet, saved.Header().Get("Location"), nil))
	if after.Code != http.StatusOK || !strings.Contains(after.Body.String(), "File saved.") {
		t.Fatalf("saved editor response: status=%d body=%s", after.Code, after.Body.String())
	}
}

func TestFileServingIsSafeAndDownloadable(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "page.html"), []byte("<script>nope</script>"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := makeTestHandler(t, root)

	inline := httptest.NewRecorder()
	h.ServeHTTP(inline, httptest.NewRequest(http.MethodGet, "/f/page.html", nil))
	if inline.Code != http.StatusOK || inline.Header().Get("Content-Type") != "text/plain; charset=utf-8" || inline.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("inline response headers=%v status=%d", inline.Header(), inline.Code)
	}

	download := httptest.NewRecorder()
	h.ServeHTTP(download, httptest.NewRequest(http.MethodGet, "/f/page.html?download=1", nil))
	if download.Code != http.StatusOK || download.Header().Get("Content-Type") != "application/octet-stream" || !strings.Contains(download.Header().Get("Content-Disposition"), "attachment") {
		t.Fatalf("download response headers=%v status=%d", download.Header(), download.Code)
	}
}

func TestEditorRejectsBinaryAndOversizedFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "binary.dat"), []byte{0, 1, 2}, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "large.txt"), make([]byte, maxEditorSize+1), 0o600); err != nil {
		t.Fatal(err)
	}
	h := makeTestHandler(t, root)

	for _, test := range []struct {
		name string
		path string
		want int
	}{
		{name: "binary", path: "/edit/binary.dat", want: http.StatusUnsupportedMediaType},
		{name: "oversized", path: "/edit/large.txt", want: http.StatusRequestEntityTooLarge},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			h.ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.path, nil))
			if response.Code != test.want {
				t.Fatalf("status=%d want=%d body=%s", response.Code, test.want, response.Body.String())
			}
		})
	}
}

func TestCreateFileAndDeleteConfirmation(t *testing.T) {
	root := t.TempDir()
	h := makeTestHandler(t, root)

	created := formRequest(t, h, http.MethodPost, "/create-file", url.Values{"name": {"new.txt"}})
	if created.Code != http.StatusSeeOther || created.Header().Get("Location") != "/" {
		t.Fatalf("create redirect: status=%d location=%q body=%s", created.Code, created.Header().Get("Location"), created.Body.String())
	}
	createdPage := get(t, h, created.Header().Get("Location"))
	if createdPage.Code != http.StatusOK || !strings.Contains(createdPage.Body.String(), "new.txt") {
		t.Fatalf("create destination: status=%d body=%s", createdPage.Code, createdPage.Body.String())
	}
	filePath := filepath.Join(root, "new.txt")
	if info, err := os.Stat(filePath); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("created file info=%v err=%v", info, err)
	}

	confirmation := httptest.NewRecorder()
	h.ServeHTTP(confirmation, httptest.NewRequest(http.MethodGet, "/confirm-delete/new.txt", nil))
	if confirmation.Code != http.StatusOK || !strings.Contains(confirmation.Body.String(), "Confirm delete") {
		t.Fatalf("confirmation status=%d body=%s", confirmation.Code, confirmation.Body.String())
	}
	if _, err := os.Stat(filePath); err != nil {
		t.Fatalf("GET confirmation deleted the file: %v", err)
	}

	deleted := formRequest(t, h, http.MethodPost, "/delete", url.Values{"path": {"new.txt"}})
	if deleted.Code != http.StatusSeeOther || deleted.Header().Get("Location") != "/" {
		t.Fatalf("delete redirect: status=%d location=%q body=%s", deleted.Code, deleted.Header().Get("Location"), deleted.Body.String())
	}
	deletedPage := get(t, h, deleted.Header().Get("Location"))
	if deletedPage.Code != http.StatusOK || strings.Contains(deletedPage.Body.String(), "new.txt") {
		t.Fatalf("delete destination: status=%d body=%s", deletedPage.Code, deletedPage.Body.String())
	}
	if _, err := os.Stat(filePath); !os.IsNotExist(err) {
		t.Fatalf("file still exists after confirmation: %v", err)
	}
}
