# AGENTS.md

## Project context

Workbook is a design-stage, repository-native project tracker for humans and coding agents. Read `README.md` before making changes. The repository may initially contain only design documentation; do not imply that proposed commands or formats are already implemented.

## Product priorities

Design for these workflows, in order:

1. A solo developer using Workbook locally with no required service.
2. A small remote team onboarding with `git clone` plus one bootstrap command.
3. Ephemeral coding agents that clone fresh, acquire work remotely, read context, record implementation commits, and safely disappear.

Keep the default experience CLI-first, local-first, and independent of a hosted tracker, IDE extension, or MCP server. Machine-readable JSON output is a first-class interface, not an afterthought.

## Architectural invariants

Preserve these decisions unless the user explicitly changes them and the documentation is updated:

- Canonical task history is a sequence/DAG of immutable operations, not a mutable SQLite database or working-tree task snapshot.
- Each task is rooted at a tool-private Git ref under `refs/workbook/tasks/`.
- A task ref points to a Git commit object.
- Each operation commit points to a small tree containing a versioned `operation.json` blob and optional attachment blobs.
- Git commit parents define causal ancestry. Root commits have no parent, ordinary edits have one parent, and reconciliations may have multiple parents.
- Operation IDs are globally unique and remain independent of Git commit IDs.
- Wall-clock timestamps are display metadata only. Do not use them as the primary semantic conflict rule.
- Shared task history is append-only. Never rebase or force-push Workbook refs.
- Deletion is represented by a tombstone operation; do not delete a task ref during normal operation.
- SQLite is a disposable materialized projection. It must always be possible to delete and rebuild it from Workbook refs.
- Direct SQL writes to projected task state are unsupported.
- Workflow state and code reachability are modeled separately. Derive branch inclusion and landed state from Git whenever possible.
- Git hooks may improve UX but must never be required for correctness.
- The core data model must not depend on a particular sync transport. Custom Git refs are the default backend; a compatibility branch or optional relay may be added as adapters.

## Concurrency rules

- Make operations idempotent and deterministic when applied to the same causal history.
- Treat meaningful concurrent scalar edits as domain conflicts unless an explicitly documented resolution rule applies.
- Prefer multi-value conflict visibility over silently discarding a user's or agent's intent.
- Model collection changes with operation semantics that support concurrent add/remove behavior.
- Use remote compare-and-swap behavior for exclusive claims. Do not CRDT-merge two claims and call the result exclusive.
- A remote-required claim is successful only after the remote accepts it.
- Offline or unsynchronized claims must be visibly tentative.
- Never assume an agent completes normally; design for retries, duplicate delivery, stale caches, and abandoned claims.

## Implementation guidance

- Keep Git plumbing behind a narrow repository interface so it can be tested with temporary repositories.
- Use Git APIs or commands for refs and objects; never depend on refs being stored as loose files under `.git/refs`.
- Do not assume a particular Git object hash length or algorithm.
- Use compare-and-swap ref updates locally and non-fast-forward rejection remotely.
- Parse authoritative operation data from the operation blob, not commit messages.
- Treat commit author/committer metadata as attribution, not verified identity, unless signing is explicitly implemented.
- Version every durable serialization format from its first release.
- Reject unknown destructive operations safely while preserving data for future versions.
- Keep bootstrap behavior explicit: normal clones do not automatically fetch arbitrary custom refs.
- Keep every background process optional and additive. `workbook sync --watch` exists to make synchronization cheap, and every command must stay correct and complete with no watcher running, dead, or stale; a command that cannot reach one synchronizes inline instead.
- Avoid introducing a coordination service until a concrete requirement cannot be met by the Git backend. Keep the operation model portable if one is added.

## Testing expectations

When implementation begins, prioritize tests for:

- root, linear, branched, and merged task histories;
- deterministic projection regardless of traversal or delivery order;
- duplicate operation delivery and retry safety;
- concurrent edits to different fields and the same field;
- conflicting and resolved status changes;
- successful and rejected remote claims;
- interrupted writes and stale SQLite projections;
- cache deletion and complete reconstruction;
- shallow, single-branch, and fresh-clone bootstrap behavior;
- code commit linkage, squash/cherry-pick trailers, and target-branch reachability;
- multiple worktrees and multiple processes using the same repository;
- Git SHA-1 and SHA-256 repositories when supported by the test environment.

Prefer integration tests using temporary local bare remotes over mocks for important Git synchronization behavior. Unit-test operation projection and conflict semantics independently from Git transport.

Report a test that cannot run because the machine lacks an optional tool with `internal/testenv.MissingCapability`, never a bare `t.Skip`: only the marked form fails where `WORKBOOK_TEST_REQUIRE_CAPABILITIES` is set and is reported by `scripts/skipreport`. A guard test in `internal/testenv` enforces this by scanning every `_test.go` file for an `exec.LookPath` probe paired with a bare skip.

Run `go test -short ./...` for local iteration and for any parallel or multi-agent run. `-short` skips only the three builds that stay expensive even with a warm build cache: the release archive test (four cross-compile targets), the release determinism test (two builds against deliberately empty caches), and the setup test that compiles the published tag's whole source tree. Other tests still shell out to quick cache-warm `go build`s, so the first run on a cold cache pays real compilation once. The full suite without `-short` is the merge and release gate.

## Documentation discipline

- Keep `README.md` aligned with implemented behavior.
- Mark proposed commands and formats clearly until they exist.
- Record meaningful architectural changes and their tradeoffs; do not silently replace the storage or synchronization model.
- Include small object-graph or event examples when documenting Git internals.
- Distinguish guarantees from best-effort behavior, especially around offline work, leases, and remote synchronization.

## Change hygiene

- Inspect existing changes before editing and preserve unrelated user work.
- Keep commits focused and use concise imperative commit messages.
- Run the most relevant tests and formatting checks available for the files changed.
- Do not add generated SQLite databases, temporary Git repositories, credentials, tokens, or local cache files to source control.
- Do not publish branches, tags, refs, releases, or packages unless the user explicitly requests it.

<!-- workbook:begin generator=dev sha256=40cef0a79115cf7aa9e8c6ea6f5755b51b6d40366fb3e94237da5ddb6a9768ff -->
## Workbook

This project tracks tasks with the Workbook CLI. Read
[`.workbook/guidelines.md`](.workbook/guidelines.md) for agent workflows and the
canonical machine values this project accepts, such as `in-progress` rather
than the `In Progress` display label.

Refresh this section with `workbook docs update`.
<!-- workbook:end -->
