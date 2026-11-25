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

// TestPreviewPaneKeepsBoundedHeightWithInternalScroll pins the fixed-size
// small-screen behavior: the preview content has a stylesheet-bounded height
// and scrolls vertically inside the pane, so long files do not push the
// directory listing and action cards off-screen.
func TestPreviewPaneKeepsBoundedHeightWithInternalScroll(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "long.txt"), []byte(strings.Repeat("a line of file content\n", 400)), 0o600); err != nil {
		t.Fatal(err)
	}
	h := makeTestHandler(t, root)

	rec := get(t, h, "/preview/long.txt")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /preview/long.txt status=%d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "a line of file content") {
		t.Fatal("preview did not render the long file content")
	}
	// The bounded height must live in the stylesheet where it is documented
	// for small displays, not in a fragile inline attribute.
	if !strings.Contains(body, ".preview-content { max-height: 480px; }") {
		t.Error("preview pane is missing the bounded-height stylesheet rule")
	}
	if strings.Contains(body, `style="max-height: 480px;"`) {
		t.Error("bounded height is set inline instead of in the stylesheet")
	}
	// Vertical scrolling inside the pane is what keeps the listing and action
	// cards reachable, and the content must stay keyboard-scrollable.
	if !strings.Contains(body, `overflow-auto`) {
		t.Error("preview content lost its overflow-auto in-pane scrolling")
	}
	if !strings.Contains(body, `tabindex="0"`) {
		t.Error("preview content is no longer keyboard-scrollable")
	}
	assertNoClientSideJavaScript(t, body)
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
		"white-space: pre-wrap", "overflow-wrap: anywhere",
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

// TestWrapToggleSwitchesPreviewRendering pins the rendered wrap states: wrap
// is on by default (matching the small-display behavior from #141), and
// wrap=off switches the preview to raw pre rendering with horizontal pane
// scrolling while the header control reflects the current state.
func TestWrapToggleSwitchesPreviewRendering(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte(strings.Repeat("a long line without spaces ", 4)), 0o600); err != nil {
		t.Fatal(err)
	}
	h := makeTestHandler(t, root)

	on := get(t, h, "/preview/a.txt")
	if on.Code != http.StatusOK {
		t.Fatalf("GET /preview/a.txt status=%d", on.Code)
	}
	body := on.Body.String()
	if !strings.Contains(body, "white-space: pre-wrap") || !strings.Contains(body, "overflow-wrap: anywhere") {
		t.Error("default preview state does not render wrapped")
	}
	if !strings.Contains(body, `<a class="btn btn-sm btn-secondary" href="/preview/a.txt?wrap=off" aria-pressed="true">Wrap</a>`) {
		t.Errorf("default preview header does not offer a toggle to non-wrapped: %s", body)
	}
	if !strings.Contains(body, `class="btn btn-sm btn-secondary"`) {
		t.Error("default wrap toggle is not rendered in its active state")
	}
	if !strings.Contains(body, `aria-pressed="true"`) {
		t.Error("default wrap toggle does not expose the current state")
	}
	// Only the toggle may mention wrap=off on a default page.
	if count := strings.Count(body, "wrap=off"); count != 1 {
		t.Errorf("default page mentions wrap=off %d times, want exactly the toggle", count)
	}
	assertNoClientSideJavaScript(t, body)

	off := get(t, h, "/preview/a.txt?wrap=off")
	if off.Code != http.StatusOK {
		t.Fatalf("GET /preview/a.txt?wrap=off status=%d", off.Code)
	}
	body = off.Body.String()
	if !strings.Contains(body, "white-space: pre;") || strings.Contains(body, "pre-wrap") || strings.Contains(body, "overflow-wrap: anywhere") {
		t.Error("non-wrapped preview does not keep raw line width without the wrap styles")
	}
	if !strings.Contains(body, `<a class="btn btn-sm btn-outline-secondary" href="/preview/a.txt" aria-pressed="false">Wrap</a>`) {
		t.Errorf("non-wrapped header does not offer a toggle back to wrapped: %s", body)
	}
	if !strings.Contains(body, `class="btn btn-sm btn-outline-secondary"`) {
		t.Error("non-wrapped toggle is not rendered inactive")
	}
	if !strings.Contains(body, `aria-pressed="false"`) {
		t.Error("non-wrapped toggle does not expose the current state")
	}
	// Long lines scroll horizontally inside the pane and stay keyboard-scrollable.
	if !strings.Contains(body, `overflow-auto`) || !strings.Contains(body, `tabindex="0"`) {
		t.Error("non-wrapped preview lost the pane scrolling or keyboard access")
	}
	assertNoClientSideJavaScript(t, body)
}

