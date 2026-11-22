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

const maxUploadSize = 32 << 20
const maxEditorSize = 1 << 20
const maxPreviewSize = 512 << 10

type explorerHandler struct {
	filesystem *rootedFS
	store      *Store
	syncer     *synchronizer
	mutations  *mutations
	renderer   *renderer
	mu         sync.Mutex
}

// newHandler creates the file-explorer routes for an existing root and store.
func newHandler(root string, store *Store) (http.Handler, error) {
	if store == nil {
		return nil, errors.New("store is required")
	}
	filesystem, err := openRootedFS(root)
	if err != nil {
		return nil, err
	}
	e := &explorerHandler{filesystem: filesystem, store: store, syncer: newSynchronizer(store, filesystem), mutations: newMutations(filesystem), renderer: newRenderer()}
	mux := http.NewServeMux()
	mux.Handle("GET /{$}", e.serialized(e.rootPage))
	mux.Handle("GET /d/{$}", e.serialized(e.rootPage))
	mux.Handle("GET /d/{path...}", e.serialized(e.directoryPage))
	mux.Handle("GET /f/{path...}", e.serialized(e.filePage))
	mux.Handle("GET /edit/{path...}", e.serialized(e.editorPage))
	mux.Handle("POST /edit/{path...}", e.serialized(e.saveFile))
	mux.Handle("GET /confirm-delete/{path...}", e.serialized(e.confirmDeletePage))
	mux.Handle("GET /preview/{path...}", e.serialized(e.previewPage))
	mux.HandleFunc("GET /static/bootstrap.min.css", e.renderer.Stylesheet)
	for _, method := range []string{http.MethodPost, http.MethodPut} {
		mux.Handle(method+" /mkdir", e.serialized(e.mkdir))
		mux.Handle(method+" /create-file", e.serialized(e.createFile))
		mux.Handle(method+" /rename", e.serialized(e.rename))
		mux.Handle(method+" /delete", e.serialized(e.delete))
		mux.Handle(method+" /upload", e.serialized(e.upload))
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requestHasTraversal(r.URL.Path) {
			e.renderError(w, http.StatusBadRequest, "Invalid path")
			return
		}
		mux.ServeHTTP(w, r)
	}), nil
}

