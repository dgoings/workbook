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
| cli-create | cold-cli | 20 | 0 | 133.50 | 165.97 | 281.81 | 9 | not-evaluated |
| cli-depend | cold-cli | 20 | 0 | 170.83 | 186.85 | 279.75 | 11 | not-evaluated |
| cli-list | cold-cli | 20 | 0 | 50.94 | 59.44 | 121.72 | 3 | not-evaluated |
| cli-move | cold-cli | 20 | 0 | 166.76 | 184.62 | 279.08 | 11 | not-evaluated |
| cli-update | cold-cli | 20 | 0 | 140.02 | 149.47 | 243.77 | 9 | not-evaluated |
| sync-already-synchronized | remote-sync | 20 | 0 | 151.73 | 190.91 | 249.54 | 9 | not-evaluated |
| sync-small-changed-ref-set | remote-sync | 20 | 0 | 286.39 | 330.71 | 369.34 | 18 | not-evaluated |
| validate-full-history | history-validation | 20 | 0 | 341.27 | 370.73 | 447.51 | 7 | not-evaluated |
| validate-cached-unchanged | history-validation | 20 | 0 | 72.09 | 89.87 | 96.38 | 4 | not-evaluated |

### active-500-depth-20

| Scenario | Surface | Completed | Timed out | Min (ms) | Median (ms) | P95 (ms) | P95 Git processes | Outcome |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| cli-create | cold-cli | 20 | 0 | 153.09 | 159.85 | 169.85 | 9 | not-evaluated |
| cli-depend | cold-cli | 20 | 0 | 188.61 | 210.02 | 221.09 | 11 | not-evaluated |
| cli-list | cold-cli | 20 | 0 | 66.49 | 68.83 | 73.42 | 3 | not-evaluated |
| cli-move | cold-cli | 20 | 0 | 197.35 | 209.98 | 218.72 | 11 | not-evaluated |
| cli-update | cold-cli | 20 | 0 | 141.22 | 158.70 | 168.47 | 9 | not-evaluated |
| sync-already-synchronized | remote-sync | 20 | 0 | 315.56 | 330.93 | 338.25 | 9 | not-evaluated |
| sync-small-changed-ref-set | remote-sync | 20 | 0 | 515.72 | 533.97 | 564.89 | 18 | not-evaluated |
| validate-full-history | history-validation | 20 | 0 | 2130.45 | 2185.81 | 2348.84 | 7 | not-evaluated |
| validate-cached-unchanged | history-validation | 20 | 0 | 116.07 | 122.85 | 131.81 | 4 | not-evaluated |

### active-500-depth-100

| Scenario | Surface | Completed | Timed out | Min (ms) | Median (ms) | P95 (ms) | P95 Git processes | Outcome |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| cli-create | cold-cli | 20 | 0 | 152.81 | 163.62 | 189.09 | 9 | not-evaluated |
| cli-depend | cold-cli | 20 | 0 | 201.93 | 222.20 | 250.37 | 11 | not-evaluated |
| cli-list | cold-cli | 20 | 0 | 67.12 | 71.94 | 80.39 | 3 | not-evaluated |
| cli-move | cold-cli | 20 | 0 | 200.04 | 216.33 | 246.67 | 11 | not-evaluated |
| cli-update | cold-cli | 20 | 0 | 150.74 | 162.65 | 190.31 | 9 | not-evaluated |
| sync-already-synchronized | remote-sync | 20 | 0 | 314.80 | 346.02 | 360.22 | 9 | not-evaluated |
| sync-small-changed-ref-set | remote-sync | 20 | 0 | 527.00 | 553.41 | 665.36 | 18 | not-evaluated |
| validate-full-history | history-validation | 20 | 0 | 13128.53 | 13299.73 | 14338.10 | 7 | not-evaluated |
| validate-cached-unchanged | history-validation | 20 | 0 | 121.34 | 127.14 | 130.78 | 4 | not-evaluated |

### active-1000-depth-20

