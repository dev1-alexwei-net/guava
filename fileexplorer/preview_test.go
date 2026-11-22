package main

import (
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"
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

func TestFilenameLinkNavigatesToPreview(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("previewable content"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := makeTestHandler(t, root)

	rec := get(t, h, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / status = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `href="/preview/a.txt">a.txt</a>`) {
		t.Fatal("file name does not link to the preview route")
	}
	if strings.Contains(body, `text-break" href="/f/a.txt"`) {
		t.Fatal("file name still links to the raw file route")
	}
}

func TestDirectoryNameLinkNavigatesIntoDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	h := makeTestHandler(t, root)

	rec := get(t, h, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / status = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `href="/d/sub">sub/</a>`) {
		t.Fatal("directory name does not navigate into the directory")
	}
}

func TestPreviewContentIsKeyboardFocusable(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("previewable content"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := makeTestHandler(t, root)

	rec := get(t, h, "/preview/a.txt")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	body := rec.Body.String()
	// The scrollable preview pane must be reachable by keyboard focus so long
	// files can be scrolled without a mouse.
	if !strings.Contains(body, `class="preview-content`) {
		t.Fatal("preview content does not render with its focusable class")
	}
	if !strings.Contains(body, `tabindex="0"`) {
		t.Fatal("preview content is not keyboard-focusable")
	}
	if !strings.Contains(body, ".preview-content:focus") {
		t.Fatal("preview content has no visible focus indicator")
	}
	if strings.Contains(body, `<script`) {
		t.Fatal("introduces client-side JavaScript")
	}
}

func TestFocusOrderIsLogicalAndTrapFree(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("previewable content"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := makeTestHandler(t, root)

	rec := get(t, h, "/preview/a.txt")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	body := rec.Body.String()

	rowName := strings.Index(body, `href="/preview/a.txt">a.txt</a>`)
	paneHide := strings.Index(body, `>Hide preview</a>`)
	paneContent := strings.Index(body, `tabindex="0"`)
	if rowName == -1 || paneHide == -1 || paneContent == -1 {
		t.Fatalf("missing focusable elements: rowName=%d paneHide=%d paneContent=%d", rowName, paneHide, paneContent)
	}
	// Tab order should follow the workspace order: listing rows first, then the
	// pane controls, then the scrollable preview content.
	if !(rowName < paneHide && paneHide < paneContent) {
		t.Fatalf("focus order is not logical: rowName=%d paneHide=%d paneContent=%d", rowName, paneHide, paneContent)
	}
	// The page is fully server-rendered, so nothing can trap keyboard focus.
	if strings.Contains(body, `<script`) {
		t.Fatal("introduces client-side JavaScript")
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

func TestSelectedFileIsIdentifiedAccessibly(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("previewable content"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "b.txt"), []byte("other content"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := makeTestHandler(t, root)

	rec := get(t, h, "/preview/a.txt")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /preview/a.txt status = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `aria-current="true"`) {
		t.Fatal("selected file is not exposed with an aria-current state")
	}
	if got := strings.Count(body, `aria-current="true"`); got != 1 {
		t.Fatalf("aria-current markers = %d, want exactly one", got)
	}
	if !strings.Contains(body, `>Selected</span>`) {
		t.Fatal("selected file has no visible Selected indicator")
	}
	if got := strings.Count(body, `>Selected</span>`); got != 1 {
		t.Fatalf("Selected indicators = %d, want exactly one", got)
	}
}

func TestPlainDirectoryPageShowsUnselectedPreviewState(t *testing.T) {
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
	if !strings.Contains(body, `id="preview-heading"`) {
		t.Fatal("plain directory page does not render the preview area")
	}
	if !strings.Contains(body, "No file selected") {
		t.Fatal("plain directory page does not show the unselected preview state")
	}
	if !strings.Contains(body, "Select a file from the list to preview it here") {
		t.Fatal("unselected preview state does not explain how to select a file")
	}
	if strings.Contains(body, "Hide preview") {
		t.Fatal("unselected preview state offers a hide-preview action")
	}
	if strings.Contains(body, "previewable content") {
		t.Fatal("unselected preview state renders a file's content")
	}
	if !strings.Contains(body, `href="/preview/a.txt">a.txt</a>`) {
		t.Fatal("filename link no longer targets the preview")
	}
	if strings.Contains(body, `>Preview</a>`) {
		t.Fatal("row offers a redundant Preview button")
	}
}

func TestEmptyPreviewStateRendersForEmptyDirectory(t *testing.T) {
	h := makeTestHandler(t, t.TempDir())

	rec := get(t, h, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / status = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `id="preview-heading"`) {
		t.Fatal("empty directory page does not render the preview area")
	}
	if !strings.Contains(body, "No file selected") {
		t.Fatal("empty directory page does not show the unselected preview state")
	}
	if !strings.Contains(body, "This directory is empty") {
		t.Fatal("empty directory listing state is missing")
	}
	if strings.Contains(body, "Hide preview") {
		t.Fatal("empty directory page offers a hide-preview action")
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
	// a.txt keeps only the default preview link (no redundant Preview button),
	// and Hide preview is offered exactly once: in the pane header, never in a row.
	if strings.Contains(body, `>Preview</a>`) {
		t.Fatal("row offers a redundant Preview button")
	}
	if !strings.Contains(body, `href="/preview/a.txt">a.txt</a>`) {
		t.Fatal("non-previewed row lost its preview link")
	}
	if count := strings.Count(body, "Hide preview"); count != 1 {
		t.Fatalf("occurrences of Hide preview = %d, want exactly 1 (pane header)", count)
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
	if strings.Contains(body, "<script") {
		t.Fatal("fallback preview state contains client-side JavaScript")
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
	if strings.Contains(body, "<script") {
		t.Fatal("fallback preview state contains client-side JavaScript")
	}
}

func TestPrimaryLinkResolvesToPreviewWithDirectoryListing(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("alpha content"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "b.txt"), []byte("beta content"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := makeTestHandler(t, root)

	// Step 1: the directory page offers the primary (filename) link.
	dir := get(t, h, "/")
	if dir.Code != http.StatusOK {
		t.Fatalf("GET / status = %d", dir.Code)
	}
	if !strings.Contains(dir.Body.String(), `href="/preview/a.txt">a.txt</a>`) {
		t.Fatal("directory page does not offer the primary file link")
	}

	// Step 2: that target renders the file's content above its directory listing.
	rec := get(t, h, "/preview/a.txt")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /preview/a.txt status = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"Preview · a.txt", "alpha content", `href="/preview/b.txt">b.txt</a>`} {
		if !strings.Contains(body, want) {
			t.Errorf("preview page does not contain %q (file and directory listing must render together)", want)
		}
	}
	if strings.Contains(body, "beta content") {
		t.Fatal("unrelated file content is rendered")
	}
	if strings.Contains(body, "<script") {
		t.Fatal("preview page contains client-side JavaScript")
	}
}

func TestChangingPreviewURLChangesSelectionAcrossDirectories(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("alpha content"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "docs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs", "b.txt"), []byte("beta content"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := makeTestHandler(t, root)

	nested := get(t, h, "/preview/docs/b.txt")
	if nested.Code != http.StatusOK {
		t.Fatalf("GET /preview/docs/b.txt status = %d", nested.Code)
	}
	nestedBody := nested.Body.String()
	if !strings.Contains(nestedBody, "Preview · b.txt") || !strings.Contains(nestedBody, "beta content") {
		t.Fatal("nested preview does not render the requested file")
	}
	if strings.Contains(nestedBody, "alpha content") {
		t.Fatal("file outside the parent directory is rendered")
	}

	top := get(t, h, "/preview/a.txt")
	if top.Code != http.StatusOK {
		t.Fatalf("GET /preview/a.txt status = %d", top.Code)
	}
	topBody := top.Body.String()
	if !strings.Contains(topBody, "Preview · a.txt") || !strings.Contains(topBody, "alpha content") {
		t.Fatal("selection does not change with the preview URL")
	}
	if strings.Contains(topBody, "beta content") {
		t.Fatal("previously selected file content remains after changing the preview URL")
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

func TestPreviewHeaderOffersDirectDownload(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("previewable content"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := makeTestHandler(t, root)

	rec := get(t, h, "/preview/a.txt")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `class="btn btn-outline-primary btn-sm" href="/f/a.txt?download=1">Download`) {
		t.Fatal("preview header does not offer a direct Download action")
	}
}

func TestUnpreviewableFileOffersDownloadFromPreviewState(t *testing.T) {
	cases := []struct {
		name    string
		content []byte
		note    string
	}{
		{"non-text", []byte{0x00, 0x01, 0x02, 0x03}, "does not appear to be UTF-8 text"},
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
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
			}
			body := rec.Body.String()
			if !strings.Contains(body, tc.note) {
				t.Fatal("expected the explanatory preview note")
			}
			if !strings.Contains(body, `class="btn btn-primary" href="/f/file.bin?download=1">Download file`) {
				t.Fatal("preview state does not offer a direct Download file action")
			}
		})
	}
}

// paneWiring pins the assistive-tech contract shared by every preview state:
// the pane region is labelled by its own heading.
var paneWiring = []string{`aria-labelledby="preview-heading"`, `id="preview-heading"`}

func TestPreviewStatesAnnounceScreenReaderStates(t *testing.T) {
	t.Run("content", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("previewable content"), 0o600); err != nil {
			t.Fatal(err)
		}
		h := makeTestHandler(t, root)

		rec := get(t, h, "/preview/a.txt")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
		body := rec.Body.String()
		assertPaneWiring(t, body)
		// The selected row announces itself as the current item.
		if !strings.Contains(body, `aria-current="true"`) {
			t.Fatal("selected row is not exposed with an aria-current state")
		}
		if strings.Contains(body, `<script`) {
			t.Fatal("introduces client-side JavaScript")
		}
	})

	t.Run("non-text note", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "blob.bin"), []byte{0x00, 0x01, 0x02}, 0o600); err != nil {
			t.Fatal(err)
		}
		h := makeTestHandler(t, root)

		rec := get(t, h, "/preview/blob.bin")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
		assertFallbackNoteState(t, rec.Body.String())
	})

	t.Run("oversized note", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "big.txt"), make([]byte, maxPreviewSize+1), 0o600); err != nil {
			t.Fatal(err)
		}
		h := makeTestHandler(t, root)

		rec := get(t, h, "/preview/big.txt")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
		assertFallbackNoteState(t, rec.Body.String())
	})

	t.Run("empty", func(t *testing.T) {
		h := makeTestHandler(t, t.TempDir())

		rec := get(t, h, "/")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
		body := rec.Body.String()
		assertPaneWiring(t, body)
		// The hint text is associated with the pane heading it belongs to.
		if !strings.Contains(body, "No file selected") {
			t.Fatal("empty state hint is missing")
		}
		if !strings.Contains(body, "Select a file from the list to preview it here") {
			t.Fatal("empty state does not explain how to select a file")
		}
		if strings.Contains(body, `<script`) {
			t.Fatal("introduces client-side JavaScript")
		}
	})
}

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
	if strings.Contains(body, `<script`) {
		t.Fatal("introduces client-side JavaScript")
	}
}
