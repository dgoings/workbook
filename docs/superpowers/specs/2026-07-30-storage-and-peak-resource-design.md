# Storage and Peak Resource Design

Date: 2026-07-30

## Context

The 2026-07-29 evaluation recorded one aggregate packed-Git-bytes number for a
500-task by 20-operation fixture. That number cannot answer the questions the
storage model actually raises: how much of the footprint is operation history,
how much is the redundant state checkpoint, how much is Git's own tree and
commit overhead, and how much is disposable cache that a user may delete at any
time. It also recorded nothing about the memory or I/O cost of rebuilding the
projection or replaying a full semantic validation.

## Goals

- Separate durable Git storage by object class: operation blobs, state blobs,
  trees, and commits, with counts and bytes for each.
- Report pack, pack-index, and loose-object bytes for the whole repository.
- Report the two disposable SQLite caches separately from durable storage.
- Capture peak resident memory and I/O for `workbook rebuild --json` and
  `workbook validate --full --json`.
- Do all of this at the corrected representative 500-by-20 and 500-by-100
  fixture depths, in SHA-1 and SHA-256 repositories, with equivalent
  deterministic logical data in both.
- Emit stable machine-readable JSON and a generated Markdown view.

## Non-goals

- Change durability behavior, the projection, the validator, or any other
  product code. This story touches only `internal/perf`, `cmd/workbook-bench`,
  and documentation.
- Attach any pass threshold, duration budget, or Git-process budget to these
  measurements. They are descriptive.
- Add a scaling or matrix mode, new projection scenarios, or a `cli-list`
  scenario. Those belong to sibling stories.
- Optimize packing, garbage collection, cache size, or command latency.

## Object classification and why it is exhaustive

Classification walks the real object graph. It never infers a class from a
count or from the fixture's dimensions.

1. `git for-each-ref --format=%(refname)%00%(objectname) refs/workbook/` yields
   every Workbook ref and its tip. The ref namespace, not a task-count guess, is
   the root set. Refs under `refs/workbook/tasks/` are counted separately so a
   report can show that the two populations agree.
2. `git rev-list --objects --stdin`, fed those tips, walks the commit graph and
   every tree it points at. It emits each reachable object exactly once. Commits
   are emitted as a bare object ID; trees and blobs are emitted as
   `<object-id> <path>`, where the path is the tree entry the object was reached
   through. Emitting an object ID twice is treated as a harness error.
3. `git cat-file --batch-check='%(objectname) %(objecttype) %(objectsize)
   %(objectsize:disk)'`, fed the object IDs from step 2, returns each object's
   type and both size representations. A missing object, a duplicate record, or
   a record count that differs from the walked set is a harness error.
4. Each object is placed in exactly one class:

   | Git object type | Tree path | Class |
   | --- | --- | --- |
   | `commit` | - | `commit` |
   | `tree` | - | `tree` |
   | `tag` | - | `annotated-tag` |
   | `blob` | `operation.json` | `operation-blob` |
   | `blob` | `state.json` | `state-blob` |
   | `blob` | anything else | `other-blob` |

The classification is total by construction: the outer switch covers every Git
object type, an unknown type is a hard error rather than a silent drop, and the
blob switch has a default bucket. `other-blob` and `annotated-tag` exist so an
attachment blob or a future annotated tag lands somewhere real instead of
vanishing. Both are zero for the current fixture, which is itself reported
rather than assumed.

Three invariants are recorded in every report and asserted by tests:

- `classifiedObjects == reachableObjects` and `unclassifiedObjects == 0`.
- The class object counts sum to `reachableObjects`, and the class byte totals
  sum to `reachableRawBytes` and `reachableDiskBytes`.
- After the fixture is packed with `git gc --quiet --prune=now`, Git's own
  `count-objects -v in-pack` equals `reachableObjects` and its loose count is
  zero. This is an independent oracle: it comes from a different Git command
  than the walk, and it would disagree if the walk missed or double-counted an
  object.

## Byte-size semantics

Two byte totals are reported per class, and they mean different things.

- `rawBytes` is `%(objectsize)`: the uncompressed size of the object's content
  as Git defines it. This is what the data costs in principle.
- `diskBytes` is `%(objectsize:disk)`: the size of the object's stored
  representation, including its delta base chain and object header. For a
  packed object this is its compressed, possibly deltified, packed size. It
  excludes per-pack overhead such as the pack header, trailer, and index.