| Scenario | Surface | Completed | Timed out | Min (ms) | Median (ms) | P95 (ms) | P95 Git processes | Outcome |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| cli-create | cold-cli | 20 | 0 | 148.43 | 175.34 | 178.70 | 9 | not-evaluated |
| cli-depend | cold-cli | 20 | 0 | 222.06 | 246.11 | 251.91 | 11 | not-evaluated |
| cli-list | cold-cli | 20 | 0 | 82.21 | 84.84 | 89.18 | 3 | not-evaluated |
| cli-move | cold-cli | 20 | 0 | 232.89 | 246.43 | 250.58 | 11 | not-evaluated |
| cli-update | cold-cli | 20 | 0 | 151.17 | 170.72 | 179.85 | 9 | not-evaluated |
| sync-already-synchronized | remote-sync | 20 | 0 | 488.28 | 522.65 | 600.02 | 9 | not-evaluated |
| sync-small-changed-ref-set | remote-sync | 20 | 0 | 777.39 | 964.68 | 1652.05 | 18 | not-evaluated |
| validate-full-history | history-validation | 20 | 0 | 6345.71 | 6424.37 | 7858.31 | 7 | not-evaluated |
| validate-cached-unchanged | history-validation | 20 | 0 | 161.45 | 168.63 | 173.71 | 4 | not-evaluated |

## Observed slopes

Each row is a descriptive log-log slope between two consecutive measured points on one axis. A slope carries no budget and no classification.