// TestWrapTogglePreservesThemeAndOtherQuery pins that the wrap toggle flips
// only the wrap parameter on the current page, keeping the theme and any
// other query values, exactly like the theme toggle.
func TestWrapTogglePreservesThemeAndOtherQuery(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := makeTestHandler(t, root)

	off := get(t, h, "/preview/a.txt?theme=dark&wrap=off&saved=1")
	if off.Code != http.StatusOK {
		t.Fatalf("wrap-off dark preview status=%d body=%s", off.Code, off.Body.String())
	}
	if !strings.Contains(off.Body.String(), `<html lang="en" data-bs-theme="dark">`) {
		t.Fatal("wrap-off preview did not keep the dark theme")
	}
	if !strings.Contains(off.Body.String(), `<a class="btn btn-sm btn-outline-secondary" href="/preview/a.txt?saved=1&amp;theme=dark" aria-pressed="false">Wrap</a>`) {
		t.Error("wrap-off toggle does not return to wrapped while preserving theme and other query values")
	}

	on := get(t, h, "/preview/a.txt?theme=dark&saved=1")
	if on.Code != http.StatusOK {
		t.Fatalf("wrapped dark preview status=%d body=%s", on.Code, on.Body.String())
	}
	if !strings.Contains(on.Body.String(), `<html lang="en" data-bs-theme="dark">`) {
		t.Fatal("wrapped preview did not keep the dark theme")
	}
	if !strings.Contains(on.Body.String(), `<a class="btn btn-sm btn-secondary" href="/preview/a.txt?saved=1&amp;theme=dark&amp;wrap=off" aria-pressed="true">Wrap</a>`) {
		t.Error("wrapped toggle does not select non-wrapped while preserving theme and other query values")
	}
}

