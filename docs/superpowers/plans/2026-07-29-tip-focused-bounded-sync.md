# Tip-Focused Bounded Sync Implementation Plan

> **Execution:** Use subagent-driven development. Complete and review each task
> before starting the next. Do not run acceptance-sized benchmarks before Task
> 6, and never replace a Task 6 measurement.

**Goal:** Make default Workbook synchronization tip-focused and constant in Git
process cardinality while preserving isolated tracking refs, CAS,
non-fast-forward safety, partial publication, and exact per-task results.

**Architecture:** Build a shared validated sync state from batched ref
enumeration, one partial `cat-file --batch`, and at most one ancestry graph.
Fetch applies canonical changes in one CAS transaction. Push uses one wildcard
remote-head query when needed, one explicit-OID non-atomic porcelain push, and
one final canonical snapshot. Sync reuses fetch state and performs zero push
when synchronized.

**Tech stack:** Go 1.26, Git plumbing, existing `internal/core`,
`internal/gitstore`, and `internal/perf` integration fixtures.

**Design:** `docs/superpowers/specs/2026-07-29-tip-focused-bounded-sync-design.md`

---

## Task 1: Preserve nonzero Git command evidence and parse bulk transport

**Files:**

- Modify: `internal/gitstore/repository.go`
- Modify: `internal/gitstore/repository_test.go`
- Create: `internal/gitstore/sync_protocol.go`
- Create: `internal/gitstore/sync_protocol_test.go`

**Produces:**

- private Git execution result containing stdout, stderr, and execution error;
- strict wildcard `ls-remote` task-head parser;
- strict `git push --porcelain` per-destination parser.

### Step 1: Write failing result-preservation tests

Use a fake Git executable that writes distinct stdout/stderr and exits nonzero.
Assert:

- the private result API retains both streams and the exit error;
- the observer records exactly one command;
- the existing public `Repository.Git` still returns no stdout and includes
  stderr in its operational error.

Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./internal/gitstore \
  -run 'TestGitResult|TestGitFailureReports' -count=1
```

Expected: RED because the result-preserving API is undefined.

### Step 2: Implement the result-preserving runner

Refactor `runGitWithEnv` over a lower-level result function. Preserve:

- `GIT_NO_REPLACE_OBJECTS=1`;
- extra environment precedence;
- repository root and resolved Git path;
- one observer notification;
- current public error category and stderr-only detail.

Do not expose the result type outside `internal/gitstore`.

### Step 3: Write failing remote-head parser tests

Provide SHA-1 and SHA-256 `ls-remote --refs` output. Assert exact task maps and
reject:

- unterminated output;
- wrong prefix or nested task ref;
- duplicate task;
- malformed or abbreviated object ID;
- extra fields;
- symbolic/peeled or code refs.

### Step 4: Implement wildcard remote-head parsing

The parser accepts only flat `refs/workbook/tasks/<task-id>` destinations and
full object IDs consistent with the repository's observed object format.

### Step 5: Write failing porcelain parser tests

Model creation, fast-forward, up-to-date, and partial rejection in the same
output. Require one status for every expected destination. Reject:

- missing or duplicate status;
- unexpected task/code destination;
- malformed tab fields;
- force and deletion flags;
- an incomplete status set on command failure.

Include the exact normal Git header/footer forms observed from a local bare
remote.

### Step 6: Implement strict porcelain parsing

Attribute by destination ref, never by abbreviated source text. Retain Git's
summary/reason as rejection detail. A complete set containing `!` is usable
even when the command exited nonzero.

### Step 7: Run focused and package tests

```sh
gofmt -w internal/gitstore/repository.go \
  internal/gitstore/repository_test.go \
  internal/gitstore/sync_protocol.go \
  internal/gitstore/sync_protocol_test.go
GOCACHE=/private/tmp/workbook-gocache go test ./internal/gitstore -count=1
git diff --check
```

Expected: PASS.

### Step 8: Commit

```sh
git add internal/gitstore/repository.go \
  internal/gitstore/repository_test.go \
  internal/gitstore/sync_protocol.go \
  internal/gitstore/sync_protocol_test.go
