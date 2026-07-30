# Performance scaling, projection refresh, and storage evidence provenance

Date: 2026-07-30

This provenance record covers three benchmark stories measured together so that
every report shares one source identity:

- `WB-01KYQPRDE8CZ4DHSSZR16TQHM8` — task-count and history-depth scaling slopes
- `WB-01KYQQJP9HPT9ZFW4X1T5MV91Y` — many-changed projection refresh
- `WB-01KYQQK0HV42NBVFMTWVTZVAW8` — storage and peak resource growth

## Source and environment

- Source commit: `725a2838adde83e20e9b73eb959820ca9871cb0c`
- Go: `go version go1.26.5 darwin/arm64`
- Git: `git version 2.50.1 (Apple Git-155)`
- OS architecture: `arm64`

## Frozen binaries

| Binary | SHA-256 |
| --- | --- |
| `workbook` | `55a3edb3740254c84224dcb925fdb403a2f3be8a1717707af112741f7145b954` |
| `workbook-bench` | `6c6b4c4455df7bf7d5c217c48edff8e6afcde0aab16718873aac2d8d289bffe7` |

The source commit was recorded as the exact 40-character output of `git
rev-parse HEAD` before either binary was built. The product binary embeds that
literal commit, so no report records `unknown`.

```sh
COMMIT=$(git rev-parse HEAD)
go build -buildvcs=false -ldflags "-X main.commit=$COMMIT" -o "$EV/workbook" ./cmd/workbook
go build -buildvcs=false -o "$EV/workbook-bench" ./cmd/workbook-bench
shasum -a 256 "$EV/workbook" "$EV/workbook-bench"
```

Neither binary was rebuilt between object formats or between the six runs. All
eight generated reports record commit `725a2838adde…` and product checksum
`55a3edb37402…`.

## Execution discipline

The six runs executed strictly serially on an otherwise idle machine. Serial
execution is load-bearing: concurrent work would corrupt the latency,
peak-resident-memory, and Git-process counts these reports record. Measured
elapsed times were 21 s and 23 s (storage), 108 s and 101 s (refresh), and
2,581 s and 2,637 s (scaling).

Two earlier pilot invocations were run only to project the scaling wall clock
and are not evidence. One additional single-sample timing attempt was discarded
because another benchmark briefly overlapped it; it was re-measured on an idle
machine and no discarded run produced a committed report.

## Scaling slope matrix

`--phase scaling` measures four fixture points with 20 samples per scenario and
no duration or Git-process targets; every scenario is therefore `not-evaluated`
by construction rather than by failure. The matrix is anchored on **active**
tasks, holding the acceptance one-in-twenty tombstone ratio, so the repository
scales exactly 1x/5x/10x along the task-count axis.

| Point | Active | Tombstoned | Total refs | Depth |
| --- | ---: | ---: | ---: | ---: |
| `active-100-depth-20` | 100 | 5 | 105 | 20 |
| `active-500-depth-20` | 500 | 25 | 525 | 20 |
| `active-500-depth-100` | 500 | 25 | 525 | 100 |
| `active-1000-depth-20` | 1000 | 50 | 1050 | 20 |

Because the 500-active point is 525 total refs, it is deliberately **not**
byte-identical to the 500-total (475 active) local acceptance fixture and is not
directly comparable to it.

```sh
"$EV/workbook-bench" --workbook "$EV/workbook" \
  --phase scaling --samples 20 --timeout 300s --object-format sha1 \
  --output-json docs/performance/2026-07-30-scaling-slopes-sha1.json \
  --output-markdown docs/performance/2026-07-30-scaling-slopes-sha1.md
# and the same invocation with --object-format sha256
```

- [SHA-1 JSON evidence](2026-07-30-scaling-slopes-sha1.json) and
  [Markdown evidence](2026-07-30-scaling-slopes-sha1.md)
