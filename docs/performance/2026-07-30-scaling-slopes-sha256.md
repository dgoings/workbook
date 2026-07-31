# Workbook performance scaling report

Phase: scaling

Object format: sha256

Samples per scenario: 20

## Matrix points
| Point | Active tasks | Tombstoned tasks | Total tasks | History depth | Object format |
| --- | ---: | ---: | ---: | ---: | --- |
| active-100-depth-20 | 100 | 5 | 105 | 20 | sha256 |
| active-500-depth-20 | 500 | 25 | 525 | 20 | sha256 |
| active-500-depth-100 | 500 | 25 | 525 | 100 | sha256 |
| active-1000-depth-20 | 1000 | 50 | 1050 | 20 | sha256 |

## Measurements

### active-100-depth-20

| Scenario | Surface | Completed | Timed out | Min (ms) | Median (ms) | P95 (ms) | P95 Git processes | Outcome |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| cli-create | cold-cli | 20 | 0 | 131.10 | 150.06 | 156.75 | 9 | not-evaluated |
| cli-depend | cold-cli | 20 | 0 | 165.33 | 186.18 | 189.03 | 11 | not-evaluated |
| cli-list | cold-cli | 20 | 0 | 44.82 | 58.15 | 59.40 | 3 | not-evaluated |
| cli-move | cold-cli | 20 | 0 | 141.03 | 183.71 | 193.48 | 11 | not-evaluated |
| cli-update | cold-cli | 20 | 0 | 119.79 | 147.48 | 152.47 | 9 | not-evaluated |
| sync-already-synchronized | remote-sync | 20 | 0 | 169.76 | 183.68 | 189.12 | 9 | not-evaluated |
| sync-small-changed-ref-set | remote-sync | 20 | 0 | 282.62 | 339.29 | 353.28 | 18 | not-evaluated |
| validate-full-history | history-validation | 20 | 0 | 354.56 | 362.59 | 371.89 | 7 | not-evaluated |
| validate-cached-unchanged | history-validation | 20 | 0 | 76.21 | 81.50 | 84.25 | 4 | not-evaluated |

### active-500-depth-20

| Scenario | Surface | Completed | Timed out | Min (ms) | Median (ms) | P95 (ms) | P95 Git processes | Outcome |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| cli-create | cold-cli | 20 | 0 | 155.47 | 162.74 | 168.53 | 9 | not-evaluated |
| cli-depend | cold-cli | 20 | 0 | 196.44 | 211.60 | 216.78 | 11 | not-evaluated |
| cli-list | cold-cli | 20 | 0 | 65.75 | 69.94 | 73.41 | 3 | not-evaluated |
| cli-move | cold-cli | 20 | 0 | 201.10 | 210.66 | 217.82 | 11 | not-evaluated |
| cli-update | cold-cli | 20 | 0 | 138.53 | 158.99 | 166.46 | 9 | not-evaluated |
| sync-already-synchronized | remote-sync | 20 | 0 | 320.45 | 329.53 | 339.30 | 9 | not-evaluated |
| sync-small-changed-ref-set | remote-sync | 20 | 0 | 500.86 | 539.62 | 555.58 | 18 | not-evaluated |
| validate-full-history | history-validation | 20 | 0 | 2228.96 | 2258.15 | 2348.38 | 7 | not-evaluated |
| validate-cached-unchanged | history-validation | 20 | 0 | 117.49 | 122.35 | 127.97 | 4 | not-evaluated |

### active-500-depth-100