git commit -m "feat: parse bounded sync transport"
```

---

## Task 2: Add partial batched tip validation and generic owned-ref reads

**Files:**

- Modify: `internal/gitstore/batch.go`
- Modify: `internal/gitstore/batch_test.go`
- Modify: `internal/gitstore/read.go`
- Modify: `internal/gitstore/read_test.go`

**Produces:**

- private ordered partial tip-read results;
- shared flat/non-symbolic owned-ref enumeration for canonical and tracking
  namespaces;
- strict `ReadTaskHeads` preserved as a wrapper.

### Step 1: Write failing partial-batch tests

Build three heads where the middle head is malformed or missing and the last is
valid. Assert:

- one `cat-file --batch` command;
- three ordered results;
- first and third valid snapshots survive;
- middle result carries its own corrupt-data error;
- all four Git batch records per head are consumed.

Add a malformed batch framing test that fails the entire call rather than
misattributing later records.

### Step 2: Refactor batch reading

Split response consumption from Workbook validation. Consume commit, tree,
operation, and state response records for one head before returning that head's
validation error.

Missing/object-type/document failures are per-head. Invalid Git framing or
truncated response is fatal.

Implement strict `ReadTaskHeads` by calling the partial reader and returning the
first per-head error, preserving its existing public contract.

### Step 3: Write failing generic owned-ref tests

Cover canonical and tracking prefixes with packed and loose refs. Assert one
`for-each-ref` per namespace and rejection of:

- symbolic refs;
- nested refs;
- duplicates;
- wrong prefix;
- invalid task ID;
- invalid/full-width object ID mismatch.

### Step 4: Generalize owned-ref enumeration

Use `%(refname)%00%(objectname)%00%(symref)` for both namespaces. Keep
`listTaskRefs` and exact `taskRef` behavior compatible.

### Step 5: Add tip-trust-boundary tests

Assert tip reads still reject:

- non-commit;
- wrong tree entries/modes/types;
- noncanonical documents;
- wrong project/task;
- operation/state generation mismatch;
- logical-clock mismatch;
- invalid root/ordinary parent topology.

Add a non-root checkpoint mismatch whose tip documents are structurally and
internally valid; tip reading must accept it without applying the operation to
the parent state.

### Step 6: Run focused and package tests

```sh
gofmt -w internal/gitstore/batch.go internal/gitstore/batch_test.go \
  internal/gitstore/read.go internal/gitstore/read_test.go
GOCACHE=/private/tmp/workbook-gocache go test ./internal/gitstore \
  -run 'TestReadTaskHeads|TestOwnedRefs|TestTip' -count=1
GOCACHE=/private/tmp/workbook-gocache go test ./internal/gitstore -count=1
git diff --check
```

Expected: PASS.

### Step 7: Commit

```sh
git add internal/gitstore/batch.go internal/gitstore/batch_test.go \
  internal/gitstore/read.go internal/gitstore/read_test.go
git commit -m "feat: validate task tips in one batch"
```

---

## Task 3: Classify head relationships and update canonical refs in one CAS

**Files:**

- Modify: `internal/gitstore/batch.go`
- Modify: `internal/gitstore/batch_test.go`
- Create: `internal/gitstore/sync_plan.go`
- Create: `internal/gitstore/sync_plan_test.go`

**Produces:**

- one-graph equal/local-ahead/remote-ahead/diverged classification;
- exact generation compatibility guard;
- one transactional canonical update-ref operation.

### Step 1: Write failing relationship tests

Create task pairs for:

- equal heads;
- remote child of local;
- local child of remote;
- common-parent divergence;
- unrelated generations;
- duplicate task IDs;
- malformed/abbreviated IDs.

Exercise SHA-1 and supported SHA-256. Observe commands and require:

- no graph process for all-equal pairs;
- exactly one `rev-list --parents --stdin` for all unequal pairs;
- no per-task `merge-base`.

### Step 2: Implement one-graph classification

Submit every unique unequal local and remote head as positive stdin revisions,
parse the parent graph once, and test reachability in both directions.

Require internally valid snapshots and compatible task/generation identity
before accepting a fast-forward relationship.

### Step 3: Write failing canonical transaction tests

Prepare creates and fast-forwards. Assert one update-ref command whose stdin
contains:

- `start`;
- `option no-deref`;
- create records for missing refs;
- update records with exact expected old IDs;
- prepare/commit;
- no force or delete.

Race one canonical ref before execution and require stale-write with no
transactional updates reported as successful.

### Step 4: Implement the canonical CAS transaction

Use `git update-ref --no-deref --create-reflog -m ... --stdin`. Validate task
IDs/ref ownership before constructing stdin. Do not invoke per-task
`check-ref-format`, `symbolic-ref`, or `update-ref`.

### Step 5: Run focused and package tests

```sh
gofmt -w internal/gitstore/batch.go internal/gitstore/batch_test.go \
  internal/gitstore/sync_plan.go internal/gitstore/sync_plan_test.go