- [SHA-256 JSON evidence](2026-07-30-scaling-slopes-sha256.json) and
  [Markdown evidence](2026-07-30-scaling-slopes-sha256.md)

### Observed slopes

Every mutation and read path is flat to sublinear on both axes. Median SHA-1
milliseconds, 100 to 1,000 active tasks at depth 20:

| Scenario | 100 | 500 | 1000 | 10x growth |
| --- | ---: | ---: | ---: | ---: |
| `cli-create` | 165.97 | 159.85 | 175.34 | 1.06x |
| `cli-update` | 149.47 | 158.70 | 170.72 | 1.14x |
| `cli-depend` | 186.85 | 210.02 | 246.11 | 1.32x |
| `cli-move` | 184.62 | 209.98 | 246.43 | 1.33x |
| `cli-list` | 59.44 | 68.83 | 84.84 | 1.43x |
| `validate-cached-unchanged` | 89.87 | 122.85 | 168.63 | 1.88x |
| `sync-already-synchronized` | 190.91 | 330.93 | 522.65 | 2.74x |
| `sync-small-changed-ref-set` | 330.71 | 533.97 | 964.68 | 2.92x |
| `validate-full-history` | 370.73 | 2185.81 | 6424.37 | 17.32x |

Along the history-depth axis at 500 active tasks, depth 20 to depth 100, every
scenario is flat (1.02x to 1.06x) except `validate-full-history` at 6.08x.

`validate-full-history` is the only superlinear scenario, and it is superlinear
on both axes. Its log-log task-count slope degrades with scale: 1.102 from 100
to 500 active tasks, then 1.555 from 500 to 1,000. Its Git-process ratio is
exactly 1.00x on every axis pair, so the growth is in-process work rather than
Git subprocess fan-out.

Normalizing by total operations distinguishes the axes. The 1,000-by-20 point is
20,000 operations at 0.321 ms per operation, while the 500-by-100 point is
50,000 operations at 0.266 ms per operation: 2.5x the operations for 2.07x the
time. Full validation is more expensive per unit of work when operations are
spread across more task refs than when they are stacked as deeper history on
fewer refs, so task count is the more punishing axis.

## Projection refresh change-count family

Deterministic 525-total (500 active) by 20 fixtures with one sample per point.
Setup and mutation are outside the timed refresh and outside its Git-process
count. Change counts are exact and verified by a ref diff before the measurement.

```sh
"$EV/workbook-bench" --workbook "$EV/workbook" \
  --tasks 525 --tombstones 25 --operations 20 --samples 1 \
  --timeout 300s --object-format sha1 --phase acceptance \
  --scenario projection-refresh-unchanged \
  --scenario projection-refresh-one-changed \
  --scenario projection-refresh-five-changed \
  --scenario projection-refresh-fifty-changed \
  --scenario projection-refresh-five-hundred-changed \
  --output-json docs/performance/2026-07-30-projection-refresh-sha1.json \
  --output-markdown docs/performance/2026-07-30-projection-refresh-sha1.md
# and the same invocation with --object-format sha256
```

- [SHA-1 JSON evidence](2026-07-30-projection-refresh-sha1.json) and
  [Markdown evidence](2026-07-30-projection-refresh-sha1.md)
- [SHA-256 JSON evidence](2026-07-30-projection-refresh-sha256.json) and
  [Markdown evidence](2026-07-30-projection-refresh-sha256.md)

| Changed heads | SHA-1 median (ms) | SHA-256 median (ms) | Git processes |
| ---: | ---: | ---: | ---: |
| 0 | 67.54 | 63.82 | 3 |
| 1 | 99.17 | 99.95 | 5 |
| 5 | 108.10 | 99.28 | 5 |
| 50 | 137.55 | 132.54 | 5 |
| 500 | 399.53 | 394.78 | 5 |

