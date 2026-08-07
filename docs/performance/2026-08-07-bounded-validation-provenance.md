# Bounded full history validation evidence provenance

Date: 2026-08-07

This provenance record covers the re-measurement required by
`WB-01KYT6XA289VV2QNGTXZG9S31H`, "Bound full history validation time and peak
memory growth". It re-runs the two families the 2026-07-30 evidence used to
identify full validation as Workbook's only superlinear path:

- the task-count and history-depth scaling matrix, and
- the storage and peak-resource measurement.

## Source and environment

- Source commit measured: `ec7e37561c17730a3d6e7f1485e4c2b1d18059fd`
- Baseline commit measured: `c4535967fc2a2a1b1477eda80c006e1857a1be3e` (`main` at the time this work branched)
- Go: `go version go1.26.5 darwin/arm64`
- Git: `git version 2.50.1 (Apple Git-155)`
- OS architecture: `arm64`

Later commits on this branch touched no measured behavior. `git diff
ec7e3756..HEAD -- '*.go'` is a corrected doc comment in
`internal/gitstore/history.go` and the unexporting of `internal/perf`'s
validation-result verifier, whose body is unchanged and whose only callers are
in that package. Everything else is this directory's documentation and the
evidence files themselves.

## Frozen binaries

| Binary | Commit | SHA-256 |
| --- | --- | --- |
| `workbook` (change) | `ec7e3756` | `1ad7a85e755f59d0a9fb70dc4a2b3ad58d804b1647bd2c694aa482afa11fa5ed` |
| `workbook` (baseline) | `c4535967` | `95c4e4ea890f73a447aa44b228d0344e10cbd78a54d44d15566ec6c261f04f92` |
| `workbook-bench` | `ec7e3756` | `f2780aecd5869c1103f844adcd9a51412890e11c6ba68f32dbd485e51e370223` |

```sh
COMMIT=$(git rev-parse HEAD)
go build -buildvcs=false -ldflags "-X main.commit=$COMMIT" -o "$EV/workbook" ./cmd/workbook
go build -buildvcs=false -o "$EV/workbook-bench" ./cmd/workbook-bench
shasum -a 256 "$EV/workbook" "$EV/workbook-bench"
```

The baseline product binary was built the same way from a `git archive` of
`c4535967`, with that commit embedded. **One** `workbook-bench` measured both
product binaries, so the only variable between the baseline and the change is
the product under test, and both were held to the same literal result oracle.

Neither binary was rebuilt between object formats or between runs. Every report
records an exact commit; the scaling phase now rejects `unknown` before fixture
construction, so this is enforced rather than merely observed.

## Execution discipline and what it does not buy

Every run executed serially. This machine was not otherwise idle: it is a shared
development host, and other work ran on it during these measurements. That is
recorded here rather than hidden, and it is why the conclusions below rest on
within-run slopes and on same-host baseline-versus-change pairs rather than on
comparisons against the 2026-07-30 millisecond figures.

Two further honesty notes, both raised on the story before the work started:

- **Peak resident memory is not run-deterministic.** The fixtures are
  deterministic — seeded identifiers and fixed timestamps — so a rerun measures
  the same repository. Allocator behavior, page-cache state, and host load still
  move the byte count. "Deterministic evidence" here means a deterministic
  fixture plus this record, not a reproducible byte count.
- **The two families do not share a fixture shape.** The scaling matrix's
  500-active point is 525 total refs (500 active plus 25 tombstoned). The storage
  run below is `--tasks 500 --tombstones 25`, which is 500 total with 475 active,
  chosen deliberately so it is the same shape the 2026-07-30 storage evidence
  used. A slope from the matrix and a byte count from the storage family
  therefore describe different repositories and must not be divided into each
  other.

## Storage and peak resource growth

Four depths rather than two, because two points give a ratio and not a shape.
Both product binaries were measured at every depth in both object formats.

