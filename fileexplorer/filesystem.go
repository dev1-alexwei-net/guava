package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

var (
	errInvalidPath = errors.New("invalid path")
	errForbidden   = errors.New("path escapes configured root")
)

// rootedFS owns the configured root and the path policy for every filesystem
// operation used by the file explorer.
type rootedFS struct {
	path string
	root *os.Root
}

func openRootedFS(root string) (*rootedFS, error) {
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
	dir, err := os.OpenRoot(resolvedRoot)
	if err != nil {
		return nil, fmt.Errorf("open root: %w", err)
	}
	return &rootedFS{path: resolvedRoot, root: dir}, nil
}

func (f *rootedFS) Path() string { return f.path }

func (f *rootedFS) Close() error { return f.root.Close() }

func (f *rootedFS) FS() fs.FS { return rootedFSView{root: f} }

type rootedFSView struct{ root *rootedFS }

func (v rootedFSView) Open(name string) (fs.File, error) { return v.root.Open(name) }

// Resolve validates a request path and returns its canonical absolute path.
func (f *rootedFS) Resolve(rel string) (string, error) {
	clean, err := f.clean(rel)
	if err != nil {
		return "", err
	}
	return filepath.Join(f.path, filepath.FromSlash(clean)), nil
}

func (f *rootedFS) Relative(target string) string {
	rel, err := filepath.Rel(f.path, target)
	if err != nil || rel == "." {
		return ""
	}
	return filepath.ToSlash(rel)
}

func (f *rootedFS) ResolveParent(rel string) (string, string, error) {
	dir, err := f.Resolve(rel)
	if err != nil {
		return "", "", err
	}
	info, err := f.Stat(f.Relative(dir))
	if err != nil {
		return "", "", err
	}
	if !info.IsDir() {
		return "", "", errInvalidPath
	}
	return dir, f.Relative(dir), nil
}

