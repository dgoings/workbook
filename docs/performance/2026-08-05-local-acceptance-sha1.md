# Workbook performance report

Phase: acceptance

## Reference budgets
Baseline targets are reference budgets, not achieved guarantees: warm p95 100.00 ms, cold p95 200.00 ms, burst 1000.00 ms.

## Scenarios
| Scenario | Surface | Completed | Timed out | Min (ms) | Median (ms) | P95 (ms) | P95 Git processes | Target duration | Target Git processes | Outcome |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| api-burst-independent-10 | warm-http | 20 | 0 | 241.94 | 245.78 | 251.15 | 70 | each < 1000.00 ms | - | pass |
| api-burst-same-task-10 | warm-http | 20 | 0 | 1144.94 | 1167.76 | 1182.86 | 70 | each < 1000.00 ms | - | miss |
| api-update | warm-http | 20 | 0 | 112.19 | 119.31 | 121.37 | 7 | p95 <= 100.00 ms | - | miss |
| cli-burst-independent-10 | cold-cli | 20 | 0 | 292.38 | 296.36 | 313.84 | 100 | each < 1000.00 ms | - | pass |
| cli-burst-same-task-10 | cold-cli | 20 | 0 | 1674.29 | 1698.26 | 1713.05 | 100 | each < 1000.00 ms | - | miss |
| cli-create | cold-cli | 20 | 0 | 139.01 | 174.72 | 176.12 | 10 | p95 <= 200.00 ms | - | pass |
| cli-delete | cold-cli | 20 | 0 | 163.93 | 172.33 | 173.83 | 10 | p95 <= 200.00 ms | - | pass |
| cli-depend | cold-cli | 20 | 0 | 218.47 | 223.61 | 225.67 | 12 | p95 <= 200.00 ms | - | miss |
| cli-free | cold-cli | 20 | 0 | 161.83 | 171.82 | 175.71 | 10 | p95 <= 200.00 ms | - | pass |
| cli-list | cold-cli | 20 | 0 | 68.51 | 70.82 | 72.56 | 3 | - | - | not-evaluated |
| cli-move | cold-cli | 20 | 0 | 191.87 | 225.28 | 229.12 | 12 | p95 <= 200.00 ms | - | miss |
| cli-restore | cold-cli | 20 | 0 | 163.18 | 171.70 | 174.14 | 10 | p95 <= 200.00 ms | - | pass |
| cli-update | cold-cli | 20 | 0 | 135.15 | 171.97 | 174.63 | 10 | p95 <= 200.00 ms | - | pass |
| cli-update-autosync | cold-cli | 20 | 0 | 681.27 | 697.02 | 706.97 | 26 | p95 <= 1000.00 ms | - | pass |
| cli-update-watched | cold-cli | 20 | 0 | 163.64 | 171.23 | 172.85 | 10 | p95 <= 200.00 ms | - | pass |