```sh
"$EV/workbook-bench" --workbook "$PRODUCT" \
  --tasks 500 --tombstones 25 --timeout 600s \
  --object-format sha1 --phase acceptance \
  --storage-resources --storage-operations 20,50,100,200 \
  --output-json docs/performance/2026-08-07-storage-resources-sha1.json \
  --output-markdown docs/performance/2026-08-07-storage-resources-sha1.md
# repeated with --object-format sha256, and with the baseline product binary
```

- [SHA-1 JSON evidence](2026-08-07-storage-resources-sha1.json) and
  [Markdown evidence](2026-08-07-storage-resources-sha1.md)
- [SHA-256 JSON evidence](2026-08-07-storage-resources-sha256.json) and
  [Markdown evidence](2026-08-07-storage-resources-sha256.md)
- [SHA-1 baseline JSON](2026-08-07-storage-resources-baseline-sha1.json) and
  [Markdown](2026-08-07-storage-resources-baseline-sha1.md)
- [SHA-256 baseline JSON](2026-08-07-storage-resources-baseline-sha256.json) and
  [Markdown](2026-08-07-storage-resources-baseline-sha256.md)

### Full validation latency, same host, same fixtures

| Depth | Reachable objects | SHA-1 baseline | SHA-1 change | SHA-256 baseline | SHA-256 change |
| ---: | ---: | ---: | ---: | ---: | ---: |
| 20 | 40,000 | 3,892.73 ms | 562.31 ms | 3,192.77 ms | 617.89 ms |
| 50 | 100,000 | 20,755.66 ms | 1,148.76 ms | 7,143.59 ms | 1,239.15 ms |
| 100 | 200,000 | 35,807.60 ms | 2,176.08 ms | 14,668.74 ms | 2,451.27 ms |
| 200 | 400,000 | 60,259.69 ms | 4,517.32 ms | 29,720.64 ms | 5,314.19 ms |

Elapsed time falls by 6.9x to 18.1x in SHA-1 and by 5.2x to 6.0x in SHA-256. The
SHA-1 baseline column is visibly noisier than the SHA-256 one — its depth-50
point is disproportionately slow — which is exactly the shared-host effect this
record warns about, and exactly why the slope conclusions come from within-run
ratios.

### Peak resident memory, and who it belongs to

`ru_maxrss` from `wait4` is a maximum over the measured process *and every
descendant it reaped*, so a measured peak can belong to a `git` child rather
than to Workbook. To attribute it, the same object set was replayed through
`git cat-file --batch` on its own for every fixture, outside Workbook entirely:

```sh
git -C "$fixture" rev-list --reverse --topo-order --parents --stdin < tips.txt > graph.txt
awk '{print $1"\n"$1"^{tree}\n"$1":operation.json\n"$1":state.json"}' graph.txt > req.txt
/usr/bin/time -l git -C "$fixture" cat-file --batch < req.txt > /dev/null
```

SHA-1:

| Depth | Baseline peak | Change peak | `git cat-file --batch` alone | Baseline above Git | Change above Git |
| ---: | ---: | ---: | ---: | ---: | ---: |
| 20 | 92,471,296 | 39,321,600 | 33,652,736 | 58,818,560 | 5,668,864 |
| 50 | 273,727,488 | 75,579,392 | 75,595,776 | 198,131,712 | -16,384 |
| 100 | 519,553,024 | 147,767,296 | 147,718,144 | 371,834,880 | 49,152 |
| 200 | 983,875,584 | 289,144,832 | 289,030,144 | 694,845,440 | 114,688 |

SHA-256:

