package main

import (
	"bytes"
	"encoding/binary"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
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

// TestRowActionsRevealOnHoverAndFocusForDesktop pins the #180 interaction
// contract from #178: on hover-capable devices the row action cluster rests
// calm and is revealed by the row's :hover or :focus-within. The hidden rest
// state uses opacity/visibility, not display, and the absolute positioning
// keeps the row from reflowing when the menu appears. Outside the hover
// query the cluster stays visible for non-hover devices (#181).
func TestRowActionsRevealOnHoverAndFocusForDesktop(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "docs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := makeTestHandler(t, root)

	rec := get(t, h, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / status=%d", rec.Code)
	}
	body := rec.Body.String()

	// The cluster is one named group, present in the DOM on every row.
	if !strings.Contains(body, `class="row-actions`) {
		t.Error("row action cluster is missing the row-actions hook")
	}
	// The rest state is gated behind the hover-capable query…
	gate := strings.Index(body, "@media (hover: hover)")
	if gate < 0 {
		t.Fatal("reveal styles are not gated behind @media (hover: hover)")
	}
	rest := strings.Index(body, "li.list-group-item .row-actions { opacity: 0; visibility: hidden;")
	if rest < 0 {
		t.Fatal("rest state is missing the calm hidden rule")
	}
	if rest < gate {
		t.Error("hidden rest state is not inside the @media (hover: hover) query")
	}
	// …and it hides with opacity/visibility so the layout box is preserved
	// (no row reflow), never with display.
	if strings.Contains(body, "display: none") {
		t.Error("hidden rest state uses display, which would reflow the row")
	}
	// Row :hover and :focus-within both reveal the cluster.
	if !strings.Contains(body, "li.list-group-item:hover .row-actions { opacity: 1; visibility: visible; }") {
		t.Error("row :hover reveal rule is missing")
	}
	// Focus must reveal immediately — no transition, no debounced state.
	if !strings.Contains(body, "li.list-group-item:focus-within .row-actions { opacity: 1; visibility: visible; transition: none; }") {
		t.Error(":focus-within reveal must be immediate, with no transition")
	}
	// Primary actions remain available, and secondary actions are reachable
	// through a keyboard-native disclosure with an entry-specific label.
	for _, want := range []string{`>Edit</a>`, `>More</summary>`, `>Rename</a>`, `>Delete</a>`} {
		if !strings.Contains(body, want) {
			t.Errorf("row action %q missing from the listing", want)
		}
	}
	detailsStart := strings.Index(body, `<details class="more-actions">`)
	if detailsStart < 0 {
		t.Fatal("More actions disclosure is missing")
	}
	detailsEnd := strings.Index(body[detailsStart:], `</details>`)
	if detailsEnd < 0 || !strings.Contains(body[detailsStart:detailsStart+detailsEnd], `>Download</a>`) {
		t.Fatal("Download is not available inside the secondary actions disclosure")
	}
	if !strings.Contains(body[detailsStart:detailsStart+detailsEnd], `aria-label="More actions for notes.txt"`) {
		t.Error("More actions disclosure is not labeled for its file")
	}
	assertNoClientSideJavaScript(t, body)
}

// TestRowActionsTouchContract pins the #181 contract from #178 items 4–5:
// on touch devices (hover: none) the row action cluster is always visible —
// its only hidden state is the one gated behind @media (hover: hover), which
// such devices never match — and every row action control presents a
// >= 44 px hit target. Touch users are not limited in any way.
func TestRowActionsTouchContract(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "docs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := makeTestHandler(t, root)

	rec := get(t, h, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / status=%d", rec.Code)
	}
	body := rec.Body.String()

	// The touch sizing lives behind its own hover: none gate…
	touchGate := strings.Index(body, "@media (hover: none)")
	if touchGate < 0 {
		t.Fatal("touch sizing styles are missing their @media (hover: none) gate")
	}
	// …and the >= 44 px hit-target rule for the row's link buttons is inside
	// that gate, so desktop sizing is untouched.
	rule := "li.list-group-item .row-actions .btn { min-height: 44px; }"
	pos := strings.Index(body, rule)
	if pos < 0 {
		t.Fatal("row action controls are missing the >= 44 px touch hit-target rule")
	}
	if pos < touchGate {
		t.Error("44 px touch hit-target rule is not inside the @media (hover: none) gate")
	}
	// The cluster has exactly one hidden rest state, the hover-gated one,
	// which hover: none devices never match — so on touch every action is
	// always visible and reachable without hover or JavaScript.
	if strings.Count(body, ".row-actions { opacity: 0") != 1 {
		t.Error("row action cluster must have exactly one hidden rest state (the hover-gated one)")
	}
	hoverGate := strings.Index(body, "@media (hover: hover)")
	if hoverGate < 0 || strings.Index(body, ".row-actions { opacity: 0") < hoverGate {
		t.Error("the only hidden rest state must stay inside the @media (hover: hover) gate")
	}
	// Touch targets keep visible labels, including the More disclosure.
	for _, want := range []string{`>Edit</a>`, `>Download</a>`, `>More</summary>`, `>Rename</a>`, `>Delete</a>`} {
		if !strings.Contains(body, want) {
			t.Errorf("row action %q missing from the listing", want)
		}
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
func TestPreviewColumnRejectsUnknownWidths(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := makeTestHandler(t, root)

	for _, bad := range []string{"", "abc", "12", "7", "0", "99"} {
		target := "/preview/a.txt?width=" + bad
		rec := get(t, h, target)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s status=%d body=%s", target, rec.Code, rec.Body.String())
		}
		body := rec.Body.String()
		for _, want := range []string{
			`class="col-12 col-lg-5 order-1 order-lg-2"`,
			`class="col-12 col-lg-7 order-2 order-lg-1"`,
			`href="/preview/a.txt?width=6" aria-label="Preview width is 5 of 12 columns; activate to change to 6 of 12">Width 5</a>`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("GET %s: expected default layout, missing %q", target, want)
			}
		}
		if strings.Contains(body, "width="+bad) && bad != "" {
			t.Errorf("GET %s: unknown width %q leaked into links", target, bad)
		}
	}
}

func TestDarkThemePersistsOnMutationErrors(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "docs", "created"), 0o700); err != nil {
		t.Fatal(err)
	}
	h := makeTestHandler(t, root)
	for _, name := range []string{"created"} {
		request := formRequest(t, h, http.MethodPost, "/mkdir?theme=dark", url.Values{"parent": {"docs"}, "name": {name}})
		if request.Code != http.StatusSeeOther {
			t.Fatalf("mkdir status=%d location=%q", request.Code, request.Header().Get("Location"))
		}
		location, err := url.Parse(request.Header().Get("Location"))
		if err != nil || location.Query().Get("theme") != "dark" {
			t.Fatalf("error redirect lost theme: location=%q err=%v", request.Header().Get("Location"), err)
		}
		page := get(t, h, request.Header().Get("Location"))
		if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), `data-bs-theme="dark"`) || !strings.Contains(page.Body.String(), "Unable to create directory") {
			t.Fatalf("error page lost dark theme or explanatory error: status=%d body=%s", page.Code, page.Body.String())
		}
	}
}
func TestPreferencesPersistAcrossExplorerNavigationAndMutations(t *testing.T) {
	preferences := []struct {
		name, key, value string
		pageState        string
	}{
		{name: "theme", key: "theme", value: "dark", pageState: `<html lang="en" data-bs-theme="dark">`},
		{name: "wrap", key: "wrap", value: "off", pageState: "white-space: pre;"},
		{name: "pane", key: "pane", value: "collapsed", pageState: `class="col-12 order-1 order-lg-2 d-lg-none"`},
		{name: "width", key: "width", value: "8", pageState: `class="col-12 col-lg-8 order-1 order-lg-2"`},
	}
	for _, preference := range preferences {
		t.Run(preference.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Mkdir(filepath.Join(root, "docs"), 0o700); err != nil {
				t.Fatal(err)
			}
			for name, content := range map[string]string{
				"root.txt": "root", filepath.Join("docs", "note.txt"): "note",
			} {
				if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			h := makeTestHandler(t, root)
			state := url.Values{preference.key: {preference.value}}
			query := state.Encode()

			directory := get(t, h, "/?"+query)
			if directory.Code != http.StatusOK {
				t.Fatalf("root status=%d body=%s", directory.Code, directory.Body.String())
			}
			assertRenderedURLHasQuery(t, directory.Body.String(), "/", state)
			for _, path := range []string{"/d/docs", "/preview/root.txt", "/edit/root.txt", "/f/root.txt", "/confirm-delete/root.txt", "/rename/root.txt", "/mkdir", "/create-file", "/upload"} {
				assertRenderedURLHasQuery(t, directory.Body.String(), path, state)
			}

			preview := get(t, h, "/preview/docs/note.txt?"+query)
			if preview.Code != http.StatusOK || !strings.Contains(preview.Body.String(), preference.pageState) {
				t.Fatalf("preview status=%d missing %q: %s", preview.Code, preference.pageState, preview.Body.String())
			}
			assertRenderedURLHasQuery(t, preview.Body.String(), "/d/docs", state)
			assertRenderedURLHasQuery(t, preview.Body.String(), "/f/docs/note.txt", state)

			editor := get(t, h, "/edit/docs/note.txt?"+query)
			if editor.Code != http.StatusOK {
				t.Fatalf("editor status=%d body=%s", editor.Code, editor.Body.String())
			}
			assertRenderedURLHasQuery(t, editor.Body.String(), "/edit/docs/note.txt", state)
			assertRenderedURLHasQuery(t, editor.Body.String(), "/d/docs", state)

			saved := formRequest(t, h, http.MethodPost, "/edit/docs/note.txt?"+query, url.Values{"content": {"saved"}})
			assertRedirectHasQuery(t, saved, "/edit/docs/note.txt", state)
			created := formRequest(t, h, http.MethodPost, "/mkdir?"+query, url.Values{"parent": {"docs"}, "name": {"created"}})
			assertRedirectHasQuery(t, created, "/d/docs", state)

			confirmation := get(t, h, "/confirm-delete/docs/created?"+query)
			if confirmation.Code != http.StatusOK {
				t.Fatalf("delete confirmation status=%d", confirmation.Code)
			}
			assertRenderedURLHasQuery(t, confirmation.Body.String(), "/delete", state)
			renamePage := get(t, h, "/rename/docs/created?"+query)
			if renamePage.Code != http.StatusOK {
				t.Fatalf("rename page status=%d", renamePage.Code)
			}
			assertRenderedURLHasQuery(t, renamePage.Body.String(), "/rename", state)
			deleted := formRequest(t, h, http.MethodPost, "/delete?"+query, url.Values{"path": {"docs/created"}})
			assertRedirectHasQuery(t, deleted, "/d/docs", state)
		})
	}
}

