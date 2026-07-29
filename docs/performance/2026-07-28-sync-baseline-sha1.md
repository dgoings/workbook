# Workbook performance report

Phase: baseline

## Reference budgets
Baseline targets are reference budgets, not achieved guarantees: warm p95 100.00 ms, cold p95 200.00 ms, burst 1000.00 ms.

## Scenarios
| Scenario | Surface | Completed | Timed out | Min (ms) | Median (ms) | P95 (ms) | P95 Git processes | Target time (ms) | Target Git processes | Outcome |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| sync-already-synchronized | remote-sync | 0 | 1 | 0.00 | 0.00 | 0.00 | 4067 | 1000.00 | < 10 | timeout |
| sync-divergent-tips | remote-sync | 0 | 1 | 0.00 | 0.00 | 0.00 | 4063 | 2000.00 | < 20 | timeout |
| sync-fresh-checkout | remote-sync | 0 | 1 | 0.00 | 0.00 | 0.00 | 4082 | 5000.00 | < 20 | timeout |
| sync-initial-publication | remote-sync | 0 | 1 | 0.00 | 0.00 | 0.00 | 4052 | 5000.00 | < 20 | timeout |
| sync-malformed-local-tip | remote-sync | 0 | 1 | 0.00 | 0.00 | 0.00 | 3997 | 2000.00 | < 20 | timeout |
| sync-malformed-remote-tip | remote-sync | 0 | 1 | 0.00 | 0.00 | 0.00 | 4073 | 2000.00 | < 20 | timeout |
| sync-small-changed-ref-set | remote-sync | 0 | 1 | 0.00 | 0.00 | 0.00 | 4063 | 2000.00 | < 20 | timeout |
