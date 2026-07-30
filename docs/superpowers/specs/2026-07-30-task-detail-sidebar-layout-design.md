# Task Detail Sidebar Layout Design

**Status:** Approved in conversation; awaiting written-spec review

**Task:** `WB-01KYQMZMKMD9RWNH6PDTMZ97MC`

**Related task:** `WB-01KYQZ64833PGMFACTMSW4HEM1`
**Pull request:** #11

## Context

The first dependency-management implementation renders **Depends On** and
**Blocks** as full-width sections below the task form. That makes relationship
editing clear, but it consumes the vertical space that
`WB-01KYQZ64833PGMFACTMSW4HEM1` intentionally gave to Description.

Task detail pages have unused horizontal space at desktop widths. The revised
layout follows the same information hierarchy as Linear: the task's primary
content occupies a wide main column, while small properties and relationships
occupy a compact sidebar.

The user selected a shared desktop shell for both existing and new tasks.
On narrow screens, both views collapse to the focused single-column form.

## Goals

- Preserve a wide, viewport-filling Description editor as the task page's
  primary surface.
- Move status, priority, labels, Depends On, and Blocks into a compact desktop
  sidebar.
- Use the same desktop shell for New Task and existing task detail.
- Let New Task stage both relationship directions before the task has an ID.
- Keep validation feedback and task actions visible and clearly associated with
  the form.
- Preserve the existing Git-durable relationship endpoints and canonical
  `dependencies` data model.
- Preserve unsaved task fields and relationship drafts through recoverable
  failures.
- Collapse to a coherent single-column mobile order.

## Non-goals

- No new durable `blocks` field.
- No replacement of the nested dependency mutation endpoints.
- No coordination service or multi-ref transaction.
- No changes to board-card dependency progress or eligibility semantics.
- No new runtime dependency, framework, or build step.
- No activity feed, comments, attachments, owners, projects, or other Linear
  properties.

## Layout

### Desktop

The task route expands from the current 48rem maximum to a wide work surface,
approximately 80–84rem, while retaining the existing page frame, palette, and
header.

The shared task form uses two columns:

```text
┌──────────────────────────────────────────────────────────────────────┐
│ Task title                                                   Full ID │
├───────────────────────────────────────────────┬──────────────────────┤
│ Title                                         │ Properties           │
│                                               │ Status               │
│ Description                                   │ Priority             │
│                                               │ Labels               │
│                                               │                      │
│                                               │ Relationships        │
│                                               │ Depends On           │
│                                               │ Blocks               │
├───────────────────────────────────────────────┴──────────────────────┤
│ Feedback                                    Back  Save/Create Delete │
└──────────────────────────────────────────────────────────────────────┘
```

- The main column is `minmax(0, 1fr)`.
- The sidebar is approximately 20rem wide.
- Title remains above Description.
- Description receives the flexible height and scrolls internally when its
  content exceeds the available height.
- The footer spans both columns so feedback and actions remain independent of
  relationship-list length.
- Delete appears only for active existing tasks.
- New Task and existing detail use the same proportions.

### Sidebar

The sidebar has two visually quiet sections:

1. **Properties**
   - Status
   - Priority
   - Labels
2. **Relationships**
   - Depends On
   - Blocks

Relationship rows become compact sidebar rows rather than bordered full-width
cards. Each row shows:

- title or `Unavailable task`;
- status and priority when available;
- the full task ID;
- `Deleted` or unavailable state when applicable; and
- Remove when the relationship is mutable.

Each relationship group retains its integrated accessible combobox. Candidate
results remain a popover/listbox and do not reserve page height while closed.
The existing tombstone rules remain unchanged:

- missing and tombstoned Depends On rows remain removable;
- active Blocks rows remain removable; and
- tombstoned Blocks rows remain read-only.

### Mobile and narrow screens

Below the desktop breakpoint, the form becomes one column in this order:

1. title;
2. Description;
3. Properties;
4. Relationships;
5. feedback and actions.

The mobile layout intentionally resembles the focused creation form. It has no
horizontal overflow, all controls remain reachable, and Description keeps a
comfortable minimum height rather than attempting to consume the full viewport.

## Shared form structure

The client keeps one shared New Task/detail component with explicit regions:

- `task-editor` for title and Description;
- `task-sidebar` for Properties and Relationships; and
- `task-actions` for feedback and task actions.

The form remains one semantic `<form>`. Relationship buttons use
`type="button"`, and combobox key handling prevents Enter from accidentally
submitting the task.

The relationship controller mounts into the sidebar without reconstructing the
form. Polling and dependency refreshes continue to preserve the form DOM node,
unsaved field values, combobox queries, and valid selections.

## Existing task behavior

Existing tasks continue to use immediate Git-durable relationship mutations:

- Depends On add/remove uses the current task as the dependent.
- Blocks add/remove reverses the orientation and mutates the selected blocked
  task.
- Successful mutations refresh active and deleted task documents before
  rerendering.
