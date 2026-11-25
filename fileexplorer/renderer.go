package main

import (
	"embed"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/dev1-alexwei-net/guava/internal/version"
)

//go:embed templates/index.html static/bootstrap.min.css static/BOOTSTRAP-LICENSE.txt
var pageFiles embed.FS

type renderer struct {
	page   *template.Template
	assets embed.FS
}

func newRenderer() *renderer {
	return &renderer{
		page:   template.Must(template.ParseFS(pageFiles, "templates/index.html")),
		assets: pageFiles,
	}
}

type pageData struct {
	Theme                string
	Version              string
	Wrap                 string
	PreviewPane          string
	Column               string
	ListingColumn        string
	NextColumn           string
	HomeURL              string
	ThemeToggleURL       string
	WrapToggleURL        string
	PreviewPaneToggleURL string
	ColumnToggleURL      string
	RenameAction         string
	MkdirAction          string
	CreateFileAction     string
	UploadAction         string
	DeleteAction         string
	Current              string
	Breadcrumbs          []breadcrumb
	ParentURL            string
	Items                []pageItem
	Error                string
	Editor               bool
	EditorPath           string
	EditorURL            string
	Content              string
	Saved                bool
	PreviewPath          string
	PreviewName          string
	PreviewContent       string
	PreviewNote          string
	PreviewReturnURL     string
	PreviewDownloadURL   string
	ConfirmDelete        bool
	DeletePath           string
	DeleteName           string
	ReturnURL            string
}

type breadcrumb struct {
	Name string
	URL  string
}

type pageItem struct {
	Name        string
	Path        string
	URL         string
	PreviewURL  string
	EditURL     string
	DownloadURL string
	DeleteURL   string
	IsDir       bool
	Size        int64
}

type previewProblem uint8

const (
	previewAvailable previewProblem = iota
	previewKnownTooLarge
	previewReadTooLarge
	previewUnsupported
)

type previewContent struct {
	text    string
	problem previewProblem
}

func (r *renderer) Render(w io.Writer, data pageData) error {
	return r.page.ExecuteTemplate(w, "index.html", data)
}

func (r *renderer) WritePage(w http.ResponseWriter, status int, data pageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_ = r.Render(w, data)
}

func (r *renderer) ForRequest(data pageData, request *http.Request) pageData {
	if request == nil || request.URL == nil {
		return data
	}

	theme := requestedTheme(request)
	wrap := requestedWrap(request)
	pane := requestedPreviewPane(request)
	column := requestedColumn(request)
	data.Theme = theme
	data.Wrap = wrap
	data.PreviewPane = pane
	data.Column = column
	data.ListingColumn = listingColumn(column)
	data.NextColumn = nextColumn(column)
	data.HomeURL = r.withState("/", theme, wrap, pane, column)
	data.ThemeToggleURL = r.toggleThemeURL(request.URL, theme)
	data.WrapToggleURL = r.toggleWrapURL(request.URL, wrap)
	data.PreviewPaneToggleURL = r.togglePreviewPaneURL(request.URL, pane)
	data.ColumnToggleURL = r.toggleColumnURL(request.URL, column)
	data.RenameAction = r.withState("/rename", theme, wrap, pane, column)
	data.MkdirAction = r.withState("/mkdir", theme, wrap, pane, column)
	data.CreateFileAction = r.withState("/create-file", theme, wrap, pane, column)
	data.UploadAction = r.withState("/upload", theme, wrap, pane, column)
	data.DeleteAction = r.withState("/delete", theme, wrap, pane, column)
	data.ParentURL = r.withState(data.ParentURL, theme, wrap, pane, column)
	data.EditorURL = r.withState(data.EditorURL, theme, wrap, pane, column)
	data.PreviewReturnURL = r.withState(data.PreviewReturnURL, theme, wrap, pane, column)
	data.PreviewDownloadURL = r.withState(data.PreviewDownloadURL, theme, wrap, pane, column)
	data.ReturnURL = r.withState(data.ReturnURL, theme, wrap, pane, column)
	for i := range data.Breadcrumbs {
		data.Breadcrumbs[i].URL = r.withState(data.Breadcrumbs[i].URL, theme, wrap, pane, column)
	}
	for i := range data.Items {
		item := &data.Items[i]
		item.URL = r.withState(item.URL, theme, wrap, pane, column)
		item.PreviewURL = r.withState(item.PreviewURL, theme, wrap, pane, column)
		item.EditURL = r.withState(item.EditURL, theme, wrap, pane, column)
		item.DownloadURL = r.withState(item.DownloadURL, theme, wrap, pane, column)
		item.DeleteURL = r.withState(item.DeleteURL, theme, wrap, pane, column)
	}
	return data
}

