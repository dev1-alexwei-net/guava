package executor

import (
	"context"
	"errors"
	"path"
	"strings"
)

// subdirectoryLister wraps a FileLister and reports only the files under one
// folder inside the filesystem root. A path is in scope when it begins with
// the exact folder prefix, the cleaned subdirectory followed by a slash, so
// a sibling folder whose name merely shares a prefix is out of scope. The
// full listing is otherwise preserved: discovery, ordering, and the
// coordinator see only in-scope files, while instruction resolution still
// reads the full index.
type subdirectoryLister struct {
	lister FileLister
	prefix string
}

// newSubdirectoryLister scopes lister to the files under subdirectory, a
// cleaned, slash-separated folder path inside the root. Callers must pass a
// non-empty, validated subdirectory; an empty value would degenerate to the
// whole root and is the caller's choice to leave unscoped instead.
func newSubdirectoryLister(lister FileLister, subdirectory string) *subdirectoryLister {
	return &subdirectoryLister{
		lister: lister,
		// path.Join would strip the trailing slash, so build the prefix
		// from the cleaned folder plus an explicit separator.
		prefix: path.Clean(subdirectory) + "/",
	}
}

func (l *subdirectoryLister) FilePaths(ctx context.Context) ([]string, error) {
	if l == nil {
		return nil, errors.New("subdirectory lister is nil")
	}
	paths, err := l.lister.FilePaths(ctx)
	if err != nil {
		return nil, err
	}
	filtered := make([]string, 0, len(paths))
	for _, file := range paths {
		if strings.HasPrefix(file, l.prefix) {
			filtered = append(filtered, file)
		}
	}
	return filtered, nil
}