GOCACHE=/private/tmp/workbook-gocache go test ./internal/gitstore \
  -run 'TestClassifyTaskHeads|TestUpdateCanonicalRefs' -count=1
GOCACHE=/private/tmp/workbook-gocache go test ./internal/gitstore -count=1
git diff --check
```

Expected: PASS.

### Step 6: Commit

```sh
git add internal/gitstore/batch.go internal/gitstore/batch_test.go \
  internal/gitstore/sync_plan.go internal/gitstore/sync_plan_test.go
git commit -m "feat: batch sync ancestry and ref updates"
```

---

## Task 4: Replace per-task Push with one bounded publication

**Files:**

- Modify: `internal/gitstore/sync.go`
- Modify: `internal/gitstore/sync_test.go`
- Modify: `internal/cli/sync_test.go`

**Produces:**

- one wildcard remote-head lookup;
- zero-or-one explicit-OID non-atomic push;
- strict per-ref partial outcomes;
- one final local snapshot and local-changed detection.

### Step 1: Write failing command-cardinality test

Create 25 valid local task refs and observe repository Git commands. Assert:

- one canonical `for-each-ref`;
- one tip `cat-file --batch`;
- one wildcard `ls-remote`;
- no per-task `ls-remote`;
- one `push --porcelain`;
- no `--atomic`, force, deletion, wildcard source, or code ref;
- 25 explicit task destinations;
- one final canonical `for-each-ref`.

Run:

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./internal/gitstore \
  -run TestPushUsesOneBoundedPublication -count=1
```

Expected: RED under the current per-task implementation.

### Step 2: Write failing behavior tests

Cover:

- first publication of multiple tasks;
- second already-synchronized Push with zero push command and exact
  `up-to-date` results;
- one remote non-fast-forward rejection plus unrelated successful publication
  in the same command;
- one invalid local tip omitted while valid independent tasks publish;
- local ref advance during the command publishes only the observed OID and
  reports `local-changed`;
- managed hook recursion guard remains effective;
- missing origin and incomplete porcelain accounting fail the phase.

### Step 3: Implement Push planning

Enumerate and partially validate canonical tips once. Mark invalid tasks and
exclude them. Query remote task heads once and exclude exact matches.

Create candidates from observed full OIDs and exact task destinations.

### Step 4: Implement one publication and final race read

Execute one non-atomic porcelain push when candidates exist. Parse complete
per-ref results even on expected nonzero rejection.

Always take one final canonical snapshot. Convert successful/up-to-date
outcomes to `local-changed` when the current local head differs from the
observed source. Rejected outcomes retain precedence.

Return aggregate errors in the approved corrupt/rejected/local-changed order.

### Step 5: Run focused CLI and repository tests

```sh
gofmt -w internal/gitstore/sync.go internal/gitstore/sync_test.go \
  internal/cli/sync_test.go
GOCACHE=/private/tmp/workbook-gocache go test ./internal/gitstore \
  -run 'TestPush' -count=1
GOCACHE=/private/tmp/workbook-gocache go test ./internal/cli \
  -run 'TestRunPush' -count=1
GOCACHE=/private/tmp/workbook-gocache go test ./internal/gitstore ./internal/cli -count=1
git diff --check
```

