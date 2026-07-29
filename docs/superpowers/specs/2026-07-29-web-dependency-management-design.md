# Web Dependency Management Design

## Goal

Make dependency state visible and safely editable from Workbook's local web
application. Board cards explain prerequisite progress and distinguish Ready
tasks that cannot be selected by `workbook next`. Task detail pages show both
directions of each relationship and let a user add or remove active
relationships without losing unsaved task-form edits.

## Scope

This increment adds:

- dependency progress to board cards that have prerequisites;
- a visible Ready-but-ineligible state when prerequisites are unfinished;
- editable **Depends On** and **Blocks** groups on the existing task detail
  route;
- an accessible integrated combobox for adding a relationship in either
  direction;
- dedicated Git-durable dependency add and remove HTTP mutations;
- explicit unavailable and deleted relationship states; and
- handler, core, presentation, and executable-client coverage for the complete
  flow.

Dependency editing during task creation remains out of scope. The board does not
gain dependency-based sorting, and this increment does not add a separate
dependency graph or depended-on-by field to the durable task format.

## Canonical Relationship Model

Workbook continues to store one directed edge:

```text
A depends on B
```

The edge is stored only in `A.dependencies`. The inverse view is derived:

```text
B blocks A
```

There is no separate durable `blocks` collection. That avoids duplicate state
and makes it impossible for the two directions to disagree.

Both detail-page groups edit this same edge:

- adding `B` under A's **Depends On** group adds `B` to `A.dependencies`;
- removing it removes `B` from `A.dependencies`;
- adding `A` under B's **Blocks** group adds `B` to `A.dependencies`; and
- removing it removes `B` from `A.dependencies`.

The inverse group is computed from all known active and tombstoned tasks whose
dependency collections contain the current task ID.

## Board Presentation

Cards with no dependencies keep their current markup and appearance.

Every card with dependencies shows a compact progress line:

```text
2 of 3 prerequisites complete
```

A prerequisite counts as complete only when it resolves to an active task whose
canonical status is `done`. Missing, tombstoned, and active non-Done
prerequisites count toward the total but not the completed count.

A Ready card whose completed count is below its total also shows:

```text
Waiting on dependencies
```

The text and markup, not color alone, communicate ineligibility. Other statuses
still show progress but do not receive the Ready-specific waiting label.

Server-rendered cards and client-refreshed cards use the same presentation
calculation so polling cannot change the meaning of the summary. The existing
`workbook.tasks` version 1 document keeps its format and version while each task
presentation entry gains the dependency summary needed by the browser.

## Detail Relationship Groups

The existing edit form remains responsible only for title, description, status,
priority, and labels. A separate relationship area below it contains:

1. **Depends On** — prerequisites stored on the current task.
2. **Blocks** — tasks whose dependency collections contain the current task.

An active relationship row includes:

- task title linked to `/tasks/<full-id>`;
- canonical status;
- priority;
- full task ID; and
- a Remove button.

The full ID remains visible even when a friendly title is available.

### Deleted and Unavailable Relationships

A tombstoned prerequisite remains visible in **Depends On** with its last known
title, status, priority, full ID, and a Deleted label. It remains removable
because removing the edge mutates the active current task, not the tombstone.

An unresolved prerequisite remains visible in **Depends On** as an unavailable
task with its full stored ID. It also remains removable. This prevents an
unresolvable edge from silently blocking the current task forever.

A tombstoned task that depended on the current task remains visible in
**Blocks**, but read-only. Removing that inverse relationship would require
mutating the tombstoned dependent task, which is forbidden by Workbook's
tombstone immutability rule. The UI explains why no Remove action is available.

An unavailable blocked task cannot be derived: the inverse view requires a
known task document whose dependency collection contains the current ID.

## Integrated Dependency Combobox

Each relationship group has one integrated combobox consisting of a search
input and a filtered popup listbox.

The **Depends On** combobox contains active tasks except:

- the current task;
- existing direct prerequisites; and
- tombstoned tasks.

The **Blocks** combobox contains active tasks except:

- the current task;
- tasks already blocked by the current task; and
- tombstoned tasks.

The browser searches case-insensitively by task title and full task ID. Each
option shows title, status, priority, and full ID. Canonical board order breaks
ties and keeps the choices deterministic.

The control follows the ARIA combobox/listbox pattern:

- the input has an accessible label, `role="combobox"`,
  `aria-autocomplete="list"`, `aria-controls`, and accurate
  `aria-expanded`/`aria-activedescendant` values;
- Arrow Down and Arrow Up move through visible options;
- Enter selects the active option;
- Escape closes the popup without selecting;
- pointer selection and keyboard selection produce the same value; and
- an empty result is announced without creating a selectable fake option.

The Add button stays disabled until the user selects a real candidate. Adding a
relationship clears the selection after the durable mutation and refresh
succeed. A rejected mutation retains the query, selection, and task-form draft.

