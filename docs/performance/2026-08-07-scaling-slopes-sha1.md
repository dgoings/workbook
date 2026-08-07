# Workbook performance scaling report

Phase: scaling

Object format: sha1

Samples per scenario: 20

## Matrix points
| Point | Active tasks | Tombstoned tasks | Total tasks | History depth | Object format |
| --- | ---: | ---: | ---: | ---: | --- |
| active-100-depth-20 | 100 | 5 | 105 | 20 | sha1 |
| active-500-depth-20 | 500 | 25 | 525 | 20 | sha1 |
| active-500-depth-100 | 500 | 25 | 525 | 100 | sha1 |
| active-1000-depth-20 | 1000 | 50 | 1050 | 20 | sha1 |

## Measurements

### active-100-depth-20

| Scenario | Surface | Completed | Timed out | Min (ms) | Median (ms) | P95 (ms) | P95 Git processes | Outcome |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| cli-create | cold-cli | 20 | 0 | 136.69 | 174.88 | 187.17 | 10 | not-evaluated |
| cli-depend | cold-cli | 20 | 0 | 164.76 | 204.06 | 224.25 | 12 | not-evaluated |
| cli-list | cold-cli | 20 | 0 | 50.51 | 60.58 | 66.50 | 3 | not-evaluated |
| cli-move | cold-cli | 20 | 0 | 158.36 | 209.87 | 223.75 | 12 | not-evaluated |
| cli-update | cold-cli | 20 | 0 | 128.60 | 172.98 | 182.91 | 10 | not-evaluated |
| sync-already-synchronized | remote-sync | 20 | 0 | 165.94 | 252.41 | 624.21 | 9 | not-evaluated |
| sync-small-changed-ref-set | remote-sync | 20 | 0 | 319.22 | 420.87 | 581.16 | 18 | not-evaluated |
| validate-full-history | history-validation | 20 | 0 | 204.53 | 218.42 | 237.84 | 7 | not-evaluated |
| validate-cached-unchanged | history-validation | 20 | 0 | 60.08 | 78.26 | 85.64 | 4 | not-evaluated |

### active-500-depth-20

| Scenario | Surface | Completed | Timed out | Min (ms) | Median (ms) | P95 (ms) | P95 Git processes | Outcome |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| cli-create | cold-cli | 20 | 0 | 152.75 | 183.70 | 267.18 | 10 | not-evaluated |
| cli-depend | cold-cli | 20 | 0 | 206.85 | 251.90 | 380.11 | 12 | not-evaluated |
| cli-list | cold-cli | 20 | 0 | 56.69 | 77.38 | 103.00 | 3 | not-evaluated |
| cli-move | cold-cli | 20 | 0 | 180.45 | 232.45 | 393.23 | 12 | not-evaluated |
| cli-update | cold-cli | 20 | 0 | 136.16 | 183.39 | 236.92 | 10 | not-evaluated |
| sync-already-synchronized | remote-sync | 20 | 0 | 313.28 | 341.24 | 427.79 | 9 | not-evaluated |
| sync-small-changed-ref-set | remote-sync | 20 | 0 | 509.74 | 541.32 | 749.49 | 18 | not-evaluated |
| validate-full-history | history-validation | 20 | 0 | 609.31 | 629.86 | 636.05 | 7 | not-evaluated |
| validate-cached-unchanged | history-validation | 20 | 0 | 103.84 | 122.22 | 124.50 | 4 | not-evaluated |

### active-500-depth-100

