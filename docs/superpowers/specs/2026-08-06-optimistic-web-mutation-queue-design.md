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

That knowledge is exactly what a forced refresh that never read the board does
not deliver, and it is the one case the re-base cannot cover. Such a refresh
leaves the model at the last successful poll, whose head for this task is the
head the server has just refused, so the head it offers is the refused one under
another name. The queue therefore takes no head from it and sends nothing more:
the intents behind the refusal are refused where they stand, with the same
rollback, and one report says the board could not be read instead of claiming
the card shows what the server holds.

The intent immediately behind the refusal has nowhere to go. Sent against the
refused head it fails identically, which is the failure the re-base exists to
prevent; sent unguarded it overwrites the change that caused the refusal, which
is what the guard is for. The ones after *it* are a real trade rather than a
free one, because every refusal forces a refresh of its own: a one-request
outage
could end in time for the third intent to be re-based and land, and with the
queue cleared it no longer does. They are dropped anyway. The outage is reported
and the board rolls back to what it read, so the loss is visible and the change
is re-doable — which is worth more than a queue that goes on writing while it
cannot see the board it is writing to.

Nothing is stranded by stopping: the poll keeps running, the model converges on
the first one that gets through, and the reader's next change is queued against
the head it read.

**What the queue asks of a refresh is whether it read the board, not what it
returned.** `refresh()` reports the two separately, because a refresh that read
the board, replaced the model with it and cleared the stale banner still answers
`superseded` when the relationship context it goes on to refresh is overtaken —
and an ordinary route render is enough to do that, since rendering one drops the
controller that refresh is holding. Read as an outage, that would clear a queue
whose re-base was already sitting in the model, and print "the board could not
be refreshed" under a banner saying the opposite. So the board read answers for
itself, and every other consumer of the status — the created-task recovery, the
restore control, and the two dependency-mutation paths — goes on reading the
status unchanged.

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
that intent fails, the form is displaying a value the server refused, so the
fields still displaying it are corrected where they stand and the form says why
they moved.

What the correction buys is display accuracy, not safety. A save from this form
sends only the fields that differ from the ones it rendered
(`changedTaskValues`), and a control nobody has touched does not differ from
them, so the refused value was never reachable by a save either way. What was
wrong was narrower: the form read as saved state while showing a value the
server had thrown away. This section used to justify the withdrawal by saying a
save would persist the refusal as a real edit, which stopped being true the
moment the form began diffing against what it rendered; the code comment carried
the same stale reason, and both are corrected here alongside the behavior.

Corrected in place rather than re-rendered, because a rebuilt form pays for that
accuracy with everything typed into it since it opened — a long description is
exactly the edit that takes long enough for a board intent to fail underneath it
— and detaches a save in flight along with it. A detached save can only report
from outside the form it was made in (below), so a reader who was saving at that
moment would read about their board change on the form they are standing at and
about their own save somewhere else entirely, with the edits that save carried
gone either way. The correction leaves the node, its listeners, and
the caret alone: a field the save would not send is one nobody has touched and
follows the task the board now holds, a field the save would send is an edit in
progress and stays as typed, and the baseline the diff is measured against moves
with the display, so the next save carries exactly this reader's own edits
against the head that now exists.

Two fields are asked that question in their own way. A status this client has no
option for — one a newer Workbook wrote — is displayed the way the form displays
it on a first render, by growing the disabled placeholder that names it, so the
correction is shown rather than skipped. Labels are the reverse: a save folds the
text still in the label input into the set, so the diff calls the field edited as
soon as a reader types a letter, and a reader partway through a word has decided
nothing about the set. Their chiclets are what decides it, and the word in
progress stays in the input either way. Underneath both is one rule the
correction never breaks: a control that goes on displaying the refused value
keeps the baseline it was measured against, because moving the baseline under it
is the one thing that would turn that value into an edit the next save sends —
and moving it under an *untouched* label set would send a set the reader was
never shown, silently dropping a label another clone had added.

