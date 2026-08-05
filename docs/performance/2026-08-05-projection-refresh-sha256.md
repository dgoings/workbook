# Workbook performance report

Phase: acceptance

## Reference budgets
Baseline targets are reference budgets, not achieved guarantees: warm p95 100.00 ms, cold p95 200.00 ms, burst 1000.00 ms.

## Scenarios
| Scenario | Surface | Completed | Timed out | Min (ms) | Median (ms) | P95 (ms) | P95 Git processes | Target duration | Target Git processes | Outcome |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| projection-refresh-fifty-changed | repository | 1 | 0 | 193.42 | 193.42 | 193.42 | 8 | - | - | not-evaluated |
| projection-refresh-five-changed | repository | 1 | 0 | 152.60 | 152.60 | 152.60 | 8 | - | - | not-evaluated |
| projection-refresh-five-hundred-changed | repository | 1 | 0 | 611.51 | 611.51 | 611.51 | 8 | - | - | not-evaluated |
| projection-refresh-one-changed | repository | 1 | 0 | 147.01 | 147.01 | 147.01 | 8 | - | - | not-evaluated |
| projection-refresh-unchanged | repository | 1 | 0 | 66.48 | 66.48 | 66.48 | 3 | - | - | not-evaluated |

## Projection refresh change-count family
Fixture: 525 total tasks (500 active, 25 tombstoned), 20 operations per task, sha256 object format; 1 sample(s) per point.

| Scenario | Changed task heads | Samples | Task refs | Ref enumeration median (ms) | Refresh median (ms) | Refresh p95 (ms) | Refresh median Git processes | Projected task rows | Projection cache (bytes) |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| projection-refresh-unchanged | 0 | 1 | 525 | 24.77 | 66.48 | 66.48 | 3 | 500 | 5398528 |
| projection-refresh-one-changed | 1 | 1 | 525 | 24.88 | 147.01 | 147.01 | 8 | 500 | 5398528 |
| projection-refresh-five-changed | 5 | 1 | 525 | 25.07 | 152.60 | 152.60 | 8 | 500 | 5402624 |
| projection-refresh-fifty-changed | 50 | 1 | 525 | 24.76 | 193.42 | 193.42 | 8 | 500 | 5431296 |
| projection-refresh-five-hundred-changed | 500 | 1 | 525 | 24.28 | 611.51 | 611.51 | 8 | 500 | 5562368 |

Slope: Median refresh latency was 66.48 ms at 0 changed task heads and 611.51 ms at 500 changed task heads across 1 sample(s) per point, an average of 1.0901 ms per additional changed task head. Measured points: 0 changed task heads at 66.48 ms; 1 changed task heads at 147.01 ms; 5 changed task heads at 152.60 ms; 50 changed task heads at 193.42 ms; 500 changed task heads at 611.51 ms. These values describe the measured samples; this family has no pass threshold.