// TestWrapStatePersistsAcrossNavigation pins that wrap=off rides every
// explorer link, redirect, and mutation the way theme=dark already does, so
// toggling wrap preserves the selected file, directory, and active theme.
func TestWrapStatePersistsAcrossNavigation(t *testing.T) {
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

	directory := get(t, h, "/?wrap=off")
	if directory.Code != http.StatusOK {
		t.Fatalf("wrap-off root status=%d body=%s", directory.Code, directory.Body.String())
	}
	for _, want := range []string{
		`href="/?wrap=off">Guava</a>`,
		`href="/?wrap=off">Root</a>`,
		`href="/d/docs?wrap=off">docs/</a>`,
		`href="/preview/root.txt?wrap=off">root.txt</a>`,
		`href="/edit/root.txt?wrap=off">Edit</a>`,
		`href="/f/root.txt?download=1&amp;wrap=off">Download</a>`,
		`href="/confirm-delete/root.txt?wrap=off"`,
		`action="/rename?wrap=off"`,
		`action="/mkdir?wrap=off"`,
		`action="/create-file?wrap=off"`,
		`action="/upload?wrap=off"`,
	} {
		if !strings.Contains(directory.Body.String(), want) {
			t.Errorf("wrap-off root page missing %q", want)
		}
	}

	preview := get(t, h, "/preview/docs/note.txt?wrap=off")
	if preview.Code != http.StatusOK {
		t.Fatalf("wrap-off preview status=%d body=%s", preview.Code, preview.Body.String())
	}
	for _, want := range []string{
		`white-space: pre;`,
		`<a class="btn btn-sm btn-outline-secondary" href="/preview/docs/note.txt" aria-pressed="false">Wrap</a>`,
		`href="/d/docs?wrap=off">Hide preview</a>`,
		`href="/f/docs/note.txt?download=1&amp;wrap=off">Download</a>`,
	} {
		if !strings.Contains(preview.Body.String(), want) {
			t.Errorf("wrap-off preview page missing %q", want)
		}
	}

	editor := get(t, h, "/edit/docs/note.txt?wrap=off")
	if editor.Code != http.StatusOK {
		t.Fatalf("wrap-off editor status=%d body=%s", editor.Code, editor.Body.String())
	}
	if !strings.Contains(editor.Body.String(), `action="/edit/docs/note.txt?wrap=off"`) ||
		!strings.Contains(editor.Body.String(), `href="/d/docs?wrap=off">Cancel</a>`) {
		t.Fatalf("editor page lost wrap state: body=%s", editor.Body.String())
	}
	saved := formRequest(t, h, http.MethodPost, "/edit/docs/note.txt?wrap=off", url.Values{"content": {"saved"}})
	if saved.Code != http.StatusSeeOther {
		t.Fatalf("wrap-off editor save status=%d location=%q body=%s", saved.Code, saved.Header().Get("Location"), saved.Body.String())
	}
	savedURL, err := url.Parse(saved.Header().Get("Location"))
	if err != nil || savedURL.Query().Get("wrap") != "off" || savedURL.Query().Get("saved") != "1" {
		t.Fatalf("editor save redirect did not preserve wrap state: location=%q err=%v", saved.Header().Get("Location"), err)
	}

	created := formRequest(t, h, http.MethodPost, "/mkdir?wrap=off", url.Values{"parent": {"docs"}, "name": {"created"}})
	createdURL, err := url.Parse(created.Header().Get("Location"))
	if created.Code != http.StatusSeeOther || err != nil || createdURL.Path != "/d/docs" || createdURL.Query().Get("wrap") != "off" {
		t.Fatalf("mkdir redirect lost wrap state: location=%q err=%v", created.Header().Get("Location"), err)
	}

	confirmation := get(t, h, "/confirm-delete/docs/created?wrap=off")
	if confirmation.Code != http.StatusOK || !strings.Contains(confirmation.Body.String(), `action="/delete?wrap=off"`) ||
		!strings.Contains(confirmation.Body.String(), `href="/d/docs?wrap=off">Cancel</a>`) {
		t.Fatalf("wrap-off delete confirmation lost wrap state: status=%d body=%s", confirmation.Code, confirmation.Body.String())
	}
	deleted := formRequest(t, h, http.MethodPost, "/delete?wrap=off", url.Values{"path": {"docs/created"}})
	deletedURL, err := url.Parse(deleted.Header().Get("Location"))
	if deleted.Code != http.StatusSeeOther || err != nil || deletedURL.Path != "/d/docs" || deletedURL.Query().Get("wrap") != "off" {
		t.Fatalf("delete redirect lost wrap state: location=%q err=%v", deleted.Header().Get("Location"), err)
	}
}

func TestPreviewPaneCollapseSwitchesLayout(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := makeTestHandler(t, root)

	expanded := get(t, h, "/preview/a.txt")
	if expanded.Code != http.StatusOK {
		t.Fatalf("GET /preview/a.txt status=%d", expanded.Code)
	}
	body := expanded.Body.String()
	// The default layout keeps the two-column large-screen arrangement.
	if !strings.Contains(body, `class="col-12 col-lg-7 order-2 order-lg-1"`) {
		t.Error("expanded page does not keep the large-screen listing column")
	}
	if !strings.Contains(body, `class="col-12 col-lg-5 order-1 order-lg-2"`) {
		t.Error("expanded page does not keep the large-screen preview column")
	}
	// The header offers a collapse control whose URL records the state.
	if !strings.Contains(body, `<a class="btn btn-sm d-none d-lg-inline-flex align-items-center btn-secondary" href="/preview/a.txt?pane=collapsed" aria-pressed="true">Collapse preview</a>`) {
		t.Errorf("expanded header does not offer a collapse control: %s", body)
	}
	if !strings.Contains(body, `>Hide preview</a>`) {
		t.Error("expanded header lost the small-screen hide control")
	}
	if strings.Contains(body, "Expand preview") {
		t.Error("expanded header renders the expand control")
	}
	// Only the control may mention pane=collapsed on a default page.
	if count := strings.Count(body, "pane=collapsed"); count != 1 {
		t.Errorf("expanded page mentions pane=collapsed %d times, want exactly the control", count)
	}
	assertNoClientSideJavaScript(t, body)

	collapsed := get(t, h, "/preview/a.txt?pane=collapsed")
	if collapsed.Code != http.StatusOK {
		t.Fatalf("GET /preview/a.txt?pane=collapsed status=%d", collapsed.Code)
	}
	body = collapsed.Body.String()
	// The collapsed layout gives the listing the full large-screen row and hides
	// the preview column at lg and up, while small screens keep the stacked pane.
	if !strings.Contains(body, `class="col-12 order-2 order-lg-1"`) {
		t.Error("collapsed page does not give the listing the full large-screen row")
	}
	if !strings.Contains(body, `class="col-12 order-1 order-lg-2 d-lg-none"`) {
		t.Error("collapsed page does not hide the preview column on large screens")
	}
	// Restoring keeps the selected file: the control targets the same preview URL.
	if !strings.Contains(body, `<a class="btn btn-sm d-none d-lg-inline-flex align-items-center btn-outline-secondary" href="/preview/a.txt" aria-pressed="false">Expand preview</a>`) {
		t.Errorf("collapsed page does not offer an expand control back to the selected file: %s", body)
	}
	if strings.Contains(body, "Collapse preview") {
		t.Error("collapsed page renders the collapse control")
	}
	// The selection and its state still render for the small-screen pane.
	if !strings.Contains(body, `aria-current="true"`) || !strings.Contains(body, `>Selected</span>`) {
		t.Error("collapsed page lost the selected-file state")
	}
	if !strings.Contains(body, `>Hide preview</a>`) {
		t.Error("collapsed page lost the small-screen hide control")
	}
	assertNoClientSideJavaScript(t, body)
}

