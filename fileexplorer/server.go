// Package main implements the self-contained file explorer HTTP handlers.
package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/dev1-alexwei-net/guava/internal/web"
	"github.com/gin-gonic/gin"
)

// application composes the filesystem, persistence, synchronization, mutation,
// presentation, and HTTP routing dependencies for one explorer instance.
type application struct {
	filesystem *rootedFS
	store      *Store
	syncer     *synchronizer
	mutations  *mutations
	renderer   *renderer
	routes     *gin.Engine
	mu         sync.Mutex
}

// newApplication wires one explorer from explicit dependencies.
func newApplication(filesystem *rootedFS, store *Store, syncer *synchronizer, mutations *mutations, renderer *renderer) (*application, error) {
	switch {
	case filesystem == nil:
		return nil, errors.New("rooted filesystem is required")
	case store == nil:
		return nil, errors.New("store is required")
	case syncer == nil:
		return nil, errors.New("synchronizer is required")
	case mutations == nil:
		return nil, errors.New("mutations are required")
	case renderer == nil:
		return nil, errors.New("renderer is required")
	}
	app := &application{
		filesystem: filesystem,
		store:      store,
		syncer:     syncer,
		mutations:  mutations,
		renderer:   renderer,
	}
	app.routes = app.buildRoutes()
	return app, nil
}

// newApplicationAt opens the rooted filesystem and composes the production dependencies.
// The application owns that filesystem handle; the caller retains the Store lifecycle.
func newApplicationAt(root string, store *Store) (*application, error) {
	if store == nil {
		return nil, errors.New("store is required")
	}
	filesystem, err := openRootedFS(root)
	if err != nil {
		return nil, err
	}
	app, err := newApplication(filesystem, store, newSynchronizer(store, filesystem), newMutations(filesystem), newRenderer())
	if err != nil {
		_ = filesystem.Close()
		return nil, err
	}
	return app, nil
}

// Close releases the rooted filesystem handle owned by the application.
func (a *application) Close() error { return a.filesystem.Close() }

// newHandler adapts a root path and store for callers that only need an HTTP handler.
func newHandler(root string, store *Store) (http.Handler, error) {
	app, err := newApplicationAt(root, store)
	if err != nil {
		return nil, err
	}
	return app.Handler(), nil
}

// Handler returns the application's Gin engine.
func (a *application) Handler() http.Handler {
	return a.routes
}

// buildRoutes is the single route table for the file explorer.
func (a *application) buildRoutes() *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	router.RedirectTrailingSlash = false
	router.RedirectFixedPath = false
	router.HandleMethodNotAllowed = true
	router.Use(web.SecurityHeaders(), func(c *gin.Context) {
		if requestHasTraversal(c.Request.URL.Path) {
			a.renderError(c.Writer, c.Request, http.StatusBadRequest, "Invalid path")
			c.Abort()
			return
		}
		c.Next()
	}, func(c *gin.Context) {
		a.mu.Lock()
		defer a.mu.Unlock()
		c.Next()
	})

	handle := func(method, route string, handler gin.HandlerFunc) {
		router.Handle(method, route, handler)
	}
	get := func(route string, handler gin.HandlerFunc) {
		handle(http.MethodGet, route, handler)
		handle(http.MethodHead, route, handler)
	}

	get("/", a.rootPage)
	get("/d/*path", a.directoryPage)
	get("/f/*path", a.filePage)
	get("/edit/*path", a.editorPage)
	handle(http.MethodPost, "/edit/*path", a.saveFile)
	get("/confirm-delete/*path", a.confirmDeletePage)
	get("/rename/*path", a.renamePage)
	get("/move/*path", a.movePage)
	handle(http.MethodPost, "/move", a.submitMove)
	get("/preview/*path", a.previewPage)
	get("/clipboard/:mode/*path", a.selectClipboard)
	handle(http.MethodPost, "/paste", a.pasteFile)
	handle(http.MethodPut, "/paste", a.pasteFile)
	get("/static/bootstrap.min.css", a.renderer.Stylesheet)

	mutations := []struct {
		path string
		run  func(*gin.Context) (string, error)
	}{
		{path: "/mkdir", run: func(c *gin.Context) (string, error) {
			return a.mutations.MakeDirectory(web.FormValue(c, "parent"), web.FormValue(c, "name"))
		}},
		{path: "/create-file", run: func(c *gin.Context) (string, error) {
			return a.mutations.CreateFile(web.FormValue(c, "parent"), web.FormValue(c, "name"))
		}},
		{path: "/rename", run: func(c *gin.Context) (string, error) {
			return a.mutations.Rename(web.FormValue(c, "old"), web.FormValue(c, "new"))
		}},
		{path: "/delete", run: func(c *gin.Context) (string, error) {
			return a.mutations.Delete(web.FormValue(c, "path"))
		}},
	}
	for _, method := range []string{http.MethodPost, http.MethodPut} {
		for _, mutation := range mutations {
			handle(method, mutation.path, a.formMutation(mutation.run))
		}
		handle(method, "/upload", a.upload)
	}
	return router
}

