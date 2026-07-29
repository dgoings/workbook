# Workbook performance report

Phase: acceptance

## Reference budgets
Baseline targets are reference budgets, not achieved guarantees: warm p95 100.00 ms, cold p95 200.00 ms, burst 1000.00 ms.

## Scenarios
| Scenario | Surface | Completed | Timed out | Min (ms) | Median (ms) | P95 (ms) | P95 Git processes | Target time (ms) | Target Git processes | Outcome |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| sync-already-synchronized | remote-sync | 1 | 0 | 313.51 | 313.51 | 313.51 | 11 | 1000.00 | < 10 | miss |
| sync-divergent-tips | remote-sync | 0 | 0 | 0.00 | 0.00 | 0.00 | 11 | 2000.00 | < 20 | failed |
| sync-fresh-checkout | remote-sync | 1 | 0 | 530.99 | 530.99 | 530.99 | 11 | 5000.00 | < 20 | pass |
| sync-initial-publication | remote-sync | 1 | 0 | 1047.08 | 1047.08 | 1047.08 | 15 | 5000.00 | < 20 | pass |
| sync-malformed-local-tip | remote-sync | 0 | 0 | 0.00 | 0.00 | 0.00 | 8 | 2000.00 | < 20 | failed |
| sync-malformed-remote-tip | remote-sync | 0 | 0 | 0.00 | 0.00 | 0.00 | 10 | 2000.00 | < 20 | failed |
| sync-small-changed-ref-set | remote-sync | 1 | 0 | 499.72 | 499.72 | 499.72 | 20 | 2000.00 | < 20 | miss |
