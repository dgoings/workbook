# Workbook performance report

Phase: acceptance

## Reference budgets
Baseline targets are reference budgets, not achieved guarantees: warm p95 100.00 ms, cold p95 200.00 ms, burst 1000.00 ms.

## Scenarios
| Scenario | Surface | Completed | Timed out | Min (ms) | Median (ms) | P95 (ms) | P95 Git processes | Target duration | Target Git processes | Outcome |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| projection-refresh-fifty-changed | repository | 1 | 0 | 132.54 | 132.54 | 132.54 | 5 | - | - | not-evaluated |
| projection-refresh-five-changed | repository | 1 | 0 | 99.28 | 99.28 | 99.28 | 5 | - | - | not-evaluated |
| projection-refresh-five-hundred-changed | repository | 1 | 0 | 394.78 | 394.78 | 394.78 | 5 | - | - | not-evaluated |
| projection-refresh-one-changed | repository | 1 | 0 | 99.95 | 99.95 | 99.95 | 5 | - | - | not-evaluated |
| projection-refresh-unchanged | repository | 1 | 0 | 63.82 | 63.82 | 63.82 | 3 | - | - | not-evaluated |

## Projection refresh change-count family
Fixture: 525 total tasks (500 active, 25 tombstoned), 20 operations per task, sha256 object format; 1 sample(s) per point.

| Scenario | Changed task heads | Samples | Task refs | Ref enumeration median (ms) | Refresh median (ms) | Refresh p95 (ms) | Refresh median Git processes | Projected task rows | Projection cache (bytes) |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| projection-refresh-unchanged | 0 | 1 | 525 | 23.16 | 63.82 | 63.82 | 3 | 500 | 393216 |
| projection-refresh-one-changed | 1 | 1 | 525 | 21.63 | 99.95 | 99.95 | 5 | 500 | 393216 |
| projection-refresh-five-changed | 5 | 1 | 525 | 26.67 | 99.28 | 99.28 | 5 | 500 | 393216 |
| projection-refresh-fifty-changed | 50 | 1 | 525 | 24.81 | 132.54 | 132.54 | 5 | 500 | 405504 |
| projection-refresh-five-hundred-changed | 500 | 1 | 525 | 22.12 | 394.78 | 394.78 | 5 | 500 | 405504 |

Slope: Median refresh latency was 63.82 ms at 0 changed task heads and 394.78 ms at 500 changed task heads across 1 sample(s) per point, an average of 0.6619 ms per additional changed task head. Measured points: 0 changed task heads at 63.82 ms; 1 changed task heads at 99.95 ms; 5 changed task heads at 99.28 ms; 50 changed task heads at 132.54 ms; 500 changed task heads at 394.78 ms. These values describe the measured samples; this family has no pass threshold.