`diskBytes` is therefore usually smaller than `rawBytes` for JSON documents,
but it can exceed `rawBytes` for very small objects such as fixture trees,
where the header and zlib framing outweigh any compression. That is a real
property of Git storage and is reported rather than hidden. `git verify-pack -v`
was considered and rejected: `%(objectsize:disk)` is the same quantity, works
for loose and packed objects alike, and needs one process instead of one per
pack.

Whole-repository bytes come from the filesystem, not from `count-objects`,
because `count-objects -v` reports kibibytes and would round away small
differences:

- `packFileBytes`, `packIndexBytes`, and `packAuxiliaryBytes` are the summed
  exact sizes of `*.pack`, `*.idx`, and every other file in `objects/pack`
  (`.rev`, `.mtimes`, `.keep`, and so on).
- `looseObjectFileBytes` is the summed exact size of every file in a two-hex
  fan-out directory under `objects`.
- `objectDirectoryBytes` is every regular file under `objects`, so it also
  covers `objects/info` artifacts such as a commit-graph.

`looseObjects` and `packedObjects` are Git's own counts from
`count-objects -v`, kept as the independent cross-check described above.

## Disposable cache accounting

The two deletable SQLite caches are reported separately from durable storage,
after the commands that create them:

- the projection cache at `<common-git-dir>/workbook/cache.sqlite`, created by
  the measured `workbook rebuild --json`;
- the validation cache at `<common-git-dir>/workbook/validation.sqlite`,
  created by the measured `workbook validate --full --json`.

The common Git directory is resolved with `git rev-parse --git-common-dir`, so
the reported path is repository-relative (`.git/workbook/...`) and the report
stays free of absolute temporary paths. `-wal`, `-shm`, and `-journal` sidecars
are summed into a separate field so a report can distinguish steady-state
database size from transient journal size. Both caches are measured after both
commands run, so neither number depends on measurement order within a depth.

## Resource capture, units, and platform caveats

Each measured command is run with `os/exec` in its own process group and reaped
by Go's `wait4`. `(*os.ProcessState).SysUsage()` returns the `*syscall.Rusage`
that `wait4` filled in for that child.

- `maxResidentRaw` is `ru_maxrss` exactly as the kernel reported it.
  `maxResidentRawUnit` names its unit, and `maxResidentBytes` is the normalized
  value. **The unit is platform-specific: darwin reports bytes, Linux and the
  BSDs report kilobytes.** Reporting the raw number without its unit would be
  wrong by a factor of 1024 on one of those platforms, so both are emitted and
  a test asserts the normalization against a child process that touches a known
  96 MiB.
- Peak resident memory from `wait4` is a *maximum*, not a sum. It reports the
  largest resident set observed for the measured process or any descendant it
  reaped, so a command that forks several concurrent `git` processes reports the
  largest single peak rather than the concurrent total. Treat it as a lower
  bound on whole-tree peak memory.
- `blockInputOperations` and `blockOutputOperations` are `ru_inblock` and
  `ru_oublock`. **Darwin does not maintain these counters.** A child that
  fsyncs 64 MiB still reports zero. `blockIoCountersSupported` is therefore
  false on darwin and true elsewhere, the counters are forced to zero when
  unsupported, and a test asserts exactly that after a real 64 MiB write. Do not
  read a zero on darwin as "no I/O happened".
- `minorPageFaults` and `majorPageFaults` (`ru_minflt`, `ru_majflt`) are
  populated on darwin and are the usable memory-pressure signal there.
  `voluntaryContextSwitches` and `involuntaryContextSwitches` are recorded for
  context.
- `repositoryBytesDelta` is the change in total on-disk bytes under the
  repository root across the command, sampled by walking the tree immediately
  before and immediately after, outside the timing window. It is a portable,
  durable-write lower bound that works on darwin where the block counters do
  not. It is not a syscall-level I/O counter and does not see rewritten bytes.
- `milliseconds`, `userMilliseconds`, and `systemMilliseconds` are recorded so
  a resource number can be read next to the work that produced it. They carry no
  target and are not compared against the scenario budgets.

## Measurement boundary

Per fixture depth, in order:

