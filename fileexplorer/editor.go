package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

func (e *explorerHandler) editorPage(w http.ResponseWriter, r *http.Request) {
	target, err := e.resolve(r.PathValue("path"))
	if err != nil {
		e.pathError(w, err)
		return
	}
	rel := e.relativePath(target)
	file, err := e.rootFS.Open(rel)
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
	parent := filepath.ToSlash(filepath.Dir(rel))
	if parent == "." {
		parent = ""
	}
	cancelURL := parentURL(parent)
	if cancelURL == "" {
		cancelURL = "/"
	}
	data := pageData{
		Editor:      true,
		EditorPath:  rel,
		EditorURL:   editorURL(rel),
		Content:     string(content),
		ParentURL:   cancelURL,
		Breadcrumbs: makeBreadcrumbs(parent),
		Saved:       r.URL.Query().Get("saved") == "1",
	}
	e.writePage(w, http.StatusOK, data)
}

func (e *explorerHandler) saveFile(w http.ResponseWriter, r *http.Request) {
	target, err := e.resolve(r.PathValue("path"))
	if err != nil {
		e.pathError(w, err)
		return
	}
	rel := e.relativePath(target)
	file, err := e.rootFS.Open(rel)
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
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			e.renderError(w, http.StatusRequestEntityTooLarge, "Editor content is too large (maximum 1 MiB)")
			return
		}
		e.renderError(w, http.StatusBadRequest, "Invalid editor submission")
		return
	}
	content := r.FormValue("content")
	if len(content) > maxEditorSize {
		e.renderError(w, http.StatusRequestEntityTooLarge, "Editor content is too large (maximum 1 MiB)")
		return
	}
	if !utf8.ValidString(content) || strings.IndexByte(content, 0) >= 0 {
		e.renderError(w, http.StatusUnsupportedMediaType, "The editor only saves UTF-8 text files")
		return
	}

	temp, err := randomTempName(path.Dir(rel))
	if err != nil {
		e.renderError(w, http.StatusInternalServerError, "Unable to prepare file save")
		return
	}
	out, err := e.rootFS.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
	if err != nil {
		e.renderError(w, http.StatusInternalServerError, "Unable to prepare file save")
		return
	}
	if err := out.Chmod(info.Mode().Perm()); err != nil {
		_ = out.Close()
		_ = e.rootFS.Remove(temp)
		e.renderError(w, http.StatusInternalServerError, "Unable to preserve file permissions")
		return
	}
	removeTemp := true
	defer func() {
		_ = out.Close()
		if removeTemp {
			_ = e.rootFS.Remove(temp)
		}
	}()
	if _, err := io.WriteString(out, content); err != nil {
		e.renderError(w, http.StatusInternalServerError, "Unable to save file")
		return
	}
	if err := out.Sync(); err != nil {
		e.renderError(w, http.StatusInternalServerError, "Unable to save file")
		return
	}
	if err := out.Close(); err != nil {
		e.renderError(w, http.StatusInternalServerError, "Unable to save file")
		return
	}
	if err := e.rootFS.Rename(temp, rel); err != nil {
		e.renderError(w, http.StatusInternalServerError, "Unable to replace file")
		return
	}
	removeTemp = false
	http.Redirect(w, r, "/edit/"+escapedPath(rel)+"?saved=1", http.StatusSeeOther)
}

func editorURL(rel string) string {
	return "/edit/" + escapedPath(rel)
}

func randomTempName(parent string) (string, error) {
	var suffix [12]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", err
	}
	name := ".guava-save-" + hex.EncodeToString(suffix[:]) + ".tmp"
	if parent == "." {
		return name, nil
	}
	return path.Join(parent, name), nil
}

func (e *explorerHandler) createFile(w http.ResponseWriter, r *http.Request) {
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
		e.renderError(w, http.StatusBadRequest, "Invalid file name")
		return
	}
	parentRel := e.relativePath(parent)
	rel := path.Join(parentRel, name)
	file, err := e.rootFS.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		e.renderError(w, http.StatusConflict, "Unable to create file")
		return
	}
	if err := file.Close(); err != nil {
		e.renderError(w, http.StatusInternalServerError, "Unable to finish creating file")
		return
	}
	e.redirectToDirectory(w, r, current)
}

func (e *explorerHandler) confirmDeletePage(w http.ResponseWriter, r *http.Request) {
	target, err := e.resolve(r.PathValue("path"))
	if err != nil {
		e.pathError(w, err)
		return
	}
	if target == e.root {
		e.renderError(w, http.StatusBadRequest, "Cannot delete the root directory")
		return
	}
	info, err := e.rootFS.Lstat(e.relativePath(target))
	if err != nil {
		e.pathError(w, err)
		return
	}
	if info.Mode()&os.ModeSymlink != 0 {
		e.pathError(w, errForbidden)
		return
	}
	parent, err := filepath.Rel(e.root, filepath.Dir(target))
	if err != nil {
		e.renderError(w, http.StatusInternalServerError, "Unable to identify parent directory")
		return
	}
	e.writePage(w, http.StatusOK, pageData{
		ConfirmDelete: true,
		DeletePath:    filepath.ToSlash(e.relativePath(target)),
		DeleteName:    filepath.Base(target),
		ReturnURL:     parentURL(filepath.ToSlash(parent)),
	})
}

func (e *explorerHandler) relativePath(target string) string {
	rel, err := filepath.Rel(e.root, target)
	if err != nil || rel == "." {
		return ""
	}
	return filepath.ToSlash(rel)
}