A task the board has stopped carrying, which is what a refusal from another
clone's deletion looks like, has nothing to correct the form against. The form
and its unsaved text stay exactly as they are and say the board no longer
carries the task: a reader mid-sentence needs their sentence more than they need
a "task not found" page.

What the form says about the fields it did correct turns on the same read the
card's report does, and is passed the same answer. The correction takes those
fields out of the model, and the model is the server's version only because the
refresh the refusal forced replaced it with one. When that refresh could not
read the board, the fields the reader has not touched are the last successful
poll's, so "the fields you have not edited now show the version the server
holds" claims precisely the request that just failed. The form says the board
could not be refreshed and that those fields may be out of date instead —
the wording the card beside it uses for the same event — and names the changes
in the plural, because the queue behind the refusal stopped there rather than
write to a board it cannot see. The correction itself still happens: the refused
value is gone from the display either way, and only the claim about what
replaced it changes. The banner keeps its single writer through all of this; it
is already saying the board is behind, and this sentence is about these fields.

**A refused save from the form itself** is treated differently, and should be.
The edits stay, the head is re-based from the same forced refresh, and the
baseline the form diffs against stays where it was, so a deliberate re-save
applies the same fields to the version that now exists and the concurrent edit
that caused the refusal survives it.

When that refresh does not read the board, the form keeps the head it rendered
and says the latest version could not be loaded rather than asking for a
re-save. The message is the whole point: "save again to apply them to the
version the server holds" is an invitation to a refusal, because no version was
read and the head the form is proposing is the one the server has just refused.
The banner already says the board is behind while that lasts, and the next
refusal whose refresh does read the board re-bases the form and brings the
invitation back with it.

A refresh that *does* read the board and finds no head for the task is the same
invitation withheld for the opposite reason. The board is the server's, so
nothing here is an outage — `/api/tasks` carries only tasks that have not been
tombstoned, which makes the absence proof that another clone deleted this one.
There is nothing to re-base onto, the head the form proposes stays the refused
one, and a save made against it is refused identically for as long as the task
is gone. Neither of the other two sentences is true in that state: the version
the server holds does not exist, and the board is not behind. So the form says
that instead — the task was deleted elsewhere, the server holds no version of it
to save to, and the edits in front of the reader are theirs to copy rather than
to re-save.

Those edits are deliberately not offered back the way a refused create's are.
The only machinery that hands typed values back puts them in a New Task form,
which would create a *different* task — a new ID carrying none of this one's
history — and choosing that over restoring the deleted task is the reader's
decision, not a consolation to hand someone mid-sentence. The values are still
in the form either way; what changed is that this form can no longer save them.

**A save detached while it is in flight** still reports its outcome. The reader
can stop waiting on a slow request and go back to the board, or a draft restore
can render a New Task form over the route, and the request is still open when
the node this form reports into leaves the document. The handler used to return
without a word at exactly that point, which is the worst moment to say nothing:
the reader walked away believing a save was on its way, and nothing on screen
ever contradicted them.

The refusal goes to the two surfaces that outlive a route instead. The notice is
read from wherever the reader ended up, so it names the task and offers the way
back to it; the task's own form says the same thing the next time that task is
opened, because that is where a message about a save belongs. The way back is an
offer only while there is a task at the end of it. A `stale-write` refusal
forces a refresh before the form is given up on, and one that read the board and
no longer found the task is proof that there is none: followed, that link lands
the reader on "Task not found", the page this client goes out of its way not to
drop anyone on. So the link goes, the report says there is nothing left to open,
and the reason it ends on names the deletion rather than the vaguer conflict the
refusal's own category would have described. The message is still staged on the
task's form, because restoring the task is what makes that form openable again
and this is exactly what its next reader needs to know. A conflict is
described rather than quoted — the server's sentence names the head the request
carried, which is this client's bookkeeping — while every other refusal is
quoted, and quoted last: a server message is a Go error, lower case and
unpunctuated, and spliced between two sentences of this client's own it runs
straight into the one after it, which is why a refused create ends on its reason
too. The edits are named as lost
rather than held anywhere, since they went with the node the moment the route
changed and a report offering to restore them would promise something this
client cannot do.

