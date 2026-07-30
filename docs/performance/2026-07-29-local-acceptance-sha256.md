# Workbook performance report

Phase: acceptance

## Reference budgets
Baseline targets are reference budgets, not achieved guarantees: warm p95 100.00 ms, cold p95 200.00 ms, burst 1000.00 ms.

## Scenarios
| Scenario | Surface | Completed | Timed out | Min (ms) | Median (ms) | P95 (ms) | P95 Git processes | Target duration | Target Git processes | Outcome |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| api-burst-independent-10 | warm-http | 20 | 0 | 212.10 | 216.49 | 225.03 | 60 | each < 1000.00 ms | - | pass |
| api-burst-same-task-10 | warm-http | 20 | 0 | 960.81 | 1000.96 | 1015.95 | 60 | each < 1000.00 ms | - | miss |
| api-update | warm-http | 20 | 0 | 97.10 | 98.54 | 102.85 | 6 | p95 <= 100.00 ms | - | miss |
| cli-burst-independent-10 | cold-cli | 20 | 0 | 270.14 | 292.25 | 349.50 | 90 | each < 1000.00 ms | - | pass |
| cli-burst-same-task-10 | cold-cli | 20 | 0 | 1221.99 | 1495.62 | 1635.24 | 90 | each < 1000.00 ms | - | miss |
| cli-create | cold-cli | 20 | 0 | 119.99 | 150.20 | 192.35 | 9 | p95 <= 200.00 ms | - | pass |
| cli-delete | cold-cli | 20 | 0 | 120.58 | 144.61 | 191.02 | 9 | p95 <= 200.00 ms | - | pass |
| cli-depend | cold-cli | 20 | 0 | 158.95 | 195.23 | 292.94 | 11 | p95 <= 200.00 ms | - | miss |
| cli-free | cold-cli | 20 | 0 | 123.36 | 163.44 | 235.95 | 10 | p95 <= 200.00 ms | - | miss |
| cli-move | cold-cli | 20 | 0 | 154.75 | 188.02 | 274.00 | 11 | p95 <= 200.00 ms | - | miss |
| cli-restore | cold-cli | 20 | 0 | 116.98 | 150.19 | 167.28 | 9 | p95 <= 200.00 ms | - | pass |
| cli-update | cold-cli | 20 | 0 | 115.10 | 147.10 | 156.27 | 9 | p95 <= 200.00 ms | - | pass |
