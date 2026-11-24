package main

import (
	"bytes"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"
)

func TestPreviewRejectsDirectoriesAndMissingFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	h := makeTestHandler(t, root)
	for _, tc := range []struct {
		target string
		status int
	}{
		{"/preview/sub", http.StatusBadRequest},
		{"/preview/missing.txt", http.StatusNotFound},
	} {
		t.Run(tc.target, func(t *testing.T) {
			rec := get(t, h, tc.target)
			if rec.Code != tc.status {
				t.Fatalf("status=%d, want %d; body=%s", rec.Code, tc.status, rec.Body.String())
			}
		})
	}
}

func TestPreviewDecodesBOMMarkedText(t *testing.T) {
	root := t.TempDir()
	const want = "café 😀"
	for _, test := range []struct {
		name  string
		order binary.ByteOrder
		bom   []byte
	}{
		{name: "utf16le", order: binary.LittleEndian, bom: []byte{0xFF, 0xFE}},
		{name: "utf16be", order: binary.BigEndian, bom: []byte{0xFE, 0xFF}},
	} {
		units := utf16.Encode([]rune(want))
		content := append([]byte(nil), test.bom...)
		for _, unit := range units {
			var pair [2]byte
			test.order.PutUint16(pair[:], unit)
			content = append(content, pair[:]...)
		}
		if err := os.WriteFile(filepath.Join(root, test.name+".txt"), content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "utf8-bom.txt"), append([]byte{0xEF, 0xBB, 0xBF}, []byte(want)...), 0o600); err != nil {
		t.Fatal(err)
	}
	h := makeTestHandler(t, root)
	for _, name := range []string{"utf16le.txt", "utf16be.txt", "utf8-bom.txt"} {
		t.Run(name, func(t *testing.T) {
			rec := get(t, h, "/preview/"+name)
			if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), want) {
				t.Fatalf("preview status=%d; expected decoded content %q; body=%s", rec.Code, want, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), "does not appear to be") {
				t.Fatal("decoded text was treated as unsupported content")
			}
		})
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

func TestUnpreviewableFileOffersDownloadFromPreviewState(t *testing.T) {
	cases := []struct {
		name    string
		content []byte
		note    string
	}{
		{"binary", []byte{0x00, 0x01, 0x02, 0x03}, "does not appear to be UTF-8 text"},
		{"oversized", make([]byte, maxPreviewSize+1), "too large to preview"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "file.bin"), tc.content, 0o600); err != nil {
				t.Fatal(err)
			}
			h := makeTestHandler(t, root)
			rec := get(t, h, "/preview/file.bin")
			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d, want %d", rec.Code, http.StatusOK)
			}
			body := rec.Body.String()
			if !strings.Contains(body, tc.note) || !strings.Contains(body, `role="note"`) {
				t.Fatalf("missing accessible fallback note %q: %s", tc.note, body)
			}
			if !strings.Contains(body, `href="/f/file.bin?download=1">Download file`) {
				t.Fatal("fallback state does not offer a direct download")
			}
			assertNoClientSideJavaScript(t, body)
		})
	}
}

func TestPreviewStatesAnnounceScreenReaderStates(t *testing.T) {
	cases := []struct {
		name string
		file string
		data []byte
		want []string
	}{
		{"selected content", "a.txt", []byte("content"), []string{`aria-current="true"`, "content"}},
		{"empty directory", "", nil, []string{"No file selected", "Select a file from the list", "This directory is empty"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writePreviewFixture(t, root, tc.file, tc.data)
			h := makeTestHandler(t, root)
			target := "/"
			if tc.file != "" {
				target = "/preview/" + tc.file
			}
			rec := get(t, h, target)
			if rec.Code != http.StatusOK {
				t.Fatalf("GET %s status=%d", target, rec.Code)
			}
			body := rec.Body.String()
			assertPaneWiring(t, body)
			for _, want := range tc.want {
				if !strings.Contains(body, want) {
					t.Errorf("state missing %q", want)
				}
			}
			if strings.Contains(body, "Hide preview") && tc.file == "" {
				t.Fatal("empty state offers a Hide preview action")
			}
			assertNoClientSideJavaScript(t, body)
		})
	}
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"binary note", []byte{0, 1, 2}},
		{"oversized note", make([]byte, maxPreviewSize+1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "file.bin"), tc.data, 0o600); err != nil {
				t.Fatal(err)
			}
			rec := get(t, makeTestHandler(t, root), "/preview/file.bin")
			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d", rec.Code)
			}
			assertFallbackNoteState(t, rec.Body.String())
		})
	}
}