| Depth | Baseline peak | Change peak | `git cat-file --batch` alone | Baseline above Git | Change above Git |
| ---: | ---: | ---: | ---: | ---: | ---: |
| 20 | 93,978,624 | 39,944,192 | 35,831,808 | 58,146,816 | 4,112,384 |
| 50 | 278,904,832 | 80,936,960 | 80,936,960 | 197,967,872 | 0 |
| 100 | 529,039,360 | 158,400,512 | 158,384,128 | 370,655,232 | 16,384 |
| 200 | 962,772,992 | 311,197,696 | 310,263,808 | 652,509,184 | 933,888 |

This is the load-bearing result. The last two columns are the only part of the
measured peak that Workbook itself can be held responsible for.

- Before: it grew from 58.8 MB to 694.8 MB across a 10x depth increase in SHA-1,
  and 58.1 MB to 652.5 MB in SHA-256 — proportional growth, 11.8x and 11.2x.
- After: it is 5.7 MB at depth 20 and 0.1 MB at depth 200 in SHA-1, and 4.1 MB
  to 0.9 MB in SHA-256. It does not grow with depth; from depth 50 upward it
  vanishes into measurement noise, because Workbook is no longer the largest
  resident set in the process tree at all.

The measured peak that remains is `git cat-file --batch` reading a packed
repository, and it matches the standalone Git figure to within 0.04% at depths
50 and above in SHA-1 and within 0.3% in SHA-256. The 2026-07-30 finding that
full validation's peak was roughly 16x the projection rebuild's peak is also
gone: at every depth and both formats, full validation's peak is now *below* the
projection rebuild's peak measured on the same fixture (for example 289 MB
against 303 MB at SHA-1 depth 200).

The residual is therefore Git's, not Workbook's, and reducing it further means
tuning Git's own read behavior — its delta base cache, for instance — for every
history read in the product. That is a separate story with its own justification
and is deliberately not attempted here; the term this story is about is the one
in the last two columns, and it is now flat.

### Disposable cache

The per-task index costs disposable bytes. SHA-1 validation cache, baseline
against change: 2,097,152 → 2,539,520 at depth 20, 4,546,560 → 5,640,192 at
depth 50, 8,654,848 → 10,862,592 at depth 100, and 16,809,984 → 21,274,624 at
depth 200 — 21% to 27% larger, growing toward the upper end with depth. SHA-256
is the same shape. The cache is disposable by construction and is rebuilt from
Workbook refs whenever it is unusable, which now includes a file that has lost
the index. `validationSidecarBytes` is zero at every measured depth in both
formats: the cache is closed cleanly, so no `-wal` or `-shm` file survives the
command even though it now runs in WAL.

### Two observations about the projection rebuild, which this story did not target

Both are recorded because the rebuild is the flat reference the 2026-07-30
evidence compared full validation against, and that reference has moved.

First, the rebuild is no longer flat. The 2026-07-30 evidence recorded 29 MB and
about 149 ms at 500-by-100 SHA-1. The same measurement against `c4535967`, this
change's baseline, is 163,790,848 bytes and 4,797.71 ms. Nothing in this story
touched the projection; the change happened on `main` between `725a2838` and
`c4535967`. Comparing today's numbers to the 2026-07-30 rebuild row would
therefore attribute someone else's drift to this work, and this record does not.

Second, the rebuild did get faster here, and for a reason this change owns.
`ReadTaskOperations` shares `walkCommitChain` with the validation path, so
sizing its cycle guard to one chain instead of to the whole parent graph helps
both: SHA-1 depth 100 rebuild moves from 4,797.71 ms to 1,542.11 ms and depth 200
from 6,750.94 ms to 3,002.63 ms. Its peak resident memory is unchanged, because
that peak belongs to the `git cat-file` child, not to the map.

## Task-count and history-depth scaling matrix

The default four-point matrix, twenty samples per scenario per point, both
object formats, run serially back to back.

```sh
"$EV/workbook-bench" --workbook "$EV/workbook" \
  --phase scaling --samples 20 --timeout 300s --object-format sha1 \
  --output-json docs/performance/2026-08-07-scaling-slopes-sha1.json \
  --output-markdown docs/performance/2026-08-07-scaling-slopes-sha1.md
# repeated with --object-format sha256
```