Expected: PASS.

### Step 6: Commit

```sh
git add internal/gitstore/sync.go internal/gitstore/sync_test.go \
  internal/cli/sync_test.go
git commit -m "feat: publish task refs in one push"
```

---

## Task 5: Make Fetch and Sync tip-focused and reuse validated state

**Files:**

- Modify: `internal/gitstore/sync.go`
- Modify: `internal/gitstore/sync_test.go`
- Modify: `internal/cli/sync_test.go`
- Modify: `internal/perf/remote_scenarios_test.go`
- Modify: `README.md`
- Modify: `docs/performance/README.md`

**Produces:**

- one-fetch tip-focused reconciliation;
- one batched canonical CAS;
- zero Push when synchronized;
- Sync reuse without repeated tip/remote inspection;
- documented default-vs-audit boundary.

### Step 1: Rewrite semantic-history tests for the intended boundary

Change the current tests that require default Fetch/Push to reject a
structurally valid checkpoint mismatch. Assert instead:

- a valid current tip with a parent checkpoint mismatch is accepted;
- the Task 1 buried-corruption fixture is accepted by default Fetch/Push;
- malformed current tips remain rejected/isolated.

Preserve the corrupt fixture as an oracle for the next `workbook validate`
task.

### Step 2: Write failing fresh and changed-set tests

Use real local bare remotes:

- fresh checkout creates canonical refs and returns complete tip state at
  logical clock 20 without replay;
- five local-ahead and five remote-ahead refs receive exact outcomes and final
  canonical/tracking/remote tips;
- divergence stops publication;
- malformed remote remains tracking-only while unrelated valid refs can
  reconcile;
- canonical CAS race fails stale without overwrite.

### Step 3: Write failing Sync reuse/cardinality tests

At 10x4 and 25x7, observe:

- exactly one fetch;
- two owned-ref enumerations plus one final local snapshot;
- one `cat-file --batch`;
- at most one ancestry graph;
- at most one canonical update transaction;
- zero per-task `merge-base`, `ls-remote`, update-ref, or push;
- synchronized case has zero push;
- changed/local-only case has one push;
- command counts are identical across fixture sizes.

Assert synchronized Push phase remains `completed` with exact `up-to-date`
tasks.

### Step 4: Implement tip-focused Fetch

Use the shared owned-ref, partial tip, relationship, and transaction helpers.
Retain invalid remote outcomes and tracking refs. Never overwrite invalid local
canonical data.

Return an internal validated fetch state alongside the public result.

### Step 5: Implement Sync state reuse

Plan publication from the fetch state:

- no tracking head: candidate;
- equal or newly fast-forwarded: `up-to-date`;
- local-ahead: candidate.

Stop on fetch errors or divergence. Reuse the Task 4 publisher without
`ls-remote`, repeated validation, or another graph.

### Step 6: Update documentation

README and performance docs must state:

- default sync validates current tips, identities, ancestry, and ref safety;
- it does not semantically replay buried checkpoints;
- the explicit validator delivered separately owns exhaustive history audit;
- transport/plumbing process counts are bounded;
- task/code ref isolation and non-force guarantees remain.

Do not describe `workbook validate` as implemented until the following task
lands.

### Step 7: Run correctness and process suites

```sh
gofmt -w internal/gitstore/sync.go internal/gitstore/sync_test.go \
  internal/cli/sync_test.go internal/perf/remote_scenarios_test.go
GOCACHE=/private/tmp/workbook-gocache go test ./internal/gitstore ./internal/cli ./internal/perf -count=1
GOCACHE=/private/tmp/workbook-gocache go test ./... -count=1
GOCACHE=/private/tmp/workbook-gocache go vet ./...
git diff --check
```

Expected: PASS.

### Step 8: Commit