- Warnings and failures remain in the initiating relationship group.

Only the layout and compact row presentation change.

## New Task relationship drafts

New Task presents both Depends On and Blocks as active controls. Because the
task does not yet have an ID, these controls modify client-side draft sets:

- `draftDependsOnIDs`
- `draftBlocksIDs`

Adding a candidate creates a compact draft row in the appropriate group.
Removing a draft row only changes local state. No dependency endpoint is called
before task creation.

Candidate filtering uses the latest reconciled active/deleted task context:

- only active tasks are eligible;
- a task already staged in that direction is excluded; and
- the two directions remain independent.

The current-task exclusion does not apply until the new task has an ID.

## Create and relationship persistence flow

When Create is pressed:

1. Disable task and relationship actions for the active form.
2. Create the task through the existing version-1 task-creation endpoint.
3. Use the returned durable task ID to apply all staged relationships through
   the existing bodyless nested endpoints:
   - Depends On:
     `PUT /api/tasks/<new-id>/dependencies/<prerequisite-id>`
   - Blocks:
     `PUT /api/tasks/<blocked-task-id>/dependencies/<new-id>`
4. Attempt every staged relationship and collect successes, warnings, and
   failures.
5. Refresh active and deleted task state once.
6. Open the new task's detail view.

Relationship operations span multiple task refs and are not atomic. The UI must
not imply rollback when only part of the relationship set succeeds.

## Failure behavior

### Task creation failure

If task creation fails:

- remain on New Task;
- preserve title, Description, status, priority, labels, both draft sets,
  combobox queries, and valid selections; and
- show the task-creation error in the form feedback region.

No relationship mutation is attempted.

### Partial relationship failure

If the task is created but one or more relationship mutations fail:

- keep the created task;
- keep successful relationships durable;
- refresh and open the new task's detail view;
- show a clear partial-success message;
- show each failed relationship as an unsaved draft row in its original group;
  and
- provide Retry and Remove actions for failed draft rows.

Failed drafts are client-session state. A full page reload may discard the
unsaved drafts, but it must never remove relationships that were already
written durably.

### Refresh failure after durable creation

If the task is created but the latest task state cannot be refreshed:

- remain on the current view;
- report that the task was created durably but could not be refreshed; and
- provide a retry path that refreshes the existing task rather than creating a
  duplicate.

## Accessibility

- Sidebar sections use headings and an `aria-label` that identifies task
  properties and relationships.
- Existing combobox/listbox/option semantics remain intact.
- Compact relationship rows keep linked titles, full IDs, state text, and
  explicit Remove/Retry button labels.
- Busy state disables all actions that could duplicate task creation or
  relationship writes.
- Group-local feedback remains in polite live regions.
- Keyboard focus is moved to the first failed draft action after a partial
  failure only when navigation would otherwise lose the initiating context.
- Mobile DOM order matches visual order.

## Responsive behavior

Desktop verification targets:

- 1440×900
- 1280×800
- 1280×600

Mobile verification targets:

- 390×844
- 360×640

At desktop sizes:

- the route uses available horizontal space;
- the main column is wider than the old 48rem form's individual field area;
- Description has extra height;
- the footer remains visible at ordinary viewport heights; and
- the sidebar does not reduce Description height.

At mobile sizes:

- the form is one column;
- no document-level horizontal overflow exists;
- Description, Properties, Relationships, and actions remain reachable; and
- relationship listboxes fit within the viewport.

## Testing

### Executable client tests

- New and existing task routes render the same sidebar shell.
- Desktop region classes and mobile DOM order are stable.
- Description retains its flexible field hook.
- Existing relationship mutations retain correct orientation and refresh
  behavior after moving into the form/sidebar.
- Enter in either combobox cannot submit the task form.
- New Task stages Depends On and Blocks without network calls.
- Draft removal is local.
- Create applies both endpoint orientations with the returned task ID.
- Complete success refreshes once and opens detail.
- Task-creation failure preserves all fields and drafts.
- Partial relationship failure preserves failed draft rows on detail and
  exposes Retry and Remove.
- Retry writes only the failed edge.
- Refresh failure after creation cannot create a duplicate task.
- Polling and navigation cannot write into a detached form/controller.

### Browser layout verification

Use real browser computed layout and screenshots to verify:

- wide desktop proportions;
- Description height and internal scrolling;
- footer visibility;
- sidebar placement and compact relationship density;
- constrained-height reachability; and
- mobile stacking and horizontal containment.

### Repository verification

- focused web/core/CLI tests;
- `go test ./... -count=1`;
- `go vet ./...`;
- production build;
- `git diff --check`; and
- independent whole-branch review after merging current `main`.

## Documentation

Update the README web-board section to explain:

- the wide main/sidebar task layout;
- Properties and Relationships placement;
- both relationship directions during task creation;
- staged relationship persistence after Create; and
- explicit partial-success behavior.

The earlier statement that dependency editing during task creation is out of
scope is superseded by this approved design.
