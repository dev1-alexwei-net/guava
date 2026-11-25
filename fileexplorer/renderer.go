package main

import (
	"embed"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"strings"
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
	Wrap                 string
	PreviewPane          string
	HomeURL              string
	ThemeToggleURL       string
	WrapToggleURL        string
	PreviewPaneToggleURL string
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
	data.Theme = theme
	data.Wrap = wrap
	data.PreviewPane = pane
	data.HomeURL = r.withState("/", theme, wrap, pane)
	data.ThemeToggleURL = r.toggleThemeURL(request.URL, theme)
	data.WrapToggleURL = r.toggleWrapURL(request.URL, wrap)
	data.PreviewPaneToggleURL = r.togglePreviewPaneURL(request.URL, pane)
	data.RenameAction = r.withState("/rename", theme, wrap, pane)
	data.MkdirAction = r.withState("/mkdir", theme, wrap, pane)
	data.CreateFileAction = r.withState("/create-file", theme, wrap, pane)
	data.UploadAction = r.withState("/upload", theme, wrap, pane)
	data.DeleteAction = r.withState("/delete", theme, wrap, pane)
	data.ParentURL = r.withState(data.ParentURL, theme, wrap, pane)
	data.EditorURL = r.withState(data.EditorURL, theme, wrap, pane)
	data.PreviewReturnURL = r.withState(data.PreviewReturnURL, theme, wrap, pane)
	data.PreviewDownloadURL = r.withState(data.PreviewDownloadURL, theme, wrap, pane)
	data.ReturnURL = r.withState(data.ReturnURL, theme, wrap, pane)
	for i := range data.Breadcrumbs {
		data.Breadcrumbs[i].URL = r.withState(data.Breadcrumbs[i].URL, theme, wrap, pane)
	}
	for i := range data.Items {
		item := &data.Items[i]
		item.URL = r.withState(item.URL, theme, wrap, pane)
		item.PreviewURL = r.withState(item.PreviewURL, theme, wrap, pane)
		item.EditURL = r.withState(item.EditURL, theme, wrap, pane)
		item.DownloadURL = r.withState(item.DownloadURL, theme, wrap, pane)
		item.DeleteURL = r.withState(item.DeleteURL, theme, wrap, pane)
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

// withState applies the URL-driven page preferences to a link the way the
// theme parameter already rides every explorer link: each preference is
// present only in its non-default state (theme=dark, wrap=off,
// pane=collapsed), so defaults stay out of the URL and navigation carries
// the user's choices forward.
func (r *renderer) withState(target string, theme, wrap, pane string) string {
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
	u.RawQuery = query.Encode()
	return u.String()
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

func (r *renderer) Error(message string) pageData { return pageData{Error: message} }

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
