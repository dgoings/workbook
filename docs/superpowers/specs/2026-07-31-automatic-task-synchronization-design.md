# Automatic Task Synchronization Design

## Goal

Make task mutations keep themselves in sync with `origin` instead of relying on
a developer or agent to remember `workbook push`. Task state that drifts between
clones is the source of the conflicts this project most wants to avoid, and the
current design places the entire burden of avoiding that drift on human
discipline.

A mutation fetches shared task refs, applies its operation to the refreshed tip,
and publishes only the ref it changed. `workbook next` fetches before answering
so two agents do not claim the same task. Configuration disables the behavior
globally or for one project, and `--no-sync` disables it for a single command.

This reverses the project's previous "publication is explicit" stance. That
reversal is deliberate and the documentation that states the old rule changes
with it.

## Current Behavior

Every mutation command — `create`, `update`, `delete`, `restore`, `move`,
`depend`, and `free` — resolves through `openService` in `internal/cli/run.go`,
calls the matching `core.Service` mutation, compare-and-swaps
`refs/workbook/tasks/<id>`, and advances the SQLite projection. No command in
that path performs any network operation.

Automatic publication exists in exactly two places today: `workbook setup` runs
a full `Sync` unless `--no-sync` is passed or no `origin` is configured, and the
opt-in pre-push hook publishes task refs during an ordinary `git push`.

## Measured Cost

The design rests on measurements rather than estimates.

Against a local bare remote holding 500 task refs:

| Operation | Duration |
| --- | ---: |
| `ls-remote` all task refs | 43.7 ms |
| `ls-remote` one task ref | 44.3 ms |
| fetch all refs | 208.8 ms |
| fetch one ref | 130.8 ms |
| push one changed ref | 121.1 ms |
| push one up-to-date ref | 47.1 ms |
| push all 500 refs, all up to date | 86.3 ms |

Against a real HTTPS remote:

| Operation | Duration |
| --- | ---: |
| `ls-remote` all task refs | 225.1 ms |
| `ls-remote` one task ref | 199.2 ms |
| `ls-remote` `HEAD` only | 203.6 ms |

The decisive result is the second table. Roughly 200 ms buys *a connection*, not
the refs carried on it; one ref and five hundred refs cost the same. Two
conclusions follow:

1. **Narrowing the fetch is pointless.** One ref costs 199 ms against 225 ms for
   every ref. The drift reduction this story exists to deliver comes precisely
   from fetching everything, so the fetch stays broad.
2. **Narrowing the push is valuable**, but for the local half of the cost rather
   than the network half. Today's `Push` validates every local tip and issues an
   `ls-remote`; on the 500-task fixture that local work is roughly 220 ms of the
   313.51 ms `sync-already-synchronized` measurement, and it grows with project
   size. A single-ref push removes it.

Reusing today's `Sync` verbatim would add about 650 ms to a `cli-update` whose
p95 is currently 153.74 ms against a 200 ms budget. The design below adds about
420 ms and keeps the added cost constant in project size.

## Synchronization Sequence

An enabled mutation runs fetch, then mutate, then push:

1. **Fetch**, broad and unchanged, reusing `Repository.Fetch`. Fetching first
   means a teammate's advance to this task fast-forwards the local ref *before*
   the operation is applied, so the edit builds on their operation. Divergence is
   prevented rather than reported.
2. **Mutate**, exactly as today.
3. **Push**, targeted at the single ref the mutation changed.

`workbook next` performs step 1 only.

The mutation observes the fetched state without additional work: `Fetch`
fast-forwards canonical refs, and the projection's `refreshChangedHeads`
reconciles changed Git heads on the next read, which the mutation performs.

Fetching first benefits `create` as well as edits, despite `create` having no
remote counterpart to fast-forward. `CreateMutation` derives a new task's rank
from every current snapshot, so a stale local view produces colliding ranks in
the same status and priority bucket.

Divergence is detected from the fetch result, which already reports a per-task
`diverged` status. The helper inspects the outcome for the task being mutated;
`create` has no prior task and therefore no divergence case. Commands with a
second task argument — `move`, `depend`, and `free` — still change exactly one
ref, and the push targets that ref alone.

## Targeted Push

`gitstore.PushTask(ctx, config, taskID)` validates that one tip through
`readTaskHeadsPartial` and runs:

```sh
git push --porcelain origin <object-id>:refs/workbook/tasks/<task-id>
```

with `WORKBOOK_PRE_PUSH_ACTIVE=1`, reusing `parsePushPorcelain` to produce a
`SyncTaskResult`.

It issues no `ls-remote`. Git's own non-fast-forward rejection is already the
remote race guard; the `ls-remote` in the existing `Push` exists only to report
`up-to-date` without pushing, and a single-ref push recovers that from porcelain
output for free.

A targeted push validates only the ref being published. An unrelated malformed
local ref therefore no longer blocks an unrelated mutation. The full validation
sweep remains available through the explicit `workbook push`.

## Policy Resolution

A new `internal/autosync` package owns one decision:

```go
type Policy struct {
	Enabled bool
	Source  string // "flag", "project", "user", or "default"
}

func Resolve(noSyncFlag bool, project core.ProjectConfig, user userconfig.Config) Policy
```

Precedence, highest first:

