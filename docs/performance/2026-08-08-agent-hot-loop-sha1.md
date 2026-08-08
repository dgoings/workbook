# Workbook performance report

Phase: acceptance

## Reference budgets
Baseline targets are reference budgets, not achieved guarantees: warm p95 100.00 ms, cold p95 200.00 ms, burst 1000.00 ms.

## Scenarios
| Scenario | Surface | Completed | Timed out | Min (ms) | Median (ms) | P95 (ms) | P95 Git processes | Target duration | Target Git processes | Outcome |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| api-tasks | warm-http | 20 | 0 | 28.62 | 30.75 | 33.16 | 1 | - | - | not-evaluated |
| api-update | warm-http | 20 | 0 | 106.59 | 114.67 | 121.98 | 8 | p95 <= 100.00 ms | - | miss |
| cli-list | cold-cli | 20 | 0 | 58.21 | 79.32 | 165.18 | 3 | - | - | not-evaluated |
| cli-next | cold-cli | 20 | 0 | 401.19 | 652.73 | 1149.70 | 10 | p95 <= 1000.00 ms | - | miss |
| cli-show | cold-cli | 20 | 0 | 52.70 | 70.05 | 119.20 | 3 | p95 <= 200.00 ms | - | pass |
| cli-update | cold-cli | 20 | 0 | 137.41 | 191.84 | 299.40 | 10 | p95 <= 200.00 ms | - | miss |
| cli-update-autosync | cold-cli | 20 | 0 | 602.78 | 906.65 | 1480.54 | 26 | p95 <= 1000.00 ms | - | miss |