var paneWiring = []string{`aria-labelledby="preview-heading"`, `id="preview-heading"`}

func assertPaneWiring(t *testing.T, body string) {
	t.Helper()
	for _, want := range paneWiring {
		if !strings.Contains(body, want) {
			t.Errorf("pane is missing %q", want)
		}
	}
}

func assertFallbackNoteState(t *testing.T, body string) {
	t.Helper()
	assertPaneWiring(t, body)
	if !strings.Contains(body, `role="note"`) {
		t.Fatal("explanatory note is not exposed with role=note")
	}
	if !strings.Contains(body, `>Download file</a>`) {
		t.Fatal("fallback state does not offer a reachable Download file action")
	}
	assertNoClientSideJavaScript(t, body)
}

// assertNoClientSideJavaScript pins the no-client-side-JavaScript contract so
// every rendered preview state can share one check.
func assertNoClientSideJavaScript(t *testing.T, body string) {
	t.Helper()
	if strings.Contains(body, "<script") {
		t.Fatal("rendered state contains client-side JavaScript")
	}
}

// TestAccessiblePreviewFlowEndToEnd pins the whole accessible preview flow in
// one scenario, in user order: directory → preview → hide. If any #46
// sub-issue regresses, this test fails at the step that depends on it.
func TestAccessiblePreviewFlowEndToEnd(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("alpha content"), 0o600); err != nil {
		t.Fatal(err)
	}
	docs := filepath.Join(root, "docs")
	if err := os.Mkdir(docs, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(docs, "b.txt"), []byte("beta content"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := makeTestHandler(t, root)

	dir := get(t, h, "/")
	if dir.Code != http.StatusOK {
		t.Fatalf("GET / status=%d", dir.Code)
	}
	dirBody := dir.Body.String()
	for _, want := range []string{`href="/preview/a.txt">a.txt</a>`, `href="/d/docs">docs/</a>`} {
		if !strings.Contains(dirBody, want) {
			t.Errorf("directory page missing %q", want)
		}
	}
	if strings.Contains(dirBody, `>Preview</a>`) {
		t.Fatal("directory page offers a redundant Preview button")
	}

	preview := get(t, h, "/preview/a.txt")
	if preview.Code != http.StatusOK {
		t.Fatalf("GET /preview/a.txt status=%d", preview.Code)
	}
	body := preview.Body.String()
	for _, want := range []string{
		"Preview · a.txt", "alpha content", `href="/d/docs">docs/</a>`,
		`aria-current="true"`, `href="/f/a.txt?download=1">Download`,
		`class="col-12 col-lg-5 order-1 order-lg-2"`,
		`class="col-12 col-lg-7 order-2 order-lg-1"`, `tabindex="0"`, ".preview-content:focus",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("preview page missing %q", want)
		}
	}
	row := strings.Index(body, `href="/preview/a.txt">a.txt</a>`)
	hide := strings.Index(body, `>Hide preview</a>`)
	content := strings.Index(body, `tabindex="0"`)
	if row < 0 || hide < 0 || content < 0 || !(row < hide && hide < content) {
		t.Fatalf("preview focus order is not listing, controls, content: %d, %d, %d", row, hide, content)
	}
	if strings.Count(body, "Hide preview") != 1 {
		t.Fatal("preview page should have one Hide preview link")
	}
	assertNoClientSideJavaScript(t, body)

	docsPage := get(t, h, "/d/docs")
	if docsPage.Code != http.StatusOK || !strings.Contains(docsPage.Body.String(), `href="/preview/docs/b.txt">b.txt</a>`) {
		t.Fatalf("directory did not link into its nested preview: status=%d body=%s", docsPage.Code, docsPage.Body.String())
	}
	nested := get(t, h, "/preview/docs/b.txt")
	if nested.Code != http.StatusOK || !strings.Contains(nested.Body.String(), "beta content") || !strings.Contains(nested.Body.String(), `href="/d/docs">Hide preview`) {
		t.Fatalf("nested preview did not follow its URL or parent: status=%d body=%s", nested.Code, nested.Body.String())
	}
	if strings.Contains(nested.Body.String(), "alpha content") {
		t.Fatal("nested preview rendered a file outside its directory")
	}

	plain := get(t, h, "/")
	if plain.Code != http.StatusOK || !strings.Contains(plain.Body.String(), "No file selected") || strings.Contains(plain.Body.String(), "alpha content") {
		t.Fatalf("plain directory retained preview state: status=%d body=%s", plain.Code, plain.Body.String())
	}
}