| Source | Location |
| --- | --- |
| `--no-sync` | one command invocation |
| project | `autoSync` in `.workbook/config.json` |
| user | `preferences.autoSync` in the user configuration |
| default | enabled |

A tracked project policy outranks a personal preference. The project setting
exists so a team can require synchronization in a repository, and that
requirement only holds if it survives an individual's global preference.
`--no-sync` remains the per-command escape hatch for anyone who needs one.

`Policy.Source` is reported in output so a surprised user can see which layer
decided.

## Project Configuration

`core.ProjectConfig` gains:

```go
AutoSync *bool `json:"autoSync,omitempty"`
```

declared after `Key` so the byte-exact canonical re-encode in `decodeConfig`
continues to hold. The field is a pointer because the precedence chain needs
three states: enabled, disabled, and unset-so-defer-to-the-next-layer.

`projectVersion` becomes `2`. Everything Workbook writes is version 2.

The decoder still accepts a version 1 document and treats it as `autoSync`
unset. This is the one piece of compatibility the design keeps, and it is
required rather than defensive: `Repository.Init` does not rewrite an existing
configuration, so a version-2-only decoder would reject the configuration in
every repository already using Workbook — including this one — on every command,
with no recovery path except hand-editing a file the guidelines forbid editing.

A version 1 document that nonetheless carries `autoSync` is corrupt data.

No accommodation is made for the reverse direction. A version 2 document read by
an older binary is rejected as corrupt data, and that rejection is sufficient.

## Command Surface

`--no-sync` is added to `create`, `update`, `delete`, `restore`, `move`,
`depend`, `free`, and `next`, matching the flag `setup` already accepts. Each
command's entry in `commandSchemas` gains the option so help output and the
schema validation in `parseFlags` stay consistent.

A single helper in `internal/cli` wraps the mutation commands so the sequence
lives in one place rather than being repeated seven times.

## Failure Semantics

The local write is durable before any publication is attempted, so a failure to
publish is not a failure to record the work.

| Condition | Local write | Push | Exit |
| --- | --- | --- | ---: |
| Everything succeeds | committed | published | 0 |
| No `origin` configured | committed | skipped | 0 |
| Fetch fails: offline, credentials | committed | skipped | 0, with warning |
| Fetch shows this task diverged | committed | skipped | 6 |
| Push rejected, non-fast-forward | committed | rejected | 6 |

Transient unreachability is a warning because the mutation genuinely succeeded
and reporting failure would invite a retry that then fails as `update does not
change task`. Divergence is an error because the task's history actually
requires reconciliation; it is reported as `stale-write`, exit 6.

A failed fetch skips the push rather than attempting it. With the network
unavailable, a second connection only buys a second timeout.

## Output

`ResultEnvelope` gains an omitted-when-empty `sync` member carrying the policy
source, the fetch outcome, and the targeted push outcome. The addition is
additive, so existing consumers of `format`, `version`, `command`, `data`, and
`warnings` are unaffected.

Human-readable output gains one `Sync:` line after the existing mutation line.
Warnings continue to reach stderr through the existing `core.Warning` path.

## Performance Regression Protection

A new `cli-update-autosync` benchmark scenario measures an enabled mutation
against a local bare remote, with an inclusive p95 target of 1,000 ms.

The existing `cli-update` scenario keeps its 200 ms budget and is measured with
`--no-sync`. Holding the local path to its current budget is the point: a
regression in local mutation cost must not be able to hide inside network
variance. Evidence is recorded under `docs/performance/` following the existing
provenance rules.

## Documentation

Three documents currently instruct the opposite of this design and change with
it:

- `README.md`, the section directing readers to publish explicitly;
- `internal/agentdocs/render.go`, the generated "Publication is explicit"
  section, whose regeneration moves the managed `sha256` stamp that
  `workbook docs update` maintains; and
- `skills/workbook/SKILL.md`, which tells agents not to run `fetch`, `sync`, or
  `push` automatically.

Each becomes a description of automatic synchronization, the `--no-sync` flag,
and the precedence chain.

## Testing

Unit coverage:

- policy precedence across all four sources, including a project policy
  overriding a conflicting user preference;
- project configuration encode and decode for version 2, version 1 read as
  unset, and version 1 carrying `autoSync` rejected as corrupt.

Integration coverage, using temporary local bare remotes rather than mocks:

- an enabled mutation publishes exactly the changed ref and leaves unrelated
  remote refs untouched;
- no `origin` configured reports skipped and exits 0;
- an unreachable `origin` warns, exits 0, and leaves the local write durable;
- a diverged task exits 6 with the local ref intact and the remote unchanged;
- `--no-sync` performs no network Git process, asserted with the Trace2 counter
  the performance harness already uses;
- `next` fetches and never pushes;
- a malformed unrelated local ref does not block a targeted push.

## Out of Scope

**The web board keeps today's local-only behavior.** `serve` is a long-running
local server where synchronizing on every drag would be visibly slow, and the
flag-based opt-out has no equivalent in a browser UI. That work, including a UI
toggle exposing and shifting the current auto-sync state, is tracked by
`WB-01KYM85R7X6FSFR5ER7ZDJ8T3N`, which depends on this story and whose
optimistic mutation queue is what makes synchronized web mutations viable.

**No retry or reconciliation.** A divergent task history still routes to the
existing manual resolution path. Automatic reconciliation remains a separate
concern.
