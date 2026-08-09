# Optimistic Web Mutation Queue Design

## Goal

Make the board respond to a mutation immediately, while HTTP success keeps
meaning the change is durable in Git.

Today every web mutation is a round trip the user watches. `mutateTask` sends
the request, the drop marker is already cleared, and the card sits in its old
column until the response lands and a full refresh re-renders. A poll firing
during that window re-renders the *pre-mutation* board, so the card visibly
snaps back before it moves.

This adds a client-side queue that renders the intent at once, serializes
intents per task, persists independent tasks concurrently, reconciles confirmed
responses, and rolls back what fails without discarding what followed. It also
gives the server the two things that make the queue safe: a client-observed head
so a stale view is reported rather than silently overwritten, and publication
through the sync watcher so the round trip being hidden is short in the first
place.

## Current Behavior

`mutateTask` (`internal/webui/assets/index.html:1150`) is the single funnel for
all nine mutations. Every call site awaits it and then calls `refresh()`.

`latestTasks` (`index.html:217`) is the whole client model. `refresh()` replaces
it wholesale (`index.html:1420`), and `render()` (`index.html:1385`)
`replaceChildren`s every column once a second. Nothing survives a refresh, and
there is no per-task identity, version, or dirty flag on the client.

Correctness is held by a generation counter, not by an `AbortController`:
`taskRefreshGeneration` is bumped synchronously at call time, and a losing
refresh returns `{status:"superseded"}` without touching state.

Three facts make this work smaller than it looks:

1. **The head is already on the wire.** `core.Task` carries `Head` and
   `HistoryGeneration` (`internal/core/task.go:152`), so `/api/tasks` and every
   mutation response already serialize them. The client simply never reads them.
2. **`stale-write` already maps to HTTP 409** with `error.category` in the
   envelope (`internal/webui/handler.go:593`). `mutateTask` throws the category
   away and keeps only the message, which is the one thing preventing a
   retry-versus-rollback decision.
3. **The deferral path already exists.** `taskSession` probes for a watcher,
   writes, nudges, and falls back to an inline push when the nudge fails
   (`internal/cli/autosync.go:111-222`).

## Publication: nudge, do not fetch-and-push inline

`runServe` builds its nine handler closures over `service.*Mutation` directly,
with no synchronization at all. A web mutation is therefore published only by
the hosted loop's next scheduled tick, up to five seconds later.

The obvious fix — apply the CLI's fetch-before/push-after to each handler — is
the wrong one. It would put two network round trips inside every request,
roughly 530 ms and 16 Git processes measured on the CLI path, which is precisely
the latency this queue exists to hide. Adding the queue and then reintroducing
the delay behind it would be self-defeating.

Instead a handler writes locally and nudges. The nudge is a receipt-only
exchange, so the handler returns as soon as the write is durable and publication
follows behind it.

The client for that nudge is the published socket, not the in-process loop.
`syncloop.Run` is fire-and-forget and exposes no in-process handle, and more
importantly `serve` runs **no** loop of its own when an external
`workbook sync --watch` already owns the repository. Dialing the pointer file
works in both cases and reuses the path the CLI already exercises, so there is
one code path rather than two and no assumption that the in-process loop exists.

Failure semantics match the CLI exactly: no watcher, a dead socket, a stale
status, or a failed last sync all fall back to an inline `PushTask`, and a
`core.WarningAutoSync` reaches the browser through
`TaskMutationDocument.Warnings`, which the client already renders.

## The client-observed head

A mutation request gains an optional `expectedHead`. The server compares it
against the parent snapshot it resolved and returns `stale-write` when they
differ.

Without it, concurrent web mutations are last-writer-wins from the client's
perspective. The server's existing compare-and-swap only catches the ref moving
*during* its own write, a window of milliseconds; a browser holding a board from
four seconds ago is not stale by that measure and silently overwrites.

**The queue is what makes `expectedHead` well-defined.** An intent is sent only
after the previous intent for that task has confirmed, and it carries the head
that confirmation returned. Without per-task serialization the client would have
no single head to name while its own writes are still in flight, and the field
would be unusable.