## HTTP Mutation Contract

One nested relationship resource supports both directions:

```text
PUT    /api/tasks/<dependent-id>/dependencies/<prerequisite-id>
DELETE /api/tasks/<dependent-id>/dependencies/<prerequisite-id>
```

`PUT` calls `core.Service.DependMutation`. `DELETE` calls
`core.Service.FreeMutation`. The detail page reverses the IDs when editing the
**Blocks** group; no separate reverse-mutation endpoint is necessary.

Both methods take their IDs from the path and accept no request body. They
return the existing `workbook.task-mutation` version 1 document, including the
updated dependent task and any projection warnings. Invocation, validation,
not-found, stale-write, corrupt-data, and operational failures continue to use
the existing `workbook.error` version 1 document and HTTP status mapping.

Other methods receive `405 Method Not Allowed` with:

```text
Allow: PUT, DELETE
```

The server remains loopback-only and calls the core service directly; it never
shells out to the Workbook executable.

## Removing an Unavailable Prerequisite

`FreeMutation` currently resolves the prerequisite task before checking whether
the edge exists. That prevents removal when the current task stores a valid full
dependency ID whose task ref is unavailable.

The mutation will instead:

1. resolve and validate the dependent task;
2. if the supplied value exactly matches an ID already in its dependency set,
   remove that value without resolving the prerequisite task;
3. otherwise retain the existing prefix-resolution and idempotent no-op
   behavior; and
4. write the same `set.remove dependencies` operation through the existing
   Git-durable compare-and-swap path.

This change removes only the relationship. It does not recreate, repair, or
otherwise mutate the unavailable task.

## Client Data and Mutation Flow

The normal active-task refresh remains the source for board cards, active
relationships, and combobox candidates. Detail rendering also loads the
existing deleted-task document so tombstoned relationships can be identified
and labeled.

For either direction, the client:

1. keeps the current task form and its input elements mounted;
2. disables only the relationship action being submitted;
3. sends the nested `PUT` or `DELETE`;
4. validates the versioned response and captures projection warnings;
5. awaits a fresh active-task document;
6. refreshes deleted-task context when needed;
7. rerenders only the Depends On and Blocks area; and
8. presents success warnings or actionable errors in that area.

Because the task form is not reconstructed, unsaved title, description, status,
priority, and label values survive successful and failed dependency operations.
The canonical refreshed task document, rather than an optimistic local edit,
determines the relationship rows and candidate sets.

If another writer changes task data between selection and submission, the core
mutation re-resolves current state. Self-dependencies, cycles, deleted
candidates, disappeared candidates, and compare-and-swap races fail visibly
instead of guessing from stale browser data.

Removal stays idempotent. If the requested edge is already absent, the core
operation returns the observed dependent task without advancing Git.

## Error and Warning Presentation

Relationship errors appear next to the group that initiated the mutation and
remain available to assistive technology through a polite live region.
Validation messages from the versioned error document are shown verbatim when
safe, including cycle and self-dependency failures.

Projection warnings from a successful durable mutation are not treated as
failure. The relationship area states that the Git operation succeeded,
displays each warning, and refreshes canonical client state. Generic network or
malformed-document failures use a clear fallback message and retain all draft
fields and combobox input.

## Testing

Core tests prove:

- an exact stored dependency ID can be removed when the referenced task is
  unavailable;
- ordinary prefix resolution remains supported;
- absent-edge removal remains an idempotent no-op;
- tombstoned dependent tasks remain immutable; and
- the durable remove operation stays `set.remove dependencies`.

Presentation and handler tests prove:

- completed and total prerequisite counts include active, missing, and
  tombstoned cases correctly;
- Ready eligibility uses the same dependency semantics as `workbook next`;
- cards without dependencies remain unchanged;
- nested `PUT` and `DELETE` methods call DependMutation and FreeMutation with
  the exact dependent and prerequisite IDs;
- mutation and error documents retain format and version 1;
- wrong methods and malformed paths are rejected; and
- cycle, self-dependency, missing-candidate, and operational errors keep their
  category and actionable message.

The executable client harness proves:

- Depends On and Blocks rows derive from the same directed edge;
- active, tombstoned, and unavailable rows render with the intended actions;
- deleted blocked tasks are read-only;
- both comboboxes filter out self, existing relationships, and deleted tasks;
- text filtering and keyboard/pointer selection work;
- adds and removals send the correctly oriented nested request;
- successful operations await refresh before rerendering both groups;
- projection warnings remain visible;
- failures retain combobox state and unsaved task-form fields; and
- board refreshes preserve dependency progress and Ready waiting labels.

Final verification runs focused core, presentation, web-handler, and executable
client tests; the full Go test suite; `go vet ./...`; `gofmt`; a production
`go build`; and `git diff --check`.
