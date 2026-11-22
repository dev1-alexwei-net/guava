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
)

const maxUploadSize = 32 << 20
const maxEditorSize = 1 << 20
const maxPreviewSize = 512 << 10

var (
	errInvalidPath = errors.New("invalid path")
	errForbidden   = errors.New("path escapes configured root")
)

type explorerHandler struct {
	root   string
	rootFS *os.Root
	store  *Store
	mu     sync.Mutex
}

// newHandler creates the file-explorer routes for an existing root and store.
func newHandler(root string, store *Store) (http.Handler, error) {
	if store == nil {
		return nil, errors.New("store is required")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	resolvedRoot, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve root: %w", err)
	}
	info, err := os.Stat(resolvedRoot)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("root must be a directory")
	}

	rootFS, err := os.OpenRoot(resolvedRoot)
	if err != nil {
		return nil, fmt.Errorf("open root: %w", err)
	}
	e := &explorerHandler{root: resolvedRoot, rootFS: rootFS, store: store}
	mux := http.NewServeMux()
	mux.Handle("GET /{$}", e.serialized(e.rootPage))
	mux.Handle("GET /d/{$}", e.serialized(e.rootPage))
	mux.Handle("GET /d/{path...}", e.serialized(e.directoryPage))
	mux.Handle("GET /f/{path...}", e.serialized(e.filePage))
	mux.Handle("GET /edit/{path...}", e.serialized(e.editorPage))
	mux.Handle("POST /edit/{path...}", e.serialized(e.saveFile))
	mux.Handle("GET /confirm-delete/{path...}", e.serialized(e.confirmDeletePage))
	mux.Handle("GET /preview/{path...}", e.serialized(e.previewPage))
	mux.HandleFunc("GET /static/bootstrap.min.css", bootstrapStylesheet)
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
	e.renderDirectory(w, "", "")
}

func (e *explorerHandler) directoryPage(w http.ResponseWriter, r *http.Request) {
	e.renderDirectory(w, r.PathValue("path"), "")
}

func (e *explorerHandler) filePage(w http.ResponseWriter, r *http.Request) {
	target, err := e.resolve(r.PathValue("path"))
	if err != nil {
		e.pathError(w, err)
		return
	}
	info, err := e.rootFS.Stat(e.rootName(e.relativePath(target)))
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
	file, err := e.rootFS.Open(e.rootName(e.relativePath(target)))
	if err != nil {
		e.renderError(w, http.StatusInternalServerError, "Unable to read file")
		return
	}
	defer file.Close()
	http.ServeContent(w, r, info.Name(), info.ModTime(), file)
}

func (e *explorerHandler) renderDirectory(w http.ResponseWriter, requested string, previewRel string) {
	dir, err := e.resolve(requested)
	if err != nil {
		e.pathError(w, err)
		return
	}
	info, err := e.rootFS.Stat(e.rootName(e.relativePath(dir)))
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
	if _, err := e.store.Sync(e.root); err != nil {
		e.renderError(w, http.StatusInternalServerError, "Unable to synchronize directory")
		return
	}

	relative, err := filepath.Rel(e.root, dir)
	if err != nil {
		e.renderError(w, http.StatusInternalServerError, "Unable to read directory")
		return
	}
	current := filepath.ToSlash(relative)
	if current == "." {
		current = ""
	}
	entries, err := e.store.List(current)
	if err != nil {
		e.renderError(w, http.StatusInternalServerError, "Unable to list directory")
		return
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir != entries[j].IsDir {
			return entries[i].IsDir
		}
		return entries[i].Name < entries[j].Name
	})

	data := pageData{
		Current:     current,
		Breadcrumbs: makeBreadcrumbs(current),
		ParentURL:   parentURL(current),
		Items:       make([]pageItem, 0, len(entries)),
	}
	for _, entry := range entries {
		data.Items = append(data.Items, pageItem{
			Name:        entry.Name,
			Path:        entry.Path,
			URL:         itemURL(entry.Path, entry.IsDir),
			EditURL:     editorURL(entry.Path),
			DownloadURL: itemURL(entry.Path, false) + "?download=1",
			DeleteURL:   "/confirm-delete/" + escapedPath(entry.Path),
			IsDir:       entry.IsDir,
			Size:        entry.Size,
		})
	}
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
	target, err := e.resolve(rel)
	if err != nil {
		e.pathError(w, err)
		return
	}
	info, err := e.rootFS.Stat(e.rootName(e.relativePath(target)))
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
	e.renderDirectory(w, parent, rel)
}