func (f *rootedFS) clean(rel string) (string, error) {
	if strings.IndexByte(rel, 0) >= 0 || filepath.IsAbs(rel) || strings.HasPrefix(rel, "/") || strings.HasPrefix(rel, `\`) {
		return "", errInvalidPath
	}
	if len(rel) >= 2 && rel[1] == ':' {
		return "", errInvalidPath
	}
	normalized := strings.ReplaceAll(rel, `\`, "/")
	components := strings.Split(normalized, "/")
	for _, component := range components {
		if component == ".." {
			return "", errInvalidPath
		}
	}
	clean := path.Clean(normalized)
	if clean == "." {
		return "", nil
	}
	current := ""
	parts := strings.Split(clean, "/")
	for i, component := range parts {
		current = path.Join(current, component)
		info, err := f.root.Lstat(f.name(current))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", errForbidden
		}
		if i < len(parts)-1 && !info.IsDir() {
			return "", errInvalidPath
		}
	}
	return clean, nil
}

func (f *rootedFS) name(rel string) string {
	if rel == "" {
		return "."
	}
	return rel
}

func (f *rootedFS) Open(name string) (*os.File, error) {
	rel, err := f.clean(name)
	if err != nil {
		return nil, err
	}
	return f.root.Open(f.name(rel))
}

func (f *rootedFS) Stat(name string) (os.FileInfo, error) {
	rel, err := f.clean(name)
	if err != nil {
		return nil, err
	}
	return f.root.Stat(f.name(rel))
}

func (f *rootedFS) Lstat(name string) (os.FileInfo, error) {
	rel, err := f.clean(name)
	if err != nil {
		return nil, err
	}
	return f.root.Lstat(f.name(rel))
}

func (f *rootedFS) OpenFile(name string, flag int, perm os.FileMode) (*os.File, error) {
	rel, err := f.clean(name)
	if err != nil {
		return nil, err
	}
	return f.root.OpenFile(f.name(rel), flag, perm)
}

func (f *rootedFS) Mkdir(name string, perm os.FileMode) error {
	rel, err := f.clean(name)
	if err != nil {
		return err
	}
	return f.root.Mkdir(f.name(rel), perm)
}

func (f *rootedFS) Rename(oldname, newname string) error {
	oldRel, err := f.clean(oldname)
	if err != nil {
		return err
	}
	newRel, err := f.clean(newname)
	if err != nil {
		return err
	}
	return f.root.Rename(f.name(oldRel), f.name(newRel))
}

func (f *rootedFS) Remove(name string) error {
	rel, err := f.clean(name)
	if err != nil {
		return err
	}
	return f.root.Remove(f.name(rel))
}

func (f *rootedFS) RemoveAll(name string) error {
	rel, err := f.clean(name)
	if err != nil {
		return err
	}
	return f.root.RemoveAll(f.name(rel))
}

type interactionError struct {
	status  int
	message string
}

func (e *interactionError) Error() string { return e.message }

func newInteractionError(status int, message string) *interactionError {
	return &interactionError{status: status, message: message}
}

type mutations struct {
	filesystem *rootedFS
}

func newMutations(filesystem *rootedFS) *mutations {
	return &mutations{filesystem: filesystem}
}

func (m *mutations) MakeDirectory(parent, name string) (string, error) {
	dir, current, err := m.filesystem.ResolveParent(parent)
	if err != nil {
		return "", err
	}
	if !validName(name) {
		return "", newInteractionError(http.StatusBadRequest, "Invalid directory name")
	}
	if err := m.filesystem.Mkdir(path.Join(m.filesystem.Relative(dir), name), 0o755); err != nil {
		return "", newInteractionError(http.StatusConflict, "Unable to create directory")
	}
	return current, nil
}

func (m *mutations) CreateFile(parent, name string) (string, error) {
	dir, current, err := m.filesystem.ResolveParent(parent)
	if err != nil {
		return "", err
	}
	if !validName(name) {
		return "", newInteractionError(http.StatusBadRequest, "Invalid file name")
	}
	file, err := m.filesystem.OpenFile(path.Join(m.filesystem.Relative(dir), name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return "", newInteractionError(http.StatusConflict, "Unable to create file")
	}
	if err := file.Close(); err != nil {
		return "", newInteractionError(http.StatusInternalServerError, "Unable to finish creating file")
	}
	return current, nil
}

func (m *mutations) Rename(old, name string) (string, error) {
	target, err := m.filesystem.Resolve(old)
	if err != nil {
		return "", err
	}
	if target == m.filesystem.Path() {
		return "", newInteractionError(http.StatusBadRequest, "Cannot rename the root directory")
	}
	if !validName(name) {
		return "", newInteractionError(http.StatusBadRequest, "Invalid name")
	}
	oldRel := m.filesystem.Relative(target)
	parent := path.Dir(oldRel)
	if parent == "." {
		parent = ""
	}
	newRel := path.Join(parent, name)
	if _, err := m.filesystem.Resolve(newRel); err != nil {
		return "", err
	}
	_, err = m.filesystem.Lstat(newRel)
	if err == nil {
		return "", newInteractionError(http.StatusConflict, "An entry with that name already exists")
	}
	if !os.IsNotExist(err) {
		return "", err
	}
	if err := m.filesystem.Rename(oldRel, newRel); err != nil {
		return "", newInteractionError(http.StatusConflict, "Unable to rename entry")
	}
	return parent, nil
}

func (m *mutations) Delete(rel string) (string, error) {
	target, err := m.filesystem.Resolve(rel)
	if err != nil {
		return "", err
	}
	if target == m.filesystem.Path() {
		return "", newInteractionError(http.StatusBadRequest, "Cannot delete the root directory")
	}
	rel = m.filesystem.Relative(target)
	parent := path.Dir(rel)
	if parent == "." {
		parent = ""
	}
	if err := m.filesystem.RemoveAll(rel); err != nil {
		return "", newInteractionError(http.StatusConflict, "Unable to delete entry")
	}
	return parent, nil
}

func (m *mutations) Upload(parent, name string, source io.Reader) (string, error) {
	dir, current, err := m.filesystem.ResolveParent(parent)
	if err != nil {
		return "", err
	}
	if !validName(name) {
		return "", newInteractionError(http.StatusBadRequest, "Invalid file name")
	}
	dest := path.Join(m.filesystem.Relative(dir), name)
	if _, err := m.filesystem.Resolve(dest); err != nil {
		return "", err
	}
	out, err := m.filesystem.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return "", newInteractionError(http.StatusConflict, "Unable to save upload")
	}
	size, copyErr := io.Copy(out, io.LimitReader(source, maxUploadSize+1))
	closeErr := out.Close()
	if copyErr == nil && closeErr == nil && size <= maxUploadSize {
		return current, nil
	}
	_ = m.filesystem.Remove(dest)
	if size > maxUploadSize {
		return "", newInteractionError(http.StatusBadRequest, "Invalid upload")
	}
	return "", newInteractionError(http.StatusInternalServerError, "Unable to save upload")
}

// SaveText atomically replaces a text file while preserving its permission bits.
func (m *mutations) SaveText(rel, content string, mode os.FileMode) error {
	if len(content) > maxEditorSize {
		return newInteractionError(http.StatusRequestEntityTooLarge, "Editor content is too large (maximum 1 MiB)")
	}
	if !utf8.ValidString(content) || strings.IndexByte(content, 0) >= 0 {
		return newInteractionError(http.StatusUnsupportedMediaType, "The editor only saves UTF-8 text files")
	}
	temp, err := randomTempName(path.Dir(rel))
	if err != nil {
		return newInteractionError(http.StatusInternalServerError, "Unable to prepare file save")
	}
	out, err := m.filesystem.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return newInteractionError(http.StatusInternalServerError, "Unable to prepare file save")
	}
	if err := out.Chmod(mode); err != nil {
		_ = out.Close()
		_ = m.filesystem.Remove(temp)
		return newInteractionError(http.StatusInternalServerError, "Unable to preserve file permissions")
	}
	removeTemp := true
	defer func() {
		_ = out.Close()
		if removeTemp {
			_ = m.filesystem.Remove(temp)
		}
	}()
	if _, err := io.WriteString(out, content); err != nil {
		return newInteractionError(http.StatusInternalServerError, "Unable to save file")
	}
	if err := out.Sync(); err != nil {
		return newInteractionError(http.StatusInternalServerError, "Unable to save file")
	}
	if err := out.Close(); err != nil {
		return newInteractionError(http.StatusInternalServerError, "Unable to save file")
	}
	if err := m.filesystem.Rename(temp, rel); err != nil {
		return newInteractionError(http.StatusInternalServerError, "Unable to replace file")
	}
	removeTemp = false
	return nil
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

func validName(name string) bool {
	return name != "" && name != "." && name != ".." &&
		!strings.ContainsAny(name, `/\`+"\x00") && !filepath.IsAbs(name)
}
