// Task provider literally consumes fileexplorer files

package tasks

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"github.com/dev1-alexwei-net/guava/conversations"
)

// FileProviderConfig describes the fileexplorer-backed task source.
type FileProviderConfig struct {
	// DatabasePath is the read-only fileexplorer SQLite manifest.
	DatabasePath string
	// RootPath is the filesystem root containing indexed files.
	RootPath string
	// InstructionsPath is used when no indexed ancestor instructions.md exists.
	InstructionsPath string
	// FileExplorerURL is the fileexplorer base URL refreshed before each task selection.
	FileExplorerURL string
	// Subdirectory optionally scopes yielded tasks to a folder under RootPath.
	Subdirectory string
	// CompletedPaths lists files that a resumed run must skip because an
	// earlier run of the same session already finished them. Paths use the
	// same cleaned, slash-separated relative form as task IDs; an empty value
	// keeps the fresh-run behavior of processing every discovered file.
	CompletedPaths []string
	// InitialPending lists files an earlier attempt of this session left
	// unfinished, in the order a resumed run must keep them ahead of files
	// discovered from the manifest. Paths use the same cleaned, slash-
	// separated relative form as task IDs; an empty value keeps the fresh-run
	// behavior of processing every discovered file in manifest order.
	InitialPending []string
	// HTTPClient overrides the client used to refresh fileexplorer. A nil client uses http.DefaultClient.
	HTTPClient *http.Client
	// Logger receives structured task-source progress details. A nil logger disables them.
	Logger *slog.Logger
}

// FileProvider implements TaskProvider for files indexed by fileexplorer.
// It owns its index and rooted reader; callers must Close it after iteration.
type FileProvider struct {
	ctx              context.Context
	index            *fileIndex
	reader           *rootedFileReader
	refresher        *HTTPRefresher
	queue            *WorkQueue
	instructionsPath string
	subdirectory     string
	logger           *slog.Logger
	completed        map[string]struct{}
	firstSelection   bool

	fallbackMu           sync.Mutex
	fallbackLoaded       bool
	fallbackInstructions string
	closed               bool
}

var _ conversations.TaskProvider = (*FileProvider)(nil)

