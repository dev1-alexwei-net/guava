# Task-session metadata

The `sessions` package stores dashboard task-session metadata in a dedicated SQLite database. Keep this database separate from the fileexplorer index passed as `Record.DatabaseFile`; the executor opens that index read-only.

Each record includes a generated stable ID, agent ID, filesystem root, selected subdirectory (empty for the whole root), fileexplorer database path, lifecycle status, timestamps, and the latest failure message. Records are created and loaded through `Store`; `Update` changes the launch configuration and lifecycle snapshot without changing the stable ID or creation time.

Supported lifecycle states are:

- `active`: a run is in progress.
- `paused`: a run was intentionally stopped and can be resumed.
- `ended`: a run completed normally.
- `failed`: a run ended with an error.

Opening a store initializes its schema. The dashboard stores metadata beside the fileexplorer database by default, using the suffix `.sessions.sqlite`; pass `-session-store` to choose another persistent path. Keep the metadata database separate from the fileexplorer index.

On dashboard startup, records left in `active` state are changed to `paused` with an interruption note. The dashboard never resumes a saved session automatically. A caller must explicitly select a record ID and submit it to `POST /resume` as `session_id`. Resuming restores the saved agent ID, root directory, subdirectory, and fileexplorer database path. Successful completion is stored as `ended`, cancellation as `paused`, and run errors as `failed`, preserving failed sessions for later resume. Inactive records can be permanently removed from the dashboard's session history with `POST /sessions/delete`, a matching `session_id`, and `confirm=yes`; active records are rejected.