func TestPreviewPaneTogglePreservesThemeAndOtherQuery(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := makeTestHandler(t, root)

	expanded := get(t, h, "/preview/a.txt?theme=dark&saved=1")
	if expanded.Code != http.StatusOK {
		t.Fatalf("expanded dark preview status=%d body=%s", expanded.Code, expanded.Body.String())
	}
	if !strings.Contains(expanded.Body.String(), `<html lang="en" data-bs-theme="dark">`) {
		t.Fatal("expanded preview did not keep the dark theme")
	}
	if !strings.Contains(expanded.Body.String(), `<a class="btn btn-sm d-none d-lg-inline-flex align-items-center btn-secondary" href="/preview/a.txt?pane=collapsed&amp;saved=1&amp;theme=dark" aria-pressed="true">Collapse preview</a>`) {
		t.Error("collapse control does not record the state while preserving theme and other query values")
	}

	collapsed := get(t, h, "/preview/a.txt?theme=dark&wrap=off&saved=1&pane=collapsed")
	if collapsed.Code != http.StatusOK {
		t.Fatalf("collapsed dark preview status=%d body=%s", collapsed.Code, collapsed.Body.String())
	}
	if !strings.Contains(collapsed.Body.String(), `<html lang="en" data-bs-theme="dark">`) {
		t.Fatal("collapsed preview did not keep the dark theme")
	}
	if !strings.Contains(collapsed.Body.String(), `<a class="btn btn-sm d-none d-lg-inline-flex align-items-center btn-outline-secondary" href="/preview/a.txt?saved=1&amp;theme=dark&amp;wrap=off" aria-pressed="false">Expand preview</a>`) {
		t.Error("expand control does not restore the pane while preserving theme, wrap, and other query values")
	}
	// The theme toggle from the collapsed page keeps the collapsed state, so the
	// pane state survives theme toggles.
	if !strings.Contains(collapsed.Body.String(), `href="/preview/a.txt?pane=collapsed&amp;saved=1&amp;wrap=off">Light mode</a>`) {
		t.Error("theme toggle does not preserve the collapsed pane state")
	}
}

