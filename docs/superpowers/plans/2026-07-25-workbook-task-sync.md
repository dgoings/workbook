# Workbook Task Sync Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add explicit origin-only task fetch/push commands and an optional safe pre-push hook so two clones can collaborate through Workbook refs.

**Architecture:** Keep transport in `internal/gitstore`, where Git commands fetch into isolated tracking refs, validate task tips, and CAS-update canonical refs only for compatible histories. Expose stable per-ref outcomes through the CLI, and install one clearly marked, recursion-safe hook through the same repository boundary.

**Tech Stack:** Go 1.26, Git plumbing/porcelain commands, temporary real repositories and bare remotes.

## Global Constraints

- Fetch only `refs/workbook/tasks/*` from `origin` into `refs/workbook/remotes/origin/tasks/*`.
- Validate fetched tips before updating canonical refs.
- Create or fast-forward compatible local refs; preserve local-ahead and divergent refs.
- Push every local task ref without force or deletion and report partial rejection with a nonzero exit.
- The optional managed pre-push hook publishes Workbook refs before an ordinary `git push origin`, prevents recursion, and blocks the code push if task publication fails.
- Do not reconcile divergent operations, auto-fetch, add a daemon, prune refs, support multiple remotes, or change the checked-out code branch.

---

### Task 1: Fetch and classify remote task refs

**Files:**
- Create: `internal/gitstore/sync.go`
- Create: `internal/gitstore/sync_test.go`

**Interfaces:**
- Produces: `Repository.Fetch(context.Context, core.ProjectConfig) (SyncResult, error)`
- Produces: per-task outcomes `created`, `fast-forwarded`, `unchanged`, `local-ahead`, `diverged`, and `invalid`

- [x] Write integration tests using a temporary bare `origin` and two clones that prove a new task is discovered, a sequential update fast-forwards, local-ahead remains unchanged, divergence is preserved, and an invalid remote tip never reaches a canonical ref.
- [x] Run `go test ./internal/gitstore -run Fetch -count=1` and confirm the missing API fails to compile.
- [x] Implement the origin fetch into isolated tracking refs, validate each fetched tip, use ancestry tests to classify valid histories, and use `git update-ref` with expected old values for canonical creates/fast-forwards.
- [x] Re-run the focused tests and refactor only while they remain green.

### Task 2: Push all task refs with partial outcomes

**Files:**
- Modify: `internal/gitstore/sync.go`
- Modify: `internal/gitstore/sync_test.go`
- Modify: `internal/gitstore/repository.go`

**Interfaces:**
- Produces: `Repository.Push(context.Context, core.ProjectConfig) (SyncResult, error)`
- `Repository` runs internal task pushes with `WORKBOOK_PRE_PUSH_ACTIVE=1`

- [x] Write integration tests proving all task refs publish, a non-fast-forward update is rejected without force, and one rejected task does not prevent an unrelated task from publishing.
- [x] Run `go test ./internal/gitstore -run Push -count=1` and confirm the missing behavior fails.
- [x] Implement independent per-ref pushes to `origin`, classify accepted/up-to-date/rejected outcomes, retain the remote error detail, and return an aggregate operational error when any ref is rejected.
- [x] Re-run the focused tests and the complete `internal/gitstore` package tests.

### Task 3: Install the optional managed pre-push hook

**Files:**
- Create: `internal/gitstore/hooks.go`
- Create: `internal/gitstore/hooks_test.go`

**Interfaces:**
- Produces: `Repository.InstallHooks(context.Context) (HookInstallResult, error)`

- [x] Write real hook tests proving first install creates an executable hook, repeated install is idempotent, the hook skips non-origin pushes, the recursion guard bypasses republishing, and an existing unmanaged hook is preserved with chaining guidance.
- [x] Run `go test ./internal/gitstore -run Hook -count=1` and confirm the missing API fails.
- [x] Atomically install a version-marked POSIX `pre-push` script at Git's configured hook path and reject any unrecognized existing file.
- [x] Re-run the focused tests and refactor only while green.

### Task 4: Expose commands and stable output

**Files:**
- Modify: `internal/cli/run.go`
- Modify: `internal/cli/flags.go`
- Modify: `internal/cli/output.go`
- Modify: `internal/cli/run_test.go`

**Interfaces:**
- Produces: `workbook fetch [--json]`, `workbook push [--json]`, and `workbook hooks install [--json]`

- [x] Write CLI integration tests for human and JSON result shapes, invocation errors, divergence reporting, partial push failure, and hook installation/refusal.
- [x] Run the focused CLI tests and confirm they fail because the commands are absent.
- [x] Wire the commands directly to `gitstore.Repository`, print deterministic per-ref summaries, and preserve nonzero errors after emitting partial results.
- [x] Re-run focused and complete CLI tests.

### Task 5: Document and verify the collaborative POC

**Files:**
- Modify: `README.md`
- Modify: `internal/cli/run_test.go`

**Interfaces:**
- Documents the implemented two-clone workflow and names multiple remotes and reconciliation as future work.

- [x] Update the README status, implemented commands, collaboration example, outcome guarantees, hook behavior, and remaining limitations.
- [x] Update the README contract test so fetch, push, and hook installation must remain in the implemented command section.
- [x] Run `gofmt` on changed Go files, then run `go test ./...`, `go vet ./...`, and a two-clone manual smoke test.
- [x] Inspect `git diff --check`, the complete diff, and LSP/compiler diagnostics before committing.
