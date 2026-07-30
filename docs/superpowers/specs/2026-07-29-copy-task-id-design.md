# Copyable Web Task ID Design

## Goal

Make task IDs directly copyable in Workbook's local web UI without weakening
board-card drag behavior or requiring users to select ID text manually.

This increment covers task IDs on active board cards and on the active task
detail route. It does not change task storage, HTTP APIs, deleted-task cards, or
terminal output.

## Interaction

The shortened ID on each board card is a semantic button whose accessible name
identifies the full task ID and the copy action. Activating the button with a
pointer or keyboard writes the full ID to the clipboard. The button remains
inside the draggable card, so a drag that starts over the ID continues to move
the card. A drag gesture suppresses the corresponding click-to-copy action; a
later intentional click copies normally.

The active task detail route renders its full ID with the same semantic button
and copy behavior. Because the detail route is not draggable, it needs no drag
suppression.

The controls retain the current monospace ID presentation. CSS removes native
button border, fill, padding, and other button chrome so the IDs read as quiet
inline text similar to an un-underlined link. Hover changes the text to the
existing interface blue, the pointer cursor communicates clickability, and
`focus-visible` retains a clear keyboard outline.

## Clipboard Feedback

Each copy control owns a compact, polite live-region message positioned
immediately after the ID. The visible success message is:

```text
Copied
```

The message is absolutely positioned within the ID's inline group, so showing
or dismissing it does not change card, board-column, or task-form geometry. Its
accessible label includes the full task ID and outcome, for example, "Copied
task ID WB-01KYQXDDQXSE8WRW7XFKXZQ2DP." Copy feedback is short-lived but
remains visible long enough to read. Repeated copy actions on an ID replace its
previous message and restart that ID's dismissal timer.

If the Clipboard API is unavailable or rejects the write, the same inline
region shows "Copy failed" and its accessible label identifies the full ID that
could not be copied. Clipboard failures do not navigate, refresh, or mutate
task state.

## Client Structure

A shared helper accepts the full task ID and its copy control's status region,
calls `navigator.clipboard.writeText`, and renders success or failure feedback.
Both the server-rendered board template and client-rendered refresh cards expose
the full ID as data on the text-like copy button while displaying only the
server-provided actionable prefix.

Document-level click handling recognizes a copy control before same-origin link
navigation. The existing drag lifecycle records when a card drag began over a
copy control and prevents that gesture from being interpreted as a copy click.
No task mutation endpoint or service callback is involved.

## Accessibility

- Copy controls use native `button` elements and therefore support Enter and
  Space without custom key handling.
- Each board control's accessible name includes the full ID even though its
  visible label is shortened.
- Success and failure messages use a visible, per-ID `role="status"` region
  with polite live announcements whose accessible labels include the full ID.
- Hover is not the only affordance: the pointer cursor and keyboard focus
  outline remain available.

## Verification

Rendered-page tests verify text-like semantic copy buttons for initial board
cards and the detail view. The existing executable JavaScript harness verifies:

- clicking a shortened board ID writes the matching full ID;
- clicking a full detail ID writes that ID;
- successful copies render the inline accessible confirmation without a
  page-level status region;
- rejected clipboard writes render the corresponding inline accessible error;
- dragging from the board ID preserves card drag behavior without copying; and
- a later intentional click after the drag does copy.

The existing card-prefix regression test continues to prove that initial and
refreshed cards display server-provided actionable prefixes. Final validation
runs the full Go test suite, `go vet`, formatting, and `git diff --check`.