1. Build the fixture with `perf.BuildFixture` (untimed, unmeasured).
2. `git pack-refs --all`, then `git gc --quiet --prune=now` (untimed,
   unmeasured). The measured state is the fully packed steady state a fresh
   clone would carry, and pruning makes the packed-object cross-check exact.
3. Classify durable objects and size the object directory. These are Git
   plumbing reads, not product commands.
4. Measure `workbook rebuild --json` for resources.
5. Measure `workbook validate --full --json` for resources.
6. Size the two disposable caches.
7. Delete the depth's fixture so peak disk stays bounded across depths.

No fixture construction, packing, or measurement bookkeeping happens inside a
measured command. A failing or timed-out measured command aborts the run rather
than producing a report, because the storage accounting around it would be
untrustworthy.

## Report schema

The measurement is a new optional top-level `storageResources` object on the
existing version-2 report. Every existing field and the existing report contract
are unchanged, so committed evidence stays valid. A run that does not measure
storage omits the key entirely.

```
storageResources
  platform                       GOOS/GOARCH of the harness host
  maxResidentRawUnit             "bytes" | "kilobytes"
  blockIoCountersSupported       whether ru_inblock/ru_oublock are maintained
  objectSizeSemantics            self-describing rawBytes/diskBytes sentence
  repositoryState                how the repository was packed before measuring
  depths[]                       ascending by fixture.operationsPerTask
    fixture                      totalTasks, activeTasks, tombstonedTasks,
                                 operationsPerTask, objectFormat
    git
      objectFormat               sha1 | sha256, read from the repository
      refPrefix                  "refs/workbook/"
      workbookRefs, taskRefs     ref populations
      reachableObjects           objects reachable from the Workbook refs
      classifiedObjects          objects placed in a class
      unclassifiedObjects        reachable minus classified; always 0
      classes[]                  class, objects, rawBytes, diskBytes
      reachableRawBytes          sum of class rawBytes
      reachableDiskBytes         sum of class diskBytes
      packs, packedObjects       pack count and Git's in-pack object count
      packFileBytes              exact summed *.pack size
      packIndexBytes             exact summed *.idx size
      packAuxiliaryBytes         exact summed size of other objects/pack files
      looseObjects               Git's loose object count
      looseObjectFileBytes       exact summed loose object file size
      objectDirectoryBytes       every regular file under objects/
    disposableCache
      projectionPath             repository-relative cache path
      projectionBytes            cache.sqlite size
      projectionSidecarBytes     -wal, -shm, -journal
      validationPath             repository-relative cache path
      validationBytes            validation.sqlite size
      validationSidecarBytes     -wal, -shm, -journal
      totalBytes                 sum of the four byte fields
    resources[]                  projection-rebuild, then full-validation
      command, argv
      milliseconds, exitCode, timedOut, error
      maxResidentBytes, maxResidentRaw, maxResidentRawUnit
      userMilliseconds, systemMilliseconds
      blockInputOperations, blockOutputOperations, blockIoCountersSupported
      minorPageFaults, majorPageFaults
      voluntaryContextSwitches, involuntaryContextSwitches
      repositoryBytesDelta
```

`objectSizeSemantics` and `repositoryState` are prose fields on purpose: a
storage report that outlives its documentation should still say what its numbers
mean.

The Markdown view renders one section per depth with an object-class table, a
repository-storage table, a disposable-cache table, and a resource table, so a
reader who never opens the JSON sees every component.

## Harness invocation

`workbook-bench --storage-resources` runs the storage and resource accounting
*instead of* the scenario families. It cannot be combined with `--scenario`,
because the scenario families build and mutate their own fixtures and would
change the storage being measured. `--storage-operations` takes a
comma-separated list of operations-per-task depths, defaulting to `20,100`; the
list is deduplicated, validated, and sorted ascending. `--tasks` and
`--tombstones` keep their existing meaning and apply to every depth.

A storage-only report runs no scenarios, so its `scenarios` array is empty and
its `targets` object is zero-valued. Neither describes the storage measurement;
the storage section carries no target of any kind. The report's top-level
`fixture` records the shallowest measured depth, and the complete per-depth
fixture metadata lives in `storageResources.depths[].fixture`.

Fixture construction is bounded at twenty times `--timeout`, because it is not
a measured command and a 500-by-100 fixture legitimately takes far longer than
any single product command.

## What is deliberately not measured

- Unreachable and dangling objects. The fixture is pruned before measurement,
  so the report describes reachable durable history only.
