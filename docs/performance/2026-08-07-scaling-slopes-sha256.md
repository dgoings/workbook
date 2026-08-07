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
| cli-create | cold-cli | 20 | 0 | 136.50 | 170.38 | 173.86 | 10 | not-evaluated |
| cli-depend | cold-cli | 20 | 0 | 179.73 | 204.80 | 208.59 | 12 | not-evaluated |
| cli-list | cold-cli | 20 | 0 | 52.29 | 59.00 | 60.45 | 3 | not-evaluated |
| cli-move | cold-cli | 20 | 0 | 193.16 | 205.41 | 207.59 | 12 | not-evaluated |
| cli-update | cold-cli | 20 | 0 | 148.20 | 167.78 | 172.65 | 10 | not-evaluated |
| sync-already-synchronized | remote-sync | 20 | 0 | 171.68 | 184.99 | 192.10 | 9 | not-evaluated |
| sync-small-changed-ref-set | remote-sync | 20 | 0 | 326.34 | 344.66 | 354.43 | 18 | not-evaluated |
| validate-full-history | history-validation | 20 | 0 | 193.72 | 208.91 | 213.34 | 7 | not-evaluated |
| validate-cached-unchanged | history-validation | 20 | 0 | 73.55 | 81.72 | 83.01 | 4 | not-evaluated |

### active-500-depth-20

| Scenario | Surface | Completed | Timed out | Min (ms) | Median (ms) | P95 (ms) | P95 Git processes | Outcome |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| cli-create | cold-cli | 20 | 0 | 156.89 | 178.15 | 182.15 | 10 | not-evaluated |
| cli-depend | cold-cli | 20 | 0 | 204.41 | 223.47 | 228.64 | 12 | not-evaluated |
| cli-list | cold-cli | 20 | 0 | 57.24 | 69.22 | 71.01 | 3 | not-evaluated |
| cli-move | cold-cli | 20 | 0 | 201.95 | 224.32 | 231.02 | 12 | not-evaluated |
| cli-update | cold-cli | 20 | 0 | 156.36 | 176.16 | 179.26 | 10 | not-evaluated |
| sync-already-synchronized | remote-sync | 20 | 0 | 325.96 | 330.74 | 333.68 | 9 | not-evaluated |
| sync-small-changed-ref-set | remote-sync | 20 | 0 | 496.73 | 519.90 | 527.69 | 18 | not-evaluated |
| validate-full-history | history-validation | 20 | 0 | 636.87 | 649.91 | 653.75 | 7 | not-evaluated |
| validate-cached-unchanged | history-validation | 20 | 0 | 106.25 | 116.16 | 118.59 | 4 | not-evaluated |

### active-500-depth-100

| Scenario | Surface | Completed | Timed out | Min (ms) | Median (ms) | P95 (ms) | P95 Git processes | Outcome |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| cli-create | cold-cli | 20 | 0 | 160.36 | 181.23 | 184.15 | 10 | not-evaluated |
| cli-depend | cold-cli | 20 | 0 | 212.75 | 226.87 | 230.14 | 12 | not-evaluated |
| cli-list | cold-cli | 20 | 0 | 61.79 | 69.89 | 71.81 | 3 | not-evaluated |
| cli-move | cold-cli | 20 | 0 | 210.27 | 229.00 | 240.33 | 12 | not-evaluated |
| cli-update | cold-cli | 20 | 0 | 162.81 | 178.07 | 182.47 | 10 | not-evaluated |
| sync-already-synchronized | remote-sync | 20 | 0 | 322.49 | 337.70 | 347.07 | 9 | not-evaluated |
| sync-small-changed-ref-set | remote-sync | 20 | 0 | 507.34 | 542.00 | 560.43 | 18 | not-evaluated |
| validate-full-history | history-validation | 20 | 0 | 2623.11 | 2647.12 | 2669.20 | 7 | not-evaluated |
| validate-cached-unchanged | history-validation | 20 | 0 | 113.12 | 127.73 | 131.66 | 4 | not-evaluated |

### active-1000-depth-20

| Scenario | Surface | Completed | Timed out | Min (ms) | Median (ms) | P95 (ms) | P95 Git processes | Outcome |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| cli-create | cold-cli | 20 | 0 | 150.17 | 191.91 | 196.07 | 10 | not-evaluated |
| cli-depend | cold-cli | 20 | 0 | 215.67 | 249.21 | 261.75 | 12 | not-evaluated |
| cli-list | cold-cli | 20 | 0 | 74.62 | 83.57 | 86.23 | 3 | not-evaluated |
| cli-move | cold-cli | 20 | 0 | 205.89 | 253.22 | 259.04 | 12 | not-evaluated |
| cli-update | cold-cli | 20 | 0 | 168.98 | 184.62 | 189.23 | 10 | not-evaluated |
| sync-already-synchronized | remote-sync | 20 | 0 | 485.89 | 500.59 | 519.18 | 9 | not-evaluated |
| sync-small-changed-ref-set | remote-sync | 20 | 0 | 732.54 | 748.54 | 770.38 | 18 | not-evaluated |
| validate-full-history | history-validation | 20 | 0 | 1227.51 | 1247.82 | 1271.64 | 7 | not-evaluated |
| validate-cached-unchanged | history-validation | 20 | 0 | 143.23 | 144.96 | 162.21 | 4 | not-evaluated |

