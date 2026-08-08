# Changelog

A release pull request adds an entry here before the release is cut, and the
bump label it carries has to agree with the version this file names. See
[Releasing](README.md#releasing) for how the two are checked against each
other.

Patch releases may be cut without an entry, so this file records the releases
worth describing rather than every release. Every release, described here or
not, has notes on the
[GitHub Releases page](https://github.com/dgoings/workbook/releases).

Releases through v0.4.1 predate this file.

## v0.4.2 — 2026-08-08

Quality and hardening for the web board, plus safety limits on task storage,
from nine reviewed pull requests.

### Web board

- Saving a new task returns you to the board, and a **Create more** toggle
  keeps a clean form open for the next task instead (#41).
- New tasks land on the board optimistically: the card renders immediately
  while the save is still in flight, and a refused save offers the draft back
  instead of losing it (#46).
- Card descriptions are hidden by default to keep columns scannable; a board
  setting restores the previous behavior (#38).
- Labels are edited as removable chiclets instead of one comma-separated text
  field (#40).
- Tasks whose status matches no column appear in an explicit unknown-status
  section, matching the terminal board, instead of being hidden (#42).
- The dependency search popup closes when it loses focus (#47).

### Limits and hardening

- Task fields are bounded — title 500 bytes, description 64 KiB, labels
  100 bytes each and 50 per task — and Workbook refuses to read a Git object
  over 4 MiB, so one oversized task can no longer exhaust memory in every
  clone. Web request bodies are capped at 1 MiB and the board server gains
  connection timeouts. The limits are documented in the README (#44).
- The web handler is built from a named options struct, and serve-level tests
  fail if the delete/restore or depend/free wirings are ever swapped (#43).

### Performance and tooling

- The benchmark harness measures the agent hot loop (`workbook next`,
  `workbook show`), warm board reads, and the sync watcher's steady-state CPU
  and memory; a replacement `api-update` p95 target is proposed with evidence
  under `docs/performance/` (#45).
- `go test -short ./...` is the supported fast suite for local iteration and
  parallel agents; the release and installer build tests share the ambient Go
  build cache instead of cold-compiling everything per test (#39).