```sh
git add internal/gitstore/sync.go internal/gitstore/sync_test.go \
  internal/cli/sync_test.go internal/perf/remote_scenarios_test.go \
  README.md docs/performance/README.md
git commit -m "feat: make sync tip-focused and bounded"
```

---

## Task 6: Record the one-shot post-change topology evidence

**Files:**

- Modify: `docs/performance/README.md`
- Create: `docs/performance/2026-07-29-sync-tip-focused-sha1.json`
- Create: `docs/performance/2026-07-29-sync-tip-focused-sha1.md`
- Create when supported:
  `docs/performance/2026-07-29-sync-tip-focused-sha256.json`
- Create when supported:
  `docs/performance/2026-07-29-sync-tip-focused-sha256.md`

### Step 1: Run final preflight

```sh
gofmt -w cmd/workbook-bench/*.go internal/perf/*.go internal/gitstore/*.go
GOCACHE=/private/tmp/workbook-gocache go test ./... -count=1
GOCACHE=/private/tmp/workbook-gocache go vet ./...
git diff --check
git status --short
```

Expected: PASS and only intentional committed code/docs before measurement.

### Step 2: Build the measured binary once

```sh
GOCACHE=/private/tmp/workbook-gocache \
  go build -buildvcs=false \
  -o /private/tmp/workbook-tip-focused-sync ./cmd/workbook
```

### Step 3: Confirm output paths are absent

Fail rather than overwrite if any Task 6 report path already exists.

### Step 4: Run exactly one SHA-1 command

```sh
GOCACHE=/private/tmp/workbook-gocache go run ./cmd/workbook-bench \
  --workbook /private/tmp/workbook-tip-focused-sync \
  --tasks 500 \
  --operations 20 \
  --samples 1 \
  --timeout 60s \
  --object-format sha1 \
  --phase acceptance \
  --scenario sync-fresh-checkout \
  --scenario sync-initial-publication \
  --scenario sync-already-synchronized \
  --scenario sync-small-changed-ref-set \
  --scenario sync-divergent-tips \
  --scenario sync-malformed-local-tip \
  --scenario sync-malformed-remote-tip \
  --output-json docs/performance/2026-07-29-sync-tip-focused-sha1.json \
  --output-markdown docs/performance/2026-07-29-sync-tip-focused-sha1.md
```

Do not retry or replace the report for any timeout, failure, or miss.

### Step 5: Probe SHA-256 and run it once when supported

Probe with a temporary bare repository. When supported, run the identical
selector set once with `--object-format sha256` and the SHA-256 output paths.

### Step 6: Verify report and bounded-process completeness

For each report require:

- format/version/phase/environment metadata;
- exact seven scenario names;
- one sample per scenario;
- approved targets and nonempty outcomes;
- seven matching Markdown rows;
- observed Git process counts below the approved exclusive limits.

Record wall-clock passes or misses exactly as observed. Per the approved user
decision, correctness plus bounded process counts may merge even when a
wall-clock target misses.

### Step 7: Document comparison without replacing evidence

Add a short comparison table linking the pre-change and post-change reports.
State exact process counts and wall-clock outcomes. Keep all Task 1 reports.

### Step 8: Commit evidence

```sh
git add docs/performance
git commit -m "docs: record tip-focused sync evidence"
```

### Step 9: Final branch verification

```sh
GOCACHE=/private/tmp/workbook-gocache go test ./... -count=1
GOCACHE=/private/tmp/workbook-gocache go vet ./...
git diff --check
git status --short
```

Expected: PASS and clean.

---

## Whole-Branch Review and Delivery

After all six tasks:

1. request a whole-branch review from the merge base through HEAD;
2. fix every Critical or Important finding and re-review the fix;
3. run fresh full tests, vet, whitespace, report-completeness, and clean-status
   checks;
4. move Workbook task `WB-01KYNYSF3ZT0RSN9GNAWJTKFWV` to In Review and push
   task refs;
5. push the branch and create a ready PR to `main`;
6. merge with a merge commit after configured checks pass;
7. move the task to Done and push task refs;
8. fast-forward local main and remove only this merged worktree/local/remote
   branch.