A save that lands after its form is detached is the same question answered the
other way. Nothing is announced, because an accepted save is never announced
here — returning to the board is the whole report — but the return itself is
dropped, so the route the reader deliberately went to while waiting is not taken
away from them a second later. The refresh behind it still runs, which is the
part that had to keep happening.

A create is deliberately left out of this. Navigating away from a New Task form
discards its draft whatever the server answers, so the loss there is the
navigation rather than the refusal, and the create's own reporting path is the
one that hands drafts back.

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
- a `stale-write` whose forced refresh fails re-bases nothing: the intent behind
  it is never sent against the head the server refused, the card says the board
  could not be refreshed rather than that it shows the server's version, and the
  first poll that gets through both converges the board and takes the reader's
  next change against the head it read;
- a `stale-write` whose forced refresh read the board and was then superseded
  only in its relationship half — the reader opens the task's page while the
  write is in flight and returns to the board while the deleted-task read is —
  re-bases on that read and sends the intent behind it, with no outage reported
  over a board that is showing the server's version;
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
- a failed intent corrects the detail form open on its task where it stands: the
  refused value goes and the fields nobody touched follow the version the server
  holds, while the unsaved edits, the caret, and the node itself stay, and the
  save afterwards carries exactly those edits against the head the refusal
  established;
- a board refusal arriving while a save from that form is open leaves the save
  attached, so it still reports into the form when it lands; one arriving after
  the task has left the board leaves the form and the text in it standing;
- the two fields with a correction of their own: a status this client has no
  option for is named by the placeholder rather than left showing the refused
  one, and a label set nobody has touched follows the server while the word
  half-typed into the input stays where it is and the save carries both;
- the detail form saves only the fields it changed, carrying the head it
  rendered, and sends nothing at all when nothing changed — including a labels
  field that was reordered rather than edited;
- a refused save keeps its edits, re-bases, and applies only those fields on the
  retry;
- a save refused after its form was detached reports in the notice rather than
  returning silently: it names the task, offers the route back to it, says why
  the save was lost without repeating the server's head sentence, and stages the
  same message on the task's form, which reads it exactly once;
- a save detached while the refusal's forced refresh is still open, whose
  refresh then reads a board the task has left, offers no route back at all: the
  report says there is nothing left to open and ends on the deletion rather than
  on the conflict, because the link it drops would have led to "Task not found";
- a save that lands after its form was detached leaves the reader on the route
  they went to instead of returning them to the board;
- a refused save whose forced refresh fails keeps the head the form rendered
  rather than the one the last poll left in the model, says the latest version
  could not be loaded instead of inviting a re-save, and re-bases as soon as a
  later refusal's refresh lands;
- a refused save whose forced refresh reads the board and finds the task gone
  keeps the reader in the form with their text intact, says the task was deleted
  elsewhere rather than that the board could not be read, and invites no retry —
  the retry, made anyway, carries the same refused head and is answered with the
  same sentence;
- a dependency edge written to the open task moves the head the form proposes,
  and the mirrored direction does not — the second read from a save the server
  refuses after the mirrored edge is written and before the removal that
  legitimately moves the head, because that removal would hide a wrong adoption
  from the final save.

Integration coverage in `internal/cli`, following the existing `TestRunServe*`
pattern of asserting Git state after a real HTTP call:

- a web mutation reaches `origin` without waiting for a scheduled tick, which is
  what proves the nudge rather than the timer published it;
- with no watcher, a web mutation still publishes inline and the response
  carries the auto-sync warning when `origin` is unreachable.
