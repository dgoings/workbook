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

## Reading the reports

A completed harness run produces a versioned, machine-readable JSON report and a
compact generated Markdown view of the same scenarios. Each scenario then
records a concrete result, timeout, or product-command failure. Harness and
output failures remain fatal and may prevent report creation, as happened for
the current hand-authored lower-bound evidence.
