package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
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
