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
	if created.Code != http.StatusSeeOther || created.Header().Get("Location") != "/" {
		t.Fatalf("mkdir redirect: status=%d location=%q body=%s", created.Code, created.Header().Get("Location"), created.Body.String())
	}
	createdDirectory := get(t, h, created.Header().Get("Location"))
	if createdDirectory.Code != http.StatusOK || !strings.Contains(createdDirectory.Body.String(), "docs/") {
		t.Fatalf("mkdir destination: status=%d body=%s", createdDirectory.Code, createdDirectory.Body.String())
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
	if uploadResponse.Code != http.StatusSeeOther || uploadResponse.Header().Get("Location") != "/d/docs" {
		t.Fatalf("upload redirect: status=%d location=%q body=%s", uploadResponse.Code, uploadResponse.Header().Get("Location"), uploadResponse.Body.String())
	}

	renamed := formRequest(t, h, http.MethodPut, "/rename", url.Values{"old": {"docs/note.txt"}, "new": {"renamed.txt"}})
	if renamed.Code != http.StatusSeeOther || renamed.Header().Get("Location") != "/d/docs" {
		t.Fatalf("rename redirect: status=%d location=%q body=%s", renamed.Code, renamed.Header().Get("Location"), renamed.Body.String())
	}
	if _, err := os.Stat(filepath.Join(root, "docs", "renamed.txt")); err != nil {
		t.Fatalf("renamed file: %v", err)
	}

	deleted := formRequest(t, h, http.MethodPost, "/delete", url.Values{"path": {"docs/renamed.txt"}})
	if deleted.Code != http.StatusSeeOther || deleted.Header().Get("Location") != "/d/docs" {
		t.Fatalf("delete redirect: status=%d location=%q body=%s", deleted.Code, deleted.Header().Get("Location"), deleted.Body.String())
	}
	if _, err := os.Stat(filepath.Join(root, "docs", "renamed.txt")); !os.IsNotExist(err) {
		t.Fatalf("file still exists after delete: err=%v", err)
	}

	putCreated := formRequest(t, h, http.MethodPut, "/mkdir", url.Values{"parent": {"docs"}, "name": {"via-put"}})
	if putCreated.Code != http.StatusSeeOther || putCreated.Header().Get("Location") != "/d/docs" {
		t.Fatalf("PUT mkdir redirect: status=%d location=%q body=%s", putCreated.Code, putCreated.Header().Get("Location"), putCreated.Body.String())
	}
}

func TestMutationErrorsRedirectToDirectoryWithRefreshableError(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "docs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs", "one.txt"), []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs", "two.txt"), []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := makeTestHandler(t, root)

	duplicateFolder := formRequest(t, h, http.MethodPost, "/mkdir", url.Values{"name": {"docs"}})
	if duplicateFolder.Code != http.StatusSeeOther || !strings.HasPrefix(duplicateFolder.Header().Get("Location"), "/?error=") {
		t.Fatalf("mkdir error redirect: status=%d location=%q body=%s", duplicateFolder.Code, duplicateFolder.Header().Get("Location"), duplicateFolder.Body.String())
	}
	rootPage := get(t, h, duplicateFolder.Header().Get("Location"))
	if rootPage.Code != http.StatusOK || !strings.Contains(rootPage.Body.String(), "Unable to create directory") || !strings.Contains(rootPage.Body.String(), "docs/") {
		t.Fatalf("mkdir error destination: status=%d body=%s", rootPage.Code, rootPage.Body.String())
	}

	renameConflict := formRequest(t, h, http.MethodPost, "/rename", url.Values{"old": {"docs/one.txt"}, "new": {"two.txt"}})
	if renameConflict.Code != http.StatusSeeOther || !strings.HasPrefix(renameConflict.Header().Get("Location"), "/d/docs?error=") {
		t.Fatalf("rename error redirect: status=%d location=%q body=%s", renameConflict.Code, renameConflict.Header().Get("Location"), renameConflict.Body.String())
	}
	docsPage := get(t, h, renameConflict.Header().Get("Location"))
	if docsPage.Code != http.StatusOK || !strings.Contains(docsPage.Body.String(), "An entry with that name already exists") || !strings.Contains(docsPage.Body.String(), "one.txt") {
		t.Fatalf("rename error destination: status=%d body=%s", docsPage.Code, docsPage.Body.String())
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
		name             string
		run              func() *httptest.ResponseRecorder
		want             int
		wantPageContains string
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
			want:             http.StatusSeeOther,
			wantPageContains: "Invalid path",
		},
		{
			name: "absolute path",
			run: func() *httptest.ResponseRecorder {
				return formRequest(t, h, http.MethodPost, "/delete", url.Values{"path": {outsideFile}})
			},
			want:             http.StatusSeeOther,
			wantPageContains: "Invalid path",
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
			want:             http.StatusSeeOther,
			wantPageContains: "outside the configured root",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := test.run()
			if response.Code != test.want {
				t.Fatalf("status=%d, want %d; body=%s", response.Code, test.want, response.Body.String())
			}
			if test.wantPageContains != "" {
				location := response.Header().Get("Location")
				if !strings.Contains(location, "error=") {
					t.Fatalf("mutation error redirect lacks an error marker: %q", location)
				}
				response = get(t, h, location)
				if !strings.Contains(response.Body.String(), test.wantPageContains) {
					t.Fatalf("redirect destination lacks %q: %s", test.wantPageContains, response.Body.String())
				}
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

func TestRootedFilesystemOwnsPathPolicy(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs", "note.txt"), []byte("note"), 0o600); err != nil {
		t.Fatal(err)
	}
	filesystem, err := openRootedFS(root)
	if err != nil {
		t.Fatal(err)
	}
	defer filesystem.Close()

	for input, relative := range map[string]string{
		".":              "",
		"docs//note.txt": "docs/note.txt",
		"new/deep/file":  "new/deep/file",
	} {
		got, err := filesystem.Resolve(input)
		if err != nil {
			t.Fatalf("Resolve(%q): %v", input, err)
		}
		want := filepath.Join(root, filepath.FromSlash(relative))
		if got != want {
			t.Errorf("Resolve(%q) = %q, want %q", input, got, want)
		}
	}
	for _, input := range []string{"../outside", "docs/../../outside", filepath.Join(root, "docs", "note.txt")} {
		if _, err := filesystem.Resolve(input); err != errInvalidPath {
			t.Errorf("Resolve(%q) error = %v, want %v", input, err, errInvalidPath)
		}
	}
	if _, err := filesystem.Stat("missing.txt"); !os.IsNotExist(err) {
		t.Errorf("Stat(missing.txt) error = %v, want not-exist", err)
	}

	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Logf("symlink test skipped: %v", err)
		return
	}
	if _, err := filesystem.Resolve("escape/secret.txt"); err != errForbidden {
		t.Errorf("Resolve through symlink error = %v, want %v", err, errForbidden)
	}
	if _, err := filesystem.Open("escape/secret.txt"); err != errForbidden {
		t.Errorf("Open through symlink error = %v, want %v", err, errForbidden)
	}
}