- [SHA-1 JSON evidence](2026-08-07-scaling-slopes-sha1.json) and
  [Markdown evidence](2026-08-07-scaling-slopes-sha1.md)
- [SHA-256 JSON evidence](2026-08-07-scaling-slopes-sha256.json) and
  [Markdown evidence](2026-08-07-scaling-slopes-sha256.md)

Both reports name commit `ec7e3756`. Every scenario at every point reports
`not-evaluated`: no scaling scenario carries a target, and none failed or timed
out.

### Within-run slopes for `validate-full-history`

This is the comparison the story turns on, and within-run slopes are the form of
it that survives a different host. Each slope is
`ln(value ratio) / ln(dimension ratio)` between two points measured minutes
apart in one run, so a uniformly faster or slower machine largely cancels.

| Axis | Pair | 2026-07-30 SHA-1 | 2026-08-07 SHA-1 | 2026-07-30 SHA-256 | 2026-08-07 SHA-256 |
| --- | --- | ---: | ---: | ---: | ---: |
| task-count | 100 → 500 active, depth 20 | 1.1024 | **0.6580** | 1.1364 | **0.7052** |
| task-count | 500 → 1000 active, depth 20 | 1.5554 | **0.8553** | 1.6880 | **0.9411** |
| history-depth | depth 20 → 100, 500 active | 1.1220 | **0.8415** | 1.1524 | **0.8726** |

Three things follow.

The task-count slope is reduced on both axis pairs and in both object formats,
which is the acceptance criterion. It is now below 1.0 everywhere, meaning full
validation costs *less* than proportionally more as the repository grows.

The slope no longer degrades with scale. The 2026-07-30 signature of the
unindexed per-task DELETE was that the second pair was worse than the first
— 1.102 then 1.555 — because each task's DELETE scanned a table that grew as the
run proceeded. That ordering is what a quadratic term looks like across two
pairs. It is still present in shape (0.658 then 0.855) but far weaker, and both
values are now below proportional; what remains is ordinary per-ref overhead
rather than a term that squares.

The history-depth slope also drops, from 1.12 to 0.84. Read this one with
caution: the same-host baseline below puts full validation's depth-axis time
slope at roughly 1.0 *before* the change as well, so most of that apparent drop
is cross-run difference rather than this change. The depth axis was never the
quadratic one, and what this change actually buys there is a large constant
factor plus the memory bound.

### Nothing else moved the wrong way

Every scenario's slope was compared, both formats, both axis pairs, 2026-07-30
against 2026-08-07. No scenario regressed.

`validate-cached-unchanged` — the path the story was explicitly told not to
disturb — stays flat on the depth axis (SHA-1 0.0213 → 0.0201, SHA-256
0.0470 → 0.0590) and improves on the task axis (SHA-1 0.4569 → 0.2393 at
500 → 1000, SHA-256 0.4245 → 0.3195).

Across the seven non-validation scenarios, no slope rose by more than 0.07 on
any axis pair in either format; the largest increase anywhere is `cli-list` on
the SHA-1 task axis, 0.0911 → 0.1521. Every larger move is a decrease, the
biggest being `sync-small-changed-ref-set` on the SHA-1 task axis at
0.8533 → 0.4350. On the SHA-1 depth axis all seven land slightly negative
(between -0.004 and -0.067) where they were slightly positive before, which is
what a genuinely flat metric measured twice on a shared host looks like rather
than a real improvement.

### Git process count and per-operation cost

`p95GitProcesses` for `validate-full-history` is exactly 7 at all four points in
both formats, unchanged from 2026-07-30 and identical across every axis pair
(ratio 1.00x). The streaming read swapped a buffered `cat-file --batch` for a
long-running one rather than adding processes, so it stays well inside the
`MaxGitProcesses` target of 12 and the growth remains in-process work, not
subprocess fan-out.

