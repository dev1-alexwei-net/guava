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
	"strings"
	"sync"
	"unicode/utf16"
	"unicode/utf8"
)

// application composes the filesystem, persistence, synchronization, mutation,
// presentation, and HTTP routing dependencies for one explorer instance.
type application struct {
	filesystem *rootedFS
	store      *Store
	syncer     *synchronizer
	mutations  *mutations
	renderer   *renderer
	routes     http.Handler
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

// Handler returns the application's traversal guard and route tree.
func (a *application) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requestHasTraversal(r.URL.Path) {
			a.renderError(w, http.StatusBadRequest, "Invalid path")
			return
		}
		a.routes.ServeHTTP(w, r)
	})
}

// buildRoutes is the single route table for the file explorer.
func (a *application) buildRoutes() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /{$}", a.serialized(a.rootPage))
	mux.Handle("GET /d/{$}", a.serialized(a.rootPage))
	mux.Handle("GET /d/{path...}", a.serialized(a.directoryPage))
	mux.Handle("GET /f/{path...}", a.serialized(a.filePage))
	mux.Handle("GET /edit/{path...}", a.serialized(a.editorPage))
	mux.Handle("POST /edit/{path...}", a.serialized(a.saveFile))
	mux.Handle("GET /confirm-delete/{path...}", a.serialized(a.confirmDeletePage))
	mux.Handle("GET /preview/{path...}", a.serialized(a.previewPage))
	mux.HandleFunc("GET /static/bootstrap.min.css", a.renderer.Stylesheet)
	mutations := []struct {
		path string
		run  func(*http.Request) (string, error)
	}{
		{path: "/mkdir", run: func(r *http.Request) (string, error) {
			return a.mutations.MakeDirectory(r.FormValue("parent"), r.FormValue("name"))
		}},
		{path: "/create-file", run: func(r *http.Request) (string, error) {
			return a.mutations.CreateFile(r.FormValue("parent"), r.FormValue("name"))
		}},
		{path: "/rename", run: func(r *http.Request) (string, error) {
			return a.mutations.Rename(r.FormValue("old"), r.FormValue("new"))
		}},
		{path: "/delete", run: func(r *http.Request) (string, error) {
			return a.mutations.Delete(r.FormValue("path"))
		}},
	}
	for _, method := range []string{http.MethodPost, http.MethodPut} {
		for _, mutation := range mutations {
			mux.Handle(method+" "+mutation.path, a.formMutation(mutation.run))
		}
		mux.Handle(method+" /upload", a.serialized(a.upload))
	}
	return mux
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

func (e *application) serialized(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.mu.Lock()
		defer e.mu.Unlock()
		next(w, r)
	})
}

func (e *application) rootPage(w http.ResponseWriter, r *http.Request) {
	e.renderDirectory(w, "", "", r.URL.Query().Get("error"))
}

func (e *application) directoryPage(w http.ResponseWriter, r *http.Request) {
	e.renderDirectory(w, r.PathValue("path"), "", r.URL.Query().Get("error"))
}

func (e *application) filePage(w http.ResponseWriter, r *http.Request) {
	target, err := e.filesystem.Resolve(r.PathValue("path"))
	if err != nil {
		e.pathError(w, err)
		return
	}
	info, err := e.filesystem.Stat(e.filesystem.Relative(target))
	if err != nil {
		e.renderReadError(w, err, "File not found", "Unable to read file")
		return
	}
	if !info.Mode().IsRegular() {
		e.renderError(w, http.StatusNotFound, "File not found")
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": info.Name()}))
	}
	file, err := e.filesystem.Open(e.filesystem.Relative(target))
	if err != nil {
		e.renderError(w, http.StatusInternalServerError, "Unable to read file")
		return
	}
	defer file.Close()
	http.ServeContent(w, r, info.Name(), info.ModTime(), file)
}

func (e *application) renderReadError(w http.ResponseWriter, err error, missing, failure string) {
	if os.IsNotExist(err) {
		e.renderError(w, http.StatusNotFound, missing)
		return
	}
	e.renderError(w, http.StatusInternalServerError, failure)
}

func (e *application) renderDirectory(w http.ResponseWriter, requested string, previewRel string, pageError string) {
	dir, err := e.filesystem.Resolve(requested)
	if err != nil {
		e.pathError(w, err)
		return
	}
	info, err := e.filesystem.Stat(e.filesystem.Relative(dir))
	if err != nil {
		e.renderReadError(w, err, "Directory not found", "Unable to read directory")
		return
	}
	if !info.IsDir() {
		e.renderError(w, http.StatusNotFound, "Directory not found")
		return
	}
	if _, err := e.syncer.Sync(); err != nil {
		e.renderError(w, http.StatusInternalServerError, "Unable to synchronize directory")
		return
	}

	current := e.filesystem.Relative(dir)
	entries, err := e.store.List(current)
	if err != nil {
		e.renderError(w, http.StatusInternalServerError, "Unable to list directory")
		return
	}
	data := e.renderer.Directory(current, entries, pageError)
	e.applyPreview(&data, current, previewRel)
	e.writePage(w, http.StatusOK, data)
}