| Axis | Scenario | Metric | From | To | Dimension ratio | Value ratio | Log-log slope | Note |
| --- | --- | --- | --- | --- | ---: | ---: | ---: | --- |
| task-count | cli-create | medianMilliseconds | active-100-depth-20 | active-500-depth-20 | 5.0000 | 0.9631 | -0.0233 | - |
| task-count | cli-create | p95Milliseconds | active-100-depth-20 | active-500-depth-20 | 5.0000 | 0.6027 | -0.3146 | - |
| task-count | cli-create | p95GitProcesses | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.0000 | 0.0000 | - |
| task-count | cli-depend | medianMilliseconds | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.1240 | 0.0726 | - |
| task-count | cli-depend | p95Milliseconds | active-100-depth-20 | active-500-depth-20 | 5.0000 | 0.7903 | -0.1462 | - |
| task-count | cli-depend | p95GitProcesses | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.0000 | 0.0000 | - |
| task-count | cli-list | medianMilliseconds | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.1580 | 0.0911 | - |
| task-count | cli-list | p95Milliseconds | active-100-depth-20 | active-500-depth-20 | 5.0000 | 0.6032 | -0.3141 | - |
| task-count | cli-list | p95GitProcesses | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.0000 | 0.0000 | - |
| task-count | cli-move | medianMilliseconds | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.1374 | 0.0800 | - |
| task-count | cli-move | p95Milliseconds | active-100-depth-20 | active-500-depth-20 | 5.0000 | 0.7837 | -0.1514 | - |
| task-count | cli-move | p95GitProcesses | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.0000 | 0.0000 | - |
| task-count | cli-update | medianMilliseconds | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.0617 | 0.0372 | - |
| task-count | cli-update | p95Milliseconds | active-100-depth-20 | active-500-depth-20 | 5.0000 | 0.6911 | -0.2296 | - |
| task-count | cli-update | p95GitProcesses | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.0000 | 0.0000 | - |
| task-count | sync-already-synchronized | medianMilliseconds | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.7334 | 0.3418 | - |
| task-count | sync-already-synchronized | p95Milliseconds | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.3555 | 0.1890 | - |
| task-count | sync-already-synchronized | p95GitProcesses | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.0000 | 0.0000 | - |
| task-count | sync-small-changed-ref-set | medianMilliseconds | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.6146 | 0.2977 | - |
| task-count | sync-small-changed-ref-set | p95Milliseconds | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.5294 | 0.2640 | - |
| task-count | sync-small-changed-ref-set | p95GitProcesses | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.0000 | 0.0000 | - |
| task-count | validate-full-history | medianMilliseconds | active-100-depth-20 | active-500-depth-20 | 5.0000 | 5.8960 | 1.1024 | - |
| task-count | validate-full-history | p95Milliseconds | active-100-depth-20 | active-500-depth-20 | 5.0000 | 5.2487 | 1.0302 | - |
| task-count | validate-full-history | p95GitProcesses | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.0000 | 0.0000 | - |
| task-count | validate-cached-unchanged | medianMilliseconds | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.3670 | 0.1943 | - |
| task-count | validate-cached-unchanged | p95Milliseconds | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.3677 | 0.1945 | - |
| task-count | validate-cached-unchanged | p95GitProcesses | active-100-depth-20 | active-500-depth-20 | 5.0000 | 1.0000 | 0.0000 | - |
| task-count | cli-create | medianMilliseconds | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.0969 | 0.1334 | - |
| task-count | cli-create | p95Milliseconds | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.0521 | 0.0732 | - |
| task-count | cli-create | p95GitProcesses | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.0000 | 0.0000 | - |
| task-count | cli-depend | medianMilliseconds | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.1718 | 0.2288 | - |
| task-count | cli-depend | p95Milliseconds | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.1394 | 0.1883 | - |
| task-count | cli-depend | p95GitProcesses | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.0000 | 0.0000 | - |
| task-count | cli-list | medianMilliseconds | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.2327 | 0.3018 | - |
| task-count | cli-list | p95Milliseconds | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.2146 | 0.2805 | - |
| task-count | cli-list | p95GitProcesses | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.0000 | 0.0000 | - |
| task-count | cli-move | medianMilliseconds | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.1736 | 0.2309 | - |
| task-count | cli-move | p95Milliseconds | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.1457 | 0.1962 | - |
| task-count | cli-move | p95GitProcesses | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.0000 | 0.0000 | - |
| task-count | cli-update | medianMilliseconds | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.0758 | 0.1054 | - |
| task-count | cli-update | p95Milliseconds | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.0676 | 0.0943 | - |
| task-count | cli-update | p95GitProcesses | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.0000 | 0.0000 | - |
| task-count | sync-already-synchronized | medianMilliseconds | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.5793 | 0.6593 | - |
| task-count | sync-already-synchronized | p95Milliseconds | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.7739 | 0.8269 | - |
| task-count | sync-already-synchronized | p95GitProcesses | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.0000 | 0.0000 | - |
| task-count | sync-small-changed-ref-set | medianMilliseconds | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.8066 | 0.8533 | - |
| task-count | sync-small-changed-ref-set | p95Milliseconds | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 2.9245 | 1.5482 | - |
| task-count | sync-small-changed-ref-set | p95GitProcesses | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.0000 | 0.0000 | - |
| task-count | validate-full-history | medianMilliseconds | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 2.9391 | 1.5554 | - |
| task-count | validate-full-history | p95Milliseconds | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 3.3456 | 1.7423 | - |
| task-count | validate-full-history | p95GitProcesses | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.0000 | 0.0000 | - |
| task-count | validate-cached-unchanged | medianMilliseconds | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.3726 | 0.4569 | - |
| task-count | validate-cached-unchanged | p95Milliseconds | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.3178 | 0.3982 | - |
| task-count | validate-cached-unchanged | p95GitProcesses | active-500-depth-20 | active-1000-depth-20 | 2.0000 | 1.0000 | 0.0000 | - |
| history-depth | cli-create | medianMilliseconds | active-500-depth-20 | active-500-depth-100 | 5.0000 | 1.0236 | 0.0145 | - |
| history-depth | cli-create | p95Milliseconds | active-500-depth-20 | active-500-depth-100 | 5.0000 | 1.1133 | 0.0667 | - |
| history-depth | cli-create | p95GitProcesses | active-500-depth-20 | active-500-depth-100 | 5.0000 | 1.0000 | 0.0000 | - |
| history-depth | cli-depend | medianMilliseconds | active-500-depth-20 | active-500-depth-100 | 5.0000 | 1.0580 | 0.0350 | - |
| history-depth | cli-depend | p95Milliseconds | active-500-depth-20 | active-500-depth-100 | 5.0000 | 1.1324 | 0.0773 | - |
| history-depth | cli-depend | p95GitProcesses | active-500-depth-20 | active-500-depth-100 | 5.0000 | 1.0000 | 0.0000 | - |
| history-depth | cli-list | medianMilliseconds | active-500-depth-20 | active-500-depth-100 | 5.0000 | 1.0452 | 0.0275 | - |
| history-depth | cli-list | p95Milliseconds | active-500-depth-20 | active-500-depth-100 | 5.0000 | 1.0950 | 0.0564 | - |
| history-depth | cli-list | p95GitProcesses | active-500-depth-20 | active-500-depth-100 | 5.0000 | 1.0000 | 0.0000 | - |
| history-depth | cli-move | medianMilliseconds | active-500-depth-20 | active-500-depth-100 | 5.0000 | 1.0302 | 0.0185 | - |
| history-depth | cli-move | p95Milliseconds | active-500-depth-20 | active-500-depth-100 | 5.0000 | 1.1278 | 0.0747 | - |
| history-depth | cli-move | p95GitProcesses | active-500-depth-20 | active-500-depth-100 | 5.0000 | 1.0000 | 0.0000 | - |
| history-depth | cli-update | medianMilliseconds | active-500-depth-20 | active-500-depth-100 | 5.0000 | 1.0249 | 0.0153 | - |
| history-depth | cli-update | p95Milliseconds | active-500-depth-20 | active-500-depth-100 | 5.0000 | 1.1297 | 0.0757 | - |
| history-depth | cli-update | p95GitProcesses | active-500-depth-20 | active-500-depth-100 | 5.0000 | 1.0000 | 0.0000 | - |
| history-depth | sync-already-synchronized | medianMilliseconds | active-500-depth-20 | active-500-depth-100 | 5.0000 | 1.0456 | 0.0277 | - |
| history-depth | sync-already-synchronized | p95Milliseconds | active-500-depth-20 | active-500-depth-100 | 5.0000 | 1.0649 | 0.0391 | - |
| history-depth | sync-already-synchronized | p95GitProcesses | active-500-depth-20 | active-500-depth-100 | 5.0000 | 1.0000 | 0.0000 | - |
| history-depth | sync-small-changed-ref-set | medianMilliseconds | active-500-depth-20 | active-500-depth-100 | 5.0000 | 1.0364 | 0.0222 | - |
| history-depth | sync-small-changed-ref-set | p95Milliseconds | active-500-depth-20 | active-500-depth-100 | 5.0000 | 1.1779 | 0.1017 | - |
| history-depth | sync-small-changed-ref-set | p95GitProcesses | active-500-depth-20 | active-500-depth-100 | 5.0000 | 1.0000 | 0.0000 | - |
| history-depth | validate-full-history | medianMilliseconds | active-500-depth-20 | active-500-depth-100 | 5.0000 | 6.0846 | 1.1220 | - |
| history-depth | validate-full-history | p95Milliseconds | active-500-depth-20 | active-500-depth-100 | 5.0000 | 6.1043 | 1.1240 | - |
| history-depth | validate-full-history | p95GitProcesses | active-500-depth-20 | active-500-depth-100 | 5.0000 | 1.0000 | 0.0000 | - |
| history-depth | validate-cached-unchanged | medianMilliseconds | active-500-depth-20 | active-500-depth-100 | 5.0000 | 1.0349 | 0.0213 | - |
| history-depth | validate-cached-unchanged | p95Milliseconds | active-500-depth-20 | active-500-depth-100 | 5.0000 | 0.9922 | -0.0049 | - |
| history-depth | validate-cached-unchanged | p95GitProcesses | active-500-depth-20 | active-500-depth-100 | 5.0000 | 1.0000 | 0.0000 | - |
