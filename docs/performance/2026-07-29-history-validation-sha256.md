# Workbook performance report

Phase: acceptance

## Reference budgets
Baseline targets are reference budgets, not achieved guarantees: warm p95 100.00 ms, cold p95 200.00 ms, burst 1000.00 ms.

## Scenarios
| Scenario | Surface | Completed | Timed out | Min (ms) | Median (ms) | P95 (ms) | P95 Git processes | Target time (ms) | Target Git processes | Outcome |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| validate-cached-unchanged | history-validation | 1 | 0 | 116.71 | 116.71 | 116.71 | 4 | 500.00 | < 12 | pass |
| validate-five-changed | history-validation | 1 | 0 | 166.03 | 166.03 | 166.03 | 7 | 1000.00 | < 12 | pass |
| validate-full-history | history-validation | 1 | 0 | 2185.65 | 2185.65 | 2185.65 | 7 | 10000.00 | < 12 | pass |
