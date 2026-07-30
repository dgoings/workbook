# Packed SHA-256 Repository Sync Design

Date: 2026-07-30

## Context

`WB-01KYQPRXX1WDF09XQ1C1HV1QHV` was created after the repository
acceptance benchmark packed task refs, ran `git gc`, and then reported:

```text
Git returned 0 push outcomes, want 500
```

The failure remains reproducible on current `main`, but it is not evidence that
Workbook cannot publish packed SHA-256 refs. The benchmark fixture is initialized
as SHA-256, while `MeasureRepository` creates its empty bare origin with plain
`git init --bare`. On systems whose default object format is SHA-1, Git rejects
that cross-format push before emitting any porcelain ref outcomes. The isolated
remote topology succeeds because its fixture explicitly propagates the selected
object format to the bare origin.

The corrected local-mutation harness did not exercise or change this repository
path. Repository samples also have no duration target, so the current report
labels an operationally failed sample `not-evaluated`, and the benchmark process
exits zero because its retained-failure gate is intentionally limited to local
CLI and API scenarios.

## Goals

- Initialize the repository benchmark's bare origin with the source repository's
  actual Git object format.
- Cover the real packed-and-garbage-collected path in SHA-1 and SHA-256
  repositories.
- Verify initial and unchanged synchronization samples complete and that the
  remote contains exactly the canonical task refs and object IDs.
- Retain repository reports for operational failures, classify those scenarios
  as `failed`, and return a nonzero benchmark exit after writing the reports.
- Run the repaired repository sync acceptance scenarios at 500 total task refs,
  including 25 tombstones, with 20 operations per task in both object formats.

## Non-goals

- Change Workbook's canonical sync protocol, push refspec construction, or
  porcelain parser.
- Support publication between repositories with different object formats.
- Add performance budgets to repository scenarios.
- Rerun remote topology, validation, task-count slope, many-changed projection,
  or resource-growth acceptance work owned by other stories.
- Optimize packing, garbage collection, ref enumeration, or sync latency.

## Approaches considered

### 1. Detect the source repository's object format

Run `git rev-parse --show-object-format` against the fixture after packing and
garbage collection, validate the single nonempty result, and pass it as
`--object-format=<format>` when initializing the bare origin.

This keeps `MeasureRepository` self-contained and makes the origin follow the
repository actually being measured rather than a duplicated caller setting. The
extra Git command is setup work outside both measured sync samples.

### 2. Pass `FixtureSpec.ObjectFormat` into `MeasureRepository`

The benchmark command already has the requested format and could add it to the
function signature. This avoids one setup command, but it lets a stale or
incorrect caller value create another mismatch with the repository on disk.

### 3. Override Git's default object format for the benchmark process

The harness could inject `init.defaultObjectFormat` into the environment before
plain `git init --bare`. This relies on ambient configuration rather than making
the repository construction explicit and is easier for future call sites to
miss.

## Decision

Use approach 1. The repository on disk is authoritative for its object format,
just as its refs and objects are authoritative for the measured data. A small
helper will create a new local bare origin from that detected format, add it as
`origin`, and run the existing initial and unchanged sync measurements. Keeping
the origin path caller-owned lets the real integration test inspect it before
temporary cleanup.

The helper will continue to use argument-vector execution rather than a shell.
It will reject an empty or multiline object-format response before invoking
`git init`.

## Data flow

1. Build the deterministic source fixture in the requested object format.
2. Measure projection behavior and loose repository metrics.
3. Run `git pack-refs --all`, verify ref enumeration is unchanged, run `git gc`,
   and record packed repository metrics.
4. Ask Git for the fixture's actual object format.
5. Initialize an empty bare origin with that exact format and add it as the
   fixture's `origin`.
6. Run the existing measured initial `workbook sync --json`.
7. Only after the initial sample succeeds, run the unchanged sync sample.
8. In tests, compare the complete canonical and remote task-ref maps after both
   samples.

Packing and object-format discovery remain outside the measured samples.

## Failure semantics

Repository scenarios still have no performance target. A successful sample is
therefore `not-evaluated`, meaning measured without a pass/miss budget. Execution
failure has higher precedence than the absence of a target: a timeout is
`timeout`, and a nonzero exit or retained error is `failed`.

After both JSON and Markdown reports are written, `workbook-bench` returns
nonzero when a local CLI, warm API, or repository sample timed out or failed.
Expected-error remote topology scenarios keep their current behavior and are not
folded into this gate.

The product sync implementation and porcelain error precedence remain unchanged.
The corrected same-format origin prevents the harness-created algorithm mismatch
that produced the zero-outcome symptom.

## Test strategy

The primary regression is a real-repository integration test, table-driven over
SHA-1 and SHA-256 when supported:

- build a deterministic fixture;
- pack refs and run garbage collection;
- invoke the production helper that detects the format, creates the bare origin,
  and runs initial plus unchanged sync;
- assert both samples complete without timeout or product error;
- assert the remote task-ref map exactly equals the local canonical task-ref map.

This test must fail against the old implementation in the SHA-256 case with the
original zero-outcome error before production code changes.

Focused model and command tests will also prove:

- a failed no-target repository sample is classified `failed`, while a successful
  no-target repository sample remains `not-evaluated`;
- retained repository failure writes both reports and returns nonzero;
- existing valid local target misses still return zero;
- existing remote topology and Git-process-shape tests remain green.

## Acceptance evidence

After the implementation is committed and all tests pass, build one Workbook
binary and one benchmark binary from that exact source commit. Use the same
binaries for SHA-1 and SHA-256. Run only:

```text
sync-initial-local-bare
sync-unchanged-local-bare
```

with acceptance phase, 500 total tasks, 25 tombstones, 20 operations per task,
one sample, and the existing command timeout. Retain JSON and generated Markdown
reports plus binary/source provenance under `docs/performance/`, and link them
from the performance README. Successful rows remain `not-evaluated` because no
latency or process budget is being introduced; their completed samples and zero
exit codes are the functional acceptance evidence.
