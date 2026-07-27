# Descriptive Workbook Task Commit Subjects

## Goal

Make the Git commit subject for each task creation and update identify the task
and summarize the human-meaningful mutation, while keeping the canonical
operation and checkpoint documents unchanged.

## Scope

This change affects only the Git commit subject written for `task.create` and
ordinary task updates. The ref namespace, operation format, `state.json`,
reflog reason, CLI output, and task-projection behavior do not change. Delete,
move, dependency, reconciliation, and future operation types retain their
current subjects in this task.

## Subject format

Every changed subject begins with `workbook:` and the task's actionable short
ID prefix.

```text
workbook: create WB-01KYDDPP More descriptive git log commits
workbook: update WB-01KYDDPP status ready → in progress; priority medium → high
```

Creation includes the task title. Updates list each changed mutable field in
the command's canonical field order: title, description, status, priority, and
labels. A title change is rendered as `title <new title>`. A description change is reported
as `description updated` rather than embedding arbitrary long or multiline
text. Status and priority changes show the prior and new values. Labels report
the sorted normalized additions as `labels +one,+two` and removals as
`labels -one,-two`; a replacement therefore has both forms. Subjects stay one
line: title text has internal whitespace collapsed and is truncated to 72
characters with `…` when longer. The short ID is the project key plus the first
eight characters of the task ULID (for example, `WB-01KYDDPP`).

## Architecture

Build subjects in `core`, immediately after a task's mutation is derived. This
layer has both the parent and resulting task state, so it can produce accurate
before-and-after summaries without teaching Git storage about task fields. Pass
the completed subject through the existing `TaskStore.Write` reason parameter;
`gitstore` continues to write it with `git commit-tree -m` and includes it in
the reflog message.

## Error handling

Subject construction is deterministic and has no I/O. It must not create a
mutation for an unchanged task, bypass existing validation, or affect the
write's compare-and-swap behavior.

## Tests

Core tests cover deterministic create and multi-field update summaries,
including user-text normalization, truncation, and label additions/removals.
Git-store integration tests assert that written commits expose the expected
subject through Git, while preserving the operation and state tree assertions.