func TestPreviewPaneStatePersistsAcrossNavigation(t *testing.T) {
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

	directory := get(t, h, "/?pane=collapsed")
	if directory.Code != http.StatusOK {
		t.Fatalf("collapsed root status=%d body=%s", directory.Code, directory.Body.String())
	}
	for _, want := range []string{
		`href="/?pane=collapsed">Guava</a>`,
		`href="/?pane=collapsed">Root</a>`,
		`href="/d/docs?pane=collapsed">docs/</a>`,
		`href="/preview/root.txt?pane=collapsed">root.txt</a>`,
		`href="/edit/root.txt?pane=collapsed">Edit</a>`,
		`href="/f/root.txt?download=1&amp;pane=collapsed">Download</a>`,
		`href="/confirm-delete/root.txt?pane=collapsed"`,
		`action="/rename?pane=collapsed"`,
		`action="/mkdir?pane=collapsed"`,
		`action="/create-file?pane=collapsed"`,
		`action="/upload?pane=collapsed"`,
	} {
		if !strings.Contains(directory.Body.String(), want) {
			t.Errorf("collapsed root page missing %q", want)
		}
	}

	preview := get(t, h, "/preview/docs/note.txt?pane=collapsed")
	if preview.Code != http.StatusOK {
		t.Fatalf("collapsed preview status=%d body=%s", preview.Code, preview.Body.String())
	}
	for _, want := range []string{
		`class="col-12 order-2 order-lg-1"`,
		`class="col-12 order-1 order-lg-2 d-lg-none"`,
		`<a class="btn btn-sm d-none d-lg-inline-flex align-items-center btn-outline-secondary" href="/preview/docs/note.txt" aria-pressed="false">Expand preview</a>`,
		`href="/d/docs?pane=collapsed">Hide preview</a>`,
		`href="/f/docs/note.txt?download=1&amp;pane=collapsed">Download</a>`,
	} {
		if !strings.Contains(preview.Body.String(), want) {
			t.Errorf("collapsed preview page missing %q", want)
		}
	}

	editor := get(t, h, "/edit/docs/note.txt?pane=collapsed")
	if editor.Code != http.StatusOK {
		t.Fatalf("collapsed editor status=%d body=%s", editor.Code, editor.Body.String())
	}
	if !strings.Contains(editor.Body.String(), `action="/edit/docs/note.txt?pane=collapsed"`) ||
		!strings.Contains(editor.Body.String(), `href="/d/docs?pane=collapsed">Cancel</a>`) {
		t.Fatalf("editor page lost pane state: body=%s", editor.Body.String())
	}
	saved := formRequest(t, h, http.MethodPost, "/edit/docs/note.txt?pane=collapsed", url.Values{"content": {"saved"}})
	if saved.Code != http.StatusSeeOther {
		t.Fatalf("collapsed editor save status=%d location=%q body=%s", saved.Code, saved.Header().Get("Location"), saved.Body.String())
	}
	savedURL, err := url.Parse(saved.Header().Get("Location"))
	if err != nil || savedURL.Query().Get("pane") != "collapsed" || savedURL.Query().Get("saved") != "1" {
		t.Fatalf("editor save redirect did not preserve pane state: location=%q err=%v", saved.Header().Get("Location"), err)
	}

	created := formRequest(t, h, http.MethodPost, "/mkdir?pane=collapsed", url.Values{"parent": {"docs"}, "name": {"created"}})
	createdURL, err := url.Parse(created.Header().Get("Location"))
	if created.Code != http.StatusSeeOther || err != nil || createdURL.Path != "/d/docs" || createdURL.Query().Get("pane") != "collapsed" {
		t.Fatalf("mkdir redirect lost pane state: location=%q err=%v", created.Header().Get("Location"), err)
	}

	confirmation := get(t, h, "/confirm-delete/docs/created?pane=collapsed")
	if confirmation.Code != http.StatusOK || !strings.Contains(confirmation.Body.String(), `action="/delete?pane=collapsed"`) ||
		!strings.Contains(confirmation.Body.String(), `href="/d/docs?pane=collapsed">Cancel</a>`) {
		t.Fatalf("collapsed delete confirmation lost pane state: status=%d body=%s", confirmation.Code, confirmation.Body.String())
	}
	deleted := formRequest(t, h, http.MethodPost, "/delete?pane=collapsed", url.Values{"path": {"docs/created"}})
	deletedURL, err := url.Parse(deleted.Header().Get("Location"))
	if deleted.Code != http.StatusSeeOther || err != nil || deletedURL.Path != "/d/docs" || deletedURL.Query().Get("pane") != "collapsed" {
		t.Fatalf("delete redirect lost pane state: location=%q err=%v", deleted.Header().Get("Location"), err)
	}
}

