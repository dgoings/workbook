# Workbook performance report

Phase: acceptance

## Reference budgets
Baseline targets are reference budgets, not achieved guarantees: warm p95 100.00 ms, cold p95 200.00 ms, burst 1000.00 ms.

## Scenarios
| Scenario | Surface | Completed | Timed out | Min (ms) | Median (ms) | P95 (ms) | P95 Git processes | Target duration | Target Git processes | Outcome |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| api-burst-independent-10 | warm-http | 20 | 0 | 240.07 | 245.24 | 249.67 | 70 | each < 1000.00 ms | - | pass |
| api-burst-same-task-10 | warm-http | 20 | 0 | 1130.26 | 1172.85 | 1183.15 | 70 | each < 1000.00 ms | - | miss |
| api-update | warm-http | 20 | 0 | 113.28 | 119.27 | 121.49 | 7 | p95 <= 100.00 ms | - | miss |
| cli-burst-independent-10 | cold-cli | 20 | 0 | 292.36 | 295.70 | 307.86 | 100 | each < 1000.00 ms | - | pass |
| cli-burst-same-task-10 | cold-cli | 20 | 0 | 1666.22 | 1697.85 | 1710.62 | 100 | each < 1000.00 ms | - | miss |
| cli-create | cold-cli | 20 | 0 | 157.65 | 174.59 | 177.53 | 10 | p95 <= 200.00 ms | - | pass |
| cli-delete | cold-cli | 20 | 0 | 158.95 | 171.51 | 174.26 | 10 | p95 <= 200.00 ms | - | pass |
| cli-depend | cold-cli | 20 | 0 | 208.46 | 225.31 | 227.59 | 12 | p95 <= 200.00 ms | - | miss |
| cli-free | cold-cli | 20 | 0 | 162.35 | 171.49 | 174.60 | 10 | p95 <= 200.00 ms | - | pass |
| cli-list | cold-cli | 20 | 0 | 67.79 | 70.81 | 71.70 | 3 | - | - | not-evaluated |
| cli-move | cold-cli | 20 | 0 | 212.31 | 223.13 | 226.15 | 12 | p95 <= 200.00 ms | - | miss |
| cli-restore | cold-cli | 20 | 0 | 149.71 | 172.23 | 174.19 | 10 | p95 <= 200.00 ms | - | pass |
| cli-update | cold-cli | 20 | 0 | 166.34 | 172.86 | 174.45 | 10 | p95 <= 200.00 ms | - | pass |
| cli-update-autosync | cold-cli | 20 | 0 | 664.77 | 698.45 | 711.54 | 26 | p95 <= 1000.00 ms | - | pass |
| cli-update-watched | cold-cli | 20 | 0 | 150.94 | 169.66 | 173.82 | 10 | p95 <= 200.00 ms | - | pass |