The marginal cost is 0.6640 ms per changed head in SHA-1 and 0.6619 in SHA-256.
The Git-process count is constant at five regardless of change count, so refresh
does not fan out one Git process per changed task. Refreshing 500 changed heads
costs about 5.9x an unchanged refresh; the step from zero to one changed head is
the fixed cost of performing any projection update at all.

These measurements supersede the 2026-07-29 `projection-refresh-one-changed`
number. That earlier scenario mutated through `workbook update`, which advances
the projection for the task it mutates, so the refresh it timed observed zero
stale heads rather than one.

## Storage and peak resource growth

Corrected representative 500-by-20 and 500-by-100 fixtures in both object
formats. Fixture construction is outside every measured command.

```sh
"$EV/workbook-bench" --workbook "$EV/workbook" \
  --tasks 500 --tombstones 25 --timeout 300s \
  --object-format sha1 --phase acceptance \
  --storage-resources --storage-operations 20,100 \
  --output-json docs/performance/2026-07-30-storage-resources-sha1.json \
  --output-markdown docs/performance/2026-07-30-storage-resources-sha1.md
# and the same invocation with --object-format sha256
```

- [SHA-1 JSON evidence](2026-07-30-storage-resources-sha1.json) and
  [Markdown evidence](2026-07-30-storage-resources-sha1.md)
- [SHA-256 JSON evidence](2026-07-30-storage-resources-sha256.json) and
  [Markdown evidence](2026-07-30-storage-resources-sha256.md)

Object classification is exhaustive in all four runs: reachable objects equal
classified objects and unclassified objects are zero, over 40,000 objects at
depth 20 and 200,000 objects at depth 100.

SHA-1 durable storage, raw and on-disk bytes by class:

| Class | Depth 20 objects | Depth 20 raw | Depth 100 objects | Depth 100 raw |
| --- | ---: | ---: | ---: | ---: |
| operation blob | 10,000 | 4,035,146 | 50,000 | 20,467,161 |
| state blob | 10,000 | 5,139,504 | 50,000 | 26,348,639 |
| tree | 10,000 | 800,000 | 50,000 | 4,000,000 |
| commit | 10,000 | 2,796,000 | 50,000 | 14,076,500 |

Operation-blob and state-blob raw bytes are byte-identical between SHA-1 and
SHA-256 at both depths. Only trees and commits grow, because those objects embed
object IDs. This demonstrates the required cross-format deterministic
equivalence on measured data rather than by assertion alone.

Peak resident memory and latency, by command:

| Command | Depth | SHA-1 latency | SHA-1 peak RSS | SHA-256 latency | SHA-256 peak RSS |
| --- | ---: | ---: | ---: | ---: | ---: |
| `projection-rebuild` | 20 | 141.15 ms | 29,212,672 B | 158.12 ms | 29,507,584 B |
| `projection-rebuild` | 100 | 148.85 ms | 29,048,832 B | 150.77 ms | 32,620,544 B |
| `full-validation` | 20 | 2,112.66 ms | 91,799,552 B | 2,137.14 ms | 94,158,848 B |
| `full-validation` | 100 | 12,873.77 ms | 469,729,280 B | 13,670.76 ms | 520,880,128 B |

Projection rebuild is flat in history depth on both latency and peak memory.
Full validation is not: at 500-by-100 its peak resident memory reaches 470 MB in
SHA-1 and 521 MB in SHA-256, roughly 16x the rebuild peak, and its latency grows
6.1x for a 5x depth increase. The disposable validation cache likewise dominates
the disposable projection cache, 8,654,848 bytes against 360,448 bytes at depth
100.

On darwin, `ru_maxrss` is reported in bytes and `ru_inblock`/`ru_oublock` are
never populated, so block I/O counters are reported as unsupported and the
usable I/O signals are major page faults and the measured repository byte delta.

## Results are descriptive

No scenario in these three families carries a duration or Git-process target. No
pass threshold was invented, and no product behavior was changed to produce this
evidence. Measured slopes are recorded so that narrow optimization work can be
justified separately.
