# Workbook performance report

Phase: acceptance

## Reference budgets
Baseline targets are reference budgets, not achieved guarantees: warm p95 100.00 ms, cold p95 200.00 ms, burst 1000.00 ms.

## Scenarios
| Scenario | Surface | Completed | Timed out | Min (ms) | Median (ms) | P95 (ms) | P95 Git processes | Target duration | Target Git processes | Outcome |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| api-burst-independent-10 | warm-http | 20 | 0 | 211.37 | 217.49 | 290.49 | 60 | each < 1000.00 ms | - | pass |
| api-burst-same-task-10 | warm-http | 20 | 0 | 838.17 | 994.96 | 1356.64 | 60 | each < 1000.00 ms | - | miss |
| api-update | warm-http | 20 | 0 | 87.64 | 97.39 | 134.16 | 6 | p95 <= 100.00 ms | - | miss |
| cli-burst-independent-10 | cold-cli | 20 | 0 | 265.11 | 273.31 | 280.06 | 90 | each < 1000.00 ms | - | pass |
| cli-burst-same-task-10 | cold-cli | 20 | 0 | 1454.17 | 1521.40 | 1541.34 | 90 | each < 1000.00 ms | - | miss |
| cli-create | cold-cli | 20 | 0 | 148.10 | 154.27 | 159.84 | 9 | p95 <= 200.00 ms | - | pass |
| cli-delete | cold-cli | 20 | 0 | 128.82 | 149.06 | 152.88 | 9 | p95 <= 200.00 ms | - | pass |
| cli-depend | cold-cli | 20 | 0 | 162.53 | 195.83 | 201.93 | 11 | p95 <= 200.00 ms | - | miss |
| cli-free | cold-cli | 20 | 0 | 130.70 | 172.25 | 175.35 | 10 | p95 <= 200.00 ms | - | pass |
| cli-move | cold-cli | 20 | 0 | 182.56 | 196.54 | 199.18 | 11 | p95 <= 200.00 ms | - | pass |
| cli-restore | cold-cli | 20 | 0 | 119.99 | 150.65 | 156.89 | 9 | p95 <= 200.00 ms | - | pass |
| cli-update | cold-cli | 20 | 0 | 141.89 | 150.52 | 153.74 | 9 | p95 <= 200.00 ms | - | pass |
