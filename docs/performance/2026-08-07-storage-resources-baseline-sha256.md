# Workbook performance report

Phase: acceptance

## Reference budgets
Baseline targets are reference budgets, not achieved guarantees: warm p95 0.00 ms, cold p95 0.00 ms, burst 0.00 ms.

## Scenarios
| Scenario | Surface | Completed | Timed out | Min (ms) | Median (ms) | P95 (ms) | P95 Git processes | Target duration | Target Git processes | Outcome |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- |

## Storage and peak resources

Descriptive measurements with no target. Platform darwin/arm64. Repository state: refs packed with git pack-refs --all, objects packed with git gc --quiet --prune=now.

Object size semantics: rawBytes is the uncompressed Git object content size (%(objectsize)); diskBytes is the size of the object's stored representation including delta and header (%(objectsize:disk)) and excludes per-pack index and header overhead.

Raw `ru_maxrss` unit on this platform: bytes. Block I/O counters (`ru_inblock`, `ru_oublock`) maintained: false.

### 500 tasks by 20 operations (sha256)

Fixture: 500 total tasks, 475 active, 25 tombstoned, 20 operations per task, sha256 objects.

Durable objects reachable from `refs/workbook/` (500 refs, 500 task refs): classified 40000 of 40000, 0 unclassified.

| Object class | Objects | Raw bytes | On-disk bytes |
| --- | ---: | ---: | ---: |
| operation-blob | 10000 | 4035146 | 2169509 |
| state-blob | 10000 | 5139504 | 2642553 |
| other-blob | 0 | 0 | 0 |
| tree | 10000 | 1040000 | 1091789 |
| commit | 10000 | 3264000 | 2036321 |
| annotated-tag | 0 | 0 | 0 |
| **Total reachable** | 40000 | 13478650 | 7940172 |

| Repository storage | Bytes |
| --- | ---: |
| Pack files (packs: 1, packed objects: 40000) | 7940216 |
| Pack indexes | 1601096 |
| Pack auxiliary files | 160076 |
| Loose objects (0) | 0 |
| Object directory total | 10542590 |

| Disposable cache | Bytes |
| --- | ---: |
| SQLite projection (`.git/workbook/cache.sqlite`) | 5148672 |
| SQLite projection sidecars | 0 |
| Validation cache (`.git/workbook/validation.sqlite`) | 2625536 |
| Validation cache sidecars | 0 |
| **Total disposable** | 7774208 |

| Command | Elapsed (ms) | Peak resident (bytes) | Raw ru_maxrss | Block input | Block output | Minor page faults | Major page faults | Repository bytes written |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| projection-rebuild | 583.05 | 64028672 | 64028672 bytes | 0 | 0 | 19268 | 57 | 5148672 |
| full-validation | 3192.77 | 93978624 | 93978624 bytes | 0 | 0 | 22951 | 50 | 2625536 |

### 500 tasks by 50 operations (sha256)

Fixture: 500 total tasks, 475 active, 25 tombstoned, 50 operations per task, sha256 objects.

Durable objects reachable from `refs/workbook/` (500 refs, 500 task refs): classified 100000 of 100000, 0 unclassified.

| Object class | Objects | Raw bytes | On-disk bytes |
| --- | ---: | ---: | ---: |
| operation-blob | 25000 | 10196786 | 5314876 |
| state-blob | 25000 | 13092564 | 6878741 |
| other-blob | 0 | 0 | 0 |
| tree | 25000 | 2600000 | 2729593 |
| commit | 25000 | 8214000 | 5125561 |
| annotated-tag | 0 | 0 | 0 |
| **Total reachable** | 100000 | 34103350 | 20048771 |

| Repository storage | Bytes |
| --- | ---: |
| Pack files (packs: 1, packed objects: 100000) | 20048815 |
| Pack indexes | 4001096 |
| Pack auxiliary files | 400076 |
| Loose objects (0) | 0 |
| Object directory total | 26551189 |

| Disposable cache | Bytes |
| --- | ---: |
| SQLite projection (`.git/workbook/cache.sqlite`) | 12283904 |
| SQLite projection sidecars | 0 |
| Validation cache (`.git/workbook/validation.sqlite`) | 5861376 |
| Validation cache sidecars | 0 |
| **Total disposable** | 18145280 |