// TestPreviewColumnWidthSwitchesLayout pins the discrete large-screen preview
// widths: each offered width takes the preview column, the listing takes the
// remainder of the row, and the width control reflects the current width and
// cycles to the next, wrapping from the widest back to the narrowest.
func TestPreviewColumnWidthSwitchesLayout(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := makeTestHandler(t, root)

	for _, test := range []struct {
		width    string
		preview  string
		listing  string
		next     string
		nextHref string
	}{
		{preview: "5", listing: "7", next: "6", nextHref: "/preview/a.txt?width=6"},
		{width: "4", preview: "4", listing: "8", next: "5", nextHref: "/preview/a.txt"},
		{width: "6", preview: "6", listing: "6", next: "8", nextHref: "/preview/a.txt?width=8"},
		{width: "8", preview: "8", listing: "4", next: "9", nextHref: "/preview/a.txt?width=9"},
		{width: "9", preview: "9", listing: "3", next: "4", nextHref: "/preview/a.txt?width=4"},
	} {
		target := "/preview/a.txt"
		if test.width != "" {
			target += "?width=" + test.width
		}
		rec := get(t, h, target)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s status=%d body=%s", target, rec.Code, rec.Body.String())
		}
		body := rec.Body.String()
		if !strings.Contains(body, `class="col-12 col-lg-`+test.preview+` order-1 order-lg-2"`) {
			t.Errorf("GET %s: preview column is not col-lg-%s", target, test.preview)
		}
		if !strings.Contains(body, `class="col-12 col-lg-`+test.listing+` order-2 order-lg-1"`) {
			t.Errorf("GET %s: listing column is not col-lg-%s", target, test.listing)
		}
		control := `<a class="btn btn-sm d-none d-lg-inline-flex align-items-center btn-outline-secondary" href="` + test.nextHref + `" aria-label="Preview width is ` + test.preview + ` of 12 columns; activate to change to ` + test.next + ` of 12">Width ` + test.preview + `</a>`
		if !strings.Contains(body, control) {
			t.Errorf("GET %s: width control missing or wrong: %q", target, control)
		}
	}
}