func TestPageReferencesEmbeddedBootstrapStylesheet(t *testing.T) {
	h := makeTestHandler(t, t.TempDir())

	page := httptest.NewRecorder()
	h.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/", nil))
	if page.Code != http.StatusOK {
		t.Fatalf("GET / status=%d body=%s", page.Code, page.Body.String())
	}
	if !strings.Contains(page.Body.String(), `href="/static/bootstrap.min.css"`) {
		t.Fatal("rendered page does not reference the local Bootstrap stylesheet")
	}
	if strings.Contains(page.Body.String(), "https://") || strings.Contains(page.Body.String(), "<script") {
		t.Fatal("rendered page references a remote asset or JavaScript")
	}

	css := httptest.NewRecorder()
	h.ServeHTTP(css, httptest.NewRequest(http.MethodGet, "/static/bootstrap.min.css", nil))
	if css.Code != http.StatusOK {
		t.Fatalf("stylesheet status=%d body=%s", css.Code, css.Body.String())
	}
	if got := css.Header().Get("Content-Type"); got != "text/css; charset=utf-8" {
		t.Fatalf("stylesheet Content-Type=%q", got)
	}
	if got := css.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("stylesheet security header=%q", got)
	}
	if !strings.Contains(css.Body.String(), "Bootstrap  v5.3.8") {
		t.Fatal("served stylesheet is not the pinned Bootstrap version")
	}

	license, err := pageFiles.ReadFile("static/BOOTSTRAP-LICENSE.txt")
	if err != nil {
		t.Fatalf("read embedded Bootstrap license: %v", err)
	}
	if !strings.Contains(string(license), "The MIT License") {
		t.Fatal("embedded Bootstrap license does not contain the MIT notice")
	}
}

func TestThemeToggleSwitchesCurrentPageAndPreservesOtherQuery(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "docs"), 0o700); err != nil {
		t.Fatal(err)
	}
	h := makeTestHandler(t, root)

	dark := get(t, h, "/d/docs?theme=dark&error=problem&status=400")
	if dark.Code != http.StatusOK {
		t.Fatalf("dark directory status=%d body=%s", dark.Code, dark.Body.String())
	}
	if !strings.Contains(dark.Body.String(), `<html lang="en" data-bs-theme="dark">`) {
		t.Fatal("dark theme was not applied to the document")
	}
	if !strings.Contains(dark.Body.String(), `href="/d/docs?error=problem&amp;status=400">Light mode</a>`) {
		t.Fatal("dark-mode toggle does not return to light on the current page while preserving other query values")
	}

	light := get(t, h, "/d/docs?error=problem&status=400")
	if light.Code != http.StatusOK {
		t.Fatalf("light directory status=%d body=%s", light.Code, light.Body.String())
	}
	if strings.Contains(light.Body.String(), "data-bs-theme") {
		t.Fatal("light theme unexpectedly sets data-bs-theme")
	}
	if !strings.Contains(light.Body.String(), `href="/d/docs?error=problem&amp;status=400&amp;theme=dark">Dark mode</a>`) {
		t.Fatal("light-mode toggle does not select dark on the current page while preserving other query values")
	}
}

func TestRootBreadcrumbPreservesTheme(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "docs"), 0o700); err != nil {
		t.Fatal(err)
	}
	h := makeTestHandler(t, root)

	dark := get(t, h, "/d/docs?theme=dark")
	if dark.Code != http.StatusOK {
		t.Fatalf("dark directory status=%d body=%s", dark.Code, dark.Body.String())
	}
	if !strings.Contains(dark.Body.String(), `href="/?theme=dark">Root</a>`) {
		t.Error("dark Root breadcrumb does not link back to Root with the dark theme")
	}

	light := get(t, h, "/d/docs")
	if light.Code != http.StatusOK {
		t.Fatalf("light directory status=%d body=%s", light.Code, light.Body.String())
	}
	if !strings.Contains(light.Body.String(), `<a href="/">Root</a>`) {
		t.Error("light Root breadcrumb does not link to Root")
	}
	if strings.Contains(light.Body.String(), `theme=dark">Root`) {
		t.Error("light Root breadcrumb unexpectedly links to the dark theme")
	}
}