- Non-Workbook refs. `refs/heads`, `refs/tags`, and the working tree, including
  `.workbook/config.json`, are outside the Workbook ref namespace and outside
  the classification. The whole-repository pack and object-directory byte
  totals do include everything Git stores, and in these fixtures the two agree
  because no other objects exist.
- Reflogs, `packed-refs`, index, and config file bytes.
- Per-object delta chain depth and pack ordering.
- Concurrent whole-process-tree peak memory. See the `wait4` caveat above.
- Latency budgets of any kind. Elapsed times are context for the resource
  numbers, not measurements against a target.
- Working-set or page-cache behavior. Nothing warms or drops caches between
  depths, so numbers are steady-state-warm, not cold-start.

## Verification

New tests in `internal/perf/storage_test.go`, `internal/perf/resources_test.go`,
and `cmd/workbook-bench/storage_test.go` prove:

- every reachable object lands in exactly one class against a real temporary
  fixture, with exact expected per-class counts, no unclassified remainder, no
  double counting, and agreement with Git's independent packed-object count;
- `diskBytes` and `rawBytes` really are different quantities, with document
  blobs compressing and the totals ordered as documented;
- a SHA-1 and a SHA-256 fixture built from the same specification carry
  equivalent deterministic logical data: identical per-task commit counts,
  identical sorted `operation.json` and `state.json` content digests, identical
  class object counts, identical document raw bytes, and wider trees in SHA-256
  as the only expected difference;
- every fixture depth reports complete fixture metadata, complete Git
  accounting, both disposable caches, and both resource measurements;
- peak resident memory is reported in the documented unit, verified against a
  child process that touches a known 96 MiB, and page faults are non-zero;
- the darwin block-I/O caveat holds after a child writes and fsyncs 64 MiB;
- the JSON field set is exactly the documented contract and encoding is
  deterministic;
- the generated Markdown names every storage component, peak resident memory,
  and the I/O columns;
- `--storage-resources` resolves and orders its depths, rejects malformed and
  scenario-mixed invocations, and produces a scenario-free report end to end.

Commands run:

```sh
gofmt -l .
go build ./...
go vet ./...
go test ./internal/perf/ ./cmd/workbook-bench/
```

## Evidence generation

Final evidence is generated after the three concurrent performance stories are
integrated, so every report shares one source identity. Build both binaries once
per evidence family, embedding the exact source commit in the product binary,
and run each object format once. Both fixture depths appear in a single report
per object format, keyed by `storageResources.depths[].fixture`.

```sh
COMMIT=$(git rev-parse HEAD)
go build -buildvcs=false -ldflags "-X main.commit=$COMMIT" -o /private/tmp/workbook-storage-resources ./cmd/workbook
go build -buildvcs=false -o /private/tmp/workbook-bench-storage-resources ./cmd/workbook-bench
shasum -a 256 /private/tmp/workbook-storage-resources /private/tmp/workbook-bench-storage-resources

/private/tmp/workbook-bench-storage-resources \
  --workbook /private/tmp/workbook-storage-resources \
  --tasks 500 \
  --tombstones 25 \
  --timeout 120s \
  --object-format sha1 \
  --phase acceptance \
  --storage-resources \
  --storage-operations 20,100 \
  --output-json docs/performance/2026-07-30-storage-resources-sha1.json \
  --output-markdown docs/performance/2026-07-30-storage-resources-sha1.md

/private/tmp/workbook-bench-storage-resources \
  --workbook /private/tmp/workbook-storage-resources \
  --tasks 500 \
  --tombstones 25 \
  --timeout 120s \
  --object-format sha256 \
  --phase acceptance \
  --storage-resources \
  --storage-operations 20,100 \
  --output-json docs/performance/2026-07-30-storage-resources-sha256.json \
  --output-markdown docs/performance/2026-07-30-storage-resources-sha256.md
```

`--timeout 120s` is chosen deliberately: at 500 by 100 the full validation is
the longest measured command by a wide margin, and the default 60 seconds
leaves little headroom on a loaded machine. A timeout is recorded evidence, not
a reason to rerun.

An untracked rehearsal at these exact dimensions completed in roughly
24 seconds of wall clock per object format on an arm64 darwin host, so both
invocations together are a sub-minute run. Neither invocation should be retried,
tuned, or replaced after it produces a report.
