# Task-session metadata

The `sessions` package stores dashboard task-session metadata in a dedicated SQLite database. Keep this database separate from the fileexplorer index passed as `Record.DatabaseFile`; the executor opens that index read-only.

Each record includes a generated stable ID, agent ID, filesystem root, selected subdirectory (empty for the whole root), fileexplorer database path, lifecycle status, timestamps, and the latest failure message. Records are created and loaded through `Store`; `Update` changes the launch configuration and lifecycle snapshot without changing the stable ID or creation time.

Supported lifecycle states are:

- `active`: a run is in progress.
- `paused`: a run was intentionally stopped and can be resumed.
- `ended`: a run completed normally.
- `failed`: a run ended with an error.

Opening a store initializes its schema. The current executor/dashboard lifecycle remains unchanged until a later integration step uses this persistence layer.
