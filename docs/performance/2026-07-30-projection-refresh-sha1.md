# Workbook performance report

Phase: acceptance

## Reference budgets
Baseline targets are reference budgets, not achieved guarantees: warm p95 100.00 ms, cold p95 200.00 ms, burst 1000.00 ms.

## Scenarios
| Scenario | Surface | Completed | Timed out | Min (ms) | Median (ms) | P95 (ms) | P95 Git processes | Target duration | Target Git processes | Outcome |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| projection-refresh-fifty-changed | repository | 1 | 0 | 137.55 | 137.55 | 137.55 | 5 | - | - | not-evaluated |
| projection-refresh-five-changed | repository | 1 | 0 | 108.10 | 108.10 | 108.10 | 5 | - | - | not-evaluated |
| projection-refresh-five-hundred-changed | repository | 1 | 0 | 399.53 | 399.53 | 399.53 | 5 | - | - | not-evaluated |
| projection-refresh-one-changed | repository | 1 | 0 | 99.17 | 99.17 | 99.17 | 5 | - | - | not-evaluated |
| projection-refresh-unchanged | repository | 1 | 0 | 67.54 | 67.54 | 67.54 | 3 | - | - | not-evaluated |

## Projection refresh change-count family
Fixture: 525 total tasks (500 active, 25 tombstoned), 20 operations per task, sha1 object format; 1 sample(s) per point.

| Scenario | Changed task heads | Samples | Task refs | Ref enumeration median (ms) | Refresh median (ms) | Refresh p95 (ms) | Refresh median Git processes | Projected task rows | Projection cache (bytes) |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| projection-refresh-unchanged | 0 | 1 | 525 | 22.84 | 67.54 | 67.54 | 3 | 500 | 372736 |
| projection-refresh-one-changed | 1 | 1 | 525 | 21.50 | 99.17 | 99.17 | 5 | 500 | 372736 |
| projection-refresh-five-changed | 5 | 1 | 525 | 23.11 | 108.10 | 108.10 | 5 | 500 | 376832 |
| projection-refresh-fifty-changed | 50 | 1 | 525 | 28.02 | 137.55 | 137.55 | 5 | 500 | 389120 |
| projection-refresh-five-hundred-changed | 500 | 1 | 525 | 22.72 | 399.53 | 399.53 | 5 | 500 | 397312 |

Slope: Median refresh latency was 67.54 ms at 0 changed task heads and 399.53 ms at 500 changed task heads across 1 sample(s) per point, an average of 0.6640 ms per additional changed task head. Measured points: 0 changed task heads at 67.54 ms; 1 changed task heads at 99.17 ms; 5 changed task heads at 108.10 ms; 50 changed task heads at 137.55 ms; 500 changed task heads at 399.53 ms. These values describe the measured samples; this family has no pass threshold.