func requestHasTraversal(requestPath string) bool {
	for _, component := range strings.Split(strings.ReplaceAll(requestPath, `\`, "/"), "/") {
		if component == ".." {
			return true
		}
	}
	return false
}

const maxUploadSize = 32 << 20
const maxEditorSize = 1 << 20
const maxPreviewSize = 512 << 10

func routeParam(c *gin.Context, key string) string {
	return strings.TrimPrefix(c.Param(key), "/")
}

func (e *application) rootPage(c *gin.Context) {
	w, r := c.Writer, c.Request
	e.renderDirectory(w, r, "", "", r.URL.Query().Get("error"))
}

func (e *application) directoryPage(c *gin.Context) {
	w, r := c.Writer, c.Request
	e.renderDirectory(w, r, routeParam(c, "path"), "", r.URL.Query().Get("error"))
}

func (e *application) filePage(c *gin.Context) {
	w, r := c.Writer, c.Request
	target, err := e.filesystem.Resolve(routeParam(c, "path"))
	if err != nil {
		e.pathError(w, r, err)
		return
	}
	info, err := e.filesystem.Stat(e.filesystem.Relative(target))
	if err != nil {
		e.renderReadError(w, r, err, "File not found", "Unable to read file")
		return
	}
	if !info.Mode().IsRegular() {
		e.renderError(w, r, http.StatusNotFound, "File not found")
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": info.Name()}))
	}
	file, err := e.filesystem.Open(e.filesystem.Relative(target))
	if err != nil {
		e.renderError(w, r, http.StatusInternalServerError, "Unable to read file")
		return
	}
	defer file.Close()
	http.ServeContent(w, r, info.Name(), info.ModTime(), file)
}

func (e *application) renderReadError(w http.ResponseWriter, r *http.Request, err error, missing, failure string) {
	if os.IsNotExist(err) {
		e.renderError(w, r, http.StatusNotFound, missing)
		return
	}
	e.renderError(w, r, http.StatusInternalServerError, failure)
}

func (e *application) renderDirectory(w http.ResponseWriter, r *http.Request, requested string, previewRel string, pageError string) {
	dir, err := e.filesystem.Resolve(requested)
	if err != nil {
		e.pathError(w, r, err)
		return
	}
	info, err := e.filesystem.Stat(e.filesystem.Relative(dir))
	if err != nil {
		e.renderReadError(w, r, err, "Directory not found", "Unable to read directory")
		return
	}
	if !info.IsDir() {
		e.renderError(w, r, http.StatusNotFound, "Directory not found")
		return
	}
	if _, err := e.syncer.Sync(); err != nil {
		e.renderError(w, r, http.StatusInternalServerError, "Unable to synchronize directory")
		return
	}

	current := e.filesystem.Relative(dir)
	entries, err := e.store.List(current)
	if err != nil {
		e.renderError(w, r, http.StatusInternalServerError, "Unable to list directory")
		return
	}
	data := e.renderer.Directory(current, entries, pageError)
	e.applyPreview(&data, current, previewRel)
	e.writePage(w, r, http.StatusOK, data)
}

// previewPage renders the parent directory of the requested file with the
// preview pane active for that file. The preview state lives in the URL, so
// the route always matches what the page shows: the pane is visible exactly
// when the path is /preview/{path}, and hiding it is a plain navigation back
// to the directory. There is no client-side state or JavaScript.
func (e *application) previewPage(c *gin.Context) {
	w, r := c.Writer, c.Request
	rel := routeParam(c, "path")
	target, err := e.filesystem.Resolve(rel)
	if err != nil {
		e.pathError(w, r, err)
		return
	}
	info, err := e.filesystem.Stat(e.filesystem.Relative(target))
	if err != nil {
		e.renderReadError(w, r, err, "File not found", "Unable to read file")
		return
	}
	if !info.Mode().IsRegular() {
		e.renderError(w, r, http.StatusBadRequest, "Preview requires a regular file")
		return
	}
	parent := filepath.ToSlash(filepath.Dir(rel))
	if parent == "." {
		parent = ""
	}
	e.renderDirectory(w, r, parent, rel, "")
}

// applyPreview reads the selected file and delegates page-model preparation to the renderer.
func (e *application) applyPreview(data *pageData, current, rel string) {
	if rel == "" {
		return
	}
	parent := filepath.ToSlash(filepath.Dir(rel))
	if parent == "." {
		parent = ""
	}
	if parent != current {
		return
	}
	info, err := e.filesystem.Stat(filepath.ToSlash(rel))
	if err != nil || !info.Mode().IsRegular() {
		return
	}
	if info.Size() > maxPreviewSize {
		e.renderer.Preview(data, current, rel, previewContent{problem: previewKnownTooLarge})
		return
	}
	file, err := e.filesystem.Open(filepath.ToSlash(rel))
	if err != nil {
		e.renderer.Preview(data, current, rel, previewContent{})
		return
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, maxPreviewSize+1))
	if err != nil {
		e.renderer.Preview(data, current, rel, previewContent{})
		return
	}
	if len(content) > maxPreviewSize {
		e.renderer.Preview(data, current, rel, previewContent{problem: previewReadTooLarge})
		return
	}
	decoded, ok := decodePreviewText(content)
	if !ok {
		e.renderer.Preview(data, current, rel, previewContent{problem: previewUnsupported})
		return
	}
	e.renderer.Preview(data, current, rel, previewContent{text: decoded})
}

func decodePreviewText(content []byte) (string, bool) {
	switch {
	case bytes.HasPrefix(content, []byte{0xEF, 0xBB, 0xBF}):
		content = content[3:]
	case bytes.HasPrefix(content, []byte{0xFF, 0xFE}):
		return decodeUTF16Preview(content[2:], true)
	case bytes.HasPrefix(content, []byte{0xFE, 0xFF}):
		return decodeUTF16Preview(content[2:], false)
	}
	if !utf8.Valid(content) || bytes.IndexByte(content, 0) >= 0 {
		return "", false
	}
	return string(content), true
}

func decodeUTF16Preview(content []byte, littleEndian bool) (string, bool) {
	if len(content)%2 != 0 {
		return "", false
	}
	units := make([]uint16, len(content)/2)
	for i := range units {
		first, second := uint16(content[2*i]), uint16(content[2*i+1])
		if littleEndian {
			units[i] = first | second<<8
			continue
		}
		units[i] = first<<8 | second
	}
	for i, unit := range units {
		if isUTF16HighSurrogate(unit) && (i+1 == len(units) || !isUTF16LowSurrogate(units[i+1])) {
			return "", false
		}
		if isUTF16LowSurrogate(unit) && (i == 0 || !isUTF16HighSurrogate(units[i-1])) {
			return "", false
		}
	}
	decoded := string(utf16.Decode(units))
	return decoded, !strings.ContainsRune(decoded, 0)
}

func isUTF16HighSurrogate(unit uint16) bool { return unit >= 0xD800 && unit <= 0xDBFF }

func isUTF16LowSurrogate(unit uint16) bool { return unit >= 0xDC00 && unit <= 0xDFFF }

func (e *application) formMutation(operation func(*gin.Context) (string, error)) gin.HandlerFunc {
	return func(c *gin.Context) {
		w, r := c.Writer, c.Request
		if err := parseForm(w, r); err != nil {
			e.redirectMutationError(w, r, http.StatusBadRequest, "Invalid form")
			return
		}
		current, err := operation(c)
		e.finishMutation(w, r, current, err)
	}
}

func (e *application) finishMutation(w http.ResponseWriter, r *http.Request, current string, err error) {
	if err == nil {
		e.redirectToDirectory(w, r, current)
		return
	}
	var interaction *interactionError
	if errors.As(err, &interaction) {
		e.redirectMutationError(w, r, interaction.status, interaction.message)
		return
	}
	e.redirectMutationPathError(w, r, err)
}

func (e *application) upload(c *gin.Context) {
	w, r := c.Writer, c.Request
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)
	if err := r.ParseMultipartForm(maxUploadSize); err != nil {
		e.redirectMutationError(w, r, http.StatusBadRequest, "Invalid upload")
		return
	}
	_, parent, err := e.filesystem.ResolveParent(web.FormValue(c, "parent"))
	if err != nil {
		e.redirectMutationPathError(w, r, err)
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		e.redirectMutationError(w, r, http.StatusBadRequest, "Upload file is required")
		return
	}
	defer file.Close()
	current, err := e.mutations.Upload(parent, header.Filename, file)
	e.finishMutation(w, r, current, err)
}

func (e *application) redirectToDirectory(w http.ResponseWriter, r *http.Request, current string) {
	if current == "." {
		current = ""
	}
	target := e.renderer.ItemURL(current, true)
	if current == "" {
		target = "/"
	}
	http.Redirect(w, r, preserveClipboard(e.renderer.withState(target, requestedTheme(r), requestedWrap(r), requestedPreviewPane(r), requestedColumn(r)), r), http.StatusSeeOther)
}

func preserveClipboard(target string, r *http.Request) string {
	if r == nil || r.URL == nil {
		return target
	}
	mode, source := r.URL.Query().Get("clipboard_mode"), r.URL.Query().Get("clipboard_path")
	if mode != "copy" && mode != "cut" {
		return target
	}
	if source == "" {
		return target
	}
	u, err := url.Parse(target)
	if err != nil {
		return target
	}
	query := u.Query()
	query.Set("clipboard_mode", mode)
	query.Set("clipboard_path", source)
	u.RawQuery = query.Encode()
	return u.String()
}

func (e *application) selectClipboard(c *gin.Context) {
	w, r := c.Writer, c.Request
	mode, requested := c.Param("mode"), routeParam(c, "path")
	if mode != "copy" && mode != "cut" {
		e.renderError(w, r, http.StatusBadRequest, "Choose copy or cut")
		return
	}
	target, err := e.filesystem.Resolve(requested)
	if err != nil {
		e.pathError(w, r, err)
		return
	}
	rel := e.filesystem.Relative(target)
	info, err := e.filesystem.Stat(rel)
	if err != nil {
		e.pathError(w, r, err)
		return
	}
	if !info.Mode().IsRegular() {
		e.renderError(w, r, http.StatusBadRequest, "Only regular files can be copied or cut")
		return
	}
	parent := path.Dir(rel)
	if parent == "." {
		parent = ""
	}
	returnTo := e.renderer.ItemURL(parent, true)
	if parent == "" {
		returnTo = "/"
	}
	query := url.Values{"clipboard_mode": {mode}, "clipboard_path": {rel}, "notice": {strings.Title(mode) + " ready: " + path.Base(rel) + ". Choose a destination folder and paste."}}
	targetURL := returnTo + "?" + query.Encode()
	http.Redirect(w, r, e.renderer.withState(targetURL, requestedTheme(r), requestedWrap(r), requestedPreviewPane(r), requestedColumn(r)), http.StatusSeeOther)
}

func (e *application) pasteFile(c *gin.Context) {
	w, r := c.Writer, c.Request
	if err := parseForm(w, r); err != nil {
		e.redirectMutationError(w, r, http.StatusBadRequest, "Invalid form")
		return
	}
	mode, source, destination := web.FormValue(c, "clipboard_mode"), web.FormValue(c, "clipboard_path"), web.FormValue(c, "destination")
	current, err := e.mutations.Paste(mode, source, destination)
	if err != nil {
		var interaction *interactionError
		if errors.As(err, &interaction) {
			e.redirectMutationError(w, r, interaction.status, interaction.message)
		} else {
			e.redirectMutationPathError(w, r, err)
		}
		return
	}
	verb := "Copied"
	if mode == "cut" {
		verb = "Moved"
	}
	message := verb + " " + path.Base(source) + "."
	target := e.renderer.ItemURL(current, true)
	if current == "" {
		target = "/"
	}
	target += "?" + url.Values{"notice": {message}}.Encode()
	http.Redirect(w, r, e.renderer.withState(target, requestedTheme(r), requestedWrap(r), requestedPreviewPane(r), requestedColumn(r)), http.StatusSeeOther)
}

func parseForm(w http.ResponseWriter, r *http.Request) error {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	return r.ParseForm()
}

func pathInteraction(err error) *interactionError {
	switch {
	case errors.Is(err, errForbidden):
		return newInteractionError(http.StatusForbidden, "Path is outside the configured root or contains a symbolic link")
	case errors.Is(err, errInvalidPath):
		return newInteractionError(http.StatusBadRequest, "Invalid path")
	case os.IsNotExist(err):
		return newInteractionError(http.StatusNotFound, "Path not found")
	default:
		return newInteractionError(http.StatusBadRequest, "Invalid path")
	}
}

func (e *application) pathError(w http.ResponseWriter, r *http.Request, err error) {
	interaction := pathInteraction(err)
	e.renderError(w, r, interaction.status, interaction.message)
}

func (e *application) redirectMutationError(w http.ResponseWriter, r *http.Request, status int, message string) {
	_, current, err := e.filesystem.ResolveParent(mutationParent(r))
	if err != nil {
		current = ""
	}
	target := "/"
	if current != "" {
		target = e.renderer.ItemURL(current, true)
	}
	query := url.Values{"error": {message}, "status": {fmt.Sprint(status)}}
	http.Redirect(w, r, preserveClipboard(e.renderer.withState(target+"?"+query.Encode(), requestedTheme(r), requestedWrap(r), requestedPreviewPane(r), requestedColumn(r)), r), http.StatusSeeOther)
}

func mutationParent(r *http.Request) string {
	if parent := r.FormValue("parent"); parent != "" {
		return parent
	}
	if destination := r.FormValue("destination"); destination != "" {
		return destination
	}
	target := r.FormValue("old")
	if target == "" {
		target = r.FormValue("path")
	}
	if target == "" {
		return ""
	}
	parent := path.Dir(strings.ReplaceAll(target, `\`, "/"))
	if parent == "." {
		return ""
	}
	return parent
}

func (e *application) redirectMutationPathError(w http.ResponseWriter, r *http.Request, err error) {
	interaction := pathInteraction(err)
	e.redirectMutationError(w, r, interaction.status, interaction.message)
}

func interactionDetails(err error, fallback string) (int, string) {
	var interaction *interactionError
	if errors.As(err, &interaction) {
		return interaction.status, interaction.message
	}
	return http.StatusInternalServerError, fallback
}

func editorSubmissionError(err error) (int, string) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return http.StatusRequestEntityTooLarge, "Editor content is too large (maximum 1 MiB)"
	}
	return http.StatusBadRequest, "Invalid editor submission"
}

func (e *application) renderError(w http.ResponseWriter, r *http.Request, status int, message string) {
	e.writePage(w, r, status, e.renderer.Error(message))
}

func (e *application) writePage(w http.ResponseWriter, r *http.Request, status int, data pageData) {
	e.renderer.WritePage(w, status, e.renderer.ForRequest(data, r))
}

func (e *application) openEditableFile(w http.ResponseWriter, r *http.Request, requested, inspectError string) (string, *os.File, os.FileInfo, bool) {
	target, err := e.filesystem.Resolve(requested)
	if err != nil {
		e.pathError(w, r, err)
		return "", nil, nil, false
	}
	rel := e.filesystem.Relative(target)
	file, err := e.filesystem.Open(rel)
	if err != nil {
		e.pathError(w, r, err)
		return "", nil, nil, false
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		e.renderError(w, r, http.StatusInternalServerError, inspectError)
		return "", nil, nil, false
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		e.renderError(w, r, http.StatusNotFound, "File not found")
		return "", nil, nil, false
	}
	if info.Size() > maxEditorSize {
		_ = file.Close()
		e.renderError(w, r, http.StatusRequestEntityTooLarge, "This file is too large to edit (maximum 1 MiB)")
		return "", nil, nil, false
	}
	return rel, file, info, true
}

func (e *application) editorPage(c *gin.Context) {
	w, r := c.Writer, c.Request
	rel, file, _, ok := e.openEditableFile(w, r, routeParam(c, "path"), "Unable to read file")
	if !ok {
		return
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, maxEditorSize+1))
	if err != nil {
		e.renderError(w, r, http.StatusInternalServerError, "Unable to read file")
		return
	}
	if len(content) > maxEditorSize {
		e.renderError(w, r, http.StatusRequestEntityTooLarge, "This file is too large to edit (maximum 1 MiB)")
		return
	}
	if !utf8.Valid(content) || strings.IndexByte(string(content), 0) >= 0 {
		e.renderError(w, r, http.StatusUnsupportedMediaType, "This file is not supported by the text editor")
		return
	}
	e.writePage(w, r, http.StatusOK, e.renderer.Editor(rel, string(content), r.URL.Query().Get("saved") == "1"))
}

func (e *application) saveFile(c *gin.Context) {
	w, r := c.Writer, c.Request
	rel, file, info, ok := e.openEditableFile(w, r, routeParam(c, "path"), "Unable to inspect file")
	if !ok {
		return
	}
	_ = file.Close()

	r.Body = http.MaxBytesReader(w, r.Body, maxEditorSize*3+4096)
	if err := r.ParseForm(); err != nil {
		status, message := editorSubmissionError(err)
		e.renderError(w, r, status, message)
		return
	}
	content := web.FormValue(c, "content")
	if err := e.mutations.SaveText(rel, content, info.Mode().Perm()); err != nil {
		status, message := interactionDetails(err, "Unable to save file")
		e.renderError(w, r, status, message)
		return
	}
	http.Redirect(w, r, e.renderer.withState(e.renderer.EditorURL(rel)+"?saved=1", requestedTheme(r), requestedWrap(r), requestedPreviewPane(r), requestedColumn(r)), http.StatusSeeOther)
}

func (e *application) confirmationTarget(w http.ResponseWriter, r *http.Request, requested, action string) (string, bool) {
	target, err := e.filesystem.Resolve(requested)
	if err != nil {
		e.pathError(w, r, err)
		return "", false
	}
	if target == e.filesystem.Path() {
		e.renderError(w, r, http.StatusBadRequest, "Cannot "+action+" the root directory")
		return "", false
	}
	rel := e.filesystem.Relative(target)
	info, err := e.filesystem.Lstat(rel)
	if err != nil {
		e.pathError(w, r, err)
		return "", false
	}
	if info.Mode()&os.ModeSymlink != 0 {
		e.pathError(w, r, errForbidden)
		return "", false
	}
	return rel, true
}

func (e *application) confirmDeletePage(c *gin.Context) {
	w, r := c.Writer, c.Request
	rel, ok := e.confirmationTarget(w, r, routeParam(c, "path"), "delete")
	if ok {
		e.writePage(w, r, http.StatusOK, e.renderer.DeleteConfirmation(rel))
	}
}

// renamePage presents the dedicated rename form for an entry, mirroring
// confirmDeletePage: GET presents the form and POST /rename commits it. The
// root directory cannot be renamed for the same reason it cannot be
// deleted, and symbolic links stay read-only entries like on the delete
// page.
func (e *application) renamePage(c *gin.Context) {
	w, r := c.Writer, c.Request
	rel, ok := e.confirmationTarget(w, r, routeParam(c, "path"), "rename")
	if ok {
		e.writePage(w, r, http.StatusOK, e.renderer.RenameConfirmation(rel))
	}
}

func (e *application) moveSource(w http.ResponseWriter, r *http.Request, requested string) (string, bool) {
	target, err := e.filesystem.Resolve(requested)
	if err != nil {
		e.pathError(w, r, err)
		return "", false
	}
	rel := e.filesystem.Relative(target)
	if rel == "" {
		e.renderError(w, r, http.StatusBadRequest, "The root directory cannot be moved")
		return "", false
	}
	info, err := e.filesystem.Stat(rel)
	if err != nil {
		e.pathError(w, r, err)
		return "", false
	}
	if !info.Mode().IsRegular() {
		e.renderError(w, r, http.StatusBadRequest, "Choose a regular file to move")
		return "", false
	}
	return rel, true
}

func (e *application) moveDirectoryChoices() ([]directoryChoice, error) {
	entries, err := e.syncer.scanner.Scan()
	if err != nil {
		return nil, err
	}
	choices := []directoryChoice{{Path: ".", Label: "Root directory (/)"}}
	for _, entry := range entries {
		if entry.IsDir {
			choices = append(choices, directoryChoice{Path: entry.Path, Label: entry.Path + "/"})
		}
	}
	sort.Slice(choices[1:], func(i, j int) bool { return choices[i+1].Label < choices[j+1].Label })
	return choices, nil
}

func (e *application) renderMoveForm(w http.ResponseWriter, r *http.Request, rel, mode, destination, returnPath, message string, status int) {
	directories, err := e.moveDirectoryChoices()
	if err != nil {
		e.renderError(w, r, http.StatusInternalServerError, "Unable to list destination folders")
		return
	}
	data := e.renderer.MoveForm(rel, mode, destination, returnPath, directories, message)
	e.writePage(w, r, status, data)
}

func requestedMoveMode(requested string) (string, bool) {
	if requested == "" {
		return "cut", true
	}
	return requested, requested == "cut" || requested == "copy"
}

func (e *application) movePage(c *gin.Context) {
	w, r := c.Writer, c.Request
	rel, ok := e.moveSource(w, r, routeParam(c, "path"))
	if !ok {
		return
	}
	mode, valid := requestedMoveMode(r.URL.Query().Get("mode"))
	if !valid {
		e.renderError(w, r, http.StatusBadRequest, "Choose copy or cut")
		return
	}
	parent := filepath.ToSlash(filepath.Dir(rel))
	if parent == "." {
		parent = ""
	}
	destination, returnPath, message := parent, parent, ""
	if destination == "" {
		destination = "."
	}
	status := http.StatusOK
	if requested := r.URL.Query().Get("destination"); requested != "" {
		if _, resolved, err := e.filesystem.ResolveParent(requested); err == nil {
			destination = resolved
			if destination == "" {
				destination = "."
			}
		} else {
			message = "Choose an available destination folder: " + pathInteraction(err).message
			status = pathInteraction(err).status
		}
	}
	if requested := r.URL.Query().Get("return"); requested != "" {
		if _, resolved, err := e.filesystem.ResolveParent(requested); err == nil {
			returnPath = resolved
		}
	}
	e.renderMoveForm(w, r, rel, mode, destination, returnPath, message, status)
}

func (e *application) submitMove(c *gin.Context) {
	w, r := c.Writer, c.Request
	if err := parseForm(w, r); err != nil {
		if rel, ok := e.moveSource(w, r, web.FormValue(c, "source")); ok {
			mode, _ := requestedMoveMode(web.FormValue(c, "mode"))
			if mode == "" {
				mode = "cut"
			}
			returnPath := web.FormValue(c, "return")
			e.renderMoveForm(w, r, rel, mode, web.FormValue(c, "destination"), returnPath, "Please choose a valid destination folder.", http.StatusBadRequest)
		}
		return
	}
	rel, ok := e.moveSource(w, r, web.FormValue(c, "source"))
	if !ok {
		return
	}
	mode, valid := requestedMoveMode(web.FormValue(c, "mode"))
	if !valid {
		e.renderMoveForm(w, r, rel, "cut", web.FormValue(c, "destination"), web.FormValue(c, "return"), "Choose copy or cut.", http.StatusBadRequest)
		return
	}
	destination := web.FormValue(c, "destination")
	returnPath := web.FormValue(c, "return")
	if _, resolved, err := e.filesystem.ResolveParent(returnPath); err == nil {
		returnPath = resolved
	} else {
		returnPath = filepath.ToSlash(filepath.Dir(rel))
		if returnPath == "." {
			returnPath = ""
		}
	}
	if _, _, err := e.filesystem.ResolveParent(destination); err != nil {
		interaction := pathInteraction(err)
		e.renderMoveForm(w, r, rel, mode, destination, returnPath, "Choose an available destination folder: "+interaction.message, interaction.status)
		return
	}
	current, err := e.mutations.Paste(mode, rel, destination)
	if err != nil {
		var interaction *interactionError
		if errors.As(err, &interaction) {
			e.renderMoveForm(w, r, rel, mode, destination, returnPath, interaction.message, interaction.status)
			return
		}
		pathIssue := pathInteraction(err)
		e.renderMoveForm(w, r, rel, mode, destination, returnPath, pathIssue.message, pathIssue.status)
		return
	}
	verb := "Moved"
	if mode == "copy" {
		verb = "Copied"
	}
	message := verb + " " + path.Base(rel) + "."
	target := e.renderer.ItemURL(current, true)
	if current == "" {
		target = "/"
	}
	query := url.Values{"notice": {message}}
	target = e.renderer.withState(target+"?"+query.Encode(), requestedTheme(r), requestedWrap(r), requestedPreviewPane(r), requestedColumn(r))
	http.Redirect(w, r, target, http.StatusSeeOther)
}
