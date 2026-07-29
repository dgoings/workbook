# Workbook performance benchmarks

Workbook's performance harness measures representative cold CLI, warm HTTP,
burst, projection, validation, synchronization, and Git repository paths against
generated task histories. Run benchmarks from the repository root.

## Bounded baseline

The 2026-07-28 baseline uses 500 active tasks with exactly 20 operations per
task, one sample per scenario, and a 60-second per-command timeout:

[The current baseline evidence](2026-07-28-baseline.md) is an explicitly
hand-authored, incomplete lower-bound record. Both SHA-1 attempts aborted before
report assembly, so there is no generated JSON report or complete per-scenario
result for this baseline.

```sh
go build -o /tmp/workbook-benchmark-target ./cmd/workbook
go run ./cmd/workbook-bench \
  --workbook /tmp/workbook-benchmark-target \
  --tasks 500 \
  --operations 20 \
  --samples 1 \
  --timeout 60s \
  --object-format sha1 \
  --phase baseline \
  --output-json docs/performance/2026-07-28-baseline.json \
  --output-markdown docs/performance/2026-07-28-baseline.md
```

Fixture construction and other setup work are excluded from scenario timings.
A baseline timeout, product-command failure, or missed reference budget is
recorded evidence rather than a reason to tune or rerun the baseline. The target
numbers in a baseline report are reference budgets, not claims that the current
implementation achieves them.

The final performance acceptance task will run multiple samples after every
target path is implemented. Acceptance may use a larger fixture, but it must use
at least 500 active tasks and 20 operations per task.

When the installed Git supports SHA-256 repositories, run the same bounded
baseline once with `--object-format sha256` and write the results to
`docs/performance/2026-07-28-baseline-sha256.json` and
`docs/performance/2026-07-28-baseline-sha256.md`.

## Remote synchronization topologies

`workbook-bench` accepts repeatable `--scenario <name>` selectors. With one or
more selectors, it runs only the selected scenarios; unknown and duplicate
names are rejected before fixture construction. Omitting `--scenario` includes
the existing local scenarios and every registered remote synchronization
scenario. Selected scenarios run in the harness's stable registry order, not
the order of the flags.

Every remote scenario requires at least 500 active tasks and 20 operations per
task, even for a baseline run. The seven remote selectors are:

Synchronization measurements exercise the bounded default path: one isolated
tracking fetch, current-tip validation, ancestry classification, and ref
publication. They deliberately do not include a replay of every buried
checkpoint; the planned explicit validation audit is a separate future path.

| Selector | Workbook command and topology | Reference target |
| --- | --- | --- |
| `sync-fresh-checkout` | `fetch` from 500 populated remote task refs into a fresh checkout | at most 5 seconds; fewer than 20 Git processes |
| `sync-initial-publication` | `push` a populated local repository to an empty bare remote | at most 5 seconds; fewer than 20 Git processes |
| `sync-already-synchronized` | `sync` when local, tracking, and remote refs match | at most 1 second; fewer than 10 Git processes |
| `sync-small-changed-ref-set` | `sync` with five local-ahead and five disjoint remote-ahead tasks | at most 2 seconds; fewer than 20 Git processes |
| `sync-divergent-tips` | `sync` with one task whose local and remote tips diverge | at most 2 seconds; fewer than 20 Git processes |
| `sync-malformed-local-tip` | `push` with one malformed owned local task ref | at most 2 seconds; fewer than 20 Git processes |
| `sync-malformed-remote-tip` | `fetch` with one malformed fetched tracking ref | at most 2 seconds; fewer than 20 Git processes |

The remote report target is evaluated per scenario. `pass` means every sample
completed within the inclusive time limit and below the exclusive Git-process
limit; `miss` means a completed sample exceeded either budget; `timeout` means
at least one sample reached its command timeout; and `failed` means a command
sample failed. Scenarios with no target are `not-evaluated`. Expected product
errors in the divergent and malformed topologies remain measured samples when
their result and ref invariants are correct, but their nonzero command samples
still produce `failed` evidence. A timeout's elapsed duration is a lower bound:
it shows the command ran for at least that long, not its final latency. Harness
setup, report encoding, JSON decoding, or ref-verification failures are fatal
because they make the measurement untrustworthy.

These targets are reference budgets, not achieved-performance guarantees. A
timeout, product failure, or target miss is evidence to record, not a reason to
tune or replace the one-shot baseline.

Build the measured binary once, then run each object format at most once. These
commands select only the seven remote scenarios and write separate reports;
they do not replace the incomplete whole-harness record above.

```sh
go build -buildvcs=false -o /private/tmp/workbook-sync-baseline ./cmd/workbook

go run ./cmd/workbook-bench \
  --workbook /private/tmp/workbook-sync-baseline \
  --tasks 500 \
  --operations 20 \
  --samples 1 \
  --timeout 60s \
  --object-format sha1 \
  --phase baseline \
  --scenario sync-fresh-checkout \
  --scenario sync-initial-publication \
  --scenario sync-already-synchronized \
  --scenario sync-small-changed-ref-set \
  --scenario sync-divergent-tips \
  --scenario sync-malformed-local-tip \
  --scenario sync-malformed-remote-tip \
  --output-json docs/performance/2026-07-28-sync-baseline-sha1.json \
  --output-markdown docs/performance/2026-07-28-sync-baseline-sha1.md
```

When `git init --object-format=sha256` is supported, run the following once;
otherwise record that SHA-256 is unsupported and do not substitute a SHA-1 run.

```sh
go run ./cmd/workbook-bench \
  --workbook /private/tmp/workbook-sync-baseline \
  --tasks 500 \
  --operations 20 \
  --samples 1 \
  --timeout 60s \
  --object-format sha256 \
  --phase baseline \
  --scenario sync-fresh-checkout \
  --scenario sync-initial-publication \
  --scenario sync-already-synchronized \
  --scenario sync-small-changed-ref-set \
  --scenario sync-divergent-tips \
  --scenario sync-malformed-local-tip \
  --scenario sync-malformed-remote-tip \
  --output-json docs/performance/2026-07-28-sync-baseline-sha256.json \
  --output-markdown docs/performance/2026-07-28-sync-baseline-sha256.md
```

## Reading the reports

A completed harness run produces a versioned, machine-readable JSON report and a
compact generated Markdown view of the same scenarios. Each scenario then
records a concrete result, timeout, or product-command failure. Harness and
output failures remain fatal and may prevent report creation, as happened for
the current hand-authored lower-bound evidence.
