package main

import (
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"unicode/utf8"
)

func (e *explorerHandler) editorPage(w http.ResponseWriter, r *http.Request) {
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

func (e *explorerHandler) saveFile(w http.ResponseWriter, r *http.Request) {
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
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			e.renderError(w, http.StatusRequestEntityTooLarge, "Editor content is too large (maximum 1 MiB)")
			return
		}
		e.renderError(w, http.StatusBadRequest, "Invalid editor submission")
		return
	}
	content := r.FormValue("content")
	if err := e.mutations.SaveText(rel, content, info.Mode().Perm()); err != nil {
		switch {
		case errors.Is(err, errEditorContentTooLarge):
			e.renderError(w, http.StatusRequestEntityTooLarge, "Editor content is too large (maximum 1 MiB)")
		case errors.Is(err, errUnsupportedText):
			e.renderError(w, http.StatusUnsupportedMediaType, "The editor only saves UTF-8 text files")
		case errors.Is(err, errPrepareFileSave):
			e.renderError(w, http.StatusInternalServerError, "Unable to prepare file save")
		case errors.Is(err, errPreserveFileMode):
			e.renderError(w, http.StatusInternalServerError, "Unable to preserve file permissions")
		case errors.Is(err, errReplaceFile):
			e.renderError(w, http.StatusInternalServerError, "Unable to replace file")
		default:
			e.renderError(w, http.StatusInternalServerError, "Unable to save file")
		}
		return
	}
	http.Redirect(w, r, e.renderer.EditorURL(rel)+"?saved=1", http.StatusSeeOther)
}

func (e *explorerHandler) createFile(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(w, r); err != nil {
		e.redirectMutationError(w, r, http.StatusBadRequest, "Invalid form")
		return
	}
	current, err := e.mutations.CreateFile(r.FormValue("parent"), r.FormValue("name"))
	switch {
	case errors.Is(err, errInvalidName):
		e.redirectMutationError(w, r, http.StatusBadRequest, "Invalid file name")
	case errors.Is(err, errCreateFile):
		e.redirectMutationError(w, r, http.StatusConflict, "Unable to create file")
	case errors.Is(err, errFinishCreateFile):
		e.redirectMutationError(w, r, http.StatusInternalServerError, "Unable to finish creating file")
	case err != nil:
		e.redirectMutationPathError(w, r, err)
	default:
		e.redirectToDirectory(w, r, current)
	}
}

func (e *explorerHandler) confirmDeletePage(w http.ResponseWriter, r *http.Request) {
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