// previewPage renders the parent directory of the requested file with the
// preview pane active for that file. The preview state lives in the URL, so
// the route always matches what the page shows: the pane is visible exactly
// when the path is /preview/{path}, and hiding it is a plain navigation back
// to the directory. There is no client-side state or JavaScript.
func (e *application) previewPage(w http.ResponseWriter, r *http.Request) {
	rel := r.PathValue("path")
	target, err := e.filesystem.Resolve(rel)
	if err != nil {
		e.pathError(w, err)
		return
	}
	info, err := e.filesystem.Stat(e.filesystem.Relative(target))
	if err != nil {
		e.renderReadError(w, err, "File not found", "Unable to read file")
		return
	}
	if !info.Mode().IsRegular() {
		e.renderError(w, http.StatusBadRequest, "Preview requires a regular file")
		return
	}
	parent := filepath.ToSlash(filepath.Dir(rel))
	if parent == "." {
		parent = ""
	}
	e.renderDirectory(w, parent, rel, "")
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

func (e *application) formMutation(operation func(*http.Request) (string, error)) http.Handler {
	return e.serialized(func(w http.ResponseWriter, r *http.Request) {
		if err := parseForm(w, r); err != nil {
			e.redirectMutationError(w, r, http.StatusBadRequest, "Invalid form")
			return
		}
		current, err := operation(r)
		e.finishMutation(w, r, current, err)
	})
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

func (e *application) upload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)
	if err := r.ParseMultipartForm(maxUploadSize); err != nil {
		e.redirectMutationError(w, r, http.StatusBadRequest, "Invalid upload")
		return
	}
	_, parent, err := e.filesystem.ResolveParent(r.FormValue("parent"))
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
	http.Redirect(w, r, target, http.StatusSeeOther)
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

func (e *application) pathError(w http.ResponseWriter, err error) {
	interaction := pathInteraction(err)
	e.renderError(w, interaction.status, interaction.message)
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
	http.Redirect(w, r, target+"?"+query.Encode(), http.StatusSeeOther)
}

func mutationParent(r *http.Request) string {
	if parent := r.FormValue("parent"); parent != "" {
		return parent
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

func (e *application) renderError(w http.ResponseWriter, status int, message string) {
	e.writePage(w, status, e.renderer.Error(message))
}

func (e *application) writePage(w http.ResponseWriter, status int, data pageData) {
	e.renderer.WritePage(w, status, data)
}

func (e *application) editorPage(w http.ResponseWriter, r *http.Request) {
	target, err := e.filesystem.Resolve(r.PathValue("path"))
	if err != nil {
		e.pathError(w, err)
		return
	}
	rel := e.filesystem.Relative(target)
	file, err := e.filesystem.Open(rel)
	if err != nil {
		e.pathError(w, err)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		e.renderError(w, http.StatusInternalServerError, "Unable to read file")
		return
	}
	if !info.Mode().IsRegular() {
		e.renderError(w, http.StatusNotFound, "File not found")
		return
	}
	if info.Size() > maxEditorSize {
		e.renderError(w, http.StatusRequestEntityTooLarge, "This file is too large to edit (maximum 1 MiB)")
		return
	}
	content, err := io.ReadAll(io.LimitReader(file, maxEditorSize+1))
	if err != nil {
		e.renderError(w, http.StatusInternalServerError, "Unable to read file")
		return
	}
	if len(content) > maxEditorSize {
		e.renderError(w, http.StatusRequestEntityTooLarge, "This file is too large to edit (maximum 1 MiB)")
		return
	}
	if !utf8.Valid(content) || strings.IndexByte(string(content), 0) >= 0 {
		e.renderError(w, http.StatusUnsupportedMediaType, "This file is not supported by the text editor")
		return
	}
	e.writePage(w, http.StatusOK, e.renderer.Editor(rel, string(content), r.URL.Query().Get("saved") == "1"))
}

func (e *application) saveFile(w http.ResponseWriter, r *http.Request) {
	target, err := e.filesystem.Resolve(r.PathValue("path"))
	if err != nil {
		e.pathError(w, err)
		return
	}
	rel := e.filesystem.Relative(target)
	file, err := e.filesystem.Open(rel)
	if err != nil {
		e.pathError(w, err)
		return
	}
	info, err := file.Stat()
	_ = file.Close()
	if err != nil {
		e.renderError(w, http.StatusInternalServerError, "Unable to inspect file")
		return
	}
	if !info.Mode().IsRegular() {
		e.renderError(w, http.StatusNotFound, "File not found")
		return
	}
	if info.Size() > maxEditorSize {
		e.renderError(w, http.StatusRequestEntityTooLarge, "This file is too large to edit (maximum 1 MiB)")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxEditorSize*3+4096)
	if err := r.ParseForm(); err != nil {
		status, message := editorSubmissionError(err)
		e.renderError(w, status, message)
		return
	}
	content := r.FormValue("content")
	if err := e.mutations.SaveText(rel, content, info.Mode().Perm()); err != nil {
		status, message := interactionDetails(err, "Unable to save file")
		e.renderError(w, status, message)
		return
	}
	http.Redirect(w, r, e.renderer.EditorURL(rel)+"?saved=1", http.StatusSeeOther)
}

func (e *application) confirmDeletePage(w http.ResponseWriter, r *http.Request) {
	target, err := e.filesystem.Resolve(r.PathValue("path"))
	if err != nil {
		e.pathError(w, err)
		return
	}
	if target == e.filesystem.Path() {
		e.renderError(w, http.StatusBadRequest, "Cannot delete the root directory")
		return
	}
	info, err := e.filesystem.Lstat(e.filesystem.Relative(target))
	if err != nil {
		e.pathError(w, err)
		return
	}
	if info.Mode()&os.ModeSymlink != 0 {
		e.pathError(w, errForbidden)
		return
	}
	e.writePage(w, http.StatusOK, e.renderer.DeleteConfirmation(e.filesystem.Relative(target)))
}
