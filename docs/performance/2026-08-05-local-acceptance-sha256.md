# Workbook performance report

Phase: acceptance

## Reference budgets
Baseline targets are reference budgets, not achieved guarantees: warm p95 100.00 ms, cold p95 200.00 ms, burst 1000.00 ms.

## Scenarios
| Scenario | Surface | Completed | Timed out | Min (ms) | Median (ms) | P95 (ms) | P95 Git processes | Target duration | Target Git processes | Outcome |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| api-burst-independent-10 | warm-http | 20 | 0 | 239.60 | 241.91 | 247.48 | 70 | each < 1000.00 ms | - | pass |
| api-burst-same-task-10 | warm-http | 20 | 0 | 1117.83 | 1159.25 | 1170.36 | 70 | each < 1000.00 ms | - | miss |
| api-update | warm-http | 20 | 0 | 105.37 | 118.16 | 121.17 | 7 | p95 <= 100.00 ms | - | miss |
| cli-burst-independent-10 | cold-cli | 20 | 0 | 291.21 | 295.54 | 299.59 | 100 | each < 1000.00 ms | - | pass |
| cli-burst-same-task-10 | cold-cli | 20 | 0 | 1673.32 | 1695.80 | 1709.63 | 100 | each < 1000.00 ms | - | miss |
| cli-create | cold-cli | 20 | 0 | 127.72 | 173.56 | 176.37 | 10 | p95 <= 200.00 ms | - | pass |
| cli-delete | cold-cli | 20 | 0 | 164.73 | 171.59 | 174.64 | 10 | p95 <= 200.00 ms | - | pass |
| cli-depend | cold-cli | 20 | 0 | 208.70 | 222.49 | 225.30 | 12 | p95 <= 200.00 ms | - | miss |
| cli-free | cold-cli | 20 | 0 | 135.52 | 170.06 | 173.32 | 10 | p95 <= 200.00 ms | - | pass |
| cli-list | cold-cli | 20 | 0 | 67.17 | 70.25 | 72.11 | 3 | - | - | not-evaluated |
| cli-move | cold-cli | 20 | 0 | 218.95 | 222.96 | 226.46 | 12 | p95 <= 200.00 ms | - | miss |
| cli-restore | cold-cli | 20 | 0 | 143.96 | 172.22 | 175.66 | 10 | p95 <= 200.00 ms | - | pass |
| cli-update | cold-cli | 20 | 0 | 155.07 | 170.59 | 176.02 | 10 | p95 <= 200.00 ms | - | pass |
| cli-update-autosync | cold-cli | 20 | 0 | 645.39 | 697.89 | 705.49 | 26 | p95 <= 1000.00 ms | - | pass |
| cli-update-watched | cold-cli | 20 | 0 | 160.33 | 170.38 | 174.65 | 10 | p95 <= 200.00 ms | - | pass |