func TestPreferenceTogglesPreserveOtherQueryValues(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := makeTestHandler(t, root)
	cases := []struct {
		name, queryKey, queryValue, togglePath, toggleKey, toggleValue string
		absent                                                         string
	}{
		{name: "theme", queryKey: "theme", queryValue: "dark", togglePath: "/preview/a.txt", absent: "theme"},
		{name: "wrap", queryKey: "wrap", queryValue: "off", togglePath: "/preview/a.txt", absent: "wrap"},
		{name: "pane", queryKey: "pane", queryValue: "collapsed", togglePath: "/preview/a.txt", absent: "pane"},
		{name: "width", queryKey: "width", queryValue: "6", togglePath: "/preview/a.txt", toggleKey: "width", toggleValue: "8"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state := url.Values{tc.queryKey: {tc.queryValue}, "saved": {"1"}}
			page := get(t, h, "/preview/a.txt?"+state.Encode())
			if page.Code != http.StatusOK {
				t.Fatalf("preview status=%d body=%s", page.Code, page.Body.String())
			}
			if tc.absent != "" {
				assertRenderedURLDropsQuery(t, page.Body.String(), tc.togglePath, tc.absent, url.Values{"saved": {"1"}})
			} else {
				assertRenderedURLHasQuery(t, page.Body.String(), tc.togglePath, url.Values{"saved": {"1"}, tc.toggleKey: {tc.toggleValue}})
			}
		})
	}
}

