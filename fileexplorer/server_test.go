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

func get(t *testing.T, h http.Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

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

func TestCreatedDirectoriesAreWritableByOtherUsers(t *testing.T) {
	root := t.TempDir()
	existing := filepath.Join(root, "existing")
	if err := os.Mkdir(existing, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(existing, 0o700); err != nil {
		t.Fatal(err)
	}
	h := makeTestHandler(t, root)

	created := formRequest(t, h, http.MethodPost, "/mkdir", url.Values{"name": {"shared"}})
	if created.Code != http.StatusSeeOther || created.Header().Get("Location") != "/" {
		t.Fatalf("mkdir status=%d location=%q body=%s", created.Code, created.Header().Get("Location"), created.Body.String())
	}
	createdInfo, err := os.Stat(filepath.Join(root, "shared"))
	if err != nil {
		t.Fatalf("stat new directory: %v", err)
	}
	if got := createdInfo.Mode().Perm(); got != 0o777 {
		t.Fatalf("new directory permissions=%#o, want %#o", got, os.FileMode(0o777))
	}
	existingInfo, err := os.Stat(existing)
	if err != nil {
		t.Fatalf("stat existing directory: %v", err)
	}
	if got := existingInfo.Mode().Perm(); got != 0o700 {
		t.Fatalf("existing directory permissions changed to %#o, want %#o", got, os.FileMode(0o700))
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
			response = followMutationError(t, h, response, test.wantPageContains)
			if got := response.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
				t.Fatalf("Content-Type=%q, want server-rendered HTML", got)
			}
		})
	}
	if contents, err := os.ReadFile(outsideFile); err != nil || string(contents) != "secret" {
		t.Fatalf("outside file changed: contents=%q err=%v", contents, err)
	}
}

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
	target := newRenderer().EditorURL("docs/note%.txt")

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

