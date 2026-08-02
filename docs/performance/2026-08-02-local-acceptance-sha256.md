# Workbook performance report

Phase: acceptance

## Reference budgets
Baseline targets are reference budgets, not achieved guarantees: warm p95 100.00 ms, cold p95 200.00 ms, burst 1000.00 ms.

## Scenarios
| Scenario | Surface | Completed | Timed out | Min (ms) | Median (ms) | P95 (ms) | P95 Git processes | Target duration | Target Git processes | Outcome |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| api-burst-independent-10 | warm-http | 20 | 0 | 221.26 | 227.63 | 232.95 | 60 | each < 1000.00 ms | - | pass |
| api-burst-same-task-10 | warm-http | 20 | 0 | 973.25 | 1030.09 | 1077.66 | 60 | each < 1000.00 ms | - | miss |
| api-update | warm-http | 20 | 0 | 92.31 | 105.82 | 108.91 | 6 | p95 <= 100.00 ms | - | miss |
| cli-burst-independent-10 | cold-cli | 20 | 0 | 275.28 | 280.24 | 289.05 | 90 | each < 1000.00 ms | - | pass |
| cli-burst-same-task-10 | cold-cli | 20 | 0 | 1523.04 | 1578.24 | 1635.86 | 90 | each < 1000.00 ms | - | miss |
| cli-create | cold-cli | 20 | 0 | 144.00 | 163.39 | 169.00 | 9 | p95 <= 200.00 ms | - | pass |
| cli-delete | cold-cli | 20 | 0 | 149.12 | 159.06 | 165.46 | 9 | p95 <= 200.00 ms | - | pass |
| cli-depend | cold-cli | 20 | 0 | 194.02 | 212.42 | 223.87 | 11 | p95 <= 200.00 ms | - | miss |
| cli-free | cold-cli | 20 | 0 | 149.52 | 160.71 | 172.31 | 9 | p95 <= 200.00 ms | - | pass |
| cli-list | cold-cli | 20 | 0 | 62.32 | 70.34 | 72.18 | 3 | - | - | not-evaluated |
| cli-move | cold-cli | 20 | 0 | 203.84 | 213.02 | 219.61 | 11 | p95 <= 200.00 ms | - | miss |
| cli-restore | cold-cli | 20 | 0 | 140.85 | 161.41 | 167.92 | 9 | p95 <= 200.00 ms | - | pass |
| cli-update | cold-cli | 20 | 0 | 148.73 | 162.96 | 170.93 | 9 | p95 <= 200.00 ms | - | pass |
| cli-update-autosync | cold-cli | 20 | 0 | 670.41 | 687.69 | 745.53 | 25 | p95 <= 1000.00 ms | - | pass |