| Scenario | Surface | Completed | Timed out | Min (ms) | Median (ms) | P95 (ms) | P95 Git processes | Outcome |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| cli-create | cold-cli | 20 | 0 | 150.79 | 166.25 | 201.60 | 9 | not-evaluated |
| cli-depend | cold-cli | 20 | 0 | 197.86 | 218.49 | 240.31 | 11 | not-evaluated |
| cli-list | cold-cli | 20 | 0 | 60.82 | 72.02 | 91.31 | 3 | not-evaluated |
| cli-move | cold-cli | 20 | 0 | 192.10 | 212.95 | 330.72 | 11 | not-evaluated |
| cli-update | cold-cli | 20 | 0 | 135.41 | 163.54 | 175.49 | 9 | not-evaluated |
| sync-already-synchronized | remote-sync | 20 | 0 | 324.67 | 338.69 | 360.35 | 9 | not-evaluated |
| sync-small-changed-ref-set | remote-sync | 20 | 0 | 529.60 | 601.58 | 1192.21 | 18 | not-evaluated |
| validate-full-history | history-validation | 20 | 0 | 14089.32 | 14429.63 | 15410.57 | 7 | not-evaluated |
| validate-cached-unchanged | history-validation | 20 | 0 | 123.60 | 131.98 | 133.50 | 4 | not-evaluated |

### active-1000-depth-20

| Scenario | Surface | Completed | Timed out | Min (ms) | Median (ms) | P95 (ms) | P95 Git processes | Outcome |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| cli-create | cold-cli | 20 | 0 | 167.67 | 177.65 | 178.93 | 9 | not-evaluated |
| cli-depend | cold-cli | 20 | 0 | 231.93 | 246.89 | 251.27 | 11 | not-evaluated |
| cli-list | cold-cli | 20 | 0 | 80.28 | 86.82 | 88.25 | 3 | not-evaluated |
| cli-move | cold-cli | 20 | 0 | 217.67 | 248.03 | 252.99 | 11 | not-evaluated |
| cli-update | cold-cli | 20 | 0 | 165.02 | 171.80 | 174.66 | 9 | not-evaluated |
| sync-already-synchronized | remote-sync | 20 | 0 | 502.60 | 518.86 | 532.49 | 9 | not-evaluated |
| sync-small-changed-ref-set | remote-sync | 20 | 0 | 733.46 | 772.52 | 796.12 | 18 | not-evaluated |
| validate-full-history | history-validation | 20 | 0 | 7178.76 | 7276.04 | 7515.81 | 7 | not-evaluated |
| validate-cached-unchanged | history-validation | 20 | 0 | 158.67 | 164.21 | 169.44 | 4 | not-evaluated |

## Observed slopes

Each row is a descriptive log-log slope between two consecutive measured points on one axis. A slope carries no budget and no classification.