// NewFileProvider opens the read-only manifest and rooted filesystem reader
// used to produce file tasks. It refreshes fileexplorer and discovers new
// paths between tasks, selecting newly discovered paths before older pending work.
func NewFileProvider(ctx context.Context, config FileProviderConfig) (*FileProvider, error) {
	if ctx == nil {
		return nil, errors.New("context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for _, field := range []struct {
		name, value string
	}{
		{"database path", config.DatabasePath},
		{"filesystem root", config.RootPath},
		//{"subdirectory root", config.Subdirectory}, // not validate this yet
		{"fileexplorer URL", config.FileExplorerURL},
	} {
		if strings.TrimSpace(field.value) == "" {
			return nil, fmt.Errorf("%s is required", field.name)
		}
	}

	index, err := openFileIndex(ctx, config.DatabasePath)
	if err != nil {
		return nil, err
	}
	reader, err := openRootedFileReader(config.RootPath)
	if err != nil {
		_ = index.close()
		return nil, err
	}
	refresher, err := NewHTTPRefresher(config.FileExplorerURL, config.HTTPClient)
	if err != nil {
		_ = reader.close()
		_ = index.close()
		return nil, err
	}

	queue := NewWorkQueue()
	if _, err := queue.Seed(config.InitialPending); err != nil {
		_ = reader.close()
		_ = index.close()
		return nil, fmt.Errorf("seed initial pending work: %w", err)
	}
	completed := make(map[string]struct{}, len(config.CompletedPaths))
	for _, raw := range config.CompletedPaths {
		path, err := normalizeQueuePath(raw)
		if err != nil {
			_ = reader.close()
			_ = index.close()
			return nil, fmt.Errorf("completed file path %q: %w", raw, err)
		}
		completed[path] = struct{}{}
	}
	return &FileProvider{
		ctx:              ctx,
		index:            index,
		reader:           reader,
		refresher:        refresher,
		queue:            NewWorkQueue(),
		instructionsPath: config.InstructionsPath,
		subdirectory:     config.Subdirectory,
		logger:           config.Logger,
		completed:        completed,
		firstSelection:   true,
	}, nil
}

// Tasks refreshes the manifest and yields each newly discovered file task in
// deterministic queue order. Instruction files are inputs to other tasks,
// never work items. A false callback return stops iteration, leaving that task
// pending if this provider is used again.
func (p *FileProvider) Tasks(fn func(conversations.Task) error) error {
	if p == nil || p.closed {
		return errors.New("file task provider is not open")
	}
	if fn == nil {
		return errors.New("task callback is required")
	}
	for {
		if err := p.ctx.Err(); err != nil {
			return err
		}
		if err := p.refresher.Refresh(p.ctx); err != nil {
			return fmt.Errorf("refresh file index: %w", err)
		}
		paths, err := p.index.FilePaths(p.ctx, p.subdirectory)
		if err != nil {
			return fmt.Errorf("list indexed files: %w", err)
		}
		if p.firstSelection {
			// The first pass of a resumed run appends the manifest behind the
			// seeded unfinished files, keeping them ahead of newly reported
			// files; later passes prioritize files created by the current
			// task exactly as on a fresh run.
			if _, err := p.queue.EnqueueNext(paths); err != nil {
				return fmt.Errorf("enqueue discovered files: %w", err)
			}
			p.firstSelection = false
		} else if _, err := p.queue.EnqueueNext(paths); err != nil {
			return fmt.Errorf("enqueue discovered files: %w", err)
		}

		pending := p.queue.Pending()
		if len(pending) == 0 {
			return nil
		}
		filePath := pending[0]
		file, found, err := p.findIndexedFile(filePath)
		if err != nil {
			return fmt.Errorf("list indexed files: %w", err)
		}
		if !found {
			p.log(slog.LevelDebug, "Skipping file no longer in index", slog.String("path", filePath))
			if popped, ok := p.queue.Pop(); !ok || popped != filePath {
				return fmt.Errorf("work queue changed while skipping %q", filePath)
			}
			continue
		}
		if isInstructionsFile(file) {
			p.log(slog.LevelDebug, "Skipping indexed instructions file", slog.String("path", file.path))
			if popped, ok := p.queue.Pop(); !ok || popped != filePath {
				return fmt.Errorf("work queue changed while skipping %q", filePath)
			}
			continue
		}
		if _, alreadyDone := p.completed[filePath]; alreadyDone {
			p.log(slog.LevelDebug, "Skipping file already completed in this session", slog.String("path", filePath))
			if popped, ok := p.queue.Pop(); !ok || popped != filePath {
				return fmt.Errorf("work queue changed while skipping %q", filePath)
			}
			continue
		}

		task := p.newFileTask(file)
		//if !fn(task) {
		//	return nil
		//}
		err = fn(task)
		if err != nil {
			return err
		}
		if popped, ok := p.queue.Pop(); !ok || popped != filePath {
			return fmt.Errorf("work queue changed while processing %q", filePath)
		}
	}
}

// Close releases the manifest and rooted filesystem handles owned by p.
func (p *FileProvider) Close() error {
	if p == nil || p.closed {
		return nil
	}
	p.closed = true
	return errors.Join(p.reader.close(), p.index.close())
}

func (p *FileProvider) log(level slog.Level, message string, attrs ...slog.Attr) {
	if p.logger == nil {
		return
	}
	p.logger.LogAttrs(p.ctx, level, message, attrs...)
}

func (p *FileProvider) findIndexedFile(filePath string) (indexedFile, bool, error) {
	files, err := p.index.regularFiles(p.ctx)
	if err != nil {
		return indexedFile{}, false, err
	}
	for _, file := range files {
		if file.path == filePath {
			return file, true, nil
		}
	}
	return indexedFile{}, false, nil
}

func (p *FileProvider) newFileTask(file indexedFile) conversations.Task {
	return FileTask{Id: file.path, file: file, reader: p.reader, p: p}
}

func normalizeProviderSubdirectory(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if strings.IndexByte(value, 0) >= 0 {
		return "", errors.New("subdirectory must not contain NUL bytes")
	}
	slash := filepath.ToSlash(value)
	if strings.HasPrefix(slash, "/") {
		return "", fmt.Errorf("subdirectory %q must be a folder inside the filesystem root", value)
	}
	cleaned := path.Clean(slash)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("subdirectory %q must be a folder inside the filesystem root", value)
	}
	return cleaned, nil
}
