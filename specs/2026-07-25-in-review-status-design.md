# In Review Status Design

## Purpose

Add a first-class `in-review` workflow status so tasks can remain visibly active
after implementation and before completion.

## Canonical workflow

Workbook defines the workflow sequence in `internal/core` as:

```text
Backlog -> Ready -> Blocked -> In Progress -> In Review -> Done
```

The durable serialized value is `in-review`. `Done` remains the only workflow
status that satisfies a task dependency.

## Single source of truth

Core owns the ordered status definition and status validation. Service list
ordering and presentation board columns derive from that definition rather than
maintaining independent status lists. This prevents a future workflow status from
being accepted yet sorted or rendered incorrectly.

## User-facing behavior

`workbook create`, `workbook update`, and status filtering accept `in-review`.
Terminal and web boards show an `In Review` column or section between `In
Progress` and `Done`; their existing layout policy decides how to fit the sixth
column. Existing task records require no migration.

`workbook next` remains deliberately unchanged: it selects only `Ready` tasks,
and a dependency becomes satisfied only when its task is `Done`.

## Tests

Tests cover acceptance of the new status, canonical service ordering, terminal
and web board presentation, and the unchanged `next` and dependency semantics.
