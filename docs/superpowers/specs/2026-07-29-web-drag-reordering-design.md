# Web Drag Reordering Design

## Goal

Make drag and drop on the local web board place a task where the user indicates,
whether the destination is in the current status column or a different one.
Workbook must preserve priority ordering while sparing the user from precisely
targeting the narrow gap at a priority boundary.

## Scope

- Reorder a task within its current status column.
- Combine a cross-column status change and destination ordering in one durable
  mutation.
- Keep the dragged task's priority unchanged.
- Clamp an intended drop outside the task's priority group to the nearest
  boundary of that group.
- Show the effective, clamped insertion point before the user drops.
- Preserve the existing immutable task-operation model, exact rational ranks,
  disposable projection, polling behavior, and versioned web documents.

This increment does not add priority changes through drag and drop, dependency-
aware visual ordering, multi-task movement, touch-specific drag controls,
keyboard reordering, or a public CLI command for moving and changing status in
one invocation.

## Interaction Model

The browser interprets the pointer as an insertion gap before or after a card.
It then considers the destination column's active cards with the dragged task's
priority, excluding the dragged task itself:

- a gap above that priority group becomes the gap before its first card;
- a gap below that priority group becomes the gap after its last card;
- a gap inside that priority group stays before or after the adjacent
  same-priority card; and
- when the destination has no other card of that priority, the status changes
  without needing a relative rank change.

The same rules apply to same-column and cross-column drags. For example,
dragging a medium-priority task to the top of a column places it before the
first medium-priority task, not before high-priority tasks. Dragging it to the
bottom places it after the last medium-priority task, not after low-priority
tasks.

During `dragover`, the board renders one insertion marker at the effective
clamped gap. The marker, rather than a whole-column outline, communicates where
the task will land. `dragend`, a successful drop, and a failed drop all clear
that marker.

## Atomic Placement Mutation

The web API adds:

```text
PATCH /api/tasks/<id>/position
```

The strict JSON request contains a required canonical destination `status` and
at most one anchor direction:

```json
{"status":"in-progress","before":"WB-01K..."}
```

```json
{"status":"ready","after":"WB-01K..."}
```

When the destination status-and-priority bucket has no other task, both anchor
fields are omitted:

```json
{"status":"done"}
```

Supplying both `before` and `after` is invalid. An anchor must be active, must
not be the moved task, and must have the dragged task's priority and the
requested destination status. An anchorless request is valid only when the
destination bucket has no other active task.

The response uses the existing `workbook.task-mutation` version 1 document,
including projection warnings. Invocation, validation, not-found, stale-write,
corrupt-data, and operational failures continue to use the existing versioned
error mapping.

## Core and Storage Design

Core adds a placement input containing the destination status and optional
before/after anchor. The placement service resolves the moved task and anchor,
validates the destination bucket, and reuses the existing exact-rational rank
calculation.

If status changes, placement adds a `field.set status` operation. If an anchor
produces a different rank, it also adds a `field.set rank` operation. Both
operations are written in one operation pack and advance only the moved task's
ref with the existing compare-and-swap write. A placement that changes neither
status nor rank is an idempotent no-op.

The existing same-bucket `MoveMutation` and CLI command retain their public
contract. Their rank planning and the new placement mutation share an internal
helper so browser and CLI ordering cannot drift.

The web handler receives a placement callback alongside its existing create,
update, status-update, delete, and restore callbacks. `workbook serve` wires it
directly to the core service; the server never shells out to the Workbook CLI.

## Client Data Flow

Each rendered card exposes its task ID and priority. On drag start, the client
records both values and the source status. On dragover, it:

1. identifies the destination status list and the pointer's intended gap;
2. reads the destination's rendered card order;
3. excludes the dragged task and selects peers with its priority;
4. clamps the gap to the peer range;
5. derives a before or after anchor when peers exist; and
6. moves the insertion marker to the effective gap.

On drop, the client sends one placement request. It does not optimistically
change card order. After success it refreshes the task document, allowing the
server's canonical status and rank to determine the rendered result.

If the destination has no same-priority peer, a same-status drop is a no-op and
a cross-status drop sends an anchorless placement request.

## Concurrency and Errors

The client task document is only a placement hint. Core re-resolves the moved
task and anchor against current task state. If an anchor has disappeared,
changed status or priority, or otherwise no longer defines the requested
bucket, the mutation fails instead of guessing from stale browser state.

On failure, the client clears the marker, refreshes the board, and displays the
existing visible stale/update message. A compare-and-swap race on the moved task
remains a stale-write error. Concurrent changes to other task refs may produce
equal rational ranks; the existing full-task-ID tie-break remains deterministic.

## Testing and Verification

Core tests cover:

- same-status placement before and after an anchor;
- atomic cross-status placement with status and rank operations in one pack;
- top and bottom boundary ranks;
- anchorless placement into an empty destination bucket;
- no-op placement;
- tombstoned, self, cross-priority, cross-status, and ambiguous-direction
  anchors; and
- compare-and-swap and projection warning behavior through the existing write
  path.

Web handler tests cover the strict position request, versioned mutation and
error documents, method handling, callback wiring, and preservation of the
existing status route.

The executable client harness covers pointer gaps within a priority group,
clamping from higher and lower priority groups, same-column and cross-column
drops, an empty same-priority bucket, insertion-marker cleanup, refresh after
success, and refresh plus visible failure state after rejection.

CLI server integration uses a real temporary repository to prove one
cross-status drop advances only the moved task ref with one operation commit
containing the expected status and rank changes. README route and board behavior
documentation is updated in the same change.

Final verification runs focused core, web, and CLI tests; the full Go test
suite; `go vet`; `gofmt`; a production build; `git diff --check`; and manual
browser checks at narrow and wide viewport sizes.
