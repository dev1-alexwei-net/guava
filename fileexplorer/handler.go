// Package main implements the self-contained file explorer HTTP handlers.
package main

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

const maxUploadSize = 32 << 20

var (
	errInvalidPath = errors.New("invalid path")
	errForbidden   = errors.New("path escapes configured root")
)

type explorerHandler struct {
	root  string
	store *Store
	mu    sync.Mutex
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

	e := &explorerHandler{root: resolvedRoot, store: store}
	mux := http.NewServeMux()
	mux.Handle("GET /{$}", e.serialized(e.rootPage))
	mux.Handle("GET /d/{$}", e.serialized(e.rootPage))
	mux.Handle("GET /d/{path...}", e.serialized(e.directoryPage))
	mux.Handle("GET /f/{path...}", e.serialized(e.filePage))
	for _, method := range []string{http.MethodPost, http.MethodPut} {
		mux.Handle(method+" /mkdir", e.serialized(e.mkdir))
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
	e.renderDirectory(w, "")
}

func (e *explorerHandler) directoryPage(w http.ResponseWriter, r *http.Request) {
	e.renderDirectory(w, r.PathValue("path"))
}

func (e *explorerHandler) filePage(w http.ResponseWriter, r *http.Request) {
	target, err := e.resolve(r.PathValue("path"))
	if err != nil {
		e.pathError(w, err)
		return
	}
	info, err := os.Stat(target)
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
	file, err := os.Open(target)
	if err != nil {
		e.renderError(w, http.StatusInternalServerError, "Unable to read file")
		return
	}
	defer file.Close()
	http.ServeContent(w, r, info.Name(), info.ModTime(), file)
}

func (e *explorerHandler) renderDirectory(w http.ResponseWriter, requested string) {
	dir, err := e.resolve(requested)
	if err != nil {
		e.pathError(w, err)
		return
	}
	info, err := os.Stat(dir)
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
			Name:  entry.Name,
			Path:  entry.Path,
			URL:   itemURL(entry.Path, entry.IsDir),
			IsDir: entry.IsDir,
			Size:  entry.Size,
		})
	}
	e.writePage(w, http.StatusOK, data)
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
	if err := os.Mkdir(filepath.Join(parent, name), 0o755); err != nil {
		e.renderError(w, http.StatusConflict, "Unable to create directory")
		return
	}
	e.renderDirectory(w, current)
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
	if err := os.Rename(oldPath, newPath); err != nil {
		e.renderError(w, http.StatusConflict, "Unable to rename entry")
		return
	}
	current, err := filepath.Rel(e.root, oldParent)
	if err != nil {
		e.renderError(w, http.StatusInternalServerError, "Unable to read directory")
		return
	}
	e.renderDirectory(w, filepath.ToSlash(current))
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
	if err := os.RemoveAll(target); err != nil {
		e.renderError(w, http.StatusConflict, "Unable to delete entry")
		return
	}
	current, err := filepath.Rel(e.root, parent)
	if err != nil {
		e.renderError(w, http.StatusInternalServerError, "Unable to read directory")
		return
	}
	e.renderDirectory(w, filepath.ToSlash(current))
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
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		e.renderError(w, http.StatusConflict, "Unable to save upload")
		return
	}
	_, copyErr := io.Copy(out, io.LimitReader(file, maxUploadSize+1))
	closeErr := out.Close()
	if copyErr != nil || closeErr != nil {
		_ = os.Remove(dest)
		e.renderError(w, http.StatusInternalServerError, "Unable to save upload")
		return
	}
	e.renderDirectory(w, current)
}

func (e *explorerHandler) resolveParent(rel string) (string, string, error) {
	dir, err := e.resolve(rel)
	if err != nil {
		return "", "", err
	}
	info, err := os.Stat(dir)
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

// resolve rejects lexical traversal and symlink targets outside the configured root.
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
	current := e.root
	if clean == "" {
		return current, nil
	}
	for _, component := range strings.Split(clean, "/") {
		if component == "" || component == "." {
			continue
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			resolved, err := filepath.EvalSymlinks(current)
			if err != nil {
				return "", errForbidden
			}
			if !insideRoot(e.root, resolved) {
				return "", errForbidden
			}
			current = resolved
		}
	}
	if !insideRoot(e.root, current) {
		return "", errForbidden
	}
	return current, nil
}

func insideRoot(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
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
		e.renderError(w, http.StatusForbidden, "Path is outside the configured root")
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

func itemURL(rel string, isDir bool) string {
	prefix := "/f/"
	if isDir {
		prefix = "/d/"
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return prefix + strings.Join(parts, "/")
}
