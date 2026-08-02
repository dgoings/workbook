# Workbook performance report

Phase: acceptance

## Reference budgets
Baseline targets are reference budgets, not achieved guarantees: warm p95 100.00 ms, cold p95 200.00 ms, burst 1000.00 ms.

## Scenarios
| Scenario | Surface | Completed | Timed out | Min (ms) | Median (ms) | P95 (ms) | P95 Git processes | Target duration | Target Git processes | Outcome |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| api-burst-independent-10 | warm-http | 20 | 0 | 218.58 | 226.27 | 233.15 | 60 | each < 1000.00 ms | - | pass |
| api-burst-same-task-10 | warm-http | 20 | 0 | 1026.08 | 1045.74 | 1076.28 | 60 | each < 1000.00 ms | - | miss |
| api-update | warm-http | 20 | 0 | 101.06 | 107.25 | 114.06 | 6 | p95 <= 100.00 ms | - | miss |
| cli-burst-independent-10 | cold-cli | 20 | 0 | 270.78 | 280.34 | 293.71 | 90 | each < 1000.00 ms | - | pass |
| cli-burst-same-task-10 | cold-cli | 20 | 0 | 1526.53 | 1580.04 | 1612.43 | 90 | each < 1000.00 ms | - | miss |
| cli-create | cold-cli | 20 | 0 | 151.13 | 163.86 | 171.06 | 9 | p95 <= 200.00 ms | - | pass |
| cli-delete | cold-cli | 20 | 0 | 150.16 | 160.29 | 179.50 | 9 | p95 <= 200.00 ms | - | pass |
| cli-depend | cold-cli | 20 | 0 | 186.25 | 215.28 | 221.73 | 11 | p95 <= 200.00 ms | - | miss |
| cli-free | cold-cli | 20 | 0 | 151.58 | 160.80 | 172.04 | 9 | p95 <= 200.00 ms | - | pass |
| cli-list | cold-cli | 20 | 0 | 63.15 | 71.26 | 75.27 | 3 | - | - | not-evaluated |
| cli-move | cold-cli | 20 | 0 | 201.57 | 211.55 | 227.90 | 11 | p95 <= 200.00 ms | - | miss |
| cli-restore | cold-cli | 20 | 0 | 145.52 | 160.03 | 172.65 | 9 | p95 <= 200.00 ms | - | pass |
| cli-update | cold-cli | 20 | 0 | 147.37 | 161.45 | 179.76 | 9 | p95 <= 200.00 ms | - | pass |
| cli-update-autosync | cold-cli | 20 | 0 | 663.54 | 678.51 | 695.90 | 25 | p95 <= 1000.00 ms | - | pass |
