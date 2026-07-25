# Task Ordering, Dependencies, and Next Selection Design

## Goal

Make local Workbook task selection useful for daily dogfooding: tasks can be
ordered within a workflow bucket, express prerequisite relationships, and yield
one deterministic next task.

## Scope

Add these CLI commands:

```text
workbook move <task-id-or-prefix> (--before <task-id-or-prefix> | --after <task-id-or-prefix>) [--json]
workbook depend <task-id-or-prefix> <dependency-id-or-prefix> [--json]
workbook free <task-id-or-prefix> <dependency-id-or-prefix> [--json]
workbook next [--json]
```

`move` changes only the moved task's rank. Both the moved task and its anchor
must be active and in the same status and priority bucket. The command rejects
self-anchors and accepts exactly one of `--before` or `--after`.

`depend TASK DEPENDENCY` records that TASK waits for DEPENDENCY. `free TASK
DEPENDENCY` removes that edge. The first argument is always the dependent task.

`next` is a query, not a claim or mutation. It returns at most one eligible task.

## Rank Model

Ranks are positive reduced rational numbers in canonical `numerator/denominator`
form. Numerator and denominator are unsigned base-10 integers without leading
zeroes; the numerator is nonzero; and the two values have greatest common divisor
one. Existing `N/1` ranks remain valid.

Tasks are ordered by status, then priority, then exact rank, then full task ID.
Within a status/priority bucket, creation appends after the current maximum rank.
Moving before an anchor chooses a rational strictly below that anchor and above
the preceding rank when one exists. Moving after an anchor chooses a rational
strictly above that anchor and below the following rank when one exists. At a
bucket boundary it chooses a positive rational below the first task or an integer
above the last task. The moved task is excluded while finding its neighbors.

Concurrent independent moves may produce equal rational ranks. This POC preserves
both task refs and orders tied ranks by full task ID; it does not renumber or
perform cross-task reconciliation.

## Dependency Model

Dependencies are canonical task-ID sets stored on the dependent task. Adding an
edge rejects a missing or tombstoned endpoint, a self-edge, an existing cycle,
or a cycle introduced by the new edge. Cycle detection traverses the current
active dependency graph from the proposed dependency and rejects when it reaches
the dependent task. Removing a missing edge succeeds without a mutation.

Tombstoning a task does not rewrite other tasks. For `next`, a missing,
tombstoned, non-done, or unreadable dependency means the dependent task is not
eligible.

## Next Selection

`workbook next` considers only nondeleted tasks with status `ready` whose every
dependency resolves to a nondeleted task with status `done`. It selects the
lowest task under this deterministic ordering:

1. priority: high, medium, low;
2. exact rational rank: lowest first; and
3. full task ID: lexical ascending.

Human output uses the existing detailed task view. JSON output uses the existing
versioned result envelope with either the selected task or `null` when no task is
eligible. An empty queue is a successful query.

## Implementation Boundaries

Core owns rational parsing, ordering, move calculation, dependency validation,
cycle detection, and next-task selection. CLI owns argument parsing and output.
The existing Git-backed task store persists resulting immutable operation packs;
no command touches another task ref during a move, dependency change, or next
query.

## Validation

Tests cover canonical rational acceptance and rejection, append and boundary
move ranks, mixed-bucket and malformed move input, dependency creation/removal,
missing/tombstoned/self/cyclic edges, dependency-gated selection, deterministic
priority/rank/ID ties, JSON `null` for an empty queue, and persistence through
real Git task refs. README command and behavior documentation will be updated in
the same change.