func TestDarkThemePersistsAcrossExplorerNavigation(t *testing.T) {
	root := t.TempDir()
	docs := filepath.Join(root, "docs")
	if err := os.Mkdir(docs, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"root.txt":                        "root",
		filepath.Join("docs", "note.txt"): "note",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	h := makeTestHandler(t, root)

	directory := get(t, h, "/?theme=dark")
	if directory.Code != http.StatusOK {
		t.Fatalf("dark root status=%d body=%s", directory.Code, directory.Body.String())
	}
	for _, want := range []string{
		`<html lang="en" data-bs-theme="dark">`,
		`href="/?theme=dark">Guava</a>`,
		`href="/?theme=dark">Root</a>`,
		`href="/d/docs?theme=dark">docs/</a>`,
		`href="/preview/root.txt?theme=dark">root.txt</a>`,
		`href="/edit/root.txt?theme=dark">Edit</a>`,
		`href="/f/root.txt?download=1&amp;theme=dark">Download</a>`,
		`href="/confirm-delete/root.txt?theme=dark"`,
		`action="/rename?theme=dark"`,
		`action="/mkdir?theme=dark"`,
		`action="/create-file?theme=dark"`,
		`action="/upload?theme=dark"`,
	} {
		if !strings.Contains(directory.Body.String(), want) {
			t.Errorf("dark root page missing %q", want)
		}
	}

	preview := get(t, h, "/preview/docs/note.txt?theme=dark")
	if preview.Code != http.StatusOK {
		t.Fatalf("dark preview status=%d body=%s", preview.Code, preview.Body.String())
	}
	for _, want := range []string{
		`<html lang="en" data-bs-theme="dark">`,
		`href="/d/docs?theme=dark">Hide preview</a>`,
		`href="/f/docs/note.txt?download=1&amp;theme=dark">Download</a>`,
		`class="preview-content bg-body-secondary border`,
	} {
		if !strings.Contains(preview.Body.String(), want) {
			t.Errorf("dark preview page missing %q", want)
		}
	}

	editor := get(t, h, "/edit/docs/note.txt?theme=dark")
	if editor.Code != http.StatusOK {
		t.Fatalf("dark editor status=%d body=%s", editor.Code, editor.Body.String())
	}
	for _, want := range []string{
		`<html lang="en" data-bs-theme="dark">`,
		`action="/edit/docs/note.txt?theme=dark"`,
		`href="/d/docs?theme=dark">Cancel</a>`,
	} {
		if !strings.Contains(editor.Body.String(), want) {
			t.Errorf("dark editor page missing %q", want)
		}
	}
	saved := formRequest(t, h, http.MethodPost, "/edit/docs/note.txt?theme=dark", url.Values{"content": {"saved"}})
	if saved.Code != http.StatusSeeOther {
		t.Fatalf("dark editor save status=%d location=%q body=%s", saved.Code, saved.Header().Get("Location"), saved.Body.String())
	}
	savedURL, err := url.Parse(saved.Header().Get("Location"))
	if err != nil || savedURL.Query().Get("theme") != "dark" || savedURL.Query().Get("saved") != "1" {
		t.Fatalf("editor save redirect did not preserve dark theme and saved state: location=%q err=%v", saved.Header().Get("Location"), err)
	}
	savedPage := get(t, h, saved.Header().Get("Location"))
	if savedPage.Code != http.StatusOK || !strings.Contains(savedPage.Body.String(), `data-bs-theme="dark"`) {
		t.Fatalf("saved editor page lost dark theme: status=%d body=%s", savedPage.Code, savedPage.Body.String())
	}
}

func TestDarkThemePersistsAcrossMutationsAndErrors(t *testing.T) {
	root := t.TempDir()
	docs := filepath.Join(root, "docs")
	if err := os.Mkdir(docs, 0o700); err != nil {
		t.Fatal(err)
	}
	h := makeTestHandler(t, root)

	created := formRequest(t, h, http.MethodPost, "/mkdir?theme=dark", url.Values{"parent": {"docs"}, "name": {"created"}})
	if created.Code != http.StatusSeeOther {
		t.Fatalf("dark mkdir status=%d location=%q body=%s", created.Code, created.Header().Get("Location"), created.Body.String())
	}
	createdURL, err := url.Parse(created.Header().Get("Location"))
	if err != nil || createdURL.Path != "/d/docs" || createdURL.Query().Get("theme") != "dark" {
		t.Fatalf("mkdir redirect lost dark theme: location=%q err=%v", created.Header().Get("Location"), err)
	}
	if page := get(t, h, created.Header().Get("Location")); page.Code != http.StatusOK || !strings.Contains(page.Body.String(), `data-bs-theme="dark"`) {
		t.Fatalf("mkdir redirect page lost dark theme: status=%d body=%s", page.Code, page.Body.String())
	}

	duplicate := formRequest(t, h, http.MethodPost, "/mkdir?theme=dark", url.Values{"parent": {"docs"}, "name": {"created"}})
	if duplicate.Code != http.StatusSeeOther {
		t.Fatalf("duplicate mkdir status=%d location=%q body=%s", duplicate.Code, duplicate.Header().Get("Location"), duplicate.Body.String())
	}
	errorURL, err := url.Parse(duplicate.Header().Get("Location"))
	if err != nil || errorURL.Query().Get("theme") != "dark" || errorURL.Query().Get("error") == "" {
		t.Fatalf("mutation error redirect lost dark theme or error: location=%q err=%v", duplicate.Header().Get("Location"), err)
	}
	errorPage := get(t, h, duplicate.Header().Get("Location"))
	if errorPage.Code != http.StatusOK || !strings.Contains(errorPage.Body.String(), `data-bs-theme="dark"`) || !strings.Contains(errorPage.Body.String(), "Unable to create directory") {
		t.Fatalf("mutation error page lost dark theme: status=%d body=%s", errorPage.Code, errorPage.Body.String())
	}

	confirmation := get(t, h, "/confirm-delete/docs/created?theme=dark")
	if confirmation.Code != http.StatusOK || !strings.Contains(confirmation.Body.String(), `data-bs-theme="dark"`) ||
		!strings.Contains(confirmation.Body.String(), `action="/delete?theme=dark"`) ||
		!strings.Contains(confirmation.Body.String(), `href="/d/docs?theme=dark">Cancel</a>`) {
		t.Fatalf("dark delete confirmation did not preserve theme: status=%d body=%s", confirmation.Code, confirmation.Body.String())
	}
	deleted := formRequest(t, h, http.MethodPost, "/delete?theme=dark", url.Values{"path": {"docs/created"}})
	deletedURL, err := url.Parse(deleted.Header().Get("Location"))
	if deleted.Code != http.StatusSeeOther || err != nil || deletedURL.Path != "/d/docs" || deletedURL.Query().Get("theme") != "dark" {
		t.Fatalf("delete redirect lost dark theme: status=%d location=%q err=%v", deleted.Code, deleted.Header().Get("Location"), err)
	}

	missing := get(t, h, "/edit/missing.txt?theme=dark")
	if missing.Code != http.StatusNotFound || !strings.Contains(missing.Body.String(), `data-bs-theme="dark"`) {
		t.Fatalf("error page lost dark theme: status=%d body=%s", missing.Code, missing.Body.String())
	}
}

func TestRenderPageEscapesEntryNames(t *testing.T) {
	var body bytes.Buffer
	data := pageData{Items: []pageItem{{Name: `<script>alert(1)</script>`, Path: "safe.txt", URL: "/f/safe.txt"}}}
	if err := newRenderer().Render(&body, data); err != nil {
		t.Fatalf("renderPage: %v", err)
	}
	if strings.Contains(body.String(), `<script>alert(1)</script>`) {
		t.Fatal("entry name was not HTML-escaped")
	}
	if !strings.Contains(body.String(), "&lt;script&gt;") {
		t.Fatal("escaped entry name not present")
	}
}

func writePreviewFixture(t *testing.T, root, name string, data []byte) {
	t.Helper()
	if name == "" {
		return
	}
	if err := os.WriteFile(filepath.Join(root, name), data, 0o600); err != nil {
		t.Fatal(err)
	}
}
