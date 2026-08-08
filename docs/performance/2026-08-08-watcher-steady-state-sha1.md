# Workbook performance report

Phase: acceptance

## Reference budgets
Baseline targets are reference budgets, not achieved guarantees: warm p95 100.00 ms, cold p95 200.00 ms, burst 1000.00 ms.

## Scenarios
| Scenario | Surface | Completed | Timed out | Min (ms) | Median (ms) | P95 (ms) | P95 Git processes | Target duration | Target Git processes | Outcome |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- |

## Sync watcher steady state

Platform: darwin/arm64. Peak resident unit: bytes. Observations: 3 of 1m2.5s each.

| Window | Interval (ms) | Observed (ms) | Synchronizations | Git processes | CPU (ms) | CPU (% of one core) | Peak resident (bytes) | Major faults | Repository bytes delta |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| idle-control | 3600000 | 62501.27 | 1 | 9 | 661.49 | 1.06 | 32555008 | 148 | -95 |
| idle-control | 3600000 | 62500.61 | 1 | 9 | 799.24 | 1.28 | 32587776 | 148 | -95 |
| idle-control | 3600000 | 62502.08 | 1 | 9 | 564.51 | 0.90 | 33374208 | 148 | -95 |
| steady-interval | 5000 | 62502.03 | 13 | 117 | 4723.93 | 7.56 | 33505280 | 952 | -95 |
| steady-interval | 5000 | 62502.08 | 13 | 117 | 4236.18 | 6.78 | 34897920 | 952 | -95 |
| steady-interval | 5000 | 62500.12 | 13 | 117 | 4775.55 | 7.64 | 34242560 | 952 | -95 |

Per synchronization with nothing pending: 338.54 ms CPU, 9.00 Git processes, 0 repository bytes. Peak resident set moved 1654784 bytes between the control and the steady window. 12 additional synchronizations over a 1m2.5s window at a 5s interval, against an idle control at a 1h0m0s interval.
