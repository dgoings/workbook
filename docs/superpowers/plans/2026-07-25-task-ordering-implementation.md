# Task Ordering, Dependencies, and Next Selection Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add exact-rank movement, dependency commands, cycle rejection, and deterministic next-task selection to the local Workbook CLI.

**Architecture:** Extend `internal/core` with canonical rational-rank helpers and service methods that produce ordinary immutable operation packs. Keep command parsing/output in `internal/cli`; `gitstore` remains unchanged because every mutation continues through `core.Service.Write`.

**Tech Stack:** Go 1.26, `math/big`, real temporary Git repositories in integration tests.

## Global Constraints

- Ranks are positive reduced canonical rationals; no renumbering or cross-task transaction.
- `move` changes only one task and requires an active anchor in the same status/priority bucket.
- Dependencies are stored on the dependent task; `free` is idempotent.
- `next` is read-only and returns `null`/a human empty message when no task is eligible.
- Keep JSON output versioned through the existing result envelope.

---

### Task 1: Canonical rational ranks and ordering helpers

**Files:**
- Modify: `internal/core/task.go`
- Modify: `internal/core/service.go`
- Modify: `internal/core/task_test.go`
- Modify: `internal/core/service_test.go`

**Interfaces:**
- Produces: `parseRank(string) (*big.Rat, error)`, `formatRank(*big.Rat) string`, and exact rank comparison used by `compareTasks`.
- Produces: `nextRank([]Snapshot, Status, Priority) (string, error)` that appends after the maximum rational rank.

- [x] **Step 1: Write failing canonical-rank tests**

```go
for _, rank := range []string{"1/2", "5/3", "9/1"} {
    if _, err := NormalizeTask("WB", validTask(rank)); err != nil { t.Fatal(err) }
}
for _, rank := range []string{"2/4", "0/1", "1/0", "01/2"} {
    if _, err := NormalizeTask("WB", validTask(rank)); err == nil { t.Fatal(rank) }
}
```

- [x] **Step 2: Run `go test ./internal/core -run 'Rank|Normalize' -count=1` and confirm the current integer-only grammar rejects `1/2`.**
- [x] **Step 3: Implement rational parse/format/compare helpers with `math/big`, update task validation and append-rank calculation.**
- [x] **Step 4: Re-run the focused core tests and `go test ./internal/core -count=1`.**

### Task 2: Move and dependency service behavior

**Files:**
- Modify: `internal/core/service.go`
- Modify: `internal/core/service_test.go`

**Interfaces:**
- Produces: `Service.Move(context.Context, string, MoveInput) (Task, error)` where `MoveInput` contains exactly one resolved anchor direction.
- Produces: `Service.Depend(context.Context, string, string) (Task, error)` and `Service.Free(context.Context, string, string) (Task, error)`.

- [x] **Step 1: Write failing service tests for before/after/boundary rank placement, self and cross-bucket move rejection, valid dependency creation, idempotent free, and A→B→C→A cycle rejection.**
- [x] **Step 2: Run `go test ./internal/core -run 'Move|Depend|Free|Cycle' -count=1` and confirm missing methods fail to compile.**
- [x] **Step 3: Implement one-task rank mutation, graph traversal over current snapshots, and set-add/set-remove operations.**
- [x] **Step 4: Re-run the focused tests and verify the only written ref is the mutated task's ref.**

### Task 3: Deterministic next-task selection

**Files:**
- Modify: `internal/core/service.go`
- Modify: `internal/core/service_test.go`

**Interfaces:**
- Produces: `Service.Next(context.Context) (*Task, error)`.

- [x] **Step 1: Write failing tests for ready-only selection, done dependency gating, high/medium/low priority order, rational-rank order, ID ties, and no eligible task returning nil without error.**
- [x] **Step 2: Run `go test ./internal/core -run Next -count=1` and confirm the API is absent.**
- [x] **Step 3: Implement eligibility lookup from one snapshot enumeration, treating missing/tombstoned/not-done dependencies as incomplete.**
- [x] **Step 4: Re-run the focused tests and the complete core suite.**

### Task 4: CLI surface and documentation

**Files:**
- Modify: `internal/cli/flags.go`
- Modify: `internal/cli/output.go`
- Modify: `internal/cli/run.go`
- Modify: `internal/cli/run_test.go`
- Create: `internal/cli/ordering_test.go`
- Modify: `README.md`

**Interfaces:**
- Produces: `workbook move`, `workbook depend`, `workbook free`, and `workbook next`, each with `--json`.

- [x] **Step 1: Write failing CLI integration tests for command parsing, human and JSON success output, invalid anchors/dependencies, cycle errors, and `next --json` with `data:null`.**
- [x] **Step 2: Run `go test ./internal/cli -run 'Move|Depend|Free|Next' -count=1` and confirm commands are unknown.**
- [x] **Step 3: Wire positional arguments and move flags to core, use the existing detailed task renderer, and update README command/status/documentation contracts.**
- [x] **Step 4: Re-run the focused CLI tests.**

### Task 5: Full verification

**Files:**
- Modify: `docs/superpowers/plans/2026-07-25-task-ordering-implementation.md`

- [x] **Step 1: Mark completed plan steps and run `gofmt -w` on changed Go files.**
- [x] **Step 2: Run `git diff --check`, `go test ./...`, and `go vet ./...`.**
- [x] **Step 3: Build `./cmd/workbook` and manually exercise create, move, depend, free, and next in a temporary repository.**
- [x] **Step 4: Inspect the complete diff and compiler diagnostics before requesting review.**
