# Refactor baseline and migration plan

This document records the pre-refactor baseline and the sequence for issue #57. The refactor preserves the behavior listed below; it does not introduce a browser-side client or a general-purpose framework.

## Baseline

Baseline commit: `20253f0332605cf00628b6447767aca78bd616cf` (2026-11-22).

Count physical lines in tracked Go files directly under `fileexplorer/`. Production LOC excludes `*_test.go`; test LOC includes those files. Embedded HTML, minified Bootstrap CSS, its license, `go.mod`, and `go.sum` are excluded because they are not Go source or tests.

| Measure | Files | Lines |
| --- | ---: | ---: |
| Production Go | 7 | 1,301 |
| Go tests | 9 | 1,668 |
| Total Go files | 16 | 2,969 |

To reproduce the production count at the baseline commit, check it out and run:

```sh
wc -l fileexplorer/assets.go fileexplorer/editor.go fileexplorer/handler.go \
  fileexplorer/main.go fileexplorer/store.go fileexplorer/sync.go fileexplorer/template.go
wc -l fileexplorer/*_test.go
```

The issue #57 production-LOC target is at most 325 lines (one quarter of baseline). The requested file-count target is at most 8 Go files total. Both are targets, not permission to minify code, remove important behavior coverage, or introduce artificial abstractions; the closeout will report measured results and explain any maintainability trade-off.

## Responsibility map

- `main.go`: flags, address/database defaults, store lifetime, HTTP server startup, signals, and graceful shutdown.
- `handler.go`: route registration, serialized access, path containment, directory/file routes, sync/listing, preview decoding, mutations, redirects, page-model assembly, and error mapping.
- `editor.go`: editor read/save plus create-file and delete-confirmation behavior.
- `store.go`: SQLite schema, database lifetime, direct-child queries, and atomic replacement.
- `sync.go`: filesystem walk and conversion to replica entries.
- `template.go` and `assets.go`: embedded template data/rendering and the embedded stylesheet response.

## Migration order

1. **#58 — baseline and plan:** establish these measurements, responsibility boundaries, invariants, and order.
2. **#59 — rooted filesystem policy:** make root/path validation and rooted filesystem access a single dependency before moving filesystem operations.
3. **#60 — store and synchronization:** clarify persistence versus scanning, keeping atomic replacement and sorted direct-child listing semantics.
4. **#61 — mutations:** move create, rename, delete, upload, and save operations behind cohesive APIs that use the rooted filesystem policy.
5. **#63 — rendering:** consolidate view preparation and template execution behind a focused presentation boundary.
6. **#62 — HTTP composition:** wire explicit application dependencies and keep routes/request parsing separate from operations.
7. **#64 — tests:** retain compact behavior-level coverage for the security and user-visible contracts below; document any removed duplicate coverage.
8. **#65 — startup and closeout:** finish the composition root, document actual abstractions, and recount LOC and file totals.

Use conventional Go source names that describe real responsibilities (for example, `server.go`, `filesystem.go`, `store.go`, and `renderer.go`). The current application has no user/browser session state: preview selection is encoded in the URL, so a speculative `session.go` abstraction would add indirection rather than model a real responsibility.

## Behavior and security invariants

- Keep the documented route paths and methods: directory browsing at `/` and `/d/{path...}`; file serving/download at `/f/{path...}`; editor GET/POST at `/edit/{path...}`; preview at `/preview/{path...}`; delete confirmation at `/confirm-delete/{path...}`; stylesheet at `/static/bootstrap.min.css`; and POST/PUT mutations at `/mkdir`, `/create-file`, `/rename`, `/delete`, and `/upload`.
- Preserve response status, redirect, content-type, download, and error-page behavior. Keep HTML template escaping, accessibility state, and the no-client-side-JavaScript design.
- Never allow traversal, absolute paths, or symlink components to escape the configured root. Rooted filesystem operations remain the authority for reads and mutations.
- Keep upload/editor/preview limits at 32 MiB, 1 MiB, and 512 KiB respectively. Editor saves remain same-directory atomic replacements and preserve permission bits.
- The filesystem is authoritative. Sync skips symlinks and replaces the replica atomically; a failed walk leaves the old replica intact. Listings contain direct children only, with directories first and names sorted within each group.
- Keep SIGINT/SIGTERM shutdown bounded by the five-second timeout and close the SQLite store after server shutdown.

The retained tests should protect these external contracts and high-risk regressions, rather than private helper structure.
