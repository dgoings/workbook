# Workbook performance report

Phase: acceptance

## Reference budgets
Baseline targets are reference budgets, not achieved guarantees: warm p95 100.00 ms, cold p95 200.00 ms, burst 1000.00 ms.

## Scenarios
| Scenario | Surface | Completed | Timed out | Min (ms) | Median (ms) | P95 (ms) | P95 Git processes | Target duration | Target Git processes | Outcome |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| projection-refresh-fifty-changed | repository | 1 | 0 | 187.98 | 187.98 | 187.98 | 8 | - | - | not-evaluated |
| projection-refresh-five-changed | repository | 1 | 0 | 153.18 | 153.18 | 153.18 | 8 | - | - | not-evaluated |
| projection-refresh-five-hundred-changed | repository | 1 | 0 | 585.20 | 585.20 | 585.20 | 8 | - | - | not-evaluated |
| projection-refresh-one-changed | repository | 1 | 0 | 144.49 | 144.49 | 144.49 | 8 | - | - | not-evaluated |
| projection-refresh-unchanged | repository | 1 | 0 | 72.31 | 72.31 | 72.31 | 3 | - | - | not-evaluated |

## Projection refresh change-count family
Fixture: 525 total tasks (500 active, 25 tombstoned), 20 operations per task, sha1 object format; 1 sample(s) per point.

| Scenario | Changed task heads | Samples | Task refs | Ref enumeration median (ms) | Refresh median (ms) | Refresh p95 (ms) | Refresh median Git processes | Projected task rows | Projection cache (bytes) |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| projection-refresh-unchanged | 0 | 1 | 525 | 24.09 | 72.31 | 72.31 | 3 | 500 | 4726784 |
| projection-refresh-one-changed | 1 | 1 | 525 | 23.93 | 144.49 | 144.49 | 8 | 500 | 4726784 |
| projection-refresh-five-changed | 5 | 1 | 525 | 25.00 | 153.18 | 153.18 | 8 | 500 | 4730880 |
| projection-refresh-fifty-changed | 50 | 1 | 525 | 24.38 | 187.98 | 187.98 | 8 | 500 | 4759552 |
| projection-refresh-five-hundred-changed | 500 | 1 | 525 | 25.64 | 585.20 | 585.20 | 8 | 500 | 4886528 |

Slope: Median refresh latency was 72.31 ms at 0 changed task heads and 585.20 ms at 500 changed task heads across 1 sample(s) per point, an average of 1.0258 ms per additional changed task head. Measured points: 0 changed task heads at 72.31 ms; 1 changed task heads at 144.49 ms; 5 changed task heads at 153.18 ms; 50 changed task heads at 187.98 ms; 500 changed task heads at 585.20 ms. These values describe the measured samples; this family has no pass threshold.
