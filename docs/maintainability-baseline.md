# Codebase maintainability baseline

Baseline snapshot: `origin/main` at `503caa1` (2026-11-26), before work for issues #256–#259 and before the current branch changes.

## Counting method

Count tracked Go source files only (`git ls-files '*.go'`): production excludes files ending in `_test.go`; test source includes those files. LOC is the newline count reported by `wc -l`, not a token/statement count. Test functions are top-level declarations matching `^func Test`. Executed test names count each `go test -json ./...` event with `Action == "run"` and a non-empty `Test` field, including table-driven subtests. Generated dependencies, embedded static assets, templates, and untracked files are excluded.

## Baseline totals

| Measure | Count |
| --- | ---: |
| Production Go files | 45 |
| Production Go LOC | 5,852 |
| Test Go files | 26 |
| Test Go LOC | 7,195 |
| Top-level test functions | 183 |
| Executed test names (including subtests) | 287 |

The baseline test run used `go test -json ./...` and passed in all seven Go packages.

## Package inventory

| Package | Production files | Production LOC | Test files | Test LOC | Top-level test functions |
| --- | ---: | ---: | ---: | ---: | ---: |
| `cmd/executor` | 1 | 95 | 1 | 323 | 1 |
| `cmd/executor-dashboard` | 3 | 840 | 1 | 1,042 | 25 |
| `conversations` | 10 | 886 | 1 | 54 | 1 |
| `executor` | 13 | 727 | 9 | 2,263 | 60 |
| `fileexplorer` | 5 | 1,918 | 3 | 2,169 | 45 |
| `internal/version` | 1 | 14 | 0 | 0 | 0 |
| `sessions` | 1 | 330 | 1 | 238 | 6 |
| `tasks` | 11 | 1,042 | 10 | 1,106 | 45 |

## Ownership coverage

The active QA stories cover `conversations`, `executor`, and `tasks` (#245), plus `sessions` (#250). This task's remaining-package inventory is `cmd/executor`, `cmd/executor-dashboard`, `fileexplorer`, and `internal/version`. Their current composition and security boundaries are already narrow; the dashboard's run controller did, however, depend directly on the concrete SQLite session store. Issue #256 adds a consumer-owned lifecycle contract there while leaving store opening, interruption recovery, list/display, and closeout with the dashboard composition root.