## Observed slopes

Each row is a descriptive log-log slope between two consecutive measured points on one axis. A slope carries no budget and no classification.

| Axis | Scenario | Metric | From | To | Dimension ratio | Value ratio | Log-log slope | Note |
| --- | --- | --- | --- | --- | ---: | ---: | ---: | --- |
| task-count | cli-create | medianMilliseconds | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.0456 | 0.0277 | - |
| task-count | cli-create | p95Milliseconds | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.0477 | 0.0289 | - |
| task-count | cli-create | p95GitProcesses | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.0000 | 0.0000 | - |
| task-count | cli-depend | medianMilliseconds | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.0912 | 0.0542 | - |
| task-count | cli-depend | p95Milliseconds | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.0961 | 0.0570 | - |
| task-count | cli-depend | p95GitProcesses | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.0000 | 0.0000 | - |
| task-count | cli-list | medianMilliseconds | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.1734 | 0.0993 | - |
| task-count | cli-list | p95Milliseconds | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.1746 | 0.1000 | - |
| task-count | cli-list | p95GitProcesses | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.0000 | 0.0000 | - |
| task-count | cli-move | medianMilliseconds | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.0921 | 0.0547 | - |
| task-count | cli-move | p95Milliseconds | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.1129 | 0.0664 | - |
| task-count | cli-move | p95GitProcesses | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.0000 | 0.0000 | - |
| task-count | cli-update | medianMilliseconds | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.0500 | 0.0303 | - |
| task-count | cli-update | p95Milliseconds | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.0383 | 0.0233 | - |
| task-count | cli-update | p95GitProcesses | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.0000 | 0.0000 | - |
| task-count | sync-already-synchronized | medianMilliseconds | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.7879 | 0.3610 | - |
| task-count | sync-already-synchronized | p95Milliseconds | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.7370 | 0.3431 | - |
| task-count | sync-already-synchronized | p95GitProcesses | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.0000 | 0.0000 | - |
| task-count | sync-small-changed-ref-set | medianMilliseconds | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.5084 | 0.2554 | - |
| task-count | sync-small-changed-ref-set | p95Milliseconds | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.4889 | 0.2473 | - |
| task-count | sync-small-changed-ref-set | p95GitProcesses | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.0000 | 0.0000 | - |
| task-count | validate-full-history | medianMilliseconds | active-100-depth-20 | active-500-depth-20 | 5.0000 | 3.1110 | 0.7052 | - |
| task-count | validate-full-history | p95Milliseconds | active-100-depth-20 | active-500-depth-20 | 5.0000 | 3.0643 | 0.6958 | - |
| task-count | validate-full-history | p95GitProcesses | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.0000 | 0.0000 | - |
| task-count | validate-cached-unchanged | medianMilliseconds | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.4215 | 0.2185 | - |
| task-count | validate-cached-unchanged | p95Milliseconds | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.4285 | 0.2216 | - |
| task-count | validate-cached-unchanged | p95GitProcesses | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.0000 | 0.0000 | - |
| task-count | cli-create | medianMilliseconds | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.0772 | 0.1074 | - |
| task-count | cli-create | p95Milliseconds | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.0764 | 0.1062 | - |
| task-count | cli-create | p95GitProcesses | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.0000 | 0.0000 | - |
| task-count | cli-depend | medianMilliseconds | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.1152 | 0.1573 | - |
| task-count | cli-depend | p95Milliseconds | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.1448 | 0.1951 | - |
| task-count | cli-depend | p95GitProcesses | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.0000 | 0.0000 | - |
| task-count | cli-list | medianMilliseconds | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.2072 | 0.2716 | - |
| task-count | cli-list | p95Milliseconds | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.2143 | 0.2801 | - |
| task-count | cli-list | p95GitProcesses | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.0000 | 0.0000 | - |
| task-count | cli-move | medianMilliseconds | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.1288 | 0.1748 | - |
| task-count | cli-move | p95Milliseconds | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.1213 | 0.1651 | - |
| task-count | cli-move | p95GitProcesses | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.0000 | 0.0000 | - |
| task-count | cli-update | medianMilliseconds | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.0480 | 0.0677 | - |
| task-count | cli-update | p95Milliseconds | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.0556 | 0.0781 | - |
| task-count | cli-update | p95GitProcesses | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.0000 | 0.0000 | - |
| task-count | sync-already-synchronized | medianMilliseconds | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.5135 | 0.5979 | - |
| task-count | sync-already-synchronized | p95Milliseconds | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.5559 | 0.6378 | - |
| task-count | sync-already-synchronized | p95GitProcesses | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.0000 | 0.0000 | - |
| task-count | sync-small-changed-ref-set | medianMilliseconds | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.4398 | 0.5258 | - |
| task-count | sync-small-changed-ref-set | p95Milliseconds | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.4599 | 0.5459 | - |
| task-count | sync-small-changed-ref-set | p95GitProcesses | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.0000 | 0.0000 | - |
| task-count | validate-full-history | medianMilliseconds | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.9200 | 0.9411 | - |
| task-count | validate-full-history | p95Milliseconds | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.9451 | 0.9599 | - |
| task-count | validate-full-history | p95GitProcesses | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.0000 | 0.0000 | - |
| task-count | validate-cached-unchanged | medianMilliseconds | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.2479 | 0.3195 | - |
| task-count | validate-cached-unchanged | p95Milliseconds | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.3678 | 0.4518 | - |
| task-count | validate-cached-unchanged | p95GitProcesses | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.0000 | 0.0000 | - |
| history-depth | cli-create | medianMilliseconds | active-500-depth-20 | active-500-depth-100 | 5.0000 | 1.0173 | 0.0107 | - |
| history-depth | cli-create | p95Milliseconds | active-500-depth-20 | active-500-depth-100 | 5.0000 | 1.0110 | 0.0068 | - |
| history-depth | cli-create | p95GitProcesses | active-500-depth-20 | active-500-depth-100 | 5.0000 | 1.0000 | 0.0000 | - |
| history-depth | cli-depend | medianMilliseconds | active-500-depth-20 | active-500-depth-100 | 5.0000 | 1.0152 | 0.0094 | - |
| history-depth | cli-depend | p95Milliseconds | active-500-depth-20 | active-500-depth-100 | 5.0000 | 1.0065 | 0.0041 | - |
| history-depth | cli-depend | p95GitProcesses | active-500-depth-20 | active-500-depth-100 | 5.0000 | 1.0000 | 0.0000 | - |
| history-depth | cli-list | medianMilliseconds | active-500-depth-20 | active-500-depth-100 | 5.0000 | 1.0097 | 0.0060 | - |
| history-depth | cli-list | p95Milliseconds | active-500-depth-20 | active-500-depth-100 | 5.0000 | 1.0113 | 0.0070 | - |
| history-depth | cli-list | p95GitProcesses | active-500-depth-20 | active-500-depth-100 | 5.0000 | 1.0000 | 0.0000 | - |
| history-depth | cli-move | medianMilliseconds | active-500-depth-20 | active-500-depth-100 | 5.0000 | 1.0208 | 0.0128 | - |
| history-depth | cli-move | p95Milliseconds | active-500-depth-20 | active-500-depth-100 | 5.0000 | 1.0403 | 0.0245 | - |
| history-depth | cli-move | p95GitProcesses | active-500-depth-20 | active-500-depth-100 | 5.0000 | 1.0000 | 0.0000 | - |
| history-depth | cli-update | medianMilliseconds | active-500-depth-20 | active-500-depth-100 | 5.0000 | 1.0108 | 0.0067 | - |
| history-depth | cli-update | p95Milliseconds | active-500-depth-20 | active-500-depth-100 | 5.0000 | 1.0179 | 0.0110 | - |
| history-depth | cli-update | p95GitProcesses | active-500-depth-20 | active-500-depth-100 | 5.0000 | 1.0000 | 0.0000 | - |
| history-depth | sync-already-synchronized | medianMilliseconds | active-500-depth-20 | active-500-depth-100 | 5.0000 | 1.0210 | 0.0129 | - |
| history-depth | sync-already-synchronized | p95Milliseconds | active-500-depth-20 | active-500-depth-100 | 5.0000 | 1.0401 | 0.0244 | - |
| history-depth | sync-already-synchronized | p95GitProcesses | active-500-depth-20 | active-500-depth-100 | 5.0000 | 1.0000 | 0.0000 | - |
| history-depth | sync-small-changed-ref-set | medianMilliseconds | active-500-depth-20 | active-500-depth-100 | 5.0000 | 1.0425 | 0.0259 | - |
| history-depth | sync-small-changed-ref-set | p95Milliseconds | active-500-depth-20 | active-500-depth-100 | 5.0000 | 1.0620 | 0.0374 | - |
| history-depth | sync-small-changed-ref-set | p95GitProcesses | active-500-depth-20 | active-500-depth-100 | 5.0000 | 1.0000 | 0.0000 | - |
| history-depth | validate-full-history | medianMilliseconds | active-500-depth-20 | active-500-depth-100 | 5.0000 | 4.0730 | 0.8726 | - |
| history-depth | validate-full-history | p95Milliseconds | active-500-depth-20 | active-500-depth-100 | 5.0000 | 4.0829 | 0.8741 | - |
| history-depth | validate-full-history | p95GitProcesses | active-500-depth-20 | active-500-depth-100 | 5.0000 | 1.0000 | 0.0000 | - |
| history-depth | validate-cached-unchanged | medianMilliseconds | active-500-depth-20 | active-500-depth-100 | 5.0000 | 1.0996 | 0.0590 | - |
| history-depth | validate-cached-unchanged | p95Milliseconds | active-500-depth-20 | active-500-depth-100 | 5.0000 | 1.1102 | 0.0650 | - |
| history-depth | validate-cached-unchanged | p95GitProcesses | active-500-depth-20 | active-500-depth-100 | 5.0000 | 1.0000 | 0.0000 | - |