func requestedTheme(request *http.Request) string {
	if request != nil && request.URL != nil && request.URL.Query().Get("theme") == "dark" {
		return "dark"
	}
	return ""
}

// requestedWrap reports the preview wrap preference from the URL. Wrap is on
// by default; "off" is the only explicit state, mirroring requestedTheme.
func requestedWrap(request *http.Request) string {
	if request != nil && request.URL != nil && request.URL.Query().Get("wrap") == "off" {
		return "off"
	}
	return ""
}

// requestedPreviewPane reports the large-screen preview pane state from the
// URL. The pane is expanded by default; "collapsed" is the only explicit
// state, mirroring requestedWrap.
func requestedPreviewPane(request *http.Request) string {
	if request != nil && request.URL != nil && request.URL.Query().Get("pane") == "collapsed" {
		return "collapsed"
	}
	return ""
}

// previewColumns are the discrete large-screen preview column widths offered
// by the width control, in cycle order from narrowest to widest. The
// directory listing takes the remainder of Bootstrap's twelve-column row, so
// the two always share the full row.
var previewColumns = []string{"4", "5", "6", "8", "9"}

// defaultColumn is the preview width of the current layout (col-lg-5); it
// stays out of the URL the way the other defaults do.
const defaultColumn = "5"

// requestedColumn reports the large-screen preview column width from the
// URL. The default width stays out of the URL, mirroring the other
// preferences; values outside the offered set fall back to the default so a
// hand-edited URL can never break the row.
func requestedColumn(request *http.Request) string {
	if request != nil && request.URL != nil {
		if width := request.URL.Query().Get("width"); isValidColumn(width) {
			return width
		}
	}
	return defaultColumn
}

func isValidColumn(width string) bool {
	for _, candidate := range previewColumns {
		if candidate == width {
			return true
		}
	}
	return false
}

// listingColumn reports the width the directory listing takes while the
// preview column takes width: together they must fill the twelve-column row.
func listingColumn(width string) string {
	value, err := strconv.Atoi(width)
	if err != nil || value < 1 || value > 11 {
		return strconv.Itoa(12 - 5)
	}
	return strconv.Itoa(12 - value)
}

// nextColumn reports the width that follows width in the cycle, wrapping from
// the widest back to the narrowest.
func nextColumn(width string) string {
	for i, candidate := range previewColumns {
		if candidate == width {
			return previewColumns[(i+1)%len(previewColumns)]
		}
	}
	return previewColumns[0]
}

// withState applies the URL-driven page preferences to a link the way the
// theme parameter already rides every explorer link: each preference is
// present only in its non-default state (theme=dark, wrap=off,
// pane=collapsed, width=N), so defaults stay out of the URL and navigation
// carries the user's choices forward.
func (r *renderer) withState(target string, theme, wrap, pane, column string) string {
	if target == "" {
		return ""
	}
	u, err := url.Parse(target)
	if err != nil {
		return target
	}
	query := u.Query()
	if theme == "dark" {
		query.Set("theme", "dark")
	} else {
		query.Del("theme")
	}
	if wrap == "off" {
		query.Set("wrap", "off")
	} else {
		query.Del("wrap")
	}
	if pane == "collapsed" {
		query.Set("pane", "collapsed")
	} else {
		query.Del("pane")
	}
	if column != defaultColumn {
		query.Set("width", column)
	} else {
		query.Del("width")
	}
	u.RawQuery = query.Encode()
	return u.String()
}