`expectedHead` is optional, so a client that omits it keeps today's behavior.
That matters because `decodeRequest` uses `DisallowUnknownFields`
(`handler.go:526`), so the field has to exist server-side before any client
sends it, and the two halves land in separate commits.

Both of the client's editing paths send it: the board's drop sends the head the
card was rendered from, and the detail form sends the head it rendered.

**The detail form sends only the fields it changed.** Sending all five on every
save is last-writer-wins even when the heads agree, because re-asserting a title
the form merely displayed is indistinguishable from editing it, and a
description-only save would revert a rename that landed beside it. Diffing
against the values the form rendered is what makes `expectedHead` worth having
on this route rather than a formality. A save that changes nothing is not sent
at all: the server refuses an update with no operations, correctly, and an
untouched form is finished rather than broken, so it returns to the board the
way an accepted save does.

The dependency routes are the exception. They require a completely empty body
(`requireEmptyRequestBody`, `handler.go:515`), so they cannot carry a head
without changing their shape. They stay as they are: a dependency edge is an
`add`/`remove` on a set, which converges rather than conflicting, so the
protection `expectedHead` buys does not apply to them. Saying so here is
cheaper than a reader discovering the asymmetry and assuming it was an
oversight. Delete and restore take no body either, and want none: a tombstone is
not a field edit, and deleting a task someone else just changed is the thing the
user asked for rather than an accident to catch.

## The queue

```js
pendingIntents: Map<taskID, {
  queue:    Intent[],   // unsent, in submission order
  draining: boolean,    // a drain loop owns this queue
  head:     string,     // head to send with the next request
}>
```

`draining` marks the loop, not an open request. The distinction is not
cosmetic: the head moves *between* sends — forward on a confirmation, sideways
on a re-base — and a flag that meant "a request is open" would invite exactly
the background head-adoption the conflict rule below rejects.

An `Intent` is a field-level change plus the request that expresses it, so
folding it over a task is a pure function and dropping one from the middle is
well defined.

**Rendering.** `render()` and `renderRoute()` read a projection rather than
`latestTasks` directly: server tasks with every pending intent for that task
folded on top, in order. Because the fold is applied at render time rather than
written into `latestTasks`, the one-second poll can keep replacing the model
wholesale and pending work survives untouched. This is the whole reason the
layer sits where it does.

**Ordering.** One task drains serially; independent tasks drain concurrently.
Serial-per-task is required for `expectedHead` to mean anything, and concurrency
across tasks is what keeps a slow write to one task from stalling the board.

**Reconciliation.** A confirmed response replaces the task in `latestTasks` and
records the returned head as the queue's next `head`, then drops the intent. If
the queue is now empty the task is removed from the map entirely, so a quiet
board carries no overlay.

**Rollback.** A failed intent is dropped and later intents for that task are
kept and re-derived over the new server state. Dropping the tail as well would
be simpler, and wrong: the user's later actions were separate decisions, and
discarding a priority change because an unrelated status move failed is exactly
the "clobbering later actions" this is meant to avoid. Where a later intent
genuinely depends on the failed one — a position within a status the task never
reached — the server rejects it in turn and it rolls back on its own merits.

**Conflict.** A `stale-write` category rolls the intent back, forces a refresh,
*waits for it*, and re-bases the queue's head from it, so the intents behind it
retry against current truth instead of failing identically. Waiting is the load-
bearing part: a re-base that lands after the next request has already gone out
is not a re-base, and the first shipped version proved it by firing the refresh
and continuing, which dropped every intent behind a conflict.

Nothing else moves the head. A poll is deliberately not allowed to: one that
left before the last confirmation returned carries an older head, and adopting
it would refuse the next intent for no reason at all. The re-base happens where
the answer is known to be newer than the head that was just refused.

**Reporting** happens on the card the refusal concerns, worded to distinguish
"this task changed elsewhere" from an ordinary failure, and written *after* the
forced refresh rather than before it, so the report is written against the board
the reader ends up looking at.

The first shipped version reported on the board-level stale banner and did not
survive contact with the poll. That banner says the board is showing state older
than the server's, and a successful refresh clears it — correctly, because the
board is current again. A refusal parked there was therefore erased within a
second whether or not anyone had looked at that corner of the screen, which on a
shared board means the conflict the queue exists to surface was never surfaced.
Ordering the write after the forced refresh bought the report a chance to be
drawn and no chance at all to be read.

