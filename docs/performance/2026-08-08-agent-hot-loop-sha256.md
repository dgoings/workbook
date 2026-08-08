# Workbook performance report

Phase: acceptance

## Reference budgets
Baseline targets are reference budgets, not achieved guarantees: warm p95 100.00 ms, cold p95 200.00 ms, burst 1000.00 ms.

## Scenarios
| Scenario | Surface | Completed | Timed out | Min (ms) | Median (ms) | P95 (ms) | P95 Git processes | Target duration | Target Git processes | Outcome |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| api-tasks | warm-http | 20 | 0 | 31.51 | 35.54 | 45.79 | 1 | - | - | not-evaluated |
| api-update | warm-http | 20 | 0 | 110.79 | 125.34 | 179.88 | 8 | p95 <= 100.00 ms | - | miss |
| cli-list | cold-cli | 20 | 0 | 55.32 | 69.38 | 124.08 | 3 | - | - | not-evaluated |
| cli-next | cold-cli | 20 | 0 | 404.26 | 483.72 | 912.81 | 10 | p95 <= 1000.00 ms | - | pass |
| cli-show | cold-cli | 20 | 0 | 52.15 | 63.59 | 138.71 | 3 | p95 <= 200.00 ms | - | pass |
| cli-update | cold-cli | 20 | 0 | 133.17 | 154.93 | 265.59 | 10 | p95 <= 200.00 ms | - | miss |
| cli-update-autosync | cold-cli | 20 | 0 | 645.25 | 765.37 | 1528.82 | 26 | p95 <= 1000.00 ms | - | miss |
