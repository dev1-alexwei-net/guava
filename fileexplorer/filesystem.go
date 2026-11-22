package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
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
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
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

var (
	errInvalidName           = errors.New("invalid name")
	errRootMutation          = errors.New("cannot mutate root")
	errCreateDirectory       = errors.New("unable to create directory")
	errCreateFile            = errors.New("unable to create file")
	errFinishCreateFile      = errors.New("unable to finish creating file")
	errEntryExists           = errors.New("entry already exists")
	errRenameEntry           = errors.New("unable to rename entry")
	errDeleteEntry           = errors.New("unable to delete entry")
	errOpenUpload            = errors.New("unable to save upload")
	errSaveUpload            = errors.New("unable to save upload")
	errUploadTooLarge        = errors.New("upload too large")
	errEditorContentTooLarge = errors.New("editor content too large")
	errUnsupportedText       = errors.New("unsupported editor content")
	errPrepareFileSave       = errors.New("unable to prepare file save")
	errPreserveFileMode      = errors.New("unable to preserve file permissions")
	errSaveFile              = errors.New("unable to save file")
	errReplaceFile           = errors.New("unable to replace file")
)

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
		return "", errInvalidName
	}
	if err := m.filesystem.Mkdir(path.Join(m.filesystem.Relative(dir), name), 0o755); err != nil {
		return "", errCreateDirectory
	}
	return current, nil
}

func (m *mutations) CreateFile(parent, name string) (string, error) {
	dir, current, err := m.filesystem.ResolveParent(parent)
	if err != nil {
		return "", err
	}
	if !validName(name) {
		return "", errInvalidName
	}
	file, err := m.filesystem.OpenFile(path.Join(m.filesystem.Relative(dir), name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return "", errCreateFile
	}
	if err := file.Close(); err != nil {
		return "", errFinishCreateFile
	}
	return current, nil
}

func (m *mutations) Rename(old, name string) (string, error) {
	target, err := m.filesystem.Resolve(old)
	if err != nil {
		return "", err
	}
	if target == m.filesystem.Path() {
		return "", errRootMutation
	}
	if !validName(name) {
		return "", errInvalidName
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
	if _, err := m.filesystem.Lstat(newRel); err == nil {
		return "", errEntryExists
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if err := m.filesystem.Rename(oldRel, newRel); err != nil {
		return "", errRenameEntry
	}
	return parent, nil
}

func (m *mutations) Delete(rel string) (string, error) {
	target, err := m.filesystem.Resolve(rel)
	if err != nil {
		return "", err
	}
	if target == m.filesystem.Path() {
		return "", errRootMutation
	}
	rel = m.filesystem.Relative(target)
	parent := path.Dir(rel)
	if parent == "." {
		parent = ""
	}
	if err := m.filesystem.RemoveAll(rel); err != nil {
		return "", errDeleteEntry
	}
	return parent, nil
}

func (m *mutations) Upload(parent, name string, source io.Reader) (string, error) {
	dir, current, err := m.filesystem.ResolveParent(parent)
	if err != nil {
		return "", err
	}
	if !validName(name) {
		return "", errInvalidName
	}
	dest := path.Join(m.filesystem.Relative(dir), name)
	if _, err := m.filesystem.Resolve(dest); err != nil {
		return "", err
	}
	out, err := m.filesystem.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return "", errOpenUpload
	}
	size, copyErr := io.Copy(out, io.LimitReader(source, maxUploadSize+1))
	closeErr := out.Close()
	if copyErr != nil || closeErr != nil || size > maxUploadSize {
		_ = m.filesystem.Remove(dest)
		if size > maxUploadSize {
			return "", errUploadTooLarge
		}
		return "", errSaveUpload
	}
	return current, nil
}

// SaveText atomically replaces a text file while preserving its permission bits.
func (m *mutations) SaveText(rel, content string, mode os.FileMode) error {
	if len(content) > maxEditorSize {
		return errEditorContentTooLarge
	}
	if !utf8.ValidString(content) || strings.IndexByte(content, 0) >= 0 {
		return errUnsupportedText
	}
	temp, err := randomTempName(path.Dir(rel))
	if err != nil {
		return errPrepareFileSave
	}
	out, err := m.filesystem.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return errPrepareFileSave
	}
	if err := out.Chmod(mode); err != nil {
		_ = out.Close()
		_ = m.filesystem.Remove(temp)
		return errPreserveFileMode
	}
	removeTemp := true
	defer func() {
		_ = out.Close()
		if removeTemp {
			_ = m.filesystem.Remove(temp)
		}
	}()
	if _, err := io.WriteString(out, content); err != nil {
		return errSaveFile
	}
	if err := out.Sync(); err != nil {
		return errSaveFile
	}
	if err := out.Close(); err != nil {
		return errSaveFile
	}
	if err := m.filesystem.Rename(temp, rel); err != nil {
		return errReplaceFile
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
