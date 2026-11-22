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

- `main.go`: flags, address/database defaults, resource lifetime, HTTP server startup, signals, and graceful shutdown.
- `server.go`: `application` composition, route registration, serialized request handling, page/editor/mutation handlers, redirects, and HTTP error mapping.
- `filesystem.go`: `rootedFS` path policy and rooted operations, plus the distinct `mutations` object for file and directory changes.
- `store.go`: the distinct `Store`, `scanner`, and `synchronizer` types for SQLite persistence, filesystem walking, and replica refresh.
- `renderer.go`: page models, URL generation, template execution, and embedded stylesheet serving.
- `server_test.go`, `web_test.go`, and `store_test.go`: behavior-level route, accessibility, filesystem, mutation, storage, and synchronization coverage.

Types with different responsibilities remain distinct; related types share source files to meet the requested file-count reduction.

## Migration order

1. **#58 — baseline and plan:** establish these measurements, responsibility boundaries, invariants, and order.
2. **#59 — rooted filesystem policy:** make root/path validation and rooted filesystem access a single dependency before moving filesystem operations.
3. **#60 — store and synchronization:** clarify persistence versus scanning, keeping atomic replacement and sorted direct-child listing semantics.
4. **#61 — mutations:** move create, rename, delete, upload, and save operations behind cohesive APIs that use the rooted filesystem policy.
5. **#63 — rendering:** consolidate view preparation and template execution behind a focused presentation boundary.
6. **#62 — HTTP composition:** wire explicit application dependencies and keep routes/request parsing separate from operations.
7. **#64 — tests:** retain compact behavior-level coverage for the security and user-visible contracts below; document any removed duplicate coverage.
8. **#65 — startup and closeout:** finish the composition root, document actual abstractions, and recount LOC and file totals.
9. **#76 — control flow and interaction errors:** flatten nested conditionals, centralize user-facing status/message construction, and share form-mutation request handling.

Use conventional Go source names that describe real responsibilities (for example, `server.go`, `filesystem.go`, `store.go`, and `renderer.go`). The current application has no user/browser session state: preview selection is encoded in the URL, so a speculative `session.go` abstraction would add indirection rather than model a real responsibility.

## Behavior and security invariants

- Keep the documented route paths and methods: directory browsing at `/` and `/d/{path...}`; file serving/download at `/f/{path...}`; editor GET/POST at `/edit/{path...}`; preview at `/preview/{path...}`; delete confirmation at `/confirm-delete/{path...}`; stylesheet at `/static/bootstrap.min.css`; and POST/PUT mutations at `/mkdir`, `/create-file`, `/rename`, `/delete`, and `/upload`.
- Preserve response status, redirect, content-type, download, and error-page behavior. Keep HTML template escaping, accessibility state, and the no-client-side-JavaScript design.
- Never allow traversal, absolute paths, or symlink components to escape the configured root. Rooted filesystem operations remain the authority for reads and mutations.
- Keep upload/editor/preview limits at 32 MiB, 1 MiB, and 512 KiB respectively. Editor saves remain same-directory atomic replacements and preserve permission bits.
- The filesystem is authoritative. Sync skips symlinks and replaces the replica atomically; a failed walk leaves the old replica intact. Listings contain direct children only, with directories first and names sorted within each group.
- Keep SIGINT/SIGTERM shutdown bounded by the five-second timeout and close the SQLite store after server shutdown.

The retained tests should protect these external contracts and high-risk regressions, rather than private helper structure.

## #64 test-scope review

At the #64 stack point, the suite is consolidated from the 9 baseline `*_test.go` files and 1,668 lines to `server_test.go`, `web_test.go`, and `store_test.go` with 1,060 lines: 36.5% fewer test lines and 66.7% fewer test files. Reproduce the current count with `wc -l fileexplorer/*_test.go`.

Redundant route listing and stylesheet checks were folded into end-to-end route/render checks; repeated preview link, focus, selection, fallback, and responsive-layout assertions were merged into an accessible preview flow and state tables. Rooted path policy remains covered separately from HTTP traversal/symlink tests. Store root ordering and nested direct-child behavior now share one test, and synchronization idempotence is exercised in the add/remove flow. The one-row `Store.Upsert` and `Store.Delete` helpers had no production callers, so they and their private CRUD tests were removed; replica replacement and listing remain covered, including rollback on a failed replacement. BOM decoding, binary/size fallback, downloads, accessibility state, editor/mutation guarantees, route errors, external filesystem refresh, and symlink boundaries remain covered.

## #65 closeout

Count physical lines in the five production Go files and three test files using the same baseline method:

```sh
wc -l fileexplorer/main.go fileexplorer/server.go fileexplorer/filesystem.go \
  fileexplorer/store.go fileexplorer/renderer.go
wc -l fileexplorer/*_test.go
```

| Measure | Baseline | Final | Change |
| --- | ---: | ---: | ---: |
| Production Go lines | 1,301 in 7 files | 1,575 in 5 files | +274 lines (+21.1%) |
| Test Go lines | 1,668 in 9 files | 1,060 in 3 files | −608 lines (−36.5%) |
| All Go files | 16 | 8 | −8 files (−50.0%) |
| All Go lines | 2,969 | 2,635 | −334 lines (−11.2%) |

The requested fourfold production-LOC reduction (at most 325 lines) was not reached. The completed decomposition preserves the rooted filesystem boundary, SQLite replica and rollback behavior, all existing routes and mutations, editor/preview limits, accessible server-rendered UI, and graceful shutdown. Those explicit types and their error/status mapping increase production LOC by 274 lines versus baseline; reaching 325 lines without dropping behavior or writing code-golfed, tightly coupled handlers is not a maintainable trade-off. The file-count target is met by keeping the responsibilities as separate types while grouping related types into five production files and retaining the three focused test files.


## #76 control-flow and interaction errors

The follow-up removes nested `if` statements across Go source and tests by using guard clauses, simple predicates, and small mapping helpers. User-visible mutation failures now use one `interactionError` factory carrying the response status and message; operation-specific error sentinels and the handler-side sentinel switch statements are gone. A shared form-mutation handler wraps serialization, form parsing, operation execution, and redirect/error handling. Path and read failures also share their status/message mappings. Routes, redirect targets, statuses, and messages remain covered by the existing behavior tests.

At the #76 stack point, production Go is 62 lines smaller than the #75 point; the test suite and file count are unchanged. Reproduce the counts with the commands in the #65 section above.

| Measure | Baseline | #75 stack point | #76 stack point |
| --- | ---: | ---: | ---: |
| Production Go lines | 1,301 | 1,575 | 1,513 |
| Test Go lines | 1,668 | 1,060 | 1,073 |
| All Go files | 16 | 8 | 8 |
| All Go lines | 2,969 | 2,635 | 2,586 |

This follow-up reduces production code by 62 lines (3.9%) from #75 and brings production code to 212 lines (16.3%) above baseline. Test helpers add 13 lines to keep nested conditionals out of the test suite; total Go lines are 49 below #75 and 383 below baseline. The requested fourfold production-LOC reduction is still not met; the upper bound of 325 production lines would require deleting behavior or adding excessive compression. The file-count target remains met. `go test ./...`, `go vet ./...`, and a `CGO_ENABLED=0` build pass at this stack point.