Normalizing median milliseconds by total operations, SHA-1. The denominator is
the fixture's *total* refs times its depth, tombstoned tasks included, since a
full audit replays their history too; the story's own figures divided by active
tasks instead, so they read about 5% higher than the 2026-07-30 column below.

| Point | Total operations | 2026-07-30 ms/op | 2026-08-07 ms/op |
| --- | ---: | ---: | ---: |
| 100 active, depth 20 | 2,100 | 0.177 | 0.104 |
| 500 active, depth 20 | 10,500 | 0.208 | 0.060 |
| 1000 active, depth 20 | 21,000 | 0.306 | 0.054 |
| 500 active, depth 100 | 52,500 | 0.253 | 0.047 |

The 2026-07-30 column rises with task count — the per-ref overhead the story
identified. The 2026-08-07 column falls monotonically with corpus size instead,
which is the expected shape when fixed per-run cost is amortized over more work
and no per-ref term dominates. SHA-256 behaves the same way (0.100, 0.062,
0.059, 0.050 ms/op).

### Same-host baseline, and what is missing

A same-host baseline scaling matrix at `c4535967` was attempted, so that the
task-axis slope would not have to be compared across runs at all. It was killed
at about 61 minutes on this shared host before the harness wrote a report, and
it was not restarted. **The task-count slope comparison above is therefore
cross-run**, 725a2838 against ec7e3756, and rests on the fact that within-run
slopes normalize host speed and that the reduction is large rather than
marginal. That is the weakest link in this record; it is stated here rather than
left for a reader to discover.

The history-depth axis does have a same-host baseline, because the storage
family measured both product binaries at four depths on identical fixtures.
Log-log slopes of full validation's *elapsed time* against depth, from those
runs:

| Pair | SHA-1 baseline | SHA-1 change | SHA-256 baseline | SHA-256 change |
| --- | ---: | ---: | ---: | ---: |
| 20 → 50 | 1.8266 | 0.7796 | 0.8789 | 0.7594 |
| 50 → 100 | 0.7868 | 0.9217 | 1.0380 | 0.9842 |
| 100 → 200 | 0.7509 | 1.0537 | 1.0187 | 1.1163 |
| 20 → 200 | 1.1898 | 0.9049 | 0.9689 | 0.9345 |

This is a more sober picture than the cross-run depth comparison, and it is the
one to believe. Against its own parent, on one host, full validation's time was
already close to linear in depth (SHA-256 0.969 end to end), and it remains close
to linear afterward (0.935). The SHA-1 baseline column is visibly noisy — the
1.83 at 20 → 50 is the disproportionately slow depth-50 point noted earlier — so
its 1.19 end-to-end figure should not be read as a real superlinearity either.

The honest conclusion for the depth axis is that this change buys a large
constant factor there, 5.2x to 18.1x, and the peak-memory bound — not a change
of complexity class. That is exactly what the root-cause analysis predicted: the
quadratic term was the unindexed per-task `DELETE`, which multiplies by task
count and never by depth. The complexity claim in this story belongs to the task
axis, and the depth axis's contribution to acceptance is the memory result
above, which *is* same-host and *is* decisive.

## No change to validation results

Both storage families and both scaling runs assert an exact literal oracle on
every measured `workbook validate` result — every task checked, every commit
checked, no cache hits, no failures, and a task count equal to the fixture's.
That oracle is new for the storage family, and it was applied to the baseline
product binary as well as to the change, so the two are known to have produced
identical validation results on all sixteen measured storage points and on every
scaling sample.

`go test ./...` passes at the measured commit, including the history validation,
gitstore, projection, and CLI suites and the Node-backed embedded web client
tests.

## Results are descriptive

No scenario in either family carries a duration or Git-process target, and none
was added. The slopes and byte counts above are recorded so that the next
optimization, if any, is justified separately.
