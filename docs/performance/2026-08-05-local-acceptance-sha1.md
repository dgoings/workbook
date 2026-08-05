# Workbook performance report

Phase: acceptance

## Reference budgets
Baseline targets are reference budgets, not achieved guarantees: warm p95 100.00 ms, cold p95 200.00 ms, burst 1000.00 ms.

## Scenarios
| Scenario | Surface | Completed | Timed out | Min (ms) | Median (ms) | P95 (ms) | P95 Git processes | Target duration | Target Git processes | Outcome |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| api-burst-independent-10 | warm-http | 20 | 0 | 237.69 | 243.88 | 248.16 | 70 | each < 1000.00 ms | - | pass |
| api-burst-same-task-10 | warm-http | 20 | 0 | 1128.04 | 1160.16 | 1174.40 | 70 | each < 1000.00 ms | - | miss |
| api-update | warm-http | 20 | 0 | 111.38 | 117.33 | 119.60 | 7 | p95 <= 100.00 ms | - | miss |
| cli-burst-independent-10 | cold-cli | 20 | 0 | 291.29 | 294.96 | 302.24 | 100 | each < 1000.00 ms | - | pass |
| cli-burst-same-task-10 | cold-cli | 20 | 0 | 1661.27 | 1697.89 | 1704.53 | 100 | each < 1000.00 ms | - | miss |
| cli-create | cold-cli | 20 | 0 | 162.23 | 173.93 | 176.42 | 10 | p95 <= 200.00 ms | - | pass |
| cli-delete | cold-cli | 20 | 0 | 166.75 | 170.73 | 173.77 | 10 | p95 <= 200.00 ms | - | pass |
| cli-depend | cold-cli | 20 | 0 | 213.44 | 222.93 | 225.47 | 12 | p95 <= 200.00 ms | - | miss |
| cli-free | cold-cli | 20 | 0 | 148.30 | 170.22 | 173.35 | 10 | p95 <= 200.00 ms | - | pass |
| cli-list | cold-cli | 20 | 0 | 65.10 | 70.07 | 71.19 | 3 | - | - | not-evaluated |
| cli-move | cold-cli | 20 | 0 | 212.85 | 223.15 | 225.30 | 12 | p95 <= 200.00 ms | - | miss |
| cli-restore | cold-cli | 20 | 0 | 151.29 | 168.08 | 173.60 | 10 | p95 <= 200.00 ms | - | pass |
| cli-update | cold-cli | 20 | 0 | 168.31 | 171.82 | 174.27 | 10 | p95 <= 200.00 ms | - | pass |
| cli-update-autosync | cold-cli | 20 | 0 | 677.44 | 695.92 | 704.35 | 26 | p95 <= 1000.00 ms | - | pass |
| cli-update-watched | cold-cli | 20 | 0 | 150.69 | 169.87 | 171.87 | 10 | p95 <= 200.00 ms | - | pass |