A card report is held in `taskFailureReports`, keyed by task ID, for the same
reason a pending intent is held outside the model: every poll replaces
`latestTasks` wholesale, so a report written into a task would be erased by the
next one. It is folded in at render time, takes part in the card signature so an
unchanged report costs one string comparison, and is built once with the card
rather than rebuilt beside the description and the labels — the region holds a
control, and a rebuild would drop the Dismiss button out from under the caret.
Dismissing leaves the caret on the card.

One report per card, newest wins, and the reader's next change to that card
takes it away. A refused *create* stacks its reports because each can hold the
only copy of what was typed; a refused intent holds nothing — it rolled back,
and the card shows what the server holds — so a second report on one card is the
same news twice.

What clears a report is the next decision, not the next confirmation. Queueing
an intent for that card is the reader answering the refusal, and from that
moment the card is drawing an optimistic value the server has not accepted,
which is precisely what the standing report says it is not doing; if the answer
is refused in turn, the report comes back saying so. A confirmation is
deliberately not enough: an intent already queued when the refusal arrived was
never an answer to it, and a placement refused as illegal while a second
placement of the same card succeeds is still news. Clearing on any confirmation
would erase the only report of a change the queue dropped.

The banner keeps its original job and its original single writer: a refresh that
failed. That separation is the point. "This board is stale" is a condition a
successful poll ends, and "your change was refused" is an event only a person
can acknowledge.

**A report whose card leaves the board.** The likeliest reason a change is
refused is also a reason the card is about to go: another clone deleted the
task, and the next poll takes the card with it. A render that finds a report
whose task it no longer draws moves it to the notice — the surface above `main`
that outlives every route and is cleared only by a person — rather than letting
it leave with the card, which would put the reader back where the banner left
them. `pendingTaskMessages` still serves the detail view; the two surfaces
report to two different readers.

What moves is not what the card was saying. A card can point at itself: "the
card shows the version the server holds" is true of a card and meaningless in a
notice above an emptied column. So each report is built as two wordings of one
event, and the lifted one names the task by ID prefix and title and says the
board no longer carries it. Both are built when the refusal arrives, before the
refresh a `stale-write` forces — that refresh is exactly what takes a deleted
task out of the model, and a report that waited until lift time to look up what
it was about would find nothing to name.

Two details keep the move honest. A dragged card stays attached even when the
model stops naming it, so a report on one is not lifted while the reader is
still holding it; the render after `dragend` does it properly, and the
alternative prints the same sentence on the card and in the notice at once. And
when the removed card held the caret, the caret goes to the lifted report's
Dismiss control rather than to the document body — the same handoff a refused
create makes to Restore draft, for the same reason: the report is now the only
thing left to act on.

**A failure with the detail form open.** The detail route projects pending
intents, so a form opened over a pending change shows the optimistic value. When
that intent fails, the form is showing a value the server refused: it reads as
saved state, and saving it would persist the refusal as a real edit. So a failed
intent re-renders the open detail route for its task and reports why in the
form.

That re-render discards anything typed into the form since it opened. Keeping
those edits and correcting only the projected fields would be better, and is
left to the render work rather than done here; a form that lies about what is
saved is the worse of the two. It also detaches a save that happens to be in
flight, and the detached save reports nothing when it lands, so a user who was
saving at that moment is told their board change failed and nothing about their
save. That is a second reason the withdrawal wants the render work rather than
another patch here.

**A refused save from the form itself** is treated differently, and should be.
The edits stay, the head is re-based from the same forced refresh, and the
baseline the form diffs against stays where it was, so a deliberate re-save
applies the same fields to the version that now exists and the concurrent edit
that caused the refusal survives it.

**The form follows its own writes.** The sidebar's dependency edges are the
other thing that moves this task's head, and a "Depends On" edge is recorded on
the dependent — which is the open task. Adding or removing a prerequisite is
therefore a write to the task being edited, and a form that kept proposing the
head it rendered would refuse the user's own next save and blame it on someone
else. So a dependency mutation hands the confirmed head back to the form when,
and only when, the edge was written to this task; the mirrored "Blocks"
direction writes to the other task and leaves this one's head alone.