| Axis | Scenario | Metric | From | To | Dimension ratio | Value ratio | Log-log slope | Note |
| --- | --- | --- | --- | --- | ---: | ---: | ---: | --- |
| task-count | cli-create | medianMilliseconds | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.0845 | 0.0504 | - |
| task-count | cli-create | p95Milliseconds | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.0751 | 0.0450 | - |
| task-count | cli-create | p95GitProcesses | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.0000 | 0.0000 | - |
| task-count | cli-depend | medianMilliseconds | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.1365 | 0.0795 | - |
| task-count | cli-depend | p95Milliseconds | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.1468 | 0.0851 | - |
| task-count | cli-depend | p95GitProcesses | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.0000 | 0.0000 | - |
| task-count | cli-list | medianMilliseconds | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.2027 | 0.1147 | - |
| task-count | cli-list | p95Milliseconds | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.2358 | 0.1315 | - |
| task-count | cli-list | p95GitProcesses | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.0000 | 0.0000 | - |
| task-count | cli-move | medianMilliseconds | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.1467 | 0.0850 | - |
| task-count | cli-move | p95Milliseconds | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.1258 | 0.0736 | - |
| task-count | cli-move | p95GitProcesses | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.0000 | 0.0000 | - |
| task-count | cli-update | medianMilliseconds | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.0780 | 0.0467 | - |
| task-count | cli-update | p95Milliseconds | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.0917 | 0.0545 | - |
| task-count | cli-update | p95GitProcesses | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.0000 | 0.0000 | - |
| task-count | sync-already-synchronized | medianMilliseconds | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.7941 | 0.3632 | - |
| task-count | sync-already-synchronized | p95Milliseconds | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.7941 | 0.3632 | - |
| task-count | sync-already-synchronized | p95GitProcesses | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.0000 | 0.0000 | - |
| task-count | sync-small-changed-ref-set | medianMilliseconds | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.5904 | 0.2883 | - |
| task-count | sync-small-changed-ref-set | p95Milliseconds | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.5727 | 0.2813 | - |
| task-count | sync-small-changed-ref-set | p95GitProcesses | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.0000 | 0.0000 | - |
| task-count | validate-full-history | medianMilliseconds | active-100-depth-20 | active-500-depth-20 | 5.0000 | 6.2278 | 1.1364 | - |
| task-count | validate-full-history | p95Milliseconds | active-100-depth-20 | active-500-depth-20 | 5.0000 | 6.3148 | 1.1451 | - |
| task-count | validate-full-history | p95GitProcesses | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.0000 | 0.0000 | - |
| task-count | validate-cached-unchanged | medianMilliseconds | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.5012 | 0.2524 | - |
| task-count | validate-cached-unchanged | p95Milliseconds | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.5189 | 0.2597 | - |
| task-count | validate-cached-unchanged | p95GitProcesses | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.0000 | 0.0000 | - |
| task-count | cli-create | medianMilliseconds | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.0916 | 0.1265 | - |
| task-count | cli-create | p95Milliseconds | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.0617 | 0.0864 | - |
| task-count | cli-create | p95GitProcesses | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.0000 | 0.0000 | - |
| task-count | cli-depend | medianMilliseconds | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.1668 | 0.2226 | - |
| task-count | cli-depend | p95Milliseconds | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.1591 | 0.2130 | - |
| task-count | cli-depend | p95GitProcesses | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.0000 | 0.0000 | - |
| task-count | cli-list | medianMilliseconds | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.2414 | 0.3120 | - |
| task-count | cli-list | p95Milliseconds | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.2022 | 0.2657 | - |
| task-count | cli-list | p95GitProcesses | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.0000 | 0.0000 | - |
| task-count | cli-move | medianMilliseconds | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.1774 | 0.2356 | - |
| task-count | cli-move | p95Milliseconds | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.1615 | 0.2160 | - |
| task-count | cli-move | p95GitProcesses | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.0000 | 0.0000 | - |
| task-count | cli-update | medianMilliseconds | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.0806 | 0.1118 | - |
| task-count | cli-update | p95Milliseconds | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.0492 | 0.0693 | - |
| task-count | cli-update | p95GitProcesses | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.0000 | 0.0000 | - |
| task-count | sync-already-synchronized | medianMilliseconds | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.5745 | 0.6549 | - |
| task-count | sync-already-synchronized | p95Milliseconds | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.5694 | 0.6502 | - |
| task-count | sync-already-synchronized | p95GitProcesses | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.0000 | 0.0000 | - |
| task-count | sync-small-changed-ref-set | medianMilliseconds | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.4316 | 0.5176 | - |
| task-count | sync-small-changed-ref-set | p95Milliseconds | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.4330 | 0.5190 | - |
| task-count | sync-small-changed-ref-set | p95GitProcesses | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.0000 | 0.0000 | - |
| task-count | validate-full-history | medianMilliseconds | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 3.2221 | 1.6880 | - |
| task-count | validate-full-history | p95Milliseconds | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 3.2004 | 1.6783 | - |
| task-count | validate-full-history | p95GitProcesses | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.0000 | 0.0000 | - |
| task-count | validate-cached-unchanged | medianMilliseconds | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.3421 | 0.4245 | - |
| task-count | validate-cached-unchanged | p95Milliseconds | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.3240 | 0.4049 | - |
| task-count | validate-cached-unchanged | p95GitProcesses | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.0000 | 0.0000 | - |
| history-depth | cli-create | medianMilliseconds | active-500-depth-20 | active-500-depth-100 | 5.0000 | 1.0216 | 0.0133 | - |
| history-depth | cli-create | p95Milliseconds | active-500-depth-20 | active-500-depth-100 | 5.0000 | 1.1962 | 0.1113 | - |
| history-depth | cli-create | p95GitProcesses | active-500-depth-20 | active-500-depth-100 | 5.0000 | 1.0000 | 0.0000 | - |
| history-depth | cli-depend | medianMilliseconds | active-500-depth-20 | active-500-depth-100 | 5.0000 | 1.0326 | 0.0199 | - |
| history-depth | cli-depend | p95Milliseconds | active-500-depth-20 | active-500-depth-100 | 5.0000 | 1.1086 | 0.0640 | - |
| history-depth | cli-depend | p95GitProcesses | active-500-depth-20 | active-500-depth-100 | 5.0000 | 1.0000 | 0.0000 | - |
| history-depth | cli-list | medianMilliseconds | active-500-depth-20 | active-500-depth-100 | 5.0000 | 1.0298 | 0.0182 | - |
| history-depth | cli-list | p95Milliseconds | active-500-depth-20 | active-500-depth-100 | 5.0000 | 1.2439 | 0.1356 | - |
| history-depth | cli-list | p95GitProcesses | active-500-depth-20 | active-500-depth-100 | 5.0000 | 1.0000 | 0.0000 | - |
| history-depth | cli-move | medianMilliseconds | active-500-depth-20 | active-500-depth-100 | 5.0000 | 1.0109 | 0.0067 | - |
| history-depth | cli-move | p95Milliseconds | active-500-depth-20 | active-500-depth-100 | 5.0000 | 1.5184 | 0.2595 | - |
| history-depth | cli-move | p95GitProcesses | active-500-depth-20 | active-500-depth-100 | 5.0000 | 1.0000 | 0.0000 | - |
| history-depth | cli-update | medianMilliseconds | active-500-depth-20 | active-500-depth-100 | 5.0000 | 1.0286 | 0.0175 | - |
| history-depth | cli-update | p95Milliseconds | active-500-depth-20 | active-500-depth-100 | 5.0000 | 1.0542 | 0.0328 | - |
| history-depth | cli-update | p95GitProcesses | active-500-depth-20 | active-500-depth-100 | 5.0000 | 1.0000 | 0.0000 | - |
| history-depth | sync-already-synchronized | medianMilliseconds | active-500-depth-20 | active-500-depth-100 | 5.0000 | 1.0278 | 0.0170 | - |
| history-depth | sync-already-synchronized | p95Milliseconds | active-500-depth-20 | active-500-depth-100 | 5.0000 | 1.0620 | 0.0374 | - |
| history-depth | sync-already-synchronized | p95GitProcesses | active-500-depth-20 | active-500-depth-100 | 5.0000 | 1.0000 | 0.0000 | - |
| history-depth | sync-small-changed-ref-set | medianMilliseconds | active-500-depth-20 | active-500-depth-100 | 5.0000 | 1.1148 | 0.0675 | - |
| history-depth | sync-small-changed-ref-set | p95Milliseconds | active-500-depth-20 | active-500-depth-100 | 5.0000 | 2.1459 | 0.4744 | - |
| history-depth | sync-small-changed-ref-set | p95GitProcesses | active-500-depth-20 | active-500-depth-100 | 5.0000 | 1.0000 | 0.0000 | - |
| history-depth | validate-full-history | medianMilliseconds | active-500-depth-20 | active-500-depth-100 | 5.0000 | 6.3900 | 1.1524 | - |
| history-depth | validate-full-history | p95Milliseconds | active-500-depth-20 | active-500-depth-100 | 5.0000 | 6.5622 | 1.1689 | - |
| history-depth | validate-full-history | p95GitProcesses | active-500-depth-20 | active-500-depth-100 | 5.0000 | 1.0000 | 0.0000 | - |
| history-depth | validate-cached-unchanged | medianMilliseconds | active-500-depth-20 | active-500-depth-100 | 5.0000 | 1.0787 | 0.0470 | - |
| history-depth | validate-cached-unchanged | p95Milliseconds | active-500-depth-20 | active-500-depth-100 | 5.0000 | 1.0432 | 0.0263 | - |
| history-depth | validate-cached-unchanged | p95GitProcesses | active-500-depth-20 | active-500-depth-100 | 5.0000 | 1.0000 | 0.0000 | - |