// applyPreview fills the preview pane for the file selected in the URL. The
// file was already validated by previewPage; if it is missing at render time
// the pane is left empty rather than showing stale content.
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
	info, err := e.rootFS.Stat(e.rootName(rel))
	if err != nil || !info.Mode().IsRegular() {
		return
	}
	data.PreviewPath = rel
	data.PreviewName = filepath.Base(rel)
	data.PreviewReturnURL = itemURL(parent, true)
	if info.Size() > maxPreviewSize {
		data.PreviewNote = fmt.Sprintf("This file is too large to preview (maximum %d KiB). Download it instead.", maxPreviewSize>>10)
		return
	}
	file, err := e.rootFS.Open(e.rootName(rel))
	if err != nil {
		return
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, maxPreviewSize+1))
	if err != nil {
		return
	}
	if len(content) > maxPreviewSize {
		data.PreviewNote = "This file is too large to preview. Download it instead."
		return
	}
	decoded, ok := decodePreviewText(content)
	if !ok {
		data.PreviewNote = "This file does not appear to be UTF-8 text or BOM-marked UTF-16 text, so it cannot be previewed. Download it instead."
		return
	}
	data.PreviewContent = decoded
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
		e.renderError(w, http.StatusBadRequest, "Invalid form")
		return
	}
	parent, current, err := e.resolveParent(r.FormValue("parent"))
	if err != nil {
		e.pathError(w, err)
		return
	}
	name := r.FormValue("name")
	if !validName(name) {
		e.renderError(w, http.StatusBadRequest, "Invalid directory name")
		return
	}
	if err := e.rootFS.Mkdir(path.Join(e.relativePath(parent), name), 0o755); err != nil {
		e.renderError(w, http.StatusConflict, "Unable to create directory")
		return
	}
	e.redirectToDirectory(w, r, current)
}

func (e *explorerHandler) rename(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(w, r); err != nil {
		e.renderError(w, http.StatusBadRequest, "Invalid form")
		return
	}
	oldRel := r.FormValue("old")
	oldPath, err := e.resolve(oldRel)
	if err != nil {
		e.pathError(w, err)
		return
	}
	if oldPath == e.root {
		e.renderError(w, http.StatusBadRequest, "Cannot rename the root directory")
		return
	}
	name := r.FormValue("new")
	if !validName(name) {
		e.renderError(w, http.StatusBadRequest, "Invalid name")
		return
	}
	oldParent := filepath.Dir(oldPath)
	newPath := filepath.Join(oldParent, name)
	newRel, err := filepath.Rel(e.root, newPath)
	if err != nil {
		e.pathError(w, errForbidden)
		return
	}
	if _, err := e.resolve(filepath.ToSlash(newRel)); err != nil {
		e.pathError(w, err)
		return
	}
	if _, err := e.rootFS.Lstat(filepath.ToSlash(newRel)); err == nil {
		e.renderError(w, http.StatusConflict, "An entry with that name already exists")
		return
	} else if !os.IsNotExist(err) {
		e.pathError(w, err)
		return
	}
	if err := e.rootFS.Rename(e.rootName(e.relativePath(oldPath)), e.relativePath(newPath)); err != nil {
		e.renderError(w, http.StatusConflict, "Unable to rename entry")
		return
	}
	current, err := filepath.Rel(e.root, oldParent)
	if err != nil {
		e.renderError(w, http.StatusInternalServerError, "Unable to read directory")
		return
	}
	e.redirectToDirectory(w, r, filepath.ToSlash(current))
}

func (e *explorerHandler) delete(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(w, r); err != nil {
		e.renderError(w, http.StatusBadRequest, "Invalid form")
		return
	}
	rel := r.FormValue("path")
	target, err := e.resolve(rel)
	if err != nil {
		e.pathError(w, err)
		return
	}
	if target == e.root {
		e.renderError(w, http.StatusBadRequest, "Cannot delete the root directory")
		return
	}
	parent := filepath.Dir(target)
	if err := e.rootFS.RemoveAll(e.relativePath(target)); err != nil {
		e.renderError(w, http.StatusConflict, "Unable to delete entry")
		return
	}
	current, err := filepath.Rel(e.root, parent)
	if err != nil {
		e.renderError(w, http.StatusInternalServerError, "Unable to read directory")
		return
	}
	e.redirectToDirectory(w, r, filepath.ToSlash(current))
}

