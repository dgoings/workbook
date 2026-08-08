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
| idle-control | 3600000 | 62502.08 | 1 | 9 | 872.46 | 1.40 | 33734656 | 144 | -95 |
| idle-control | 3600000 | 62500.46 | 1 | 9 | 571.64 | 0.91 | 31227904 | 144 | -95 |
| idle-control | 3600000 | 62502.09 | 1 | 9 | 577.21 | 0.92 | 32292864 | 144 | -95 |
| steady-interval | 5000 | 62502.06 | 13 | 117 | 4186.64 | 6.70 | 34144256 | 924 | -95 |
| steady-interval | 5000 | 62502.02 | 13 | 117 | 3848.36 | 6.16 | 34095104 | 924 | -94 |
| steady-interval | 5000 | 62502.07 | 13 | 117 | 4194.97 | 6.71 | 34586624 | 924 | -95 |

Per synchronization with nothing pending: 300.79 ms CPU, 9.00 Git processes, 0 repository bytes. Peak resident set moved 1851392 bytes between the control and the steady window. 12 additional synchronizations over a 1m2.5s window at a 5s interval, against an idle control at a 1h0m0s interval.