One known edge remains: the form sends the head it rendered, and a *board*
intent for the same task confirming while the form is open moves the server's
head without moving the form's. The first save is then refused once and succeeds
on the retry. That is the conflict path working rather than a lost update, and
adopting confirmed heads into an open form belongs with the render work too.

**Labels are compared as a set**, because the server stores them as one
(`normalizeLabels` sorts and de-duplicates). A positional diff would call
"web, docs" an edit to "docs, web", send it, and land the server's "update does
not change task" in the form verbatim — the raw error the empty-diff guard
exists to keep out of it.

## Auto-sync state in the UI

The board reports whether it is deferring to a watcher or publishing inline, and
offers a toggle.

The toggle now means something narrower than when this task was written. It is
no longer "does the board synchronize at all" — it always does — but "does it
hand publication to a watcher or wait for the push." Inline is the honest choice
for someone who wants the response to mean *origin has it*; deferred is the fast
one.

State is read from the same probe the mutation path performs, so the indicator
reflects what the next mutation will actually do rather than a separately cached
opinion. A board set to defer still publishes inline when no watcher answers,
and the indicator says so rather than reporting a mode the board cannot honor.

The mode lives in memory for the life of the server. It is a preference about
how this board behaves, not a project setting, and `workbook config set
auto-sync` already means something different — whether CLI mutations publish at
all. Overloading it would make one name mean two things.

The capability arrives through a separate `NewHandlerWithSyncControl`
constructor rather than two more positional parameters on one that already
carries nine.

## Out of Scope

**Server-sent events or any change to the one-second poll.** The poll is what
makes the queue's overlay design work, and replacing it is a separate question.

**Undo.** An inverse operation needs no queue machinery and is tracked
elsewhere.

**Offline queueing across a page reload.** Pending intents live in memory and
are lost on reload, which is the same guarantee the current board gives.

**Changing conflict semantics.** `expectedHead` makes an existing conflict
observable; it does not add a new kind.

## Testing

Go handler coverage, extending `internal/webui/handler_test.go`:

- `expectedHead` matching, mismatching, and omitted, for each request shape that
  carries it;
- a mismatch surfaces as `stale-write` with HTTP 409 and the category intact;
- dependency routes still reject a non-empty body.

Client coverage through the existing Node harness, which skips when `node` is
absent and drives a hand-written fake DOM (`handler_test.go:4173`):

- an intent renders before its response arrives;
- a poll landing mid-flight does not revert the optimistic card;
- two intents on one task are sent serially, the second carrying the head the
  first returned;
- intents on different tasks are in flight together;
- a failed intent rolls back and leaves a later intent for the same task
  standing;
- a `stale-write` response rolls back, refreshes, reports, and re-bases the
  queue's head so the intent behind it is sent against the refreshed head;
- a refusal is reported on the card it concerns, survives repeated poll ticks
  driven through the captured interval callback, and leaves only when it is
  dismissed — the report the banner could not keep;
- a report whose card the board stops drawing moves to the notice exactly once
  rather than leaving with the card, naming the task and taking the caret the
  removed card was holding, and it is not lifted out from under a drag;
- a lifted report still names a task the refusal's own forced refresh removed
  from the model;
- changing a card again clears the refusal standing on it, while an intent that
  was already queued confirming does not;
- a failed intent re-renders the detail form open on its task, so the form stops
  showing the value the server refused;
- the detail form saves only the fields it changed, carrying the head it
  rendered, and sends nothing at all when nothing changed — including a labels
  field that was reordered rather than edited;
- a refused save keeps its edits, re-bases, and applies only those fields on the
  retry;
- a dependency edge written to the open task moves the head the form proposes,
  and the mirrored direction does not — the second read from a save the server
  refuses between the two edits, because the edge that legitimately moves the
  head afterwards would hide a wrong adoption from the final save.

Integration coverage in `internal/cli`, following the existing `TestRunServe*`
pattern of asserting Git state after a real HTTP call:

- a web mutation reaches `origin` without waiting for a scheduled tick, which is
  what proves the nudge rather than the timer published it;
- with no watcher, a web mutation still publishes inline and the response
  carries the auto-sync warning when `origin` is unreachable.
