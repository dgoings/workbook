# Descriptive Task Commit Subjects Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make create and update task commits carry a concise, deterministic human-readable subject.

**Architecture:** Add a pure core formatter that receives the complete task ID and normalized before/after task values. `Service.Create` and `Service.Update` pass its output through the existing `TaskStore.Write` reason parameter; `gitstore` continues to persist that parameter verbatim as the commit subject and reflog reason.

**Tech Stack:** Go 1.26, standard library, Git plumbing integration tests.

## Global Constraints

- Do not change `operation.json`, `state.json`, task refs, compare-and-swap behavior, or projection behavior.
- Prefix create and update subjects with `workbook:` and `WB-` plus the first eight task-ULID characters.
- Use canonical mutation order: title, description, status, priority, labels.
- Collapse title whitespace and truncate display titles to 72 characters with `…`.
- Keep delete, move, dependency, and future operation subjects unchanged.

---

### Task 1: Format create and update mutation subjects in core

**Files:**
- Modify: `internal/core/service.go:49-106,193-230,366-405`
- Modify: `internal/core/service_test.go:23-76,264-308`

**Interfaces:**
- Consumes: normalized `TaskData`, full task IDs, and existing `changedOperations` output.
- Produces: `createCommitSubject(taskID string, task TaskData) string` and `updateCommitSubject(taskID string, before, after TaskData) string`.

- [ ] **Step 1: Write failing core assertions for the formatted reason**

Extend `memoryWrite` to retain the `reason string` passed to `TaskStore.Write`, then assert the existing create fixture writes:

```go
if got, want := write.reason, "workbook: create WB-01K0M6B8 Build service"; got != want {

    t.Fatalf("Create() write reason = %q, want %q", got, want)
}
```

Add table-driven update cases asserting the exact multi-field subject below,
plus title whitespace collapse and a title longer than 72 characters ending in
`…`:

```go
"workbook: update WB-01K0M6B8 title New title; description updated; status backlog → ready; priority medium → high; labels -zeta; labels +beta"
```

- [ ] **Step 2: Run the focused core test to verify it fails**

Run: `GOCACHE=/private/tmp/workbook-gocache go test ./internal/core -run 'TestService(Create|Update).*'`

Expected: FAIL because the store discards the reason or because the current literal subjects are still `create task` and `update task`.

- [ ] **Step 3: Implement pure, deterministic subject helpers**

Add helpers in `internal/core/service.go` that use `taskULIDSuffix`, return the project-key-plus-eight-ULID short ID, and format the examples below:

```go
func createCommitSubject(taskID string, task TaskData) string
func updateCommitSubject(taskID string, before, after TaskData) string
```

Have `Create` pass `createCommitSubject(taskID, taskData)` to `Store.Write`. In `Update`, pass `updateCommitSubject(parent.State.TaskID, parent.State.Task, next)` as the existing `writeMutation` reason. Keep `writeMutation` generic and preserve the existing caller-provided reasons for non-`Update` mutations so their subjects stay unchanged.

Use `strings.Fields` plus `strings.Join` to collapse title whitespace. Count runes, not bytes, so truncation cannot split UTF-8 text. Build label deltas with the existing sorted `setDifference` helper. Join field clauses with `; `.

- [ ] **Step 4: Run focused core tests**

Run: `GOCACHE=/private/tmp/workbook-gocache go test ./internal/core -run 'TestService(Create|Update).*'`

Expected: PASS, including existing operation-pack and no-op assertions.

- [ ] **Step 5: Commit the core formatter and tests**

```bash
git add internal/core/service.go internal/core/service_test.go
git commit -m "feat: describe task create and update commits"
```

### Task 2: Prove Git persists the formatted subject and document it

**Files:**
- Modify: `internal/gitstore/write_test.go:19-52`
- Modify: `README.md:88-97`

**Interfaces:**
- Consumes: `TaskStore.Write(..., reason string)` and the pure core subject format from Task 1.
- Produces: Git-level proof that `commit-tree -m` preserves the descriptive subject, plus user documentation of the log behavior.

- [ ] **Step 1: Write a failing Git-store commit-subject assertion**

Add a dedicated test adjacent to `TestWriteCreatesRootCommitAndTaskRef` that
passes `workbook: create WB-01K0M6B8 Create task` as the `Repository.Write`
reason and asserts the commit subject after `assertTaskTree`:

```go
if got, want := gitOutput(t, repo, "show", "-s", "--format=%s", snapshot.Head), "workbook: create WB-01K0M6B8 Create task"; got != want {

    t.Fatalf("commit subject = %q, want %q", got, want)
}
```

This proves arbitrary descriptive text survives the Git boundary without
requiring Git storage to understand task fields.

- [ ] **Step 2: Run the focused Git-store test to verify it fails**

Run: `GOCACHE=/private/tmp/workbook-gocache go test ./internal/gitstore -run TestWriteCreatesRootCommitAndTaskRef`

Expected: FAIL after the expectation changes because the current write reason is still the old literal.

- [ ] **Step 3: Add the Git integration assertion and README note**

Keep `Repository.Write` and `writeCommit` unchanged. Update the test fixture to pass a descriptive Workbook subject and assert it through `git show -s --format=%s`. In the implemented-command description in `README.md`, add one sentence stating that creation and ordinary updates write descriptive task-operation commit subjects suitable for `git log`, while canonical data remains in the operation and state blobs.

- [ ] **Step 4: Run targeted checks and the full suite**

Run:

```bash
gofmt -w internal/core/service.go internal/core/service_test.go internal/gitstore/write_test.go
GOCACHE=/private/tmp/workbook-gocache go test ./internal/core ./internal/gitstore
GOCACHE=/private/tmp/workbook-gocache go test ./...
go vet ./...
git diff --check
```

Expected: all commands exit zero.

- [ ] **Step 5: Commit the integration proof and documentation**

```bash
git add internal/gitstore/write_test.go README.md
git commit -m "docs: describe task operation commit subjects"
```