| Command | Elapsed (ms) | Peak resident (bytes) | Raw ru_maxrss | Block input | Block output | Minor page faults | Major page faults | Repository bytes written |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| projection-rebuild | 1110.95 | 101203968 | 101203968 bytes | 0 | 0 | 25115 | 57 | 12283904 |
| full-validation | 7143.59 | 278904832 | 278904832 bytes | 0 | 0 | 39026 | 50 | 5861376 |

### 500 tasks by 100 operations (sha256)

Fixture: 500 total tasks, 475 active, 25 tombstoned, 100 operations per task, sha256 objects.

Durable objects reachable from `refs/workbook/` (500 refs, 500 task refs): classified 200000 of 200000, 0 unclassified.

| Object class | Objects | Raw bytes | On-disk bytes |
| --- | ---: | ---: | ---: |
| operation-blob | 50000 | 20467161 | 10579001 |
| state-blob | 50000 | 26348639 | 13967900 |
| other-blob | 0 | 0 | 0 |
| tree | 50000 | 5200000 | 5459017 |
| commit | 50000 | 16464500 | 10275506 |
| annotated-tag | 0 | 0 | 0 |
| **Total reachable** | 200000 | 68480300 | 40281424 |

| Repository storage | Bytes |
| --- | ---: |
| Pack files (packs: 1, packed objects: 200000) | 40281468 |
| Pack indexes | 8001096 |
| Pack auxiliary files | 800076 |
| Loose objects (0) | 0 |
| Object directory total | 53283842 |

| Disposable cache | Bytes |
| --- | ---: |
| SQLite projection (`.git/workbook/cache.sqlite`) | 24096768 |
| SQLite projection sidecars | 0 |
| Validation cache (`.git/workbook/validation.sqlite`) | 11333632 |
| Validation cache sidecars | 0 |
| **Total disposable** | 35430400 |

| Command | Elapsed (ms) | Peak resident (bytes) | Raw ru_maxrss | Block input | Block output | Minor page faults | Major page faults | Repository bytes written |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| projection-rebuild | 1931.55 | 169476096 | 169476096 bytes | 0 | 0 | 36706 | 57 | 24096768 |
| full-validation | 14668.74 | 529039360 | 529039360 bytes | 0 | 0 | 63542 | 50 | 11333632 |

### 500 tasks by 200 operations (sha256)

Fixture: 500 total tasks, 475 active, 25 tombstoned, 200 operations per task, sha256 objects.

Durable objects reachable from `refs/workbook/` (500 refs, 500 task refs): classified 400000 of 400000, 0 unclassified.

| Object class | Objects | Raw bytes | On-disk bytes |
| --- | ---: | ---: | ---: |
| operation-blob | 100000 | 41105961 | 21280527 |
| state-blob | 100000 | 52958864 | 28147370 |
| other-blob | 0 | 0 | 0 |
| tree | 100000 | 10400000 | 10917849 |
| commit | 100000 | 33014500 | 20603049 |
| annotated-tag | 0 | 0 | 0 |
| **Total reachable** | 400000 | 137479325 | 80948795 |

| Repository storage | Bytes |
| --- | ---: |
| Pack files (packs: 1, packed objects: 400000) | 80948839 |
| Pack indexes | 16001096 |
| Pack auxiliary files | 1600076 |
| Loose objects (0) | 0 |
| Object directory total | 106951213 |

| Disposable cache | Bytes |
| --- | ---: |
| SQLite projection (`.git/workbook/cache.sqlite`) | 48132096 |
| SQLite projection sidecars | 0 |
| Validation cache (`.git/workbook/validation.sqlite`) | 22183936 |
| Validation cache sidecars | 0 |
| **Total disposable** | 70316032 |

| Command | Elapsed (ms) | Peak resident (bytes) | Raw ru_maxrss | Block input | Block output | Minor page faults | Major page faults | Repository bytes written |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| projection-rebuild | 3262.36 | 311476224 | 311476224 bytes | 0 | 0 | 58112 | 57 | 48132096 |
| full-validation | 29720.64 | 962772992 | 962772992 bytes | 0 | 0 | 110247 | 50 | 22183936 |
