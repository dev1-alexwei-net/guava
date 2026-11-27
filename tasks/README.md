# Tasks

The `tasks` package defines source-agnostic task abstractions and provides
file-backed tasks from the fileexplorer manifest. A file task ID is its
manifest-relative path. Its prompt is built from the nearest indexed
`instructions.md` and the current file contents, falling back to the
configured instructions file. Instruction files are inputs, not tasks.

```go
provider, err := tasks.NewFileProvider(ctx, tasks.FileProviderConfig{
	DatabasePath:     "/var/lib/fileexplorer.sqlite",
	RootPath:         "/srv/files",
	InstructionsPath: "./instructions.md",
	FileExplorerURL:  "http://127.0.0.1:8080",
	Subdirectory:     "docs", // optional
})
if err != nil {
	return err
}
defer provider.Close()

err = provider.Tasks(func(task tasks.Task) bool {
	prompt, err := task.Prompt()
	if err != nil {
		return false
	}
	// Send prompt to the task's conversation.
	_ = prompt
	return true
})
```

The provider refreshes fileexplorer before each task selection and merges
newly discovered files with older pending work. The complete pending set is
sorted by binary path order after each merge, including files created during a
run. Tasks are scoped to the configured subdirectory when present.
The provider owns its SQLite index and rooted filesystem reader and must be
closed after use.

`ListSubdirectories` returns deterministic manifest-backed choices without
starting a task run. `HTTPRefresher` implements the fileexplorer GET refresh,
and `WorkQueue` holds per-run file discovery state. `EnqueueNext` merges new
paths into the pending set and sorts the complete set by binary path order.
