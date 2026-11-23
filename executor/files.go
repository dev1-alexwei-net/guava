package executor

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

type rootedFileReader struct {
	root *os.Root
}

func openRootedFileReader(rootPath string) (*rootedFileReader, error) {
	absoluteRoot, err := filepath.Abs(rootPath)
	if err != nil {
		return nil, fmt.Errorf("resolve filesystem root: %w", err)
	}
	resolvedRoot, err := filepath.EvalSymlinks(absoluteRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve filesystem root: %w", err)
	}
	info, err := os.Stat(resolvedRoot)
	if err != nil {
		return nil, fmt.Errorf("inspect filesystem root: %w", err)
	}
	if !info.IsDir() {
		return nil, errors.New("filesystem root is not a directory")
	}
	root, err := os.OpenRoot(resolvedRoot)
	if err != nil {
		return nil, fmt.Errorf("open filesystem root: %w", err)
	}
	return &rootedFileReader{root: root}, nil
}

func (r *rootedFileReader) read(file indexedFile) ([]byte, error) {
	if r == nil || r.root == nil {
		return nil, errors.New("rooted file reader is not open")
	}
	if err := validateIndexedPath(file.path); err != nil {
		return nil, fmt.Errorf("invalid indexed file path %q: %w", file.path, err)
	}

	current, err := r.root.Open(file.path)
	if err != nil {
		return nil, fmt.Errorf("open indexed file %q: %w", file.path, err)
	}
	defer current.Close()

	info, err := current.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspect indexed file %q: %w", file.path, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("indexed path %q is not a regular file", file.path)
	}

	content, err := io.ReadAll(current)
	if err != nil {
		return nil, fmt.Errorf("read indexed file %q: %w", file.path, err)
	}
	return content, nil
}

func validateIndexedPath(name string) error {
	if name == "" || name == "." || strings.IndexByte(name, 0) >= 0 || !fs.ValidPath(name) {
		return errors.New("path must be a non-empty relative file path")
	}
	filePath := filepath.FromSlash(name)
	if filepath.IsAbs(filePath) || filepath.VolumeName(filePath) != "" {
		return errors.New("path must be relative to the filesystem root")
	}
	return nil
}

func (r *rootedFileReader) close() error {
	if r == nil || r.root == nil {
		return nil
	}
	return r.root.Close()
}