// canonicalQuery aligns the width parameter with what the page actually
// rendered: an invalid value and the default drop out of the URL the way the
// other defaults do, since the page falls back to the default width for both
// anyway. A valid non-default width is kept so the preference survives the
// round trip.
func canonicalQuery(query url.Values) {
	width := query.Get("width")
	if !isValidColumn(width) || width == defaultColumn {
		query.Del("width")
	}
}

func (r *renderer) toggleThemeURL(current *url.URL, theme string) string {
	if current == nil {
		return "/?theme=dark"
	}
	u := &url.URL{Path: current.Path, RawPath: current.RawPath}
	if u.Path == "" {
		u.Path = "/"
	}
	query := current.Query()
	if theme == "dark" {
		query.Del("theme")
	} else {
		query.Set("theme", "dark")
	}
	canonicalQuery(query)
	u.RawQuery = query.Encode()
	return u.String()
}

// toggleWrapURL flips the wrap preference on the current page's URL, keeping
// every other query value, mirroring toggleThemeURL.
func (r *renderer) toggleWrapURL(current *url.URL, wrap string) string {
	if current == nil {
		return "/?wrap=off"
	}
	u := &url.URL{Path: current.Path, RawPath: current.RawPath}
	if u.Path == "" {
		u.Path = "/"
	}
	query := current.Query()
	if wrap == "off" {
		query.Del("wrap")
	} else {
		query.Set("wrap", "off")
	}
	canonicalQuery(query)
	u.RawQuery = query.Encode()
	return u.String()
}

// toggleColumnURL advances the large-screen preview column width through the
// offered set on the current page's URL, keeping every other query value,
// mirroring togglePreviewPaneURL. The toggle always targets the page's own
// path, so the selected file survives the round trip, and the default width
// drops out of the URL the way the other defaults do.
func (r *renderer) toggleColumnURL(current *url.URL, column string) string {
	next := nextColumn(column)
	if current == nil {
		if next == defaultColumn {
			return "/"
		}
		return "/?width=" + next
	}
	u := &url.URL{Path: current.Path, RawPath: current.RawPath}
	if u.Path == "" {
		u.Path = "/"
	}
	query := current.Query()
	if next == defaultColumn {
		query.Del("width")
	} else {
		query.Set("width", next)
	}
	u.RawQuery = query.Encode()
	return u.String()
}

// togglePreviewPaneURL flips the large-screen preview pane state on the
// current page's URL, keeping every other query value, mirroring
// toggleWrapURL. The toggle always targets the page's own path, so expanding
// restores the preview for the same file the page rendered and the selected
// file survives the round trip.
func (r *renderer) togglePreviewPaneURL(current *url.URL, pane string) string {
	if current == nil {
		return "/?pane=collapsed"
	}
	u := &url.URL{Path: current.Path, RawPath: current.RawPath}
	if u.Path == "" {
		u.Path = "/"
	}
	query := current.Query()
	if pane == "collapsed" {
		query.Del("pane")
	} else {
		query.Set("pane", "collapsed")
	}
	canonicalQuery(query)
	u.RawQuery = query.Encode()
	return u.String()
}

