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

The dependency routes are the exception. They require a completely empty body
(`requireEmptyRequestBody`, `handler.go:515`), so they cannot carry a head
without changing their shape. They stay as they are: a dependency edge is an
`add`/`remove` on a set, which converges rather than conflicting, so the
protection `expectedHead` buys does not apply to them. Saying so here is
cheaper than a reader discovering the asymmetry and assuming it was an
oversight.

## The queue

```js
pendingIntents: Map<taskID, {
  queue:   Intent[],   // unsent, in submission order
  inFlight: Intent | null,
  head:    string,     // head to send with the next request
}>
```

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
and reports it on the task rather than in the global banner, because it is about
one card and the board around it is fine.

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
opinion.

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
- a `stale-write` response rolls back, refreshes, and reports on the task.

Integration coverage in `internal/cli`, following the existing `TestRunServe*`
pattern of asserting Git state after a real HTTP call:

- a web mutation reaches `origin` without waiting for a scheduled tick, which is
  what proves the nudge rather than the timer published it;
- with no watcher, a web mutation still publishes inline and the response
  carries the auto-sync warning when `origin` is unreachable.