// TestPreviewColumnTogglePreservesOtherPreferences pins that the width
// control changes only the width parameter, and that the width rides the
// theme, wrap, and pane toggles the way the other preferences ride each
// other, so one round trip loses no state.
func TestPreviewColumnTogglePreservesOtherPreferences(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := makeTestHandler(t, root)

	page := get(t, h, "/preview/a.txt?theme=dark&wrap=off&width=6")
	if page.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", page.Code, page.Body.String())
	}
	body := page.Body.String()
	if !strings.Contains(body, `<html lang="en" data-bs-theme="dark">`) {
		t.Fatal("dark theme was not applied to the document")
	}
	for _, want := range []string{
		`<a class="btn btn-sm d-none d-lg-inline-flex align-items-center btn-outline-secondary" href="/preview/a.txt?theme=dark&amp;width=8&amp;wrap=off" aria-label="Preview width is 6 of 12 columns; activate to change to 8 of 12">Width 6</a>`,
		`href="/preview/a.txt?width=6&amp;wrap=off">Light mode</a>`,
		`<a class="btn btn-sm btn-outline-secondary" href="/preview/a.txt?theme=dark&amp;width=6" aria-pressed="false">Wrap</a>`,
		`href="/preview/a.txt?pane=collapsed&amp;theme=dark&amp;width=6&amp;wrap=off" aria-pressed="true">Collapse preview</a>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("width page missing %q", want)
		}
	}
	assertNoClientSideJavaScript(t, body)
}

// TestPreviewColumnStatePersistsAcrossNavigation pins that the selected
// width rides every explorer link, redirect, and mutation the way theme,
// wrap, and pane do, so it survives navigation, file selection, and edits.
func TestPreviewColumnStatePersistsAcrossNavigation(t *testing.T) {
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

	directory := get(t, h, "/?width=8")
	if directory.Code != http.StatusOK {
		t.Fatalf("width directory status=%d body=%s", directory.Code, directory.Body.String())
	}
	for _, want := range []string{
		`class="col-12 col-lg-8 order-1 order-lg-2"`,
		`class="col-12 col-lg-4 order-2 order-lg-1"`,
		`href="/?width=8">Guava</a>`,
		`href="/?width=8">Root</a>`,
		`href="/d/docs?width=8">docs/</a>`,
		`href="/preview/root.txt?width=8">root.txt</a>`,
		`href="/edit/root.txt?width=8">Edit</a>`,
		`href="/f/root.txt?download=1&amp;width=8">Download</a>`,
		`href="/confirm-delete/root.txt?width=8"`,
		`action="/rename?width=8"`,
		`action="/mkdir?width=8"`,
		`action="/create-file?width=8"`,
		`action="/upload?width=8"`,
		`<a class="btn btn-sm d-none d-lg-inline-flex align-items-center btn-outline-secondary" href="/?width=9" aria-label="Preview width is 8 of 12 columns; activate to change to 9 of 12">Width 8</a>`,
	} {
		if !strings.Contains(directory.Body.String(), want) {
			t.Errorf("width directory page missing %q", want)
		}
	}

	preview := get(t, h, "/preview/docs/note.txt?width=8")
	if preview.Code != http.StatusOK {
		t.Fatalf("width preview status=%d body=%s", preview.Code, preview.Body.String())
	}
	for _, want := range []string{
		`class="col-12 col-lg-8 order-1 order-lg-2"`,
		`class="col-12 col-lg-4 order-2 order-lg-1"`,
		`href="/d/docs?width=8">Hide preview</a>`,
		`href="/f/docs/note.txt?download=1&amp;width=8">Download</a>`,
		`<a class="btn btn-sm d-none d-lg-inline-flex align-items-center btn-outline-secondary" href="/preview/docs/note.txt?width=9" aria-label="Preview width is 8 of 12 columns; activate to change to 9 of 12">Width 8</a>`,
	} {
		if !strings.Contains(preview.Body.String(), want) {
			t.Errorf("width preview page missing %q", want)
		}
	}

	editor := get(t, h, "/edit/docs/note.txt?width=8")
	if editor.Code != http.StatusOK {
		t.Fatalf("width editor status=%d body=%s", editor.Code, editor.Body.String())
	}
	if !strings.Contains(editor.Body.String(), `action="/edit/docs/note.txt?width=8"`) ||
		!strings.Contains(editor.Body.String(), `href="/d/docs?width=8">Cancel</a>`) {
		t.Fatalf("editor page lost width state: body=%s", editor.Body.String())
	}
	saved := formRequest(t, h, http.MethodPost, "/edit/docs/note.txt?width=8", url.Values{"content": {"saved"}})
	if saved.Code != http.StatusSeeOther {
		t.Fatalf("width editor save status=%d location=%q body=%s", saved.Code, saved.Header().Get("Location"), saved.Body.String())
	}
	savedURL, err := url.Parse(saved.Header().Get("Location"))
	if err != nil || savedURL.Query().Get("width") != "8" || savedURL.Query().Get("saved") != "1" {
		t.Fatalf("editor save redirect did not preserve width state: location=%q err=%v", saved.Header().Get("Location"), err)
	}

	created := formRequest(t, h, http.MethodPost, "/mkdir?width=8", url.Values{"parent": {"docs"}, "name": {"created"}})
	createdURL, err := url.Parse(created.Header().Get("Location"))
	if created.Code != http.StatusSeeOther || err != nil || createdURL.Path != "/d/docs" || createdURL.Query().Get("width") != "8" {
		t.Fatalf("mkdir redirect lost width state: location=%q err=%v", created.Header().Get("Location"), err)
	}

	confirmation := get(t, h, "/confirm-delete/docs/created?width=8")
	if confirmation.Code != http.StatusOK || !strings.Contains(confirmation.Body.String(), `action="/delete?width=8"`) ||
		!strings.Contains(confirmation.Body.String(), `href="/d/docs?width=8">Cancel</a>`) {
		t.Fatalf("width delete confirmation lost width state: status=%d body=%s", confirmation.Code, confirmation.Body.String())
	}
	deleted := formRequest(t, h, http.MethodPost, "/delete?width=8", url.Values{"path": {"docs/created"}})
	deletedURL, err := url.Parse(deleted.Header().Get("Location"))
	if deleted.Code != http.StatusSeeOther || err != nil || deletedURL.Path != "/d/docs" || deletedURL.Query().Get("width") != "8" {
		t.Fatalf("delete redirect lost width state: location=%q err=%v", deleted.Header().Get("Location"), err)
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
