# Projection refresh re-baseline provenance

Date: 2026-08-05

This record re-baselines the projection refresh change-count family after
`WB-01KZ1JCYZCPD156TCXMRB4Z6ZB` added the change history view. That work made
projection refresh and rebuild scale with operation count rather than task
count, so it owns the resulting regression and re-baselines it from measured
evidence rather than guessing a budget. The family is still descriptive: no
duration or Git-process target is attached, and none was invented here.

## Source and environment

- Source commit: `d61853d9cc8b60bad35db97671e2fe00e2730b86`
- Go: `go version go1.26.5 darwin/arm64`
- Git: `git version 2.50.1 (Apple Git-155)`
- OS architecture: `arm64`

## Frozen binaries

| Binary | SHA-256 |
| --- | --- |
| `workbook` | `4c54f9092512652d44e2e38c61fe45543cd8270127b99d5c48f6a8e01b06565b` |
| `workbook-bench` | `20206303d2ff84e7cf5ca4f510629b6ab1c888aebc28eb6b52e35efaaf8c29d1` |

The source commit was recorded as the exact 40-character output of `git
rev-parse HEAD` before either binary was built, so neither report records
`unknown`:

```sh
COMMIT=$(git rev-parse HEAD)
go build -ldflags "-X github.com/dgoings/workbook/internal/release.Commit=$COMMIT" \
  -o "$EV/workbook" ./cmd/workbook
go build -o "$EV/workbook-bench" ./cmd/workbook-bench
shasum -a 256 "$EV/workbook" "$EV/workbook-bench"
```

Neither binary was rebuilt between object formats. Both reports record commit
`d61853d9cc8b…` and product checksum `4c54f9092512…`.

## Invocation

The two runs executed strictly serially on an otherwise idle machine, against
the same 525-total (500 active, 25 tombstoned) by 20 fixture shape the
2026-07-30 baseline used, so the two dates are directly comparable.

```sh
"$EV/workbook-bench" --workbook "$EV/workbook" \
  --tasks 525 --tombstones 25 --operations 20 --samples 1 \
  --object-format sha1 --phase acceptance \
  --scenario projection-refresh-unchanged \
  --scenario projection-refresh-one-changed \
  --scenario projection-refresh-five-changed \
  --scenario projection-refresh-fifty-changed \
  --scenario projection-refresh-five-hundred-changed \
  --output-json docs/performance/2026-08-05-projection-refresh-sha1.json \
  --output-markdown docs/performance/2026-08-05-projection-refresh-sha1.md
# and the same invocation with --object-format sha256
```

`--timeout` was left at the harness default of 60 s, which bounds only the
measured command. The slowest measured refresh was 611.51 ms, so no sample came
near it and the smaller bound cannot have influenced a measurement.

- [SHA-1 JSON evidence](2026-08-05-projection-refresh-sha1.json) and
  [Markdown evidence](2026-08-05-projection-refresh-sha1.md)
- [SHA-256 JSON evidence](2026-08-05-projection-refresh-sha256.json) and
  [Markdown evidence](2026-08-05-projection-refresh-sha256.md)

## Measured points

| Changed heads | SHA-1 median (ms) | SHA-256 median (ms) | Git processes |
| ---: | ---: | ---: | ---: |
| 0 | 72.31 | 66.48 | 3 |
| 1 | 144.49 | 147.01 | 8 |
| 5 | 153.18 | 152.60 | 8 |
| 50 | 187.98 | 193.42 | 8 |
| 500 | 585.20 | 611.51 | 8 |

The marginal cost is 1.0258 ms per changed head in SHA-1 and 1.0901 in SHA-256.

## Comparison with the 2026-07-30 baseline

| Changed heads | SHA-1 before (ms) | SHA-1 after (ms) | Change |
| ---: | ---: | ---: | ---: |
| 0 | 67.54 | 72.31 | +4.77 |
| 1 | 99.17 | 144.49 | +45.32 |
| 5 | 108.10 | 153.18 | +45.08 |
| 50 | 137.55 | 187.98 | +50.43 |
| 500 | 399.53 | 585.20 | +185.67 |

The unchanged point is the control: refresh still does nothing when no head
moved, and its measured cost and its three Git processes are unchanged within
run-to-run noise. Every changed point pays two costs the previous shape did not.

The first is fixed and shows up immediately at one changed head: refreshing any
changed task now also walks that task from the head the projection holds to its
new one, which adds three Git processes — an object probe, a parent-graph walk,
and one operation-blob batch — taking the measured count from five to eight
regardless of how many heads changed. Refresh still does not fan out one Git
process per changed task.

The second grows with the work actually read. From 1 to 500 changed heads the
marginal cost rose from 0.6640 to 1.0258 ms per head in SHA-1, because each
changed head now contributes its unread operation packs rather than its tip
alone. Refreshing 500 changed heads costs about 8.1x an unchanged refresh, up
from 5.9x.

The disposable projection also grew, from 372,736 to 4,726,784 bytes in SHA-1 at
20 operations per task: roughly 12.7x, or about 435 bytes of `operations` rows
per recorded operation across 10,000 operations. SHA-256 measures 5,398,528
bytes for the same fixture. The cache remains disposable and rebuildable from
Workbook refs.

Both costs are the intended consequence of the projection now materializing
operations rather than current state alone, which is what lets `workbook show
--history` and `--compare` answer from SQLite instead of walking Git on every
read. No per-row state checkpoints were added; state is reconstructed by
replaying from the root. Whether checkpoints are worth their invalidation cost
is a question for measurement against this baseline, not a guess to make now.

## Results are descriptive

This family carries no duration or Git-process target. No pass threshold was
invented, and no product behavior was changed to produce this evidence.