| Scenario | Surface | Completed | Timed out | Min (ms) | Median (ms) | P95 (ms) | P95 Git processes | Outcome |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| cli-create | cold-cli | 20 | 0 | 151.20 | 182.41 | 242.84 | 10 | not-evaluated |
| cli-depend | cold-cli | 20 | 0 | 195.42 | 226.09 | 353.35 | 12 | not-evaluated |
| cli-list | cold-cli | 20 | 0 | 64.31 | 70.92 | 85.38 | 3 | not-evaluated |
| cli-move | cold-cli | 20 | 0 | 198.58 | 226.88 | 293.90 | 12 | not-evaluated |
| cli-update | cold-cli | 20 | 0 | 134.01 | 174.81 | 200.76 | 10 | not-evaluated |
| sync-already-synchronized | remote-sync | 20 | 0 | 319.31 | 339.45 | 730.56 | 9 | not-evaluated |
| sync-small-changed-ref-set | remote-sync | 20 | 0 | 510.40 | 527.48 | 539.94 | 18 | not-evaluated |
| validate-full-history | history-validation | 20 | 0 | 2414.99 | 2440.17 | 2465.60 | 7 | not-evaluated |
| validate-cached-unchanged | history-validation | 20 | 0 | 110.57 | 126.23 | 129.33 | 4 | not-evaluated |

### active-1000-depth-20

| Scenario | Surface | Completed | Timed out | Min (ms) | Median (ms) | P95 (ms) | P95 Git processes | Outcome |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| cli-create | cold-cli | 20 | 0 | 149.71 | 190.73 | 196.47 | 10 | not-evaluated |
| cli-depend | cold-cli | 20 | 0 | 208.91 | 252.72 | 261.63 | 12 | not-evaluated |
| cli-list | cold-cli | 20 | 0 | 72.01 | 85.18 | 88.76 | 3 | not-evaluated |
| cli-move | cold-cli | 20 | 0 | 207.74 | 252.17 | 259.46 | 12 | not-evaluated |
| cli-update | cold-cli | 20 | 0 | 148.82 | 186.07 | 191.54 | 10 | not-evaluated |
| sync-already-synchronized | remote-sync | 20 | 0 | 480.17 | 495.56 | 510.70 | 9 | not-evaluated |
| sync-small-changed-ref-set | remote-sync | 20 | 0 | 665.57 | 731.84 | 756.08 | 18 | not-evaluated |
| validate-full-history | history-validation | 20 | 0 | 1126.13 | 1139.47 | 1172.31 | 7 | not-evaluated |
| validate-cached-unchanged | history-validation | 20 | 0 | 141.70 | 144.27 | 151.21 | 4 | not-evaluated |

## Observed slopes

Each row is a descriptive log-log slope between two consecutive measured points on one axis. A slope carries no budget and no classification.