func (e *explorerHandler) upload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)
	if err := r.ParseMultipartForm(maxUploadSize); err != nil {
		e.renderError(w, http.StatusBadRequest, "Invalid upload")
		return
	}
	parent, current, err := e.resolveParent(r.FormValue("parent"))
	if err != nil {
		e.pathError(w, err)
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		e.renderError(w, http.StatusBadRequest, "Upload file is required")
		return
	}
	defer file.Close()
	if !validName(header.Filename) {
		e.renderError(w, http.StatusBadRequest, "Invalid file name")
		return
	}
	dest := filepath.Join(parent, header.Filename)
	destRel, err := filepath.Rel(e.root, dest)
	if err != nil {
		e.pathError(w, errForbidden)
		return
	}
	if _, err := e.resolve(filepath.ToSlash(destRel)); err != nil {
		e.pathError(w, err)
		return
	}
	destRel = filepath.ToSlash(destRel)
	out, err := e.rootFS.OpenFile(destRel, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		e.renderError(w, http.StatusConflict, "Unable to save upload")
		return
	}
	_, copyErr := io.Copy(out, io.LimitReader(file, maxUploadSize+1))
	closeErr := out.Close()
	if copyErr != nil || closeErr != nil {
		_ = e.rootFS.Remove(destRel)
		e.renderError(w, http.StatusInternalServerError, "Unable to save upload")
		return
	}
	e.redirectToDirectory(w, r, current)
}

func (e *explorerHandler) redirectToDirectory(w http.ResponseWriter, r *http.Request, current string) {
	if current == "." {
		current = ""
	}
	target := itemURL(current, true)
	if current == "" {
		target = "/"
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func (e *explorerHandler) resolveParent(rel string) (string, string, error) {
	dir, err := e.resolve(rel)
	if err != nil {
		return "", "", err
	}
	info, err := e.rootFS.Stat(e.rootName(e.relativePath(dir)))
	if err != nil {
		return "", "", err
	}
	if !info.IsDir() {
		return "", "", errInvalidPath
	}
	current, err := filepath.Rel(e.root, dir)
	if err != nil {
		return "", "", errForbidden
	}
	current = filepath.ToSlash(current)
	if current == "." {
		current = ""
	}
	return dir, current, nil
}

// resolve rejects lexical traversal and every symlink component before returning
// the canonical absolute path used by the existing filesystem helpers.
func (e *explorerHandler) resolve(rel string) (string, error) {
	if strings.IndexByte(rel, 0) >= 0 || filepath.IsAbs(rel) || strings.HasPrefix(rel, "/") || strings.HasPrefix(rel, `\`) {
		return "", errInvalidPath
	}
	if len(rel) >= 2 && rel[1] == ':' {
		return "", errInvalidPath
	}
	normalized := strings.ReplaceAll(rel, `\`, "/")
	for _, component := range strings.Split(normalized, "/") {
		if component == ".." {
			return "", errInvalidPath
		}
	}
	clean := path.Clean(normalized)
	if clean == "." {
		clean = ""
	}
	if clean == "" {
		return e.root, nil
	}
	components := strings.Split(clean, "/")
	current := ""
	for i, component := range components {
		current = path.Join(current, component)
		info, err := e.rootFS.Lstat(current)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", errForbidden
		}
		if i < len(components)-1 && !info.IsDir() {
			return "", errInvalidPath
		}
	}
	return filepath.Join(e.root, filepath.FromSlash(clean)), nil
}

func (e *explorerHandler) rootName(rel string) string {
	if rel == "" {
		return "."
	}
	return rel
}

func validName(name string) bool {
	return name != "" && name != "." && name != ".." &&
		!strings.ContainsAny(name, `/\`+"\x00") && !filepath.IsAbs(name)
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

func (e *explorerHandler) renderError(w http.ResponseWriter, status int, message string) {
	e.writePage(w, status, pageData{Error: message})
}

func (e *explorerHandler) writePage(w http.ResponseWriter, status int, data pageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_ = renderPage(w, data)
}

func makeBreadcrumbs(current string) []breadcrumb {
	if current == "" {
		return nil
	}
	parts := strings.Split(current, "/")
	crumbs := make([]breadcrumb, 0, len(parts))
	var accumulated []string
	for _, part := range parts {
		accumulated = append(accumulated, part)
		crumbs = append(crumbs, breadcrumb{Name: part, URL: itemURL(strings.Join(accumulated, "/"), true)})
	}
	return crumbs
}

func parentURL(current string) string {
	if current == "" {
		return ""
	}
	parent := path.Dir(current)
	if parent == "." {
		return "/"
	}
	return itemURL(parent, true)
}

func escapedPath(rel string) string {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}

func itemURL(rel string, isDir bool) string {
	prefix := "/f/"
	if isDir {
		prefix = "/d/"
	}
	return prefix + escapedPath(rel)
}