func TestRenameConfirmationPage(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "docs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs", "note.txt"), []byte("note"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := makeTestHandler(t, root)

	// GET /rename/{path} presents the dedicated rename form with the entry's
	// current name prefilled, and it must not touch the filesystem.
	confirmation := httptest.NewRecorder()
	h.ServeHTTP(confirmation, httptest.NewRequest(http.MethodGet, "/rename/docs/note.txt", nil))
	if confirmation.Code != http.StatusOK {
		t.Fatalf("rename page status=%d body=%s", confirmation.Code, confirmation.Body.String())
	}
	body := confirmation.Body.String()
	for _, want := range []string{
		`<h1 class="h3 mb-1">Rename entry</h1>`,
		`<h2 class="h4" id="rename-heading">Rename note.txt</h2>`,
		`<form action="/rename" method="post" class="d-flex flex-column flex-sm-row gap-2">`,
		`<input type="hidden" name="old" value="docs/note.txt">`,
		`<input class="form-control form-control-sm" name="new" value="note.txt" aria-label="New name for note.txt" required>`,
		`<button class="btn btn-primary" type="submit">Confirm rename</button>`,
		`<a class="btn btn-outline-secondary" href="/d/docs">Cancel</a>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("rename page missing %q", want)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "docs", "note.txt")); err != nil {
		t.Fatalf("GET rename page changed the filesystem: %v", err)
	}

	// The listing links the row's Rename action to the dedicated page.
	docsPage := get(t, h, "/d/docs")
	if docsPage.Code != http.StatusOK ||
		!strings.Contains(docsPage.Body.String(), `<a class="btn btn-outline-secondary btn-sm" href="/rename/docs/note.txt" aria-label="Rename note.txt">Rename</a>`) {
		t.Errorf("directory listing does not link to the rename page: status=%d body=%s", docsPage.Code, docsPage.Body.String())
	}

	// The rename page preserves the listing's link state, mirroring the
	// confirm-delete page.
	styled := get(t, h, "/rename/docs/note.txt?theme=dark")
	if styled.Code != http.StatusOK ||
		!strings.Contains(styled.Body.String(), `<form action="/rename?theme=dark"`) ||
		!strings.Contains(styled.Body.String(), `href="/d/docs?theme=dark">Cancel</a>`) {
		t.Errorf("rename page lost theme state: status=%d body=%s", styled.Code, styled.Body.String())
	}

	// The form's POST commits the rename and lands back on the parent
	// directory.
	renamed := formRequest(t, h, http.MethodPost, "/rename", url.Values{"old": {"docs/note.txt"}, "new": {"renamed.txt"}})
	if renamed.Code != http.StatusSeeOther || renamed.Header().Get("Location") != "/d/docs" {
		t.Fatalf("rename redirect: status=%d location=%q body=%s", renamed.Code, renamed.Header().Get("Location"), renamed.Body.String())
	}
	if _, err := os.Stat(filepath.Join(root, "docs", "renamed.txt")); err != nil {
		t.Fatalf("renamed file: %v", err)
	}

	// The root directory cannot be renamed, mirroring the delete page.
	rootRename := httptest.NewRecorder()
	h.ServeHTTP(rootRename, httptest.NewRequest(http.MethodGet, "/rename/", nil))
	if rootRename.Code != http.StatusBadRequest {
		t.Fatalf("rename root page status=%d want=%d body=%s", rootRename.Code, http.StatusBadRequest, rootRename.Body.String())
	}
}

func TestDirectoryPageReflectsExternalFilesystemChanges(t *testing.T) {
	root := t.TempDir()
	h := makeTestHandler(t, root)

	request := func() string {
		t.Helper()
		recorder := httptest.NewRecorder()
		h.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("GET / status=%d body=%s", recorder.Code, recorder.Body.String())
		}
		return recorder.Body.String()
	}

	if body := request(); strings.Contains(body, "outside.txt") {
		t.Fatalf("unexpected entry before file creation: %s", body)
	}
	file := filepath.Join(root, "outside.txt")
	if err := os.WriteFile(file, []byte("live"), 0o600); err != nil {
		t.Fatal(err)
	}
	if body := request(); !strings.Contains(body, "outside.txt") {
		t.Fatalf("new file is absent from refreshed page: %s", body)
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if body := request(); strings.Contains(body, "outside.txt") {
		t.Fatalf("removed file remains on refreshed page: %s", body)
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

func followMutationError(t *testing.T, h http.Handler, response *httptest.ResponseRecorder, want string) *httptest.ResponseRecorder {
	t.Helper()
	if want == "" {
		return response
	}
	location := response.Header().Get("Location")
	if !strings.Contains(location, "error=") {
		t.Fatalf("mutation error redirect lacks an error marker: %q", location)
	}
	response = get(t, h, location)
	if !strings.Contains(response.Body.String(), want) {
		t.Fatalf("redirect destination lacks %q: %s", want, response.Body.String())
	}
	return response
}

func TestMoveDestinationForm(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "archive"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "source.txt"), []byte("move me"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "already-there.txt"), []byte("stay"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "archive", "already-there.txt"), []byte("keep"), 0o640); err != nil {
		t.Fatal(err)
	}
	h := makeTestHandler(t, root)

	listing := get(t, h, "/")
	if !strings.Contains(listing.Body.String(), `href="/move/source.txt"`) {
		t.Fatal("file row does not offer the dedicated Move form")
	}
	form := get(t, h, "/move/source.txt")
	body := form.Body.String()
	if form.Code != http.StatusOK || !strings.Contains(body, "Choose a destination") || !strings.Contains(body, `name="destination" required`) || !strings.Contains(body, `value="archive"`) {
		t.Fatalf("move form: status=%d body=%s", form.Code, body)
	}

	moved := formRequest(t, h, http.MethodPost, "/move", url.Values{"source": {"source.txt"}, "destination": {"archive"}})
	if moved.Code != http.StatusSeeOther || !strings.Contains(moved.Header().Get("Location"), "notice=") {
		t.Fatalf("move submission: status=%d location=%q body=%s", moved.Code, moved.Header().Get("Location"), moved.Body.String())
	}
	if _, err := os.Stat(filepath.Join(root, "source.txt")); !os.IsNotExist(err) {
		t.Fatalf("source still exists after move: %v", err)
	}
	if contents, err := os.ReadFile(filepath.Join(root, "archive", "source.txt")); err != nil || string(contents) != "move me" {
		t.Fatalf("destination contents=%q err=%v", contents, err)
	}

	conflict := formRequest(t, h, http.MethodPost, "/move", url.Values{"source": {"already-there.txt"}, "destination": {"archive"}})
	if conflict.Code != http.StatusConflict || !strings.Contains(conflict.Body.String(), "already exists") {
		t.Fatalf("move conflict feedback: status=%d body=%s", conflict.Code, conflict.Body.String())
	}
	if contents, err := os.ReadFile(filepath.Join(root, "already-there.txt")); err != nil || string(contents) != "stay" {
		t.Fatalf("source changed after rejected move: contents=%q err=%v", contents, err)
	}
}

func TestCutCopyPasteFileActions(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "destination"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "source.txt"), []byte("file contents"), 0o640); err != nil {
		t.Fatal(err)
	}
	h := makeTestHandler(t, root)

	listing := get(t, h, "/")
	for _, action := range []string{">Copy</a>", ">Cut</a>"} {
		if !strings.Contains(listing.Body.String(), action) {
			t.Errorf("file secondary actions missing %q", action)
		}
	}
	if !strings.Contains(listing.Body.String(), `href="/clipboard/copy/source.txt"`) || !strings.Contains(listing.Body.String(), `href="/clipboard/cut/source.txt"`) {
		t.Fatal("copy/cut actions are not in the secondary action disclosure")
	}

	selected := get(t, h, "/clipboard/copy/source.txt")
	if selected.Code != http.StatusSeeOther || !strings.Contains(selected.Header().Get("Location"), "clipboard_mode=copy") {
		t.Fatalf("copy selection: status=%d location=%q", selected.Code, selected.Header().Get("Location"))
	}
	location := "/d/destination?clipboard_mode=copy&clipboard_path=source.txt"
	destinationPage := get(t, h, location)
	if destinationPage.Code != http.StatusOK || !strings.Contains(destinationPage.Body.String(), "Ready to copy") || !strings.Contains(destinationPage.Body.String(), `name="destination" value="destination"`) {
		t.Fatalf("copy clipboard did not persist to destination: status=%d body=%s", destinationPage.Code, destinationPage.Body.String())
	}
	copied := formRequest(t, h, http.MethodPost, "/paste", url.Values{
		"clipboard_mode": {"copy"}, "clipboard_path": {"source.txt"}, "destination": {"destination"},
	})
	if copied.Code != http.StatusSeeOther || !strings.Contains(copied.Header().Get("Location"), "notice=") {
		t.Fatalf("copy paste: status=%d location=%q body=%s", copied.Code, copied.Header().Get("Location"), copied.Body.String())
	}
	for _, name := range []string{"source.txt", filepath.Join("destination", "source.txt")} {
		contents, err := os.ReadFile(filepath.Join(root, name))
		if err != nil || string(contents) != "file contents" {
			t.Errorf("%s after copy: contents=%q err=%v", name, contents, err)
		}
	}

	cutSelected := get(t, h, "/clipboard/cut/source.txt")
	if cutSelected.Code != http.StatusSeeOther {
		t.Fatalf("cut selection: status=%d", cutSelected.Code)
	}
	conflict := formRequest(t, h, http.MethodPost, "/paste", url.Values{
		"clipboard_mode": {"cut"}, "clipboard_path": {"source.txt"}, "destination": {"destination"},
	})
	if conflict.Code != http.StatusSeeOther || !strings.Contains(conflict.Header().Get("Location"), "error=") {
		t.Fatalf("cut conflict: status=%d location=%q", conflict.Code, conflict.Header().Get("Location"))
	}
	if _, err := os.Stat(filepath.Join(root, "source.txt")); err != nil {
		t.Fatalf("source removed by conflicting cut: %v", err)
	}
	if err := os.Remove(filepath.Join(root, "destination", "source.txt")); err != nil {
		t.Fatal(err)
	}
	moved := formRequest(t, h, http.MethodPost, "/paste", url.Values{
		"clipboard_mode": {"cut"}, "clipboard_path": {"source.txt"}, "destination": {"destination"},
	})
	if moved.Code != http.StatusSeeOther || !strings.Contains(moved.Header().Get("Location"), "notice=") {
		t.Fatalf("cut paste: status=%d location=%q body=%s", moved.Code, moved.Header().Get("Location"), moved.Body.String())
	}
	if _, err := os.Stat(filepath.Join(root, "source.txt")); !os.IsNotExist(err) {
		t.Fatalf("source still exists after cut: err=%v", err)
	}
	contents, err := os.ReadFile(filepath.Join(root, "destination", "source.txt"))
	if err != nil || string(contents) != "file contents" {
		t.Fatalf("moved file contents=%q err=%v", contents, err)
	}
}