| Axis | Scenario | Metric | From | To | Dimension ratio | Value ratio | Log-log slope | Note |
| --- | --- | --- | --- | --- | ---: | ---: | ---: | --- |
| task-count | cli-create | medianMilliseconds | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.0504 | 0.0306 | - |
| task-count | cli-create | p95Milliseconds | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.4275 | 0.2211 | - |
| task-count | cli-create | p95GitProcesses | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.0000 | 0.0000 | - |
| task-count | cli-depend | medianMilliseconds | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.2344 | 0.1309 | - |
| task-count | cli-depend | p95Milliseconds | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.6950 | 0.3279 | - |
| task-count | cli-depend | p95GitProcesses | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.0000 | 0.0000 | - |
| task-count | cli-list | medianMilliseconds | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.2773 | 0.1521 | - |
| task-count | cli-list | p95Milliseconds | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.5489 | 0.2719 | - |
| task-count | cli-list | p95GitProcesses | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.0000 | 0.0000 | - |
| task-count | cli-move | medianMilliseconds | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.1076 | 0.0635 | - |
| task-count | cli-move | p95Milliseconds | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.7575 | 0.3504 | - |
| task-count | cli-move | p95GitProcesses | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.0000 | 0.0000 | - |
| task-count | cli-update | medianMilliseconds | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.0602 | 0.0363 | - |
| task-count | cli-update | p95Milliseconds | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.2953 | 0.1608 | - |
| task-count | cli-update | p95GitProcesses | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.0000 | 0.0000 | - |
| task-count | sync-already-synchronized | medianMilliseconds | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.3519 | 0.1874 | - |
| task-count | sync-already-synchronized | p95Milliseconds | active-100-depth-20 | active-500-depth-20 | 5.0000 | 0.6853 | -0.2348 | - |
| task-count | sync-already-synchronized | p95GitProcesses | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.0000 | 0.0000 | - |
| task-count | sync-small-changed-ref-set | medianMilliseconds | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.2862 | 0.1564 | - |
| task-count | sync-small-changed-ref-set | p95Milliseconds | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.2897 | 0.1581 | - |
| task-count | sync-small-changed-ref-set | p95GitProcesses | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.0000 | 0.0000 | - |
| task-count | validate-full-history | medianMilliseconds | active-100-depth-20 | active-500-depth-20 | 5.0000 | 2.8837 | 0.6580 | - |
| task-count | validate-full-history | p95Milliseconds | active-100-depth-20 | active-500-depth-20 | 5.0000 | 2.6742 | 0.6112 | - |
| task-count | validate-full-history | p95GitProcesses | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.0000 | 0.0000 | - |
| task-count | validate-cached-unchanged | medianMilliseconds | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.5617 | 0.2770 | - |
| task-count | validate-cached-unchanged | p95Milliseconds | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.4537 | 0.2325 | - |
| task-count | validate-cached-unchanged | p95GitProcesses | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.0000 | 0.0000 | - |
| task-count | cli-create | medianMilliseconds | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.0383 | 0.0542 | - |
| task-count | cli-create | p95Milliseconds | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 0.7353 | -0.4435 | - |
| task-count | cli-create | p95GitProcesses | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.0000 | 0.0000 | - |
| task-count | cli-depend | medianMilliseconds | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.0032 | 0.0046 | - |
| task-count | cli-depend | p95Milliseconds | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 0.6883 | -0.5389 | - |
| task-count | cli-depend | p95GitProcesses | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.0000 | 0.0000 | - |
| task-count | cli-list | medianMilliseconds | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.1007 | 0.1385 | - |
| task-count | cli-list | p95Milliseconds | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 0.8617 | -0.2147 | - |
| task-count | cli-list | p95GitProcesses | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.0000 | 0.0000 | - |
| task-count | cli-move | medianMilliseconds | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.0848 | 0.1175 | - |
| task-count | cli-move | p95Milliseconds | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 0.6598 | -0.5998 | - |
| task-count | cli-move | p95GitProcesses | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.0000 | 0.0000 | - |
| task-count | cli-update | medianMilliseconds | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.0146 | 0.0209 | - |
| task-count | cli-update | p95Milliseconds | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 0.8085 | -0.3067 | - |
| task-count | cli-update | p95GitProcesses | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.0000 | 0.0000 | - |
| task-count | sync-already-synchronized | medianMilliseconds | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.4522 | 0.5383 | - |
| task-count | sync-already-synchronized | p95Milliseconds | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.1938 | 0.2556 | - |
| task-count | sync-already-synchronized | p95GitProcesses | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.0000 | 0.0000 | - |
| task-count | sync-small-changed-ref-set | medianMilliseconds | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.3519 | 0.4350 | - |
| task-count | sync-small-changed-ref-set | p95Milliseconds | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.0088 | 0.0126 | - |
| task-count | sync-small-changed-ref-set | p95GitProcesses | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.0000 | 0.0000 | - |
| task-count | validate-full-history | medianMilliseconds | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.8091 | 0.8553 | - |
| task-count | validate-full-history | p95Milliseconds | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.8431 | 0.8821 | - |
| task-count | validate-full-history | p95GitProcesses | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.0000 | 0.0000 | - |
| task-count | validate-cached-unchanged | medianMilliseconds | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.1804 | 0.2393 | - |
| task-count | validate-cached-unchanged | p95Milliseconds | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.2145 | 0.2804 | - |
| task-count | validate-cached-unchanged | p95GitProcesses | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.0000 | 0.0000 | - |
| history-depth | cli-create | medianMilliseconds | active-500-depth-20 | active-500-depth-100 | 5.0000 | 0.9930 | -0.0044 | - |
| history-depth | cli-create | p95Milliseconds | active-500-depth-20 | active-500-depth-100 | 5.0000 | 0.9089 | -0.0594 | - |
| history-depth | cli-create | p95GitProcesses | active-500-depth-20 | active-500-depth-100 | 5.0000 | 1.0000 | 0.0000 | - |
| history-depth | cli-depend | medianMilliseconds | active-500-depth-20 | active-500-depth-100 | 5.0000 | 0.8975 | -0.0672 | - |
| history-depth | cli-depend | p95Milliseconds | active-500-depth-20 | active-500-depth-100 | 5.0000 | 0.9296 | -0.0454 | - |
| history-depth | cli-depend | p95GitProcesses | active-500-depth-20 | active-500-depth-100 | 5.0000 | 1.0000 | 0.0000 | - |
| history-depth | cli-list | medianMilliseconds | active-500-depth-20 | active-500-depth-100 | 5.0000 | 0.9165 | -0.0542 | - |
| history-depth | cli-list | p95Milliseconds | active-500-depth-20 | active-500-depth-100 | 5.0000 | 0.8289 | -0.1166 | - |
| history-depth | cli-list | p95GitProcesses | active-500-depth-20 | active-500-depth-100 | 5.0000 | 1.0000 | 0.0000 | - |
| history-depth | cli-move | medianMilliseconds | active-500-depth-20 | active-500-depth-100 | 5.0000 | 0.9760 | -0.0151 | - |
| history-depth | cli-move | p95Milliseconds | active-500-depth-20 | active-500-depth-100 | 5.0000 | 0.7474 | -0.1809 | - |
| history-depth | cli-move | p95GitProcesses | active-500-depth-20 | active-500-depth-100 | 5.0000 | 1.0000 | 0.0000 | - |
| history-depth | cli-update | medianMilliseconds | active-500-depth-20 | active-500-depth-100 | 5.0000 | 0.9532 | -0.0298 | - |
| history-depth | cli-update | p95Milliseconds | active-500-depth-20 | active-500-depth-100 | 5.0000 | 0.8474 | -0.1029 | - |
| history-depth | cli-update | p95GitProcesses | active-500-depth-20 | active-500-depth-100 | 5.0000 | 1.0000 | 0.0000 | - |
| history-depth | sync-already-synchronized | medianMilliseconds | active-500-depth-20 | active-500-depth-100 | 5.0000 | 0.9947 | -0.0033 | - |
| history-depth | sync-already-synchronized | p95Milliseconds | active-500-depth-20 | active-500-depth-100 | 5.0000 | 1.7078 | 0.3325 | - |
| history-depth | sync-already-synchronized | p95GitProcesses | active-500-depth-20 | active-500-depth-100 | 5.0000 | 1.0000 | 0.0000 | - |
| history-depth | sync-small-changed-ref-set | medianMilliseconds | active-500-depth-20 | active-500-depth-100 | 5.0000 | 0.9744 | -0.0161 | - |
| history-depth | sync-small-changed-ref-set | p95Milliseconds | active-500-depth-20 | active-500-depth-100 | 5.0000 | 0.7204 | -0.2038 | - |
| history-depth | sync-small-changed-ref-set | p95GitProcesses | active-500-depth-20 | active-500-depth-100 | 5.0000 | 1.0000 | 0.0000 | - |
| history-depth | validate-full-history | medianMilliseconds | active-500-depth-20 | active-500-depth-100 | 5.0000 | 3.8742 | 0.8415 | - |
| history-depth | validate-full-history | p95Milliseconds | active-500-depth-20 | active-500-depth-100 | 5.0000 | 3.8764 | 0.8419 | - |
| history-depth | validate-full-history | p95GitProcesses | active-500-depth-20 | active-500-depth-100 | 5.0000 | 1.0000 | 0.0000 | - |
| history-depth | validate-cached-unchanged | medianMilliseconds | active-500-depth-20 | active-500-depth-100 | 5.0000 | 1.0328 | 0.0201 | - |
| history-depth | validate-cached-unchanged | p95Milliseconds | active-500-depth-20 | active-500-depth-100 | 5.0000 | 1.0388 | 0.0236 | - |
| history-depth | validate-cached-unchanged | p95GitProcesses | active-500-depth-20 | active-500-depth-100 | 5.0000 | 1.0000 | 0.0000 | - |