func (r *renderer) Stylesheet(w http.ResponseWriter, _ *http.Request) {
	css, err := r.assets.ReadFile("static/bootstrap.min.css")
	if err != nil {
		http.Error(w, "stylesheet unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(css)
}

func (r *renderer) Directory(current string, entries []Entry, pageError string) pageData {
	data := pageData{
		Current:     current,
		Version:     version.SemVer,
		Breadcrumbs: r.Breadcrumbs(current),
		ParentURL:   r.ParentURL(current),
		Items:       make([]pageItem, 0, len(entries)),
		Error:       pageError,
	}
	for _, entry := range entries {
		item := pageItem{
			Name:        entry.Name,
			Path:        entry.Path,
			URL:         r.ItemURL(entry.Path, entry.IsDir),
			EditURL:     r.EditorURL(entry.Path),
			DownloadURL: r.ItemURL(entry.Path, false) + "?download=1",
			DeleteURL:   "/confirm-delete/" + r.EscapedPath(entry.Path),
			IsDir:       entry.IsDir,
			Size:        entry.Size,
		}
		if !entry.IsDir {
			item.PreviewURL = "/preview/" + r.EscapedPath(entry.Path)
		}
		data.Items = append(data.Items, item)
	}
	return data
}

func (r *renderer) Editor(rel, content string, saved bool) pageData {
	parent := path.Dir(rel)
	if parent == "." {
		parent = ""
	}
	cancelURL := r.ParentURL(rel)
	if cancelURL == "" {
		cancelURL = "/"
	}
	return pageData{
		Editor:      true,
		Version:     version.SemVer,
		EditorPath:  rel,
		EditorURL:   r.EditorURL(rel),
		Content:     content,
		ParentURL:   cancelURL,
		Breadcrumbs: r.Breadcrumbs(parent),
		Saved:       saved,
	}
}

func (r *renderer) DeleteConfirmation(rel string) pageData {
	parent := path.Dir(rel)
	if parent == "." {
		parent = ""
	}
	return pageData{
		ConfirmDelete: true,
		Version:       version.SemVer,
		DeletePath:    rel,
		DeleteName:    path.Base(rel),
		ReturnURL:     r.ParentURL(rel),
	}
}

func (r *renderer) Preview(data *pageData, current, rel string, preview previewContent) {
	if rel == "" {
		return
	}
	parent := path.Dir(rel)
	if parent == "." {
		parent = ""
	}
	if parent != current {
		return
	}
	data.PreviewPath = rel
	data.PreviewName = path.Base(rel)
	data.PreviewReturnURL = r.ItemURL(parent, true)
	data.PreviewDownloadURL = r.ItemURL(rel, false) + "?download=1"
	data.PreviewContent = preview.text
	switch preview.problem {
	case previewKnownTooLarge:
		data.PreviewNote = "This file is too large to preview (maximum 512 KiB). Download it instead."
	case previewReadTooLarge:
		data.PreviewNote = "This file is too large to preview. Download it instead."
	case previewUnsupported:
		data.PreviewNote = "This file does not appear to be UTF-8 text or BOM-marked UTF-16 text, so it cannot be previewed. Download it instead."
	}
}

func (r *renderer) Error(message string) pageData {
	return pageData{Error: message, Version: version.SemVer}
}

func (r *renderer) Breadcrumbs(current string) []breadcrumb {
	if current == "" {
		return nil
	}
	parts := strings.Split(current, "/")
	crumbs := make([]breadcrumb, 0, len(parts))
	var accumulated []string
	for _, part := range parts {
		accumulated = append(accumulated, part)
		crumbs = append(crumbs, breadcrumb{Name: part, URL: r.ItemURL(strings.Join(accumulated, "/"), true)})
	}
	return crumbs
}

func (r *renderer) ParentURL(current string) string {
	if current == "" {
		return ""
	}
	parent := path.Dir(current)
	if parent == "." {
		return "/"
	}
	return r.ItemURL(parent, true)
}

func (r *renderer) EscapedPath(rel string) string {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}

func (r *renderer) ItemURL(rel string, isDir bool) string {
	prefix := "/f/"
	if isDir {
		prefix = "/d/"
	}
	return prefix + r.EscapedPath(rel)
}

func (r *renderer) EditorURL(rel string) string {
	return "/edit/" + r.EscapedPath(rel)
}
