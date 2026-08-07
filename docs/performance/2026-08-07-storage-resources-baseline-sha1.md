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

### 500 tasks by 20 operations (sha1)

Fixture: 500 total tasks, 475 active, 25 tombstoned, 20 operations per task, sha1 objects.

Durable objects reachable from `refs/workbook/` (500 refs, 500 task refs): classified 40000 of 40000, 0 unclassified.

| Object class | Objects | Raw bytes | On-disk bytes |
| --- | ---: | ---: | ---: |
| operation-blob | 10000 | 4035146 | 2169509 |
| state-blob | 10000 | 5139504 | 2642553 |
| other-blob | 0 | 0 | 0 |
| tree | 10000 | 800000 | 838795 |
| commit | 10000 | 2796000 | 1755064 |
| annotated-tag | 0 | 0 | 0 |
| **Total reachable** | 40000 | 12770650 | 7405921 |

| Repository storage | Bytes |
| --- | ---: |
| Pack files (packs: 1, packed objects: 40000) | 7405953 |
| Pack indexes | 1121072 |
| Pack auxiliary files | 160052 |
| Loose objects (0) | 0 |
| Object directory total | 9288243 |

| Disposable cache | Bytes |
| --- | ---: |
| SQLite projection (`.git/workbook/cache.sqlite`) | 4513792 |
| SQLite projection sidecars | 0 |
| Validation cache (`.git/workbook/validation.sqlite`) | 2097152 |
| Validation cache sidecars | 0 |
| **Total disposable** | 6610944 |

| Command | Elapsed (ms) | Peak resident (bytes) | Raw ru_maxrss | Block input | Block output | Minor page faults | Major page faults | Repository bytes written |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| projection-rebuild | 719.98 | 61014016 | 61014016 bytes | 0 | 0 | 18394 | 58 | 4513792 |
| full-validation | 3892.73 | 92471296 | 92471296 bytes | 0 | 0 | 22165 | 52 | 2097152 |

### 500 tasks by 50 operations (sha1)

Fixture: 500 total tasks, 475 active, 25 tombstoned, 50 operations per task, sha1 objects.

Durable objects reachable from `refs/workbook/` (500 refs, 500 task refs): classified 100000 of 100000, 0 unclassified.

| Object class | Objects | Raw bytes | On-disk bytes |
| --- | ---: | ---: | ---: |
| operation-blob | 25000 | 10196786 | 5314876 |
| state-blob | 25000 | 13092564 | 6878741 |
| other-blob | 0 | 0 | 0 |
| tree | 25000 | 2000000 | 2096915 |
| commit | 25000 | 7026000 | 4411977 |
| annotated-tag | 0 | 0 | 0 |
| **Total reachable** | 100000 | 32315350 | 18702509 |

| Repository storage | Bytes |
| --- | ---: |
| Pack files (packs: 1, packed objects: 100000) | 18702541 |
| Pack indexes | 2801072 |
| Pack auxiliary files | 400052 |
| Loose objects (0) | 0 |
| Object directory total | 23404831 |

| Disposable cache | Bytes |
| --- | ---: |
| SQLite projection (`.git/workbook/cache.sqlite`) | 10883072 |
| SQLite projection sidecars | 0 |
| Validation cache (`.git/workbook/validation.sqlite`) | 4546560 |
| Validation cache sidecars | 0 |
| **Total disposable** | 15429632 |

| Command | Elapsed (ms) | Peak resident (bytes) | Raw ru_maxrss | Block input | Block output | Minor page faults | Major page faults | Repository bytes written |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| projection-rebuild | 1653.67 | 97992704 | 97992704 bytes | 0 | 0 | 24825 | 58 | 10883072 |
| full-validation | 20755.66 | 273727488 | 273727488 bytes | 0 | 0 | 38184 | 52 | 4546560 |

### 500 tasks by 100 operations (sha1)

Fixture: 500 total tasks, 475 active, 25 tombstoned, 100 operations per task, sha1 objects.

Durable objects reachable from `refs/workbook/` (500 refs, 500 task refs): classified 200000 of 200000, 0 unclassified.

| Object class | Objects | Raw bytes | On-disk bytes |
| --- | ---: | ---: | ---: |
| operation-blob | 50000 | 20467161 | 10579001 |
| state-blob | 50000 | 26348639 | 13967900 |
| other-blob | 0 | 0 | 0 |
| tree | 50000 | 4000000 | 4194067 |
| commit | 50000 | 14076500 | 8841617 |
| annotated-tag | 0 | 0 | 0 |
| **Total reachable** | 200000 | 64892300 | 37582585 |

| Repository storage | Bytes |
| --- | ---: |
| Pack files (packs: 1, packed objects: 200000) | 37582617 |
| Pack indexes | 5601072 |
| Pack auxiliary files | 800052 |
| Loose objects (0) | 0 |
| Object directory total | 46984907 |

| Disposable cache | Bytes |
| --- | ---: |
| SQLite projection (`.git/workbook/cache.sqlite`) | 21790720 |
| SQLite projection sidecars | 0 |
| Validation cache (`.git/workbook/validation.sqlite`) | 8654848 |
| Validation cache sidecars | 0 |
| **Total disposable** | 30445568 |

| Command | Elapsed (ms) | Peak resident (bytes) | Raw ru_maxrss | Block input | Block output | Minor page faults | Major page faults | Repository bytes written |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| projection-rebuild | 4797.71 | 163790848 | 163790848 bytes | 0 | 0 | 34583 | 58 | 21790720 |
| full-validation | 35807.60 | 519553024 | 519553024 bytes | 0 | 0 | 61034 | 52 | 8654848 |

### 500 tasks by 200 operations (sha1)

Fixture: 500 total tasks, 475 active, 25 tombstoned, 200 operations per task, sha1 objects.

Durable objects reachable from `refs/workbook/` (500 refs, 500 task refs): classified 400000 of 400000, 0 unclassified.

| Object class | Objects | Raw bytes | On-disk bytes |
| --- | ---: | ---: | ---: |
| operation-blob | 100000 | 41105961 | 21280527 |
| state-blob | 100000 | 52958864 | 28147370 |
| other-blob | 0 | 0 | 0 |
| tree | 100000 | 8000000 | 8388262 |
| commit | 100000 | 28226500 | 17734627 |
| annotated-tag | 0 | 0 | 0 |
| **Total reachable** | 400000 | 130291325 | 75550786 |

| Repository storage | Bytes |
| --- | ---: |
| Pack files (packs: 1, packed objects: 400000) | 75550818 |
| Pack indexes | 11201072 |
| Pack auxiliary files | 1600052 |
| Loose objects (0) | 0 |
| Object directory total | 94353108 |

| Disposable cache | Bytes |
| --- | ---: |
| SQLite projection (`.git/workbook/cache.sqlite`) | 43229184 |
| SQLite projection sidecars | 0 |
| Validation cache (`.git/workbook/validation.sqlite`) | 16809984 |
| Validation cache sidecars | 0 |
| **Total disposable** | 60039168 |

| Command | Elapsed (ms) | Peak resident (bytes) | Raw ru_maxrss | Block input | Block output | Minor page faults | Major page faults | Repository bytes written |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| projection-rebuild | 6750.94 | 305364992 | 305364992 bytes | 0 | 0 | 54841 | 58 | 43229184 |
| full-validation | 60259.69 | 983875584 | 983875584 bytes | 0 | 0 | 106230 | 52 | 16809984 |