func requestHasTraversal(requestPath string) bool {
	for _, component := range strings.Split(strings.ReplaceAll(requestPath, `\`, "/"), "/") {
		if component == ".." {
			return true
		}
	}
	return false
}

func (e *explorerHandler) serialized(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.mu.Lock()
		defer e.mu.Unlock()
		next(w, r)
	})
}

func (e *explorerHandler) rootPage(w http.ResponseWriter, r *http.Request) {
	e.renderDirectory(w, "", "", r.URL.Query().Get("error"))
}

func (e *explorerHandler) directoryPage(w http.ResponseWriter, r *http.Request) {
	e.renderDirectory(w, r.PathValue("path"), "", r.URL.Query().Get("error"))
}

func (e *explorerHandler) filePage(w http.ResponseWriter, r *http.Request) {
	target, err := e.filesystem.Resolve(r.PathValue("path"))
	if err != nil {
		e.pathError(w, err)
		return
	}
	info, err := e.filesystem.Stat(e.filesystem.Relative(target))
	if err != nil {
		if os.IsNotExist(err) {
			e.renderError(w, http.StatusNotFound, "File not found")
			return
		}
		e.renderError(w, http.StatusInternalServerError, "Unable to read file")
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

func (e *explorerHandler) renderDirectory(w http.ResponseWriter, requested string, previewRel string, pageError string) {
	dir, err := e.filesystem.Resolve(requested)
	if err != nil {
		e.pathError(w, err)
		return
	}
	info, err := e.filesystem.Stat(e.filesystem.Relative(dir))
	if err != nil {
		if os.IsNotExist(err) {
			e.renderError(w, http.StatusNotFound, "Directory not found")
			return
		}
		e.renderError(w, http.StatusInternalServerError, "Unable to read directory")
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
func (e *explorerHandler) previewPage(w http.ResponseWriter, r *http.Request) {
	rel := r.PathValue("path")
	target, err := e.filesystem.Resolve(rel)
	if err != nil {
		e.pathError(w, err)
		return
	}
	info, err := e.filesystem.Stat(e.filesystem.Relative(target))
	if err != nil {
		if os.IsNotExist(err) {
			e.renderError(w, http.StatusNotFound, "File not found")
			return
		}
		e.renderError(w, http.StatusInternalServerError, "Unable to read file")
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
func (e *explorerHandler) applyPreview(data *pageData, current, rel string) {
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
	if bytes.HasPrefix(content, []byte{0xEF, 0xBB, 0xBF}) {
		content = content[3:]
	}
	if bytes.HasPrefix(content, []byte{0xFF, 0xFE}) || bytes.HasPrefix(content, []byte{0xFE, 0xFF}) {
		littleEndian := content[0] == 0xFF
		content = content[2:]
		if len(content)%2 != 0 {
			return "", false
		}
		units := make([]uint16, len(content)/2)
		for i := range units {
			if littleEndian {
				units[i] = uint16(content[2*i]) | uint16(content[2*i+1])<<8
			} else {
				units[i] = uint16(content[2*i])<<8 | uint16(content[2*i+1])
			}
		}
		for i := 0; i < len(units); i++ {
			unit := units[i]
			if unit >= 0xD800 && unit <= 0xDBFF {
				if i+1 >= len(units) || units[i+1] < 0xDC00 || units[i+1] > 0xDFFF {
					return "", false
				}
				i++
			} else if unit >= 0xDC00 && unit <= 0xDFFF {
				return "", false
			}
		}
		decoded := string(utf16.Decode(units))
		return decoded, !strings.ContainsRune(decoded, 0)
	}
	if !utf8.Valid(content) || bytes.IndexByte(content, 0) >= 0 {
		return "", false
	}
	return string(content), true
}

func (e *explorerHandler) mkdir(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(w, r); err != nil {
		e.redirectMutationError(w, r, http.StatusBadRequest, "Invalid form")
		return
	}
	current, err := e.mutations.MakeDirectory(r.FormValue("parent"), r.FormValue("name"))
	if errors.Is(err, errInvalidName) {
		e.redirectMutationError(w, r, http.StatusBadRequest, "Invalid directory name")
		return
	}
	if err != nil {
		if errors.Is(err, errCreateDirectory) {
			e.redirectMutationError(w, r, http.StatusConflict, "Unable to create directory")
		} else {
			e.redirectMutationPathError(w, r, err)
		}
		return
	}
	e.redirectToDirectory(w, r, current)
}

func (e *explorerHandler) rename(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(w, r); err != nil {
		e.redirectMutationError(w, r, http.StatusBadRequest, "Invalid form")
		return
	}
	current, err := e.mutations.Rename(r.FormValue("old"), r.FormValue("new"))
	switch {
	case errors.Is(err, errRootMutation):
		e.redirectMutationError(w, r, http.StatusBadRequest, "Cannot rename the root directory")
	case errors.Is(err, errInvalidName):
		e.redirectMutationError(w, r, http.StatusBadRequest, "Invalid name")
	case errors.Is(err, errEntryExists):
		e.redirectMutationError(w, r, http.StatusConflict, "An entry with that name already exists")
	case errors.Is(err, errRenameEntry):
		e.redirectMutationError(w, r, http.StatusConflict, "Unable to rename entry")
	case err != nil:
		e.redirectMutationPathError(w, r, err)
	default:
		e.redirectToDirectory(w, r, current)
	}
}

func (e *explorerHandler) delete(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(w, r); err != nil {
		e.redirectMutationError(w, r, http.StatusBadRequest, "Invalid form")
		return
	}
	current, err := e.mutations.Delete(r.FormValue("path"))
	switch {
	case errors.Is(err, errRootMutation):
		e.redirectMutationError(w, r, http.StatusBadRequest, "Cannot delete the root directory")
	case errors.Is(err, errDeleteEntry):
		e.redirectMutationError(w, r, http.StatusConflict, "Unable to delete entry")
	case err != nil:
		e.redirectMutationPathError(w, r, err)
	default:
		e.redirectToDirectory(w, r, current)
	}
}

func (e *explorerHandler) upload(w http.ResponseWriter, r *http.Request) {
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
	switch {
	case errors.Is(err, errInvalidName):
		e.redirectMutationError(w, r, http.StatusBadRequest, "Invalid file name")
	case errors.Is(err, errOpenUpload):
		e.redirectMutationError(w, r, http.StatusConflict, "Unable to save upload")
	case errors.Is(err, errUploadTooLarge):
		e.redirectMutationError(w, r, http.StatusBadRequest, "Invalid upload")
	case errors.Is(err, errSaveUpload):
		e.redirectMutationError(w, r, http.StatusInternalServerError, "Unable to save upload")
	case err != nil:
		e.redirectMutationPathError(w, r, err)
	default:
		e.redirectToDirectory(w, r, current)
	}
}

func (e *explorerHandler) redirectToDirectory(w http.ResponseWriter, r *http.Request, current string) {
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

func (e *explorerHandler) pathError(w http.ResponseWriter, err error) {
	if errors.Is(err, errForbidden) {
		e.renderError(w, http.StatusForbidden, "Path is outside the configured root or contains a symbolic link")
		return
	}
	if errors.Is(err, errInvalidPath) {
		e.renderError(w, http.StatusBadRequest, "Invalid path")
		return
	}
	if os.IsNotExist(err) {
		e.renderError(w, http.StatusNotFound, "Path not found")
		return
	}
	e.renderError(w, http.StatusBadRequest, "Invalid path")
}

func (e *explorerHandler) redirectMutationError(w http.ResponseWriter, r *http.Request, status int, message string) {
	parent := r.FormValue("parent")
	if parent == "" {
		target := r.FormValue("old")
		if target == "" {
			target = r.FormValue("path")
		}
		if target != "" {
			parent = path.Dir(strings.ReplaceAll(target, `\`, "/"))
			if parent == "." {
				parent = ""
			}
		}
	}
	_, current, err := e.filesystem.ResolveParent(parent)
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

func (e *explorerHandler) redirectMutationPathError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, errForbidden) {
		e.redirectMutationError(w, r, http.StatusForbidden, "Path is outside the configured root or contains a symbolic link")
		return
	}
	if errors.Is(err, errInvalidPath) {
		e.redirectMutationError(w, r, http.StatusBadRequest, "Invalid path")
		return
	}
	if os.IsNotExist(err) {
		e.redirectMutationError(w, r, http.StatusNotFound, "Path not found")
		return
	}
	e.redirectMutationError(w, r, http.StatusBadRequest, "Invalid path")
}

func (e *explorerHandler) renderError(w http.ResponseWriter, status int, message string) {
	e.writePage(w, status, e.renderer.Error(message))
}

func (e *explorerHandler) writePage(w http.ResponseWriter, status int, data pageData) {
	e.renderer.WritePage(w, status, data)
}