var renderedURLAttribute = regexp.MustCompile(`(?:href|action)="([^"]+)"`)

func assertRenderedURLHasQuery(t *testing.T, body, path string, want url.Values) {
	t.Helper()
	assertRenderedURL(t, body, path, want, nil)
}

func assertRenderedURLDropsQuery(t *testing.T, body, path, absent string, want url.Values) {
	t.Helper()
	assertRenderedURL(t, body, path, want, []string{absent})
}

func assertRenderedURL(t *testing.T, body, path string, want url.Values, absent []string) {
	t.Helper()
	for _, match := range renderedURLAttribute.FindAllStringSubmatch(body, -1) {
		raw := html.UnescapeString(match[1])
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Path != path {
			continue
		}
		query := parsed.Query()
		ok := true
		for key, values := range want {
			if !reflect.DeepEqual(query[key], values) {
				ok = false
			}
		}
		for _, key := range absent {
			if query.Has(key) {
				ok = false
			}
		}
		if ok {
			return
		}
	}
	t.Errorf("rendered page has no URL for %q with query %v and absent keys %v", path, want, absent)
}

func assertRedirectHasQuery(t *testing.T, response *httptest.ResponseRecorder, path string, want url.Values) {
	t.Helper()
	if response.Code != http.StatusSeeOther {
		t.Fatalf("redirect status=%d location=%q body=%s", response.Code, response.Header().Get("Location"), response.Body.String())
	}
	location, err := url.Parse(response.Header().Get("Location"))
	if err != nil || location.Path != path {
		t.Fatalf("redirect location=%q, want path %q (err=%v)", response.Header().Get("Location"), path, err)
	}
	query := location.Query()
	for key, values := range want {
		if !reflect.DeepEqual(query[key], values) {
			t.Errorf("redirect query %s=%v, want %v", key, query[key], values)
		}
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
