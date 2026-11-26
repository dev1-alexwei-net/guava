package executor

import (
	"context"
	"errors"
	"sort"
	"strings"
)

// ListSubdirectories returns the candidate subdirectories derived from the
// fileexplorer manifest stored at databasePath, without starting an executor
// session. The candidates are the union of the manifest's directory rows
// (is_dir = 1) and the folder prefixes implied by regular-file paths,
// restricted to folders that contain at least one file, in deterministic
// binary path order. Paths are slash-separated and relative to the
// filesystem root, so every result is a valid value for Config.Subdirectory.
//
// The database is opened read-only and never modified, so the method is safe
// to call against the -database a caller such as cmd/executor-dashboard
// already received at startup. The database file must already exist.
func ListSubdirectories(ctx context.Context, databasePath string) ([]string, error) {
	if ctx == nil {
		return nil, errors.New("context is nil")
	}
	if strings.TrimSpace(databasePath) == "" {
		return nil, errors.New("database path is required")
	}

	index, err := openFileIndex(ctx, databasePath)
	if err != nil {
		return nil, err
	}
	defer func() {
		err = errors.Join(err, index.close())
	}()
	return index.subdirectories(ctx)
}

// subdirectories derives the candidate subdirectories from the manifest: the
// union of the explicit directory rows and every folder prefix implied by a
// regular-file path, keeping only the folders that contain at least one file
// and returning them in binary path order.
func (i *fileIndex) subdirectories(ctx context.Context) ([]string, error) {
	directories, err := i.directoryPaths(ctx)
	if err != nil {
		return nil, err
	}
	files, err := i.regularFiles(ctx)
	if err != nil {
		return nil, err
	}

	folders := make(map[string]bool, len(directories))
	for _, directory := range directories {
		addFolder(folders, directory)
	}
	containsFile := make(map[string]bool)
	for _, file := range files {
		for _, folder := range folderPrefixes(file.path) {
			folders[folder] = true
			containsFile[folder] = true
		}
	}

	candidates := make([]string, 0, len(folders))
	for folder := range folders {
		if containsFile[folder] {
			candidates = append(candidates, folder)
		}
	}
	sort.Strings(candidates)
	return candidates, nil
}

// folderPrefixes returns every folder that contains the file at path,
// innermost first: "a/b/c.txt" yields "a/b" and then "a". A file directly
// under the root yields none, so the root itself is never a candidate.
func folderPrefixes(path string) []string {
	folder := path
	prefixes := make([]string, 0, 2)
	for {
		slash := strings.LastIndexByte(folder, '/')
		if slash <= 0 {
			return prefixes
		}
		folder = folder[:slash]
		prefixes = append(prefixes, folder)
	}
}

// addFolder records a manifest directory row as a candidate folder, skipping
// blank or root-only spellings that cannot scope a session.
func addFolder(folders map[string]bool, directory string) {
	if directory == "" || directory == "." {
		return
	}
	folders[directory] = true
}
